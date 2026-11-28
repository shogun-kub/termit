package dns_test

import (
	"context"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/dns"
)

// The callers count answers, not errors (a resolver that cannot answer is not evidence against a node), so
// what matters here is that a resolver that cannot answer says so instead of hanging or inventing an answer.

func TestAServerWithoutAnAddressRefuses(t *testing.T) {
	t.Parallel()
	if _, err := (dns.Server{}).Lookup(context.Background(), "mn.example.com"); err == nil {
		t.Fatal("a resolver without an address answered")
	}
}

func TestACancelledContextStopsBothResolvers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (dns.Server{Addr: "127.0.0.1:53"}).Lookup(ctx, "mn.example.com"); err == nil {
		t.Error("the server resolver ignored a cancelled context")
	}
	if _, err := (dns.System{}).Lookup(ctx, "mn.example.com"); err == nil {
		t.Error("the system resolver ignored a cancelled context")
	}
}
