package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// deviceFixture is the options fixture with a host that can write a proxy config: the executor answers
// every command with success, which is what a box with the core installed looks like. Left as the
// failing stub, `device add` would stop at the `xray -test` step and never reach the page.
func deviceFixture(t *testing.T) (*application, *config.Config, string) {
	t.Helper()
	app, cfg, configPath := optionsFixture(t, "")
	app.exec = stubExecutor{handler: func(install.Cmd) (install.Result, error) {
		return install.Result{}, nil
	}}
	return app, cfg, configPath
}

// pageText reads the rendered page of the fixture's node.
func pageText(t *testing.T, cfg *config.Config) string {
	t.Helper()
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if node.Options.Token == "" {
		t.Fatal("the fixture has no page; run `options` first")
	}
	page, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, node.Options.Token, "index.html"))
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	return string(page)
}

func TestDeviceAddAndRemoveRedrawThePage(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := deviceFixture(t)
	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("options exit = %d; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	if page := pageText(t, cfg); strings.Contains(page, "laptop") {
		t.Fatal("the page already lists a device the fixture never added")
	}

	if code := app.run([]string{"device", "add", "--config", configPath, "--label", "laptop"}); code != exitOK {
		t.Fatalf("device add exit = %d; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	if page := pageText(t, cfg); !strings.Contains(page, "laptop") {
		t.Error("adding a device did not redraw the page, or the page does not list it")
	}

	// The label first and the flag after it: Go's flag package stops at the first positional, and this
	// order is how an operator writes the command.
	if code := app.run([]string{"device", "remove", "laptop", "--config", configPath}); code != exitOK {
		t.Fatalf("device remove (label first) exit = %d; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	if page := pageText(t, cfg); strings.Contains(page, "laptop") {
		t.Error("removing a device left its link on the page")
	}
}

func TestDeviceRemoveTakesTheLabelInEitherOrder(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"label first", "flag first"} {
		app, cfg, configPath := deviceFixture(t)
		if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
			t.Fatalf("%s: options exit = %d", order, code)
		}
		node, err := state.LoadInstalled(cfg.Paths.StatePath())
		if err != nil {
			t.Fatalf("%s: load state: %v", order, err)
		}
		if err := node.AddLocalUser("11111111-2222-4333-8444-555555555555", "phone", time.Unix(0, 0)); err != nil {
			t.Fatalf("%s: add a device: %v", order, err)
		}
		if err := node.Save(cfg.Paths.StatePath()); err != nil {
			t.Fatalf("%s: save state: %v", order, err)
		}
		line := []string{"device", "remove", "phone", "--config", configPath}
		if order == "flag first" {
			line = []string{"device", "remove", "--config", configPath, "phone"}
		}
		if code := app.run(line); code != exitOK {
			t.Fatalf("%s: %v exit = %d; stderr: %s", order, line, code, app.stderr.(*strings.Builder).String())
		}
		node, err = state.LoadInstalled(cfg.Paths.StatePath())
		if err != nil {
			t.Fatalf("%s: load state: %v", order, err)
		}
		if len(node.LocalUsers) != 0 {
			t.Errorf("%s: %v left %d device(s)", order, line, len(node.LocalUsers))
		}
	}
}

func TestDeviceRemoveStillRefusesAMissingLabel(t *testing.T) {
	t.Parallel()
	app, _, configPath := deviceFixture(t)
	if code := app.run([]string{"device", "remove", "--config", configPath}); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(app.stderr.(*strings.Builder).String(), "give one device label or UUID") {
		t.Errorf("the refusal does not say what is missing: %s", app.stderr.(*strings.Builder).String())
	}
}
