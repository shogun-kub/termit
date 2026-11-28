package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/proto"
)

// Vantage asks a peer on another network what it sees, and turns the answer into the same Result this
// process's own worker would have produced (docs/IMPLEMENTATION_PLAN.md §5.2.4).
//
// It is deliberately a client of one call: the home server still decides and writes state, and the answer
// travels back as an observation with a timestamp. A stale answer, an unreachable peer or a peer whose own
// prober is down is not evidence either way, so it comes back as an unknown Result.
type Vantage struct {
	// Name labels this vantage in the check row and in `uv show`.
	Name string
	// BaseURL is the peer's API base, prefix included, e.g. https://mn2.example.com/34xpus3fnghw.
	BaseURL string
	// Token is the mesh credential the peer accepts; the same one replication uses.
	Token string
	// LeafSHA256 pins the peer's certificate (hex, colons allowed). Empty means the system roots decide.
	LeafSHA256 string
	// Timeout bounds one call. Zero means 20 seconds: longer than the local probe's own budget, because
	// the peer runs a whole worker for the answer.
	Timeout time.Duration
	// HTTP overrides the client; tests set it. Production leaves it nil.
	HTTP *http.Client
	// Now is the clock that judges staleness; tests set it.
	Now func() time.Time
	// MaxAge is how old an answer may be before it stops being a verdict. Zero means five minutes.
	MaxAge time.Duration
}

// Probe asks one question and returns the peer's observation as a Result. An error means the vantage could
// not answer at all, and the Result is ClassWorker, which every caller must read as unknown — never as a
// failure of the node.
func (v *Vantage) Probe(ctx context.Context, target Target) (Result, error) {
	timeout := v.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := v.client()
	if err != nil {
		return Result{ID: target.ID, Class: ClassWorker, Err: err}, err
	}
	body, err := json.Marshal(proto.ProbeRequest{Target: proto.ProbeTarget{
		ID:               target.ID,
		Address:          target.Address,
		Port:             target.Port,
		ServerName:       target.ServerName,
		Path:             target.Path,
		Mode:             target.Mode,
		UUID:             target.UUID,
		PinnedCertSHA256: target.PinnedCertSHA256,
		Transport:        target.Transport,
	}})
	if err != nil {
		return Result{ID: target.ID, Class: ClassWorker, Err: err}, err
	}
	url := strings.TrimSuffix(v.BaseURL, "/") + proto.PeerProbePath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{ID: target.ID, Class: ClassWorker, Err: err}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+v.Token)

	response, err := client.Do(request)
	if err != nil {
		return Result{ID: target.ID, Class: ClassWorker, Err: err}, err
	}
	defer response.Body.Close() //nolint:errcheck // read-only body.
	if response.StatusCode != http.StatusOK {
		return Result{ID: target.ID, Class: ClassWorker,
			Err: fmt.Errorf("vantage %s answered %s", v.Name, response.Status)}, nil
	}
	var answer proto.ProbeAnswer
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&answer); err != nil {
		return Result{ID: target.ID, Class: ClassWorker, Err: err}, err
	}

	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	maxAge := v.MaxAge
	if maxAge == 0 {
		maxAge = 5 * time.Minute
	}
	if answer.At.IsZero() || now().Sub(answer.At) > maxAge {
		// An observation from a cycle that has already passed is not a verdict about now.
		return Result{ID: target.ID, Class: ClassWorker,
			Err: fmt.Errorf("vantage %s answered from %s, which is too old", v.Name, answer.At)}, nil
	}

	result := Result{
		ID:      target.ID,
		OK:      answer.OK,
		Latency: time.Duration(answer.LatencyMS) * time.Millisecond,
		Class:   Class(answer.Class),
		Vantage: v.Name,
	}
	if answer.Error != "" {
		result.Err = errors.New(answer.Error)
	}
	return result, nil
}

// client is the HTTP client this vantage talks through, pinned the way a mesh peer is pinned.
func (v *Vantage) client() (*http.Client, error) {
	if v.HTTP != nil {
		return v.HTTP, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if v.LeafSHA256 != "" {
		pin, err := config.ParsePin(v.LeafSHA256)
		if err != nil {
			return nil, fmt.Errorf("vantage %s: %w", v.Name, err)
		}
		transport.TLSClientConfig = &tls.Config{
			// #nosec G402 -- the pin below is the check, and it is stricter than a name: it holds
			// whether or not the chain validates, which is what a pin is for.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("vantage: the peer presented no certificate")
				}
				sum := sha256.Sum256(rawCerts[0])
				if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
					return errors.New("vantage: the peer's certificate is not the pinned one")
				}
				return nil
			},
		}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}
