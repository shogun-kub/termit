//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestLabOwnerSurface exercises what the owner of a node sees: the wizard on an installed box, the
// page of client links, its language, and the server's menu in both languages.
//
// It is read-mostly on purpose: the only thing it changes is the page, which `options` is supposed to
// write, and the answer to "is this node installed" must be the wizard's own dry run of the pipeline —
// the real thing, not a stub.
func TestLabOwnerSurface(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	uv := l.UV2
	domain := statusDomain(t, ctx, uv)

	// The lab nodes were installed by an older build, so the first thing this binary says about them is
	// that converging would change something — the honest answer — and only after `init` does the
	// wizard call the node installed. That is also the upgrade path in miniature.
	t.Run("the wizard names an outdated node and what to run", func(t *testing.T) {
		out := uv.MustRun(t, ctx, "LANG=ru_RU.UTF-8 termit-uv wizard --config /etc/termit/uv/config.yaml < /dev/null")
		t.Logf("wizard on a node behind this build:\n%s", out)
		if !strings.Contains(out, "termit-uv init") {
			t.Errorf("the wizard did not say what to run:\n%s", out)
		}
		if strings.Contains(out, "Домен (имя в DNS") {
			t.Errorf("the wizard asked for a domain on an installed node:\n%s", out)
		}
	})

	t.Run("after converge, the wizard reports an installed node instead of asking", func(t *testing.T) {
		if out, err := uv.Run(ctx, "termit-uv init --config /etc/termit/uv/config.yaml < /dev/null"); err != nil {
			t.Fatalf("converge: %v\n%s", err, out)
		}
		out := uv.MustRun(t, ctx, "LANG=ru_RU.UTF-8 termit-uv wizard --config /etc/termit/uv/config.yaml < /dev/null")
		t.Logf("wizard:\n%s", out)
		if !strings.Contains(out, "уже установлен") {
			t.Errorf("the wizard did not recognise the installed node:\n%s", out)
		}
		if strings.Contains(out, "Домен (имя в DNS") {
			t.Errorf("the wizard asked for a domain on an installed node:\n%s", out)
		}
		// The console is UTF-8, so the Russian menu must not have fallen back.
		if strings.Contains(out, "already installed") {
			t.Errorf("the wizard fell back to English on a UTF-8 console:\n%s", out)
		}
	})

	t.Run("the page is rendered, readable and speaks Russian", func(t *testing.T) {
		url := strings.TrimSpace(uv.MustRun(t, ctx, "termit-uv options --config /etc/termit/uv/config.yaml"))
		if !strings.HasPrefix(url, "https://"+domain+"/") || !strings.HasSuffix(url, "/") {
			t.Fatalf("the printed address is %q", url)
		}
		page := uv.MustRun(t, ctx, "curl -sS --max-time 30 "+url)
		for _, want := range []string{`lang="ru"`, "Этот узел", "Подписка", "Эта страница", url} {
			if !strings.Contains(page, want) {
				t.Errorf("the page does not contain %q", want)
			}
		}
		// The lab has three nodes: the subscription lists all three, and the page must not count this
		// node among the peers it is shown next to.
		if !strings.Contains(page, "Mesh (2)") {
			t.Errorf("the page does not list the two other nodes, or still counts this one")
		}
		// A wrong path must not answer with the page: the address is the only secret.
		if got := uv.MustRun(t, ctx, "curl -sS -o /dev/null -w '%{http_code}' https://"+domain+
			"/0123456789abcdef0123456789abcdef/"); strings.TrimSpace(got) != "404" {
			t.Errorf("a guessed path answered %q, want 404", strings.TrimSpace(got))
		}
	})

	t.Run("status prints the page address", func(t *testing.T) {
		out := uv.MustRun(t, ctx, "termit-uv status --config /etc/termit/uv/config.yaml")
		if !strings.Contains(out, "options:      https://"+domain+"/") {
			t.Errorf("status does not print the page address:\n%s", out)
		}
	})

	t.Run("the server menu speaks Russian on a UTF-8 console", func(t *testing.T) {
		out := l.MN.MustRun(t, ctx, "LANG=ru_RU.UTF-8 termit-mn menu --token-file /etc/termit/mn/admin.token < /dev/null")
		t.Logf("menu:\n%s", oneLine(out))
		if !strings.Contains(out, "Termit — сервер") || !strings.Contains(out, "состояние") {
			t.Errorf("the server menu is not Russian:\n%s", out)
		}
	})

	t.Run("the server menu falls back to English on a C console", func(t *testing.T) {
		out := l.MN.MustRun(t, ctx, "LANG=C LC_ALL=C termit-mn menu --token-file /etc/termit/mn/admin.token < /dev/null")
		if !strings.Contains(out, "Termit — server") {
			t.Errorf("the server menu did not fall back to English:\n%s", out)
		}
		if strings.Contains(out, "состояние") {
			t.Errorf("Cyrillic leaked onto a console that cannot show it:\n%s", out)
		}
	})
}
