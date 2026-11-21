package probe

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	hclient "github.com/apernet/hysteria/core/v2/client"

	"github.com/shogun-kub/termit/internal/proto"
)

// quicProbe checks one node through its QUIC transport. The hysteria client runs in this process instead
// of the worker binary, so a node that serves nothing else can still be checked; the classification is
// the same vocabulary as the TLS path, so the state machine reads one kind of result.
func (w *Worker) quicProbe(ctx context.Context, t Target) Result {
	start := time.Now()
	res := Result{ID: t.ID}

	ctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()

	port := t.Port
	if port == 0 {
		port = proto.HysteriaPort
	}
	serverAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(t.Address, strconv.Itoa(port)))
	if err != nil {
		res.Class, res.Err = ClassWorker, fmt.Errorf("probe: resolve %s: %w", t.Address, err)
		return res
	}
	tlsConfig := hclient.TLSConfig{ServerName: t.ServerName, RootCAs: w.cfg.Roots}
	if t.PinnedCertSHA256 != "" {
		// The node may serve a leaf no public root signs, and the pin is the stronger statement anyway:
		// the chain is not verified, the exact certificate is.
		tlsConfig.InsecureSkipVerify = true
		tlsConfig.VerifyPeerCertificate = pinVerifier(t.PinnedCertSHA256)
	}
	client, _, err := hclient.NewClient(&hclient.Config{
		ServerAddr: serverAddr,
		// userpass: the server splits the string on the first colon.
		Auth:      proto.HysteriaUser(t.UUID) + ":" + proto.HysteriaPassword(t.UUID),
		TLSConfig: tlsConfig,
	})
	if err != nil {
		// The handshake failing is a statement about the path or the node, never about the prober: a
		// node that listens on nothing is closed, one that answers too slowly is a timeout. Only a
		// configuration this process cannot use at all is a worker failure.
		res.Class, res.Err = ClassClosed, err
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
			res.Class = ClassTimeout
		}
		return res
	}
	defer func() { _ = client.Close() }()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			// The client dials without a context, so the timeout is enforced here: a dial that outlives
			// the request is closed instead of leaking.
			type dialed struct {
				conn net.Conn
				err  error
			}
			done := make(chan dialed, 1)
			go func() {
				conn, err := client.TCP(addr)
				done <- dialed{conn: conn, err: err}
			}()
			select {
			case outcome := <-done:
				return outcome.conn, outcome.err
			case <-ctx.Done():
				go func() {
					if outcome := <-done; outcome.conn != nil {
						_ = outcome.conn.Close()
					}
				}()
				return nil, ctx.Err()
			}
		},
		// One request: a pooled connection would outlive the answer it gave.
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, w.cfg.ProbeURL, nil)
	if err != nil {
		res.Class, res.Err = ClassWorker, err
		return res
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	res.Latency = time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			res.Class = ClassTimeout
		} else {
			res.Class, res.Err = classify(err)
		}
		res.Err = err
		return res
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != w.cfg.ExpectStatus {
		res.Class = ClassHTTP
		res.Err = fmt.Errorf("probe: got HTTP %d, want %d", response.StatusCode, w.cfg.ExpectStatus)
		return res
	}
	res.OK = true
	return res
}

// pinVerifier accepts exactly one leaf certificate, compared the way the rest of the project pins: the
// SHA-256 of the DER bytes, lowercase hex.
func pinVerifier(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("probe: the node presented no certificate")
		}
		sum := sha256.Sum256(rawCerts[0])
		served := hex.EncodeToString(sum[:])
		if served != want {
			return fmt.Errorf("probe: the node serves certificate %s…, not the pinned one", served[:12])
		}
		return nil
	}
}
