package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/options"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// runOptions renders the owner's page of client links and prints its address.
//
// The page is written into the decoy site's tree, which is what makes it reachable without a single
// new nginx directive: the vhost already serves that directory, so a random subdirectory is just
// another file on a site nobody links to. The cost of that choice is that `init` and `renew-site`
// regenerate the tree and drop it, which is why both re-render the page afterwards.
func (a *application) runOptions(args []string) int {
	flags := a.newFlagSet("options", `Renders the owner's private page of client links and prints its address.

The page lists this node's own device, every mesh peer the subscription describes, and the two
subscription forms. It is reachable at a random path that only the owner knows; --rotate issues a new
one and makes the old address answer 404.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	rotate := flags.Bool("rotate", false, "issue a new random path and retire the old one")
	quiet := flags.Bool("quiet", false, "render without printing the address")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv options: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configFlag)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv options: %v\n", err)
		return exitFailure
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if errors.Is(err, state.ErrNotInstalled) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no state at %s\n", cfg.Paths.StatePath())
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv options: %v\n", err)
		return exitFailure
	}
	if node.Identity.SelfUUID == "" || node.Identity.XHTTPPath == "" {
		fmt.Fprintln(a.stderr, "termit-uv options: the node has no identity yet; run `termit-uv init`")
		return exitFailure
	}

	ctx, cancel := a.context()
	defer cancel()

	previous := node.Options.Token
	token := previous
	switch {
	case *rotate || !options.ValidToken(token):
		token, err = options.NewToken()
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv options: %v\n", err)
			return exitFailure
		}
	}

	data := a.optionsData(ctx, cfg, node, token)
	page, err := options.Render(data)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv options: %v\n", err)
		return exitFailure
	}

	dir := filepath.Join(cfg.Paths.SiteDir, token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv options: create %s: %v\n", dir, err)
		return exitFailure
	}
	pagePath := filepath.Join(dir, "index.html")
	if err := state.WriteFileAtomic(pagePath, page, 0o644); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv options: write %s: %v\n", pagePath, err)
		return exitFailure
	}

	// The old address must stop working when the owner rotates: a leaked page is the whole reason the
	// option exists. Only a well-formed token is removed, never an arbitrary path from a hand-edited
	// state file.
	if previous != "" && previous != token && options.ValidToken(previous) {
		if err := os.RemoveAll(filepath.Join(cfg.Paths.SiteDir, previous)); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv options: retire the old page: %v\n", err)
			return exitFailure
		}
	}

	node.Options = state.Options{Token: token, RenderedAt: a.now().UTC()}
	node.AddPath(dir, state.KindDir, "options", a.now())
	node.AddPath(pagePath, state.KindFile, "options", a.now())
	node.Touch()
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv options: save state: %v\n", err)
		return exitFailure
	}

	if !*quiet {
		fmt.Fprintln(a.stdout, options.PageURL(cfg.Domain, token))
	}
	return exitOK
}

// optionsData gathers what the page shows. Every failure that is not fatal degrades the page instead
// of stopping it: a node whose management server is unreachable still has its own device to hand out,
// and that is exactly when the owner wants to look at this page.
func (a *application) optionsData(ctx context.Context, cfg *config.Config, node *state.State, token string) options.Data {
	data := options.Data{
		Domain:  cfg.Domain,
		Token:   token,
		Lang:    cfg.Lang(),
		PageURL: options.PageURL(cfg.Domain, token),
		Local: options.Link{
			Label: cfg.Domain,
			URI:   localURI(cfg, node),
		},
		Generated: a.now().UTC(),
	}
	// The QUIC transport is a second way into the same node, and it needs its own link: the user name and
	// the password are derived from the same UUID, so the page can hand it out without asking anyone.
	if cfg.Hysteria != nil {
		if uri, err := proto.BuildHysteriaURL(cfg.Domain, node.Identity.SelfUUID, cfg.Domain+" (QUIC)"); err == nil {
			data.Devices = append(data.Devices, options.Link{Label: cfg.Domain + " (QUIC)", URI: uri})
		}
	}
	// The owner's own devices belong here: the page is where they pick the one they need, and
	// `device list` in a terminal is the fallback, not the only way.
	for _, user := range node.LocalUsers {
		uri, err := a.deviceURI(cfg, node, user.ID, user.Label)
		if err != nil {
			continue
		}
		data.Devices = append(data.Devices, options.Link{Label: user.Label, URI: uri})
		if cfg.Hysteria != nil {
			if quic, err := proto.BuildHysteriaURL(cfg.Domain, user.ID, user.Label+" (QUIC)"); err == nil {
				data.Devices = append(data.Devices, options.Link{Label: user.Label + " (QUIC)", URI: quic})
			}
		}
	}
	if node.Credentials == nil || node.Credentials.SubscriptionURL == "" {
		return data
	}
	data.SubscriptionURL = node.Credentials.SubscriptionURL
	data.JSONURL = strings.TrimRight(node.Credentials.SubscriptionURL, "/") + "/json"

	peers, err := a.fetchSubscription(ctx, node.Credentials.SubscriptionURL)
	if err != nil {
		// The reason is deliberately coarse: the URL carries the node's token, and an error that
		// quotes it would put the token into a terminal scrollback or a bug report.
		data.Warning = "The mesh could not be fetched from the management server just now; " +
			"this page shows the device of this node only. Try `termit-uv sync`, then re-render."
		return data
	}
	// The subscription is rendered for every peer, this node included; the page lists it once, under
	// "this node", and that is where its own credential belongs.
	data.Peers = options.WithoutLocal(peers, cfg.Domain)
	return data
}

// fetchSubscription reads the plain list and turns it into links, the way a client would.
func (a *application) fetchSubscription(ctx context.Context, url string) ([]options.Link, error) {
	client := a.http
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return options.ParseList(body)
}

// localURI is this node's own device, the one credential that never depends on the server.
func localURI(cfg *config.Config, node *state.State) string {
	uri, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: cfg.Domain,
		UUID:   node.Identity.SelfUUID,
		Path:   "/" + node.Identity.XHTTPPath + "/",
		Label:  cfg.Domain,
	})
	// The mesh links the page shows already carry the server's parameters; this one carries the node's,
	// so the page does not mix two answers to the same question.
	uri = withFragmentation(uri, cfg)
	if err != nil {
		// BuildVLESSUrl only fails on a profile the node cannot have: an identity with an empty UUID
		// or a domain that is not a DNS name. Both are caught before this function is called.
		return ""
	}
	return uri
}

// refreshOptions re-renders an existing page and never creates one: a node whose owner never asked for
// it should not grow one because a sync ran. Callers treat a failure as a warning — the page is a
// convenience, and a certificate renewal must not fail because a page could not be written.
func (a *application) refreshOptions(cfg *config.Config) error {
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		return err
	}
	if !options.ValidToken(node.Options.Token) || node.Identity.SelfUUID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page, err := options.Render(a.optionsData(ctx, cfg, node, node.Options.Token))
	if err != nil {
		return err
	}
	dir := filepath.Join(cfg.Paths.SiteDir, node.Options.Token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pagePath := filepath.Join(dir, "index.html")
	if err := state.WriteFileAtomic(pagePath, page, 0o644); err != nil {
		return err
	}
	node.Options.RenderedAt = a.now().UTC()
	node.AddPath(dir, state.KindDir, "options", a.now())
	node.AddPath(pagePath, state.KindFile, "options", a.now())
	node.Touch()
	return node.Save(cfg.Paths.StatePath())
}
