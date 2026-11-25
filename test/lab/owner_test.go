//go:build lab

package lab_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/test/lab"
)

// TestLabOwnerSurface exercises what the owner of a node sees: the wizard on an installed box, the
// page of client links, its language, and the server's menu in both languages.
//
// It is read-mostly on purpose: the only thing it changes is the page, which `options` is supposed to
// write, and the answer to "is this node installed" must be the wizard's own dry run of the pipeline —
// the real thing, not a stub.
// countAfter returns the integer that follows prefix, or -1 when there is none. It is how a test reads
// "peers:  2 (a, b)" from status and "Меш (2)" from the page without depending on the padding.
func countAfter(text, prefix string) int {
	i := strings.Index(text, prefix)
	if i < 0 {
		return -1
	}
	rest := strings.TrimLeft(text[i+len(prefix):], " ")
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return -1
	}
	return n
}

func TestLabOwnerSurface(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	uv := l.UV2
	domain := statusDomain(t, ctx, uv)

	// The lab nodes were installed by this build, so the first thing to test is made here: one unit the
	// pipeline writes is removed, which is the smallest difference a dry run of the install notices. The
	// converge in the second case puts it back — the same measurement from the other side, and the reason
	// this test can be re-run on a converged node.
	bootUnit := "/etc/systemd/system/" + service.FirewallBootUnitName
	t.Run("the wizard names an outdated node and what to run", func(t *testing.T) {
		uv.MustRun(t, ctx, "rm -f "+bootUnit)
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
		// node among the peers it is shown next to. The heading counts machines, so a peer that serves a
		// second transport is still one row of the mesh and one node in the count.
		if !strings.Contains(page, "Меш (2)") {
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

	t.Run("a device appears on the page and leaves it again", func(t *testing.T) {
		// Re-runnable: a previous run that failed between the add and the remove leaves the device.
		_, _ = uv.Run(ctx, "termit-uv device remove --config /etc/termit/uv/config.yaml labprobe")
		url := strings.TrimSpace(uv.MustRun(t, ctx, "termit-uv options --config /etc/termit/uv/config.yaml"))
		if page := uv.MustRun(t, ctx, "curl -sS --max-time 30 "+url); strings.Contains(page, "labprobe") {
			t.Fatalf("the page already lists a device that was just removed")
		}
		uv.MustRun(t, ctx, "termit-uv device add --config /etc/termit/uv/config.yaml --label labprobe")
		page := uv.MustRun(t, ctx, "curl -sS --max-time 30 "+url)
		if !strings.Contains(page, "labprobe") {
			t.Errorf("adding a device did not refresh the page, or the page does not list it")
		}
		uv.MustRun(t, ctx, "termit-uv device remove --config /etc/termit/uv/config.yaml labprobe")
		if page := uv.MustRun(t, ctx, "curl -sS --max-time 30 "+url); strings.Contains(page, "labprobe") {
			t.Errorf("removing a device left its link on the page")
		}
	})

	t.Run("a sync redraws the page and the mesh count matches the node", func(t *testing.T) {
		url := strings.TrimSpace(uv.MustRun(t, ctx, "termit-uv options --config /etc/termit/uv/config.yaml"))
		want := countAfter(uv.MustRun(t, ctx, "termit-uv status --config /etc/termit/uv/config.yaml"), "peers:")
		if want < 0 {
			t.Fatalf("status does not say how many peers there are")
		}
		if got := countAfter(uv.MustRun(t, ctx, "curl -sS --max-time 30 "+url), "Меш ("); got != want {
			t.Errorf("the page lists %d peer(s), the node has %d", got, want)
		}
		// The mesh the page describes has to be the one the last sync applied, so a sync must rewrite
		// the file even when the peer set did not change: it is the same path the agent's daily cycle
		// takes, and the page is the only thing that would stay stale silently.
		token := url[strings.LastIndex(strings.TrimRight(url, "/"), "/")+1:]
		pageFile := "/var/www/termit/site/" + token + "/index.html"
		before := uv.MustRun(t, ctx, "stat -c %y "+pageFile)
		uv.MustRun(t, ctx, "termit-uv sync --config /etc/termit/uv/config.yaml")
		if after := uv.MustRun(t, ctx, "stat -c %y "+pageFile); after == before {
			t.Errorf("a sync left the page untouched: %s", before)
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
