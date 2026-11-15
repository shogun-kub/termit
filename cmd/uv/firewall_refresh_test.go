package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// listServer serves a network list over HTTP and lets a test change it between calls. It is the
// transport a real operator uses; `file://` is the lab's, and the firewall package tests it there.
type listServer struct {
	server *httptest.Server
	body   string
	status int
}

func newListServer(t *testing.T, body string) *listServer {
	t.Helper()
	lists := &listServer{body: body, status: http.StatusOK}
	lists.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if lists.status != http.StatusOK {
			w.WriteHeader(lists.status)
			return
		}
		fmt.Fprint(w, lists.body)
	}))
	t.Cleanup(lists.server.Close)
	return lists
}

func (l *listServer) url() string { return l.server.URL + "/list.txt" }

// blocklistFixture is the firewall fixture with a table applied.
func blocklistFixture(t *testing.T) *firewallFixture {
	t.Helper()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("firewall on = %d: %s", got, f.stderr.String())
	}
	f.host.cmds = nil
	f.host.writes = nil
	f.stdout.Reset()
	f.stderr.Reset()
	return f
}

// listPath is where the fixture's node keeps the list in force.
func (f *firewallFixture) listPath(t *testing.T) string {
	t.Helper()
	cfg, err := f.app.loadConfig(f.configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg.Paths.BlocklistPath()
}

// exitFailed renders the buffers of a failed command.
func exitFailed(f *firewallFixture) string {
	return f.stdout.String() + f.stderr.String()
}

// TestFirewallRefreshLoadsTheList: the file and the kernel end up holding the same networks, and the
// batch that put them there flushed the sets first.
func TestFirewallRefreshLoadsTheList(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "192.0.2.0/24\n198.51.100.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
		t.Fatalf("refresh = %d: %s", got, exitFailed(f))
	}
	if !strings.Contains(f.stdout.String(), "2 networks, loaded") {
		t.Errorf("output = %q", f.stdout.String())
	}
	inForce, err := os.ReadFile(f.listPath(t))
	if err != nil {
		t.Fatalf("read the list in force: %v", err)
	}
	for _, want := range []string{"192.0.2.0/24", "198.51.100.0/24"} {
		if !strings.Contains(string(inForce), want) {
			t.Errorf("the file in force is missing %q:\n%s", want, inForce)
		}
	}
	batch := strings.Join(f.host.writes, "\n")
	for _, want := range []string{"flush set inet termit_uv blocked4", "192.0.2.0/24"} {
		if !strings.Contains(batch, want) {
			t.Errorf("the kernel did not receive %q:\n%s", want, batch)
		}
	}
}

// TestFirewallRefreshKeepsTheListInForceWhenTheSourceIsBroken: a truncated download or an empty list
// must not unblock the host.
func TestFirewallRefreshKeepsTheListInForceWhenTheSourceIsBroken(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"truncated":      "192.0.2.0/24\n198.51.100",
		"empty":          "# nothing here\n\n",
		"not a list":     "<html>404</html>\n",
		"nothing at all": "",
		"bad prefix":     "192.0.2.0/33\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := blocklistFixture(t)
			// First a good list, so there is something to keep.
			lists := newListServer(t, "203.0.113.0/24\n")
			if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
				t.Fatalf("the good list was refused: %d %s", got, exitFailed(f))
			}
			before, err := os.ReadFile(f.listPath(t))
			if err != nil {
				t.Fatalf("read the list in force: %v", err)
			}
			f.host.writes = nil
			f.stdout.Reset()
			f.stderr.Reset()

			lists.body = body
			if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitFailure {
				t.Fatalf("refresh accepted a broken source: %d\n%s", got, f.stdout.String())
			}
			after, err := os.ReadFile(f.listPath(t))
			if err != nil {
				t.Fatalf("read the list in force: %v", err)
			}
			if string(after) != string(before) {
				t.Errorf("the list in force changed:\nbefore %s\nafter  %s", before, after)
			}
			if len(f.host.writes) != 0 {
				t.Errorf("the kernel was changed anyway: %v", f.host.writes)
			}
		})
	}
}

// TestFirewallRefreshSurvivesAnUnreachableSource: the list in force stays, and the message says so.
func TestFirewallRefreshSurvivesAnUnreachableSource(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "203.0.113.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
		t.Fatalf("the good list was refused: %d", got)
	}
	lists.status = http.StatusInternalServerError
	f.stdout.Reset()
	f.stderr.Reset()
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitFailure {
		t.Fatalf("refresh accepted a 500: %d", got)
	}
	if !strings.Contains(f.stderr.String(), "untouched") {
		t.Errorf("the failure does not say the list is untouched: %q", f.stderr.String())
	}
}

// TestFirewallRefreshReloadsAnUnchangedList: the file is the record of truth, so a source that has not
// changed still gets loaded — the kernel may have lost the sets to a reboot or a failed load.
func TestFirewallRefreshReloadsAnUnchangedList(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "192.0.2.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
		t.Fatalf("refresh = %d: %s", got, exitFailed(f))
	}
	// The server answers 304 from now on, which is what an unchanged source looks like.
	notModified := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer notModified.Close()
	f.host.writes = nil
	f.stdout.Reset()
	if got := f.run("firewall", "refresh", "--url", notModified.URL+"/list.txt"); got != exitOK {
		t.Fatalf("second refresh = %d: %s", got, exitFailed(f))
	}
	if !strings.Contains(f.stdout.String(), "has not changed") {
		t.Errorf("output = %q", f.stdout.String())
	}
	if len(f.host.writes) == 0 {
		t.Error("the kernel was not reloaded when the source was unchanged")
	}
}

// TestFirewallRefreshNeedsAURL: an operator who never configured one gets a sentence, not a fetch of
// something arbitrary.
func TestFirewallRefreshNeedsAURL(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "refresh"); got != exitUsage {
		t.Fatalf("refresh without a URL = %d, want %d: %s", got, exitUsage, f.stderr.String())
	}
	if !strings.Contains(f.stderr.String(), "blocklist_url") {
		t.Errorf("the message does not name the key: %q", f.stderr.String())
	}
}

// TestFirewallRefreshDryRun: --dry-run parses and reports, and changes neither the file nor the kernel.
func TestFirewallRefreshDryRun(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "192.0.2.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url(), "--dry-run"); got != exitOK {
		t.Fatalf("refresh --dry-run = %d: %s", got, exitFailed(f))
	}
	if !strings.Contains(f.stdout.String(), "would replace") {
		t.Errorf("output = %q", f.stdout.String())
	}
	if _, err := os.Stat(f.listPath(t)); !os.IsNotExist(err) {
		t.Errorf("the dry run wrote the list: %v", err)
	}
	if len(f.host.writes) != 0 {
		t.Errorf("the dry run touched the kernel: %v", f.host.writes)
	}
}

// TestFirewallOnRestoresTheList: `on` starts from empty sets, so the list in force goes back in before
// the operator walks away.
func TestFirewallOnRestoresTheList(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "192.0.2.0/24\n198.51.100.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
		t.Fatalf("refresh = %d: %s", got, exitFailed(f))
	}
	if got := f.run("firewall", "off"); got != exitOK {
		t.Fatalf("off = %d", got)
	}
	f.host.writes = nil
	f.stdout.Reset()
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("on = %d: %s", got, exitFailed(f))
	}
	if !strings.Contains(f.stdout.String(), "blocklist: 2 networks loaded") {
		t.Errorf("output = %q", f.stdout.String())
	}
	if batch := strings.Join(f.host.writes, "\n"); !strings.Contains(batch, "192.0.2.0/24") {
		t.Errorf("the list was not loaded after `on`: %s", batch)
	}
}

// TestFirewallStatusReportsTheList: the status says how many networks are in force and when they were
// fetched.
func TestFirewallStatusReportsTheList(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "192.0.2.0/24\n198.51.100.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
		t.Fatalf("refresh = %d: %s", got, exitFailed(f))
	}
	f.stdout.Reset()
	if got := f.run("firewall", "status"); got != exitOK {
		t.Fatalf("status = %d: %s", got, exitFailed(f))
	}
	out := f.stdout.String()
	if !strings.Contains(out, "2 networks") || !strings.Contains(out, "fetched") {
		t.Errorf("status output = %q", out)
	}
	f.stdout.Reset()
	if got := f.run("firewall", "status", "--json"); got != exitOK {
		t.Fatalf("status --json = %d", got)
	}
	if !strings.Contains(f.stdout.String(), `"blocklist": 2`) {
		t.Errorf("json = %q", f.stdout.String())
	}
}

// TestBlocklistInForceIsWhatFirewallOnLoads is a cheap guard against the two files drifting: the list
// the status reads is the same one `firewall on` loads.
func TestBlocklistInForceIsWhatFirewallOnLoads(t *testing.T) {
	t.Parallel()
	f := blocklistFixture(t)
	lists := newListServer(t, "192.0.2.0/24\n198.51.100.0/24\n")
	if got := f.run("firewall", "refresh", "--url", lists.url()); got != exitOK {
		t.Fatalf("refresh = %d: %s", got, exitFailed(f))
	}
	cfg, err := f.app.loadConfig(f.configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	prefixes, fetchedAt, err := firewall.LoadBlocklist(cfg.Paths.BlocklistPath())
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if len(prefixes) != 2 || fetchedAt.IsZero() {
		t.Errorf("the list in force holds %d networks, fetched at %v", len(prefixes), fetchedAt)
	}
	if age := time.Since(fetchedAt); age > time.Hour {
		t.Errorf("the file's modification time is %s old", age)
	}
}
