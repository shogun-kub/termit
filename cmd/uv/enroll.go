package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

// enrollAgentVersion is what the server records for this node. It is the binary's identity, not a
// hand-written string, so `termit-mn uv list` shows what actually runs.
func enrollAgentVersion() string { return version.Agent("termit-uv") }

// absoluteSubscriptionURL completes a relative subscription path with the server it belongs to.
//
// The path is resolved against the base's origin, not appended to the base: the base carries the API
// prefix, and a subscription lives outside it (`/sub/<token>` at the server root). Appending would
// produce .../prefix/sub/token, a URL that answers 404 in a client nobody would think to debug.
func absoluteSubscriptionURL(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Host == "" {
		return strings.TrimRight(base, "/") + path
	}
	ref, err := url.Parse(path)
	if err != nil {
		return strings.TrimRight(base, "/") + path
	}
	return baseURL.ResolveReference(ref).String()
}

// runEnroll registers this node with its management server and stores the credentials.
func (a *application) runEnroll(args []string) int {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	invite := flags.String("invite", "", "invite code from the server's operator")
	mnURL := flags.String("mn", "", "management server URL (default: mn.url from the configuration)")
	noSync := flags.Bool("no-sync", false, "do not run the first sync after enrolling")
	force := flags.Bool("force", false, "enroll again even though credentials are already stored")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv enroll: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(filepath.Join(cfg.Paths.StateDir, "state.json"))
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv enroll: %v\n", err)
		return exitFailure
	}
	if st.Credentials != nil && st.Credentials.AgentKey != "" && !*force {
		fmt.Fprintln(a.stderr, "termit-uv enroll: this node already has credentials; pass --force only if the server forgot it")
		fmt.Fprintln(a.stderr, "  (an enrollment that duplicates a registered domain is refused, and the invite is not spent)")
		return exitFailure
	}
	if st.Identity.SelfUUID == "" || st.Identity.XHTTPPath == "" {
		fmt.Fprintln(a.stderr, "termit-uv enroll: the node identity is missing; run `termit-uv init` first")
		return exitFailure
	}

	url := *mnURL
	if url == "" {
		url = cfg.MN.URL
	}
	if url == "" {
		fmt.Fprintln(a.stderr, "termit-uv enroll: no management server: pass --mn or set mn.url in the configuration")
		return exitUsage
	}

	basic, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: cfg.Domain,
		UUID:   st.Identity.SelfUUID,
		Path:   "/" + st.Identity.XHTTPPath + "/",
		Label:  cfg.Domain,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv enroll: %v\n", err)
		return exitFailure
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	remote := agent.Remote{BaseURL: url, CAFile: cfg.MN.CAFile}
	answer, err := remote.Enroll(ctx, agent.EnrollRequest{
		InviteCode:    *invite,
		Domain:        cfg.Domain,
		BasicVLESSURL: basic,
		AgentVersion:  enrollAgentVersion(),
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv enroll: %v\n", err)
		return exitFailure
	}

	// The URL the enrollment just used becomes the node's own: a node that enrolls and then cannot sync
	// because nobody wrote mn.url is the gap this closes. An existing value is left alone — `--mn` is an
	// override for one call unless the configuration was empty.
	if cfg.MN.URL == "" {
		cfg.MN.URL = strings.TrimRight(url, "/")
		if err := cfg.Save(*configPath); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv enroll: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(a.stdout, "mn.url:    %s (saved to %s)\n", cfg.MN.URL, *configPath)
	}

	st.Credentials = &state.Credentials{
		AgentKey:  answer.AgentKey,
		SubToken:  answer.SubToken,
		ProbeUUID: answer.ProbeUUID,
		// Stored absolute: the answer carries a path, and "where this node's subscription is" is a
		// question the state should be able to answer without knowing which server answered.
		SubscriptionURL: absoluteSubscriptionURL(url, answer.SubscriptionURL),
		EnrolledAt:      a.now().UTC(),
	}
	st.Touch()
	if err := st.Save(filepath.Join(cfg.Paths.StateDir, "state.json")); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv enroll: %v\n", err)
		return exitFailure
	}

	fmt.Fprintf(a.stdout, "node:      %s\n", answer.Node.ID)
	fmt.Fprintf(a.stdout, "domain:    %s\n", answer.Node.Domain)
	fmt.Fprintf(a.stdout, "state:     %s\n", answer.Node.State)
	if answer.Notice != "" {
		fmt.Fprintf(a.stdout, "notice:    %s\n", answer.Notice)
	}
	if answer.SubscriptionURL != "" {
		fmt.Fprintf(a.stdout, "subscribe: %s\n", st.Credentials.SubscriptionURL)
	}
	fmt.Fprintln(a.stdout, "credentials are stored in the state file (0600); they are never printed again")

	if *noSync {
		return exitOK
	}
	// The design's first sync follows enrollment: the probe credential has to reach the core before the
	// server's first check, or the node looks broken the moment it is registered.
	fmt.Fprintln(a.stdout)
	return a.runSync(nil)
}
