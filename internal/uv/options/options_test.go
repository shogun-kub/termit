package options_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/uv/options"
)

func TestNewTokenIsHexAndRandom(t *testing.T) {
	t.Parallel()
	first, err := options.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	second, err := options.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(first) != options.TokenBytes*2 {
		t.Errorf("token %q has %d characters, want %d", first, len(first), options.TokenBytes*2)
	}
	if !options.ValidToken(first) {
		t.Errorf("ValidToken rejected a token it produced: %q", first)
	}
	if first == second {
		t.Errorf("two tokens are identical: %q", first)
	}
}

func TestValidTokenRejectsPathsAndParagraphs(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"",
		"..",
		"../../../etc",
		"abc",
		strings.Repeat("a", options.TokenBytes*2-1),
		strings.Repeat("a", options.TokenBytes*2+1),
		strings.Repeat("z", options.TokenBytes*2),
		strings.Repeat("a", options.TokenBytes*2-1) + "/",
	} {
		if options.ValidToken(value) {
			t.Errorf("ValidToken accepted %q", value)
		}
	}
}

func TestParseListReadsTheSubscriptionLikeAClient(t *testing.T) {
	t.Parallel()
	uris := []string{
		"vless://11111111-2222-4333-8444-555555555555@one.example.com:443?type=xhttp#one.example.com",
		"vless://11111111-2222-4333-8444-555555555555@two.example.com:443?type=xhttp#two.example.com (degraded)",
		"vless://no-label.example.com:443?type=xhttp",
	}
	body := base64.StdEncoding.EncodeToString([]byte(strings.Join(uris, "\n") + "\n"))
	links, err := options.ParseList([]byte(body))
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("parsed %d links, want 3", len(links))
	}
	if links[0].Label != "one.example.com" || links[0].Degraded {
		t.Errorf("first link = %+v", links[0])
	}
	if !links[1].Degraded {
		t.Errorf("the degraded marker was not recognised: %+v", links[1])
	}
	if links[2].Label == "" {
		t.Errorf("a link without a label lost its label entirely: %+v", links[2])
	}

	if _, err := options.ParseList([]byte("not base64 %%%")); err == nil {
		t.Error("ParseList accepted something that is not base64")
	}
}

func TestRenderCarriesEveryLinkAndSaysWhatItIs(t *testing.T) {
	t.Parallel()
	data := options.Data{
		Domain: "node.example.com",
		Lang:   i18n.EN,
		Token:  strings.Repeat("ab", options.TokenBytes),
		Local: options.Link{
			Label: "node.example.com",
			URI:   "vless://self@node.example.com:443?type=xhttp&security=tls#node.example.com",
		},
		Devices: []options.Link{
			{Label: "phone", URI: "vless://dev@node.example.com:443?type=xhttp#phone"},
		},
		Peers: []options.Link{
			{Label: "far.example.com", URI: "vless://peer@far.example.com:443?a=1&b=2#far.example.com"},
			{Label: "slow.example.com", URI: "vless://peer@slow.example.com:443#slow.example.com (degraded)", Degraded: true},
		},
		SubscriptionURL: "https://mn.example.com/sub/token",
		JSONURL:         "https://mn.example.com/sub/token/json",
		Generated:       time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC),
	}
	data.PageURL = options.PageURL(data.Domain, data.Token)

	page, err := options.Render(data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := string(page)
	for _, want := range []string{
		data.Local.URI[:40],
		"Devices (1)",
		"phone",
		"far.example.com",
		"slow.example.com",
		`noindex`,
		data.SubscriptionURL,
		data.JSONURL,
		data.PageURL,
		"2026-11-15 09:00 UTC",
		"degraded",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not contain %q", want)
		}
	}
	// A URI holds query separators; written raw into HTML they would end the tag attribute or the
	// text node in a way a browser renders differently from what a client copies.
	if !strings.Contains(html, "&amp;") {
		t.Error("the page does not escape the ampersands in a URI, so copying it from the browser would not round-trip")
	}
	if strings.Contains(html, "http://") || strings.Contains(html, "//cdn") {
		t.Error("the page references an external host; a decoy-adjacent page that phones home is a fingerprint")
	}
}

func TestRenderWithoutPeersIsHonest(t *testing.T) {
	t.Parallel()
	page, err := options.Render(options.Data{
		Domain: "node.example.com",
		Lang:   i18n.EN,
		Token:  strings.Repeat("cd", options.TokenBytes),
		Local:  options.Link{Label: "node.example.com", URI: "vless://self@node.example.com:443"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := string(page)
	if !strings.Contains(html, "No peers") {
		t.Error("a page without peers does not say that the node is not enrolled")
	}
	if strings.Contains(html, "Subscription") {
		t.Error("a page without a subscription URL still renders its section")
	}
}

func TestRenderShowsAWarningInsteadOfAnEmptyMesh(t *testing.T) {
	t.Parallel()
	page, err := options.Render(options.Data{
		Domain:  "node.example.com",
		Lang:    i18n.EN,
		Token:   strings.Repeat("ef", options.TokenBytes),
		Local:   options.Link{Label: "node.example.com", URI: "vless://self@node.example.com:443"},
		Warning: "The mesh could not be fetched just now.",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := string(page)
	if !strings.Contains(html, "could not be fetched") {
		t.Error("the warning is missing from the page")
	}
	if strings.Contains(html, "No peers") {
		t.Error("the page claims the node is not enrolled while it is only unreachable")
	}
}

func TestRenderInRussian(t *testing.T) {
	t.Parallel()
	page, err := options.Render(options.Data{
		Domain: "node.example.com",
		Lang:   i18n.RU,
		Token:  strings.Repeat("ab", options.TokenBytes),
		Local:  options.Link{Label: "node.example.com", URI: "vless://self@node.example.com:443#node.example.com"},
		Peers: []options.Link{
			{Label: "pesok.example.com", URI: "vless://peer@pesok.example.com:443#pesok.example.com", Degraded: true},
		},
		Warning:   "",
		Generated: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := string(page)
	for _, want := range []string{
		`lang="ru"`,
		"Ссылки для клиентов",
		"Этот узел",
		"Mesh (1)",
		"замедлен",
		"копировать",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the Russian page does not contain %q", want)
		}
	}
	if strings.Contains(html, "degraded") {
		t.Error("the Russian page still carries the English badge")
	}
}
