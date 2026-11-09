package main

import (
	"context"
	"flag"
	"fmt"
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
// hand-written string, so `mn uv list` shows what actually runs.
func enrollAgentVersion() string { return version.String("termit-uv") }

// absoluteSubscriptionURL completes a relative subscription path with the server it belongs to.
func absoluteSubscriptionURL(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(base, "/") + path
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
		fmt.Fprintf(a.stderr, "uv enroll: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(filepath.Join(cfg.Paths.StateDir, "state.json"))
	if err != nil {
		fmt.Fprintf(a.stderr, "uv enroll: %v\n", err)
		return exitFailure
	}
	if st.Credentials != nil && st.Credentials.AgentKey != "" && !*force {
		fmt.Fprintln(a.stderr, "uv enroll: this node already has credentials; pass --force only if the server forgot it")
		fmt.Fprintln(a.stderr, "  (an enrollment that duplicates a registered domain is refused, and the invite is not spent)")
		return exitFailure
	}
	if st.Identity.SelfUUID == "" || st.Identity.XHTTPPath == "" {
		fmt.Fprintln(a.stderr, "uv enroll: the node identity is missing; run `uv init` first")
		return exitFailure
	}

	url := *mnURL
	if url == "" {
		url = cfg.MN.URL
	}
	if url == "" {
		fmt.Fprintln(a.stderr, "uv enroll: no management server: pass --mn or set mn.url in the configuration")
		return exitUsage
	}

	basic, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: cfg.Domain,
		UUID:   st.Identity.SelfUUID,
		Path:   "/" + st.Identity.XHTTPPath + "/",
		Label:  cfg.Domain,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "uv enroll: %v\n", err)
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
		fmt.Fprintf(a.stderr, "uv enroll: %v\n", err)
		return exitFailure
	}

	st.Credentials = &state.Credentials{
		AgentKey:        answer.AgentKey,
		SubToken:        answer.SubToken,
		ProbeUUID:       answer.ProbeUUID,
		SubscriptionURL: answer.SubscriptionURL,
		EnrolledAt:      a.now().UTC(),
	}
	st.Touch()
	if err := st.Save(filepath.Join(cfg.Paths.StateDir, "state.json")); err != nil {
		fmt.Fprintf(a.stderr, "uv enroll: %v\n", err)
		return exitFailure
	}

	fmt.Fprintf(a.stdout, "node:      %s\n", answer.Node.ID)
	fmt.Fprintf(a.stdout, "domain:    %s\n", answer.Node.Domain)
	fmt.Fprintf(a.stdout, "state:     %s\n", answer.Node.State)
	if answer.Notice != "" {
		fmt.Fprintf(a.stdout, "notice:    %s\n", answer.Notice)
	}
	if answer.SubscriptionURL != "" {
		fmt.Fprintf(a.stdout, "subscribe: %s\n", absoluteSubscriptionURL(url, answer.SubscriptionURL))
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
