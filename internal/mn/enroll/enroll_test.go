package enroll_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

const (
	testDomain = "node1.example.com"
	testIP     = "203.0.113.10"
	testUUID   = "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func testURL(t *testing.T, domain string) string {
	t.Helper()
	raw, err := proto.BuildVLESSUrl(proto.Profile{Domain: domain, UUID: testUUID, Path: "/secret/"})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	return raw
}

type staticResolver struct {
	ips []string
	err error
}

func (r staticResolver) Lookup(ctx context.Context, domain string) ([]net.IP, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]net.IP, 0, len(r.ips))
	for _, ip := range r.ips {
		out = append(out, net.ParseIP(ip))
	}
	return out, nil
}

type fakeProber struct {
	outcome enroll.Outcome
	// seen records the pin the enrollment passed to the probe.
	seen string
}

func (p *fakeProber) Probe(ctx context.Context, target enroll.Target) enroll.Outcome {
	p.seen = target.Pin
	return p.outcome
}

type fakeCerts struct{ err error }

const testPin = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

func (c fakeCerts) Check(ctx context.Context, domain string) (enroll.Pin, error) {
	if c.err != nil {
		return enroll.Pin{}, c.err
	}
	return enroll.Pin{Fingerprint: testPin, NotAfter: testNow.Add(60 * 24 * time.Hour)}, nil
}

func newService(t *testing.T, mode string, resolver enroll.Resolver, prober enroll.Prober) (*enroll.Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resolvers := []enroll.Resolver{resolver}
	service, err := enroll.New(enroll.Options{
		Store: db, Mode: mode, Resolvers: resolvers, Prober: prober, Certs: fakeCerts{},
		Now:   func() time.Time { return testNow },
		NewID: func() string { return "uv_test" },
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	return service, db
}

func createInvite(t *testing.T, db *store.Store, code string, uses int, expires *time.Time) string {
	t.Helper()
	id := "inv_" + strings.ToUpper(code)
	err := db.CreateInvite(context.Background(), store.Invite{
		ID: id, CodeHash: secret.Digest(strings.ToUpper(code)), CreatedAt: testNow,
		MaxUses: uses, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	return id
}

func request(t *testing.T, code string) proto.EnrollRequest {
	t.Helper()
	return proto.EnrollRequest{
		InviteCode:    code,
		Domain:        testDomain,
		BasicVLESSURL: testURL(t, testDomain),
		AgentVersion:  "termit-uv/test",
		Profile:       proto.ProfileV1,
	}
}

func TestEnrollActivatesWithAValidInvite(t *testing.T) {
	t.Parallel()
	service, db := newService(t, config.ModeInvite, staticResolver{ips: []string{testIP}}, &fakeProber{})
	createInvite(t, db, "ABCD-EFGH", 1, nil)

	result, err := service.Enroll(context.Background(), request(t, "abcd-efgh"), testIP, "termit-uv/test")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if result.UV.State != store.StateActive {
		t.Errorf("state = %s, want active", result.UV.State)
	}
	if result.Secrets.AgentKey == "" || result.Secrets.SubToken == "" || result.Secrets.ProbeUUID == "" {
		t.Error("the response is missing a credential")
	}
	if result.Secrets.AgentKey == result.Secrets.SubToken {
		t.Error("the agent key and the subscription token are the same value")
	}
	if !strings.HasPrefix(result.Secrets.SubscriptionURL, "/sub/") {
		t.Errorf("subscription URL = %q", result.Secrets.SubscriptionURL)
	}

	// The stored node holds digests, never the secrets themselves.
	stored, err := db.UVByID(context.Background(), "uv_test")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if stored.AgentKeyHash == result.Secrets.AgentKey || stored.AgentKeyHash == "" {
		t.Error("the agent key is not stored as a digest")
	}
	if stored.AgentKeyHash != secret.Digest(result.Secrets.AgentKey) {
		t.Error("the stored digest does not match the issued key")
	}
	// The invite was consumed exactly once.
	invite, err := db.InviteByCodeHash(context.Background(), secret.Digest("ABCD-EFGH"))
	if err != nil {
		t.Fatalf("InviteByCodeHash: %v", err)
	}
	if invite.UsedCount != 1 {
		t.Errorf("invite used %d times, want 1", invite.UsedCount)
	}
}

func TestEnrollParksTheNodeInApprovalMode(t *testing.T) {
	t.Parallel()
	service, db := newService(t, config.ModeInviteApproval, staticResolver{ips: []string{testIP}}, &fakeProber{})
	createInvite(t, db, "CODE-1234", 1, nil)

	result, err := service.Enroll(context.Background(), request(t, "CODE-1234"), testIP, "termit-uv/test")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if result.UV.State != store.StatePending {
		t.Errorf("state = %s, want pending", result.UV.State)
	}
	if result.Secrets.Notice == "" {
		t.Error("a pending node was not told why it is pending")
	}
	if result.UV.ApprovedAt != nil {
		t.Error("a pending node has an approval timestamp")
	}
}

func TestEnrollRefusals(t *testing.T) {
	t.Parallel()
	expired := testNow.Add(-time.Hour)
	for _, tc := range []struct {
		name     string
		code     string
		invite   *time.Time
		uses     int
		noInvite bool
		drain    bool
		resolve  enroll.Resolver
		prober   enroll.Prober
		want     string
	}{
		{
			name: "unknown invite", code: "NOPE-NOPE", noInvite: true,
			resolve: staticResolver{ips: []string{testIP}}, prober: &fakeProber{},
			want: proto.CodeInviteInvalid,
		},
		{
			name: "expired invite", code: "OLD-CODE", invite: &expired, uses: 1,
			resolve: staticResolver{ips: []string{testIP}}, prober: &fakeProber{}, want: proto.CodeInviteInvalid,
		},
		{
			name: "exhausted invite", code: "USED-UP", uses: 1, drain: true,
			resolve: staticResolver{ips: []string{testIP}}, prober: &fakeProber{}, want: proto.CodeInviteExhausted,
		},
		{
			name: "domain resolves elsewhere", code: "CODE-A", uses: 1,
			resolve: staticResolver{ips: []string{"198.51.100.7"}}, prober: &fakeProber{}, want: proto.CodeDNSMismatch,
		},
		{
			name: "no resolver answers", code: "CODE-B", uses: 1,
			resolve: staticResolver{err: errors.New("timeout")}, prober: &fakeProber{}, want: proto.CodeDNSMismatch,
		},
		{
			name: "prober cannot check", code: "CODE-C", uses: 1,
			resolve: staticResolver{ips: []string{testIP}}, prober: &fakeProber{outcome: enroll.Outcome{Unknown: true}},
			want: proto.CodeCheckFailed,
		},
		{
			name: "node does not proxy", code: "CODE-D", uses: 1,
			resolve: staticResolver{ips: []string{testIP}},
			prober:  &fakeProber{outcome: enroll.Outcome{Class: "tunnel_closed", Err: errors.New("closed")}},
			want:    proto.CodeCheckFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, db := newService(t, config.ModeInvite, tc.resolve, tc.prober)
			if !tc.noInvite {
				id := createInvite(t, db, tc.code, tc.uses, tc.invite)
				if tc.drain {
					if err := db.RedeemInvite(context.Background(), id, testNow); err != nil {
						t.Fatalf("RedeemInvite: %v", err)
					}
				}
			}

			_, err := service.Enroll(context.Background(), request(t, tc.code), testIP, "termit-uv/test")
			if err == nil {
				t.Fatal("Enroll accepted a request it should have refused")
			}
			var refused *enroll.Error
			if !errors.As(err, &refused) {
				t.Fatalf("error %v is not an enrollment refusal", err)
			}
			if refused.Code != tc.want {
				t.Errorf("code = %s, want %s", refused.Code, tc.want)
			}
			// A refusal must not have burned the invite or created a node. The drained invite is the
			// exception: it was consumed by the fixture, not by the refusal.
			if !tc.drain {
				invite, inviteErr := db.InviteByCodeHash(context.Background(), secret.Digest(strings.ToUpper(tc.code)))
				if inviteErr == nil && invite.UsedCount != 0 {
					t.Errorf("a refused enrollment consumed the invite (%d uses)", invite.UsedCount)
				}
			}
			if _, nodeErr := db.UVByDomain(context.Background(), testDomain); !errors.Is(nodeErr, store.ErrNotFound) {
				t.Errorf("a refused enrollment created a node: %v", nodeErr)
			}
		})
	}
}

func TestEnrollRefusesADuplicateDomain(t *testing.T) {
	t.Parallel()
	service, db := newService(t, config.ModeInvite, staticResolver{ips: []string{testIP}}, &fakeProber{})
	createInvite(t, db, "CODE-ONE", 2, nil)
	createInvite(t, db, "CODE-TWO", 2, nil)

	if _, err := service.Enroll(context.Background(), request(t, "CODE-ONE"), testIP, "agent"); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	_, err := service.Enroll(context.Background(), request(t, "CODE-TWO"), testIP, "agent")
	var refused *enroll.Error
	if !errors.As(err, &refused) || refused.Code != proto.CodeDomainTaken {
		t.Fatalf("second Enroll = %v, want %s", err, proto.CodeDomainTaken)
	}
}

func TestEnrollRefusesAMismatchedURL(t *testing.T) {
	t.Parallel()
	service, db := newService(t, config.ModeInvite, staticResolver{ips: []string{testIP}}, &fakeProber{})
	createInvite(t, db, "CODE-X", 1, nil)

	req := request(t, "CODE-X")
	req.BasicVLESSURL = testURL(t, "other.example.com")
	_, err := service.Enroll(context.Background(), req, testIP, "agent")
	var refused *enroll.Error
	if !errors.As(err, &refused) || refused.Code != proto.CodeDomainInvalid {
		t.Fatalf("Enroll = %v, want %s", err, proto.CodeDomainInvalid)
	}
}

func TestEnrollPinsTheCheckedCertificate(t *testing.T) {
	t.Parallel()
	prober := &fakeProber{}
	service, db := newService(t, config.ModeInvite, staticResolver{ips: []string{testIP}}, prober)
	createInvite(t, db, "PIN-PIN1", 1, nil)

	if _, err := service.Enroll(context.Background(), request(t, "PIN-PIN1"), testIP, "agent"); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if prober.seen != testPin {
		t.Errorf("the probe was not pinned to the checked certificate: %q", prober.seen)
	}
}

func TestEnrollRefusesABadCertificate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"expired", errors.New("tls_expiring: the certificate expires in 24h")},
		{"untrusted", errors.New("tls_invalid: x509: certificate signed by unknown authority")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			service, err := enroll.New(enroll.Options{
				Store: db, Mode: config.ModeInvite,
				Resolvers: []enroll.Resolver{staticResolver{ips: []string{testIP}}},
				Prober:    &fakeProber{}, Certs: fakeCerts{err: tc.err},
				Now: func() time.Time { return testNow }, NewID: func() string { return "uv_test" },
			})
			if err != nil {
				t.Fatalf("enroll.New: %v", err)
			}
			createInvite(t, db, "CERT-"+tc.name, 1, nil)

			_, err = service.Enroll(context.Background(), request(t, "CERT-"+tc.name), testIP, "agent")
			var refused *enroll.Error
			if !errors.As(err, &refused) || refused.Code != proto.CodeCheckFailed {
				t.Fatalf("Enroll = %v, want %s", err, proto.CodeCheckFailed)
			}
		})
	}
}

func TestEnrollOpenModeNeedsNoInvite(t *testing.T) {
	t.Parallel()
	service, _ := newService(t, config.ModeOpen, staticResolver{ips: []string{testIP}}, &fakeProber{})

	result, err := service.Enroll(context.Background(), request(t, ""), testIP, "agent")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if result.UV.State != store.StateActive {
		t.Errorf("state = %s, want active", result.UV.State)
	}
}
