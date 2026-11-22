package options_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/uv/options"
)

// A phone has no shared clipboard with the node, so every link it needs must be scannable. The QR is
// drawn in-process because the page may not reference an external host: an encoder service would hand the
// credentials to a third party and make the page a fingerprint.
func TestEveryLinkCanBeScanned(t *testing.T) {
	t.Parallel()
	dense := "vless://" + strings.Repeat("a", 500) + "@node.example.com:443?type=xhttp&security=tls#node"
	page := renderPage(t, options.Data{
		Domain: "node.example.com",
		Token:  "0123456789abcdef0123456789abcdef",
		Local:  []options.Link{{Label: "node.example.com", URI: dense}},
		Devices: []options.Link{
			{Label: "tablet", URI: "hy2://user:secret@node.example.com:443?sni=node.example.com#tablet"},
		},
		Peers: []options.Link{
			{Label: "then2", URI: "vless://b@then2.example.com:443?type=xhttp#then2"},
		},
		Generated: time.Unix(0, 0).UTC(),
	})

	if got := strings.Count(page, `<details class="qr">`); got != 3 {
		t.Fatalf("the page shows %d codes, want one per link (3)", got)
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("the page references a host; a decoy-adjacent page that phones home is a fingerprint")
	}
	// Every code carries its quiet zone inside the viewBox, so its side is a QR dimension plus 8.
	for _, side := range svgSides(page) {
		if side%4 != 1 {
			t.Errorf("viewBox side %d is not a QR dimension plus a quiet zone", side)
		}
	}
}

// A URI the encoder cannot fit degrades to the copy button, and the rest of the page is untouched.
func TestAnUnfittableLinkKeepsItsCopyButton(t *testing.T) {
	t.Parallel()
	long := "vless://" + strings.Repeat("x", 4000) + "@node.example.com:443#huge"
	page := renderPage(t, options.Data{
		Domain:    "node.example.com",
		Token:     "0123456789abcdef0123456789abcdef",
		Local:     []options.Link{{Label: "node.example.com", URI: "hy2://u:p@node.example.com:443#node"}},
		Devices:   []options.Link{{Label: "huge", URI: long}},
		Generated: time.Unix(0, 0).UTC(),
	})

	if got := strings.Count(page, `<details class="qr">`); got != 1 {
		t.Errorf("the page shows %d codes, want only the link that fits (1)", got)
	}
	if !strings.Contains(page, long) {
		t.Error("the long link lost its row; the copy button must survive an unfittable code")
	}
}

func renderPage(t *testing.T, data options.Data) string {
	t.Helper()
	data.Lang = i18n.Default
	body, err := options.Render(data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return string(body)
}

// svgSides returns the viewBox side of every code on the page.
func svgSides(page string) []int {
	var sides []int
	for _, part := range strings.Split(page, `<svg viewBox="0 0 `)[1:] {
		n := 0
		for n < len(part) && part[n] >= '0' && part[n] <= '9' {
			n++
		}
		if n > 0 {
			side := 0
			for _, c := range part[:n] {
				side = side*10 + int(c-'0')
			}
			sides = append(sides, side)
		}
	}
	return sides
}
