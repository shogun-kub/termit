// Package server is the management server's HTTP surface: the agent API, the subscription endpoint
// and the two operational endpoints.
//
// Admin operations are deliberately absent. They live on a unix socket (M2.6) so that the public
// surface stays small enough to reason about: enrollment needs an invite, everything else needs an
// agent key or a subscription token.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/metrics"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/version"
)

// maxBody bounds a request body. An enrollment is a few hundred bytes.
const maxBody = 64 << 10

// Options wire a Handler.
type Options struct {
	Store      *store.Store
	Enrollment *enroll.Service
	Domain     string
	Logger     *slog.Logger
	// Attempts is how many enrollments one address may try inside Window.
	Attempts int
	Window   time.Duration
	Now      func() time.Time
	// MasterKey derives pair credentials; it never leaves the process.
	MasterKey []byte
	// CheckInterval and SyncInterval are the timing hints a node receives.
	CheckInterval time.Duration
	SyncInterval  time.Duration
	// MinSyncDelay is how long a fresh node waits before its first own cycle.
	MinSyncDelay time.Duration
	// Metrics receives the counters behind /metrics. Optional: a test or a plain handler runs without
	// one, and the endpoint then reports only the gauges it can read from the database.
	Metrics *metrics.Registry
	// ProberUp says whether checks can actually run. The server keeps serving either way; this is
	// what makes "every node is unknown" visible instead of mysterious.
	ProberUp bool
	// CertPath is the server's own certificate; its expiry is a gauge, because a management server
	// that renews only on restart is a failure an alert should catch.
	CertPath string
}

// Handler is the router plus its state.
type Handler struct {
	opts    Options
	limiter *limiter
	subs    *limiter
	mux     *http.ServeMux
}

// Subscription limits are looser than enrollment's and counted separately. An enrollment limit
// defends a guessable invite code; a subscription token is a 256-bit secret that cannot be guessed,
// so its counter is load control. Ten requests per quarter hour is what an app doing one refresh at
// launch would hit in a week of restarts, and a client that reconnects in a loop would look like an
// attack to the owner.
const (
	subscriptionAttempts = 60
	subscriptionWindow   = 15 * time.Minute
)

// New builds the router.
func New(opts Options) (*Handler, error) {
	if opts.Store == nil {
		return nil, errors.New("server: a store is required")
	}
	if opts.Enrollment == nil {
		return nil, errors.New("server: an enrollment service is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Attempts == 0 {
		opts.Attempts = 10
	}
	if opts.Window == 0 {
		opts.Window = 15 * time.Minute
	}
	h := &Handler{opts: opts, limiter: newLimiter(opts.Attempts, opts.Window, opts.Now),
		subs: newLimiter(subscriptionAttempts, subscriptionWindow, opts.Now)}
	h.mux = http.NewServeMux()
	h.mux.HandleFunc("GET /healthz", h.health)
	h.mux.HandleFunc("GET /metrics", h.metrics)
	h.mux.HandleFunc("POST /api/v1/enroll", h.enroll)
	h.mux.HandleFunc("POST /api/v1/agent/sync", h.sync)
	h.mux.HandleFunc("POST /api/v1/agent/unblock", h.unblock)
	h.mux.HandleFunc("POST /api/v1/agent/relocate", h.relocate)
	h.mux.HandleFunc("GET /sub/{token}", h.subscription)
	h.mux.HandleFunc("GET /sub/{token}/json", h.subscription)
	h.mux.HandleFunc("/", h.notFound)
	return h, nil
}

// ServeHTTP makes Handler an http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	body := map[string]any{
		"status":         "ok",
		"api_version":    proto.APIVersion,
		"build":          version.Version,
		"schema_version": store.SchemaVersion,
		"domain":         h.opts.Domain,
	}
	if err := h.opts.Store.Ping(ctx); err != nil {
		body["status"] = "degraded"
		body["database"] = "unavailable"
		h.opts.Logger.Error("health check: database", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// metrics is loopback-only: it describes the server's internals, and the monitoring agent that reads
// it runs on the same host.
//
// The counters are aggregates and the gauges are read here rather than cached, because a number that
// disagrees with `termit-mn status` would cost more to explain than one small query costs to run.
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		http.Error(w, "metrics are available on loopback only", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	snap := metrics.Snapshot{
		Version:       version.Version,
		SchemaVersion: store.SchemaVersion,
		Now:           h.opts.Now().UTC(),
		ProberUp:      h.opts.ProberUp,
	}
	if counts, err := h.opts.Store.StateCounts(ctx); err != nil {
		h.opts.Logger.Warn("metrics: state counts", "err", err)
	} else {
		snap.Nodes = counts
	}
	if queue, err := h.opts.Store.Queue(ctx, snap.Now); err != nil {
		h.opts.Logger.Warn("metrics: check queue", "err", err)
	} else {
		snap.Due, snap.OldestDue, snap.NextDue = queue.Due, queue.OldestDue, queue.NextDue
	}
	if h.opts.CertPath != "" {
		if notAfter, err := acme.CertNotAfter(h.opts.CertPath); err == nil {
			snap.CertificateNotAfter = &notAfter
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if h.opts.Metrics != nil {
		h.opts.Metrics.Write(w, snap)
		return
	}
	metrics.New(snap.Now).Write(w, snap)
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.opts.Logger.Debug("unknown request", "method", r.Method, "path", r.URL.Path)
	http.NotFound(w, r)
}

// enroll handles POST /api/v1/enroll.
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	// Every path out of this handler is one enrollment attempt: refused unless the last line says
	// otherwise. The counter is what makes a wave of refusals visible without reading logs.
	outcome := metrics.ResultRefused
	defer func() { h.observeEnrollment(outcome) }()

	ip := sourceIP(r)
	if !h.limiter.allow(ip) {
		// The limit exists because an invite code is a guessable-length secret and enrollment is the
		// only endpoint an unauthenticated caller can reach.
		writeProblem(w, http.StatusTooManyRequests, proto.NewProblem(http.StatusTooManyRequests,
			proto.CodeRateLimited, "Too Many Requests", "too many enrollment attempts; wait and retry"))
		return
	}

	var req proto.EnrollRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
			proto.CodeURLInvalid, "Bad Request", "the body is not a valid enrollment request: "+err.Error()))
		return
	}

	result, err := h.opts.Enrollment.Enroll(r.Context(), req, ip, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		var refused *enroll.Error
		if errors.As(err, &refused) {
			h.opts.Logger.Info("enrollment refused", "domain", req.Domain, "code", refused.Code, "status", refused.Status)
			writeProblem(w, refused.Status, proto.NewProblem(refused.Status, refused.Code, titleFor(refused.Status), refused.Detail))
			return
		}
		h.opts.Logger.Error("enrollment failed", "domain", req.Domain, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not complete the enrollment"))
		return
	}

	h.opts.Logger.Info("node enrolled",
		"uv", result.UV.ID, "domain", result.UV.Domain, "state", result.UV.State, "ip", ip)
	if result.UV.State == store.StatePending {
		outcome = metrics.ResultPending
	} else {
		outcome = metrics.ResultOK
	}
	// The secrets are in this response and nowhere else: no log line above carries them.
	writeJSON(w, http.StatusCreated, result.Secrets)
}

// observeEnrollment records one attempt, tolerating a handler built without a registry.
func (h *Handler) observeEnrollment(outcome string) {
	if h.opts.Metrics != nil {
		h.opts.Metrics.ObserveEnrollment(outcome)
	}
}

func titleFor(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "Bad Request"
	case http.StatusConflict:
		return "Conflict"
	case http.StatusUnauthorized:
		return "Unauthorized"
	case http.StatusTooManyRequests:
		return "Too Many Requests"
	case http.StatusServiceUnavailable:
		return "Service Unavailable"
	case http.StatusBadGateway:
		return "Bad Gateway"
	}
	return "Error"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeProblem(w http.ResponseWriter, status int, body proto.Problem) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// sourceIP is the peer of the TCP connection. Forwarded headers are ignored on purpose: the DNS check
// exists to prove the request came from the node's own address, and a header any client can set would
// make that proof meaningless.
func sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// limiter is a fixed-window counter per address: enough to stop a script guessing invite codes, and
// small enough to be obviously correct.
type limiter struct {
	mu       sync.Mutex
	attempts int
	window   time.Duration
	now      func() time.Time
	seen     map[string]*window
}

type window struct {
	count int
	start time.Time
}

func newLimiter(attempts int, period time.Duration, now func() time.Time) *limiter {
	return &limiter{attempts: attempts, window: period, now: now, seen: map[string]*window{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry, ok := l.seen[key]
	if !ok || now.Sub(entry.start) >= l.window {
		l.seen[key] = &window{count: 1, start: now}
		// Bound the map: an attacker with a range of addresses must not grow it without limit.
		if len(l.seen) > 4096 {
			for k, v := range l.seen {
				if now.Sub(v.start) >= l.window {
					delete(l.seen, k)
				}
			}
		}
		return true
	}
	entry.count++
	return entry.count <= l.attempts
}
