// Package certcheck inspects the certificate a node presents.
//
// It does two jobs at once. First, it answers the question the design asks: is this a real,
// unexpired certificate for this hostname? Second, it produces the SHA-256 of the leaf, which is what
// the tunnel probe pins. Pinning is what makes the probe independent of the machine's trust store: the
// prober does not have to be told which CA to trust — it trusts exactly the certificate that was
// checked here, and nothing else.
//
// The two answers are kept apart because callers treat them differently. An expired certificate is a
// verdict about the node (clients see the same rejection). A chain or hostname failure may be the
// checker's own trust store, so the caller decides; and a leaf that differs from the pinned one is
// what a renewal looks like, which is never evidence against the node.
package certcheck

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"
)

// MinValidity is how long a node certificate must still be valid. A node that expires tomorrow cannot
// renew in time to stay in the mesh, so enrollment refuses it while the owner is still watching.
const MinValidity = 14 * 24 * time.Hour

// Class is the stable reason a check failed.
type Class string

const (
	ClassNone         Class = ""
	ClassUnreachable  Class = "tls_unreachable"
	ClassInvalidChain Class = "tls_invalid"
	ClassHostname     Class = "tls_hostname_mismatch"
	ClassExpiring     Class = "tls_expiring"
	// ClassExpired means the certificate's validity window has passed. It is a verdict about the node:
	// clients see the same error. ClassNotYetValid is kept apart because a certificate from the future
	// is more likely to mean the checking clock is behind than the node is broken.
	ClassExpired     Class = "tls_expired"
	ClassNotYetValid Class = "tls_not_yet_valid"
)

// Result is what the node presented.
type Result struct {
	// Fingerprint is the lowercase hex SHA-256 of the leaf certificate, ready for the probe pin.
	Fingerprint string
	NotAfter    time.Time
	DNSNames    []string
	// ChainVerified is true when the leaf chains to a trusted root for DNSName.
	ChainVerified bool
}

// Options configure Check.
type Options struct {
	// Port defaults to 443.
	Port int
	// Roots is the trust store. Nil means the system roots; a private CA is supplied here instead of
	// being installed on the host.
	Roots *x509.CertPool
	// Now is the clock; tests set it.
	Now func() time.Time
	// Timeout bounds the handshake. Defaults to 10s.
	Timeout time.Duration
	// Address is the host to dial. Empty means the domain itself; tests point it at a loopback
	// listener while still checking the certificate for the real hostname.
	Address string
}

// Error is a failed check with a class the caller can report.
type Error struct {
	Class  Class
	Detail string
}

func (e *Error) Error() string { return string(e.Class) + ": " + e.Detail }

// Check connects to domain, reads the certificate and judges it.
//
// The handshake itself does not verify the chain: the leaf has to be read to compute the pin even when
// the chain is untrusted, and a probe with a pin does not need a CA at all. Verification is done
// explicitly afterwards, against opts.Roots, so the result can say both "this is the certificate" and
// "this chain is (not) trusted".
func Check(ctx context.Context, domain string, opts Options) (Result, error) {
	if domain == "" {
		return Result{}, &Error{Class: ClassUnreachable, Detail: "no hostname to check"}
	}
	if opts.Port == 0 {
		opts.Port = 443
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}

	host := opts.Address
	if host == "" {
		host = domain
	}
	dialer := net.Dialer{Timeout: opts.Timeout}
	conn, err := tls.DialWithDialer(&dialer, "tcp", net.JoinHostPort(host, fmt.Sprint(opts.Port)), &tls.Config{
		ServerName: domain,
		// Reading the leaf is the point; the chain is judged below, where the reason can be reported.
		InsecureSkipVerify: true, //nolint:gosec // verified explicitly with leaf.Verify.
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return Result{}, &Error{Class: ClassUnreachable, Detail: err.Error()}
	}
	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return Result{}, &Error{Class: ClassUnreachable, Detail: "the server presented no certificate"}
	}
	leaf := state.PeerCertificates[0]
	sum := sha256.Sum256(leaf.Raw)
	result := Result{
		Fingerprint: hex.EncodeToString(sum[:]),
		NotAfter:    leaf.NotAfter,
		DNSNames:    append([]string(nil), leaf.DNSNames...),
	}

	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	now := opts.Now()
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         opts.Roots,
		Intermediates: intermediates,
		DNSName:       domain,
		CurrentTime:   now,
	}); err != nil {
		// A hostname problem is a different answer than an untrusted chain: the owner has to fix the
		// certificate, not the trust store.
		class := ClassInvalidChain
		var hostnameErr x509.HostnameError
		if errors.As(err, &hostnameErr) {
			class = ClassHostname
		}
		// Expiry is worth its own class: it is the one certificate failure that is unambiguously about
		// the node, while a chain failure can also mean the checker trusts the wrong root.
		var invalid x509.CertificateInvalidError
		if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
			class = ClassNotYetValid
			if !now.Before(leaf.NotAfter) {
				class = ClassExpired
			}
		}
		return result, &Error{Class: class, Detail: err.Error()}
	}
	result.ChainVerified = true

	if remaining := leaf.NotAfter.Sub(now); remaining < MinValidity {
		return result, &Error{
			Class:  ClassExpiring,
			Detail: fmt.Sprintf("the certificate expires in %s; renew it and enroll again", remaining.Round(time.Hour)),
		}
	}
	return result, nil
}
