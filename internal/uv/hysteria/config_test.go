package hysteria_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/hysteria"
)

var update = flag.Bool("update", false, "rewrite the golden file")

func testSpec() hysteria.Spec {
	return hysteria.Spec{
		CertFile: "/var/lib/termit/uv/certs/fullchain.pem",
		KeyFile:  "/var/lib/termit/uv/certs/privkey.pem",
		Users: map[string]string{
			"owner": "Kf3s0Q2rZx8pWv1nYb6tLd4hJm9cRa5u",
		},
		HopAddress:    "127.0.0.1:10801",
		MasqueradeURL: "https://127.0.0.1:443",
	}
}

// TestGenerateGolden pins the document the transport reads. It is the contract with a third-party binary,
// so a change a reviewer cannot see in a diff is a change nobody can review.
func TestGenerateGolden(t *testing.T) {
	t.Parallel()
	data, err := hysteria.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden := filepath.Join("testdata", "hysteria.yaml")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- a fixed test path.
	if err != nil {
		t.Fatalf("read golden file: %v (run `go test ./internal/uv/hysteria -update`)", err)
	}
	if !bytes.Equal(want, data) {
		t.Errorf("%s differs from the generated config; regenerate with -update and review the diff", golden)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	t.Parallel()
	first, err := hysteria.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := hysteria.Generate(testSpec())
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d produced different bytes", i+2)
		}
	}
}

// TestNoBrutal: the transport must queue like every other sender. Brutal shapes to a bandwidth it was
// told to assume, which is a fingerprint and a network bully, and this project refuses both.
func TestNoBrutal(t *testing.T) {
	t.Parallel()
	data, err := hysteria.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "type: bbr") {
		t.Errorf("the document does not select bbr:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), "brutal") {
		t.Errorf("the document mentions Brutal:\n%s", text)
	}
	if !strings.Contains(text, "ignoreClientBandwidth: true") {
		t.Errorf("the document does not ignore a client's claimed bandwidth:\n%s", text)
	}
}

// TestEveryRequestGoesThroughTheCore: the whole point of the hop is that the transport does not decide
// where traffic leaves. One catch-all ACL rule, and one outbound: the core.
func TestEveryRequestGoesThroughTheCore(t *testing.T) {
	t.Parallel()
	data, err := hysteria.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	text := string(data)
	for _, want := range []string{"addr: 127.0.0.1:10801", "outbound: core", "type: socks5"} {
		if !strings.Contains(text, want) {
			t.Errorf("the document is missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateRefusesBrokenSpecs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*hysteria.Spec)
		want   string
	}{
		{"no users", func(s *hysteria.Spec) { s.Users = nil }, "at least one user"},
		{"empty user name", func(s *hysteria.Spec) { s.Users = map[string]string{"": "Kf3s0Q2rZx8pWv1nYb6t"} }, "empty name"},
		{"upper-case user", func(s *hysteria.Spec) { s.Users = map[string]string{"Owner": "Kf3s0Q2rZx8pWv1nYb6t"} }, "must be lower case"},
		{"colon in the name", func(s *hysteria.Spec) { s.Users = map[string]string{"own:er": "Kf3s0Q2rZx8pWv1nYb6t"} }, "must not contain a colon"},
		{"short password", func(s *hysteria.Spec) { s.Users = map[string]string{"owner": "short"} }, "shorter than"},
		{"relative certificate", func(s *hysteria.Spec) { s.CertFile = "fullchain.pem" }, "not an absolute path"},
		{"no key", func(s *hysteria.Spec) { s.KeyFile = "" }, "key is required"},
		{"bad listen", func(s *hysteria.Spec) { s.Listen = "443" }, "not host:port"},
		{"bad hop", func(s *hysteria.Spec) { s.HopAddress = "127.0.0.1" }, "hop address"},
		{"http masquerade", func(s *hysteria.Spec) { s.MasqueradeURL = "http://127.0.0.1:80" }, "not an https URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := testSpec()
			tc.mutate(&spec)
			_, err := hysteria.Generate(spec)
			if err == nil {
				t.Fatalf("Generate accepted a spec it should refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
