package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// optionsFixture builds an installed-looking node in a temporary directory: a configuration with every
// path redirected there, a state file with an identity and an enrollment, and an HTTP client that
// answers the subscription with one peer.
func optionsFixture(t *testing.T, subscriptionBody string) (*application, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Domain = "node.example.com"
	// The tests read the page in English; the language itself is asserted separately below.
	cfg.Locale = "en"
	cfg.Paths.ConfigDir = dir
	cfg.Paths.StateDir = filepath.Join(dir, "state")
	cfg.Paths.SiteDir = filepath.Join(dir, "site")
	cfg.Paths.CertDir = filepath.Join(dir, "certs")
	cfg.Paths.BinDir = filepath.Join(dir, "bin")
	cfg.Paths.XrayConfig = filepath.Join(dir, "xray.json")
	cfg.Paths.NginxSite = filepath.Join(dir, "site.conf")
	cfg.Paths.DecoySnippet = filepath.Join(dir, "decoy.conf")
	cfg.Paths.UnitDir = filepath.Join(dir, "units")
	cfg.Paths.ConfigFile = filepath.Join(dir, "config.yaml")
	cfg.Paths.ACMEWebroot = filepath.Join(dir, "acme")
	cfg.Paths.AccessLog = filepath.Join(dir, "access.log")
	cfg.Paths.ErrorLog = filepath.Join(dir, "error.log")
	if err := os.MkdirAll(cfg.Paths.StateDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := cfg.Save(cfg.Paths.ConfigFile); err != nil {
		t.Fatalf("save config: %v", err)
	}

	node := state.New(cfg.Domain, time.Unix(0, 0).UTC())
	node.Identity = state.Identity{SelfUUID: "11111111-2222-4333-8444-555555555555", XHTTPPath: "0123456789abcdef"}
	node.Credentials = &state.Credentials{
		AgentKey:        "agent-key",
		SubscriptionURL: "https://mn.example.com/sub/token",
	}
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}

	app, _, _ := newTestApp(t)
	app.stdout = &strings.Builder{}
	app.stderr = &strings.Builder{}
	app.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(subscriptionBody)),
		}, nil
	})}
	return app, cfg, cfg.Paths.ConfigFile
}

func TestOptionsRendersThePageAndPrintsItsAddress(t *testing.T) {
	t.Parallel()
	list := base64.StdEncoding.EncodeToString([]byte(
		"vless://11111111-2222-4333-8444-555555555555@node.example.com:443?type=xhttp#node.example.com\n" +
			"vless://11111111-2222-4333-8444-555555555555@far.example.com:443?type=xhttp#far.example.com\n"))
	app, cfg, configPath := optionsFixture(t, list)

	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	printed := strings.TrimSpace(app.stdout.(*strings.Builder).String())
	if !strings.HasPrefix(printed, "https://node.example.com/") || !strings.HasSuffix(printed, "/") {
		t.Fatalf("the printed address is %q", printed)
	}

	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(node.Options.Token) != 32 {
		t.Fatalf("the state kept token %q", node.Options.Token)
	}
	if printed != "https://node.example.com/"+node.Options.Token+"/" {
		t.Errorf("the printed address does not match the state: %q", printed)
	}

	pagePath := filepath.Join(cfg.Paths.SiteDir, node.Options.Token, "index.html")
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("the page is not where nginx would find it: %v", err)
	}
	html := string(page)
	for _, want := range []string{"node.example.com", "far.example.com", "https://mn.example.com/sub/token"} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not mention %q", want)
		}
	}
	// nginx reads the tree as an unprivileged user, so the page has to be world readable. The exact
	// mode is the platform's business (Windows emulates it), the read bits are not.
	if mode := fileMode(t, pagePath); mode&0o444 != 0o444 {
		t.Errorf("the page is %o; nginx could not read it", mode)
	}
}

func TestOptionsRotateRetiresTheOldAddress(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	first, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	firstToken := first.Options.Token

	if code := app.run([]string{"options", "--rotate", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	second, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if second.Options.Token == firstToken {
		t.Fatal("--rotate kept the old token")
	}
	if _, err := os.Stat(filepath.Join(cfg.Paths.SiteDir, firstToken)); !os.IsNotExist(err) {
		t.Errorf("the old page is still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Paths.SiteDir, second.Options.Token, "index.html")); err != nil {
		t.Errorf("the new page is missing: %v", err)
	}
}

func TestOptionsSurvivesAnUnreachableServer(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	app.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})}
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, node.Options.Token, "index.html"))
	if err != nil {
		t.Fatalf("the page is missing: %v", err)
	}
	html := string(page)
	if !strings.Contains(html, "could not be fetched") {
		t.Error("the page does not say that the mesh is missing because the server did not answer")
	}
	// The URL itself belongs on the page — the owner asked for a place to copy it from — but the error
	// that came back must not be pasted in: it can quote the request, and the request carries the token.
	if strings.Contains(html, "unexpected EOF") || strings.Contains(html, "Get \"") {
		t.Error("the raw transport error was pasted onto the page")
	}
}

func TestOptionsListsTheOwnersDevices(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if err := node.AddLocalUser("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "phone", time.Unix(0, 0)); err != nil {
		t.Fatalf("add a local user: %v", err)
	}
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	node, err = state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state after the render: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, node.Options.Token, "index.html"))
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	html := string(page)
	// A device lives only on this node, so the page is the one place the owner can pick its link
	// without a terminal.
	if !strings.Contains(html, "Devices (1)") || !strings.Contains(html, "phone") {
		t.Errorf("the page does not list the local device:\n%s", html)
	}
	if !strings.Contains(html, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee") {
		t.Error("the device row carries no link with the device's UUID")
	}
}

func TestThePageFollowsTheSubscriptionWhenTheMeshChanges(t *testing.T) {
	t.Parallel()
	one := base64.StdEncoding.EncodeToString([]byte(
		"vless://11111111-2222-4333-8444-555555555555@node.example.com:443?type=xhttp#node.example.com\n" +
			"vless://22222222-3333-4444-8555-666666666666@far.example.com:443?type=xhttp#far.example.com\n"))
	app, cfg, configPath := optionsFixture(t, one)
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	if page := pageText(t, cfg); !strings.Contains(page, "far.example.com") || strings.Contains(page, "near.example.com") {
		t.Fatal("the page did not start from the first subscription")
	}

	// A peer left and another joined on the server. The refresh is the call `sync` and the agent's
	// cycle make, so this is the whole path from a new subscription to new bytes on disk.
	changed := base64.StdEncoding.EncodeToString([]byte(
		"vless://11111111-2222-4333-8444-555555555555@node.example.com:443?type=xhttp#node.example.com\n" +
			"vless://22222222-3333-4444-8555-666666666666@near.example.com:443?type=xhttp#near.example.com\n" +
			"vless://33333333-4444-4555-8666-777777777777@another.example.com:443?type=xhttp#another.example.com\n"))
	app.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(changed)),
		}, nil
	})}
	if err := app.refreshOptions(cfg); err != nil {
		t.Fatalf("refreshOptions: %v", err)
	}
	page := pageText(t, cfg)
	for _, want := range []string{"near.example.com", "another.example.com", "Mesh (2)"} {
		if !strings.Contains(page, want) {
			t.Errorf("the refreshed page does not list %q", want)
		}
	}
	if strings.Contains(page, "far.example.com") {
		t.Error("the refreshed page still lists the peer the server dropped")
	}
}

func TestOptionsWithoutAnIdentityExplainsItself(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	node.Identity = state.Identity{}
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if code := app.run([]string{"options", "--config", configPath}); code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(app.stderr.(*strings.Builder).String(), "no identity") {
		t.Errorf("the failure does not name the cause: %s", app.stderr.(*strings.Builder).String())
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestOptionsHonoursTheConfiguredLanguage(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	cfg.Locale = "ru"
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("save: %v", err)
	}
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, node.Options.Token, "index.html"))
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	// The page follows the configuration, not the console: a browser always has the glyphs.
	if !strings.Contains(string(page), `lang="ru"`) || !strings.Contains(string(page), "Этот узел") {
		t.Errorf("a Russian node rendered a page that is not Russian:\n%s", string(page)[:200])
	}
}

func TestOptionsCarriesTheNodesOwnFragmentParameter(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	cfg.HappFragment = "300-500,1-5,tlshello"
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("save: %v", err)
	}
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, node.Options.Token, "index.html"))
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	// The local device is the one link the node builds itself; without this the page would mix a
	// fragmented mesh with a plain local entry.
	if !strings.Contains(string(page), "&amp;fragment=300-500,1-5,tlshello") {
		t.Errorf("the local link does not carry the node's fragmentation parameter")
	}
}
