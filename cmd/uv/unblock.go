package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// localProfile is the endpoint this box actually serves: its configured domain, its own self
// credential and its current path. The server verifies exactly this, so unblock and migrate build it
// the same way instead of each growing its own copy.
func localProfile(cfg *config.Config, st *state.State, domain string) (proto.Profile, error) {
	if st.Identity.SelfUUID == "" || st.Identity.XHTTPPath == "" {
		return proto.Profile{}, errors.New("the node identity is missing; run `uv init` first")
	}
	return proto.Profile{
		Domain: domain,
		UUID:   st.Identity.SelfUUID,
		Path:   "/" + st.Identity.XHTTPPath + "/",
		Label:  domain,
	}, nil
}

// runUnblock is Scenario B from the node's side: ask the server to re-run the enrollment checks and
// let this node back in. It carries the identity the node answers on now, so a rebuilt box can send
// the URL it was just given.
func (a *application) runUnblock(args []string) int {
	flags := a.newFlagSet("unblock", `Asks the management server to verify this node and take it out of the blocked
state. Only a successful set of checks changes anything; traffic and state stay as they are otherwise.`)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	domain := flags.String("domain", "", "the domain this node answers on (default: the configured domain)")
	vlessURL := flags.String("vless-url", "",
		"the node's own vless:// URI (default: built from the local identity, which is what a rebuilt box needs)")
	noSync := flags.Bool("no-sync", false, "do not run a sync after a successful unblock")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv unblock: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv unblock: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(filepath.Join(cfg.Paths.StateDir, "state.json"))
	if err != nil {
		fmt.Fprintf(a.stderr, "uv unblock: %v\n", err)
		return exitFailure
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		fmt.Fprintln(a.stderr, "uv unblock: this node is not enrolled; there is nothing to unblock")
		return exitFailure
	}

	request := proto.UnblockRequest{Domain: *domain}
	if request.Domain == "" {
		request.Domain = cfg.Domain
	}
	profile, err := localProfile(cfg, st, request.Domain)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv unblock: %v\n", err)
		return exitFailure
	}
	// The default URL is built from the local identity. A rebuilt box knows its own domain, credential
	// and path, and a URL on the command line would put a credential into the shell history and the
	// remote process list for no benefit; --vless-url stays for the odd case where the operator wants
	// to name a different endpoint.
	url := *vlessURL
	if url == "" {
		built, err := proto.BuildVLESSUrl(profile)
		if err != nil {
			fmt.Fprintf(a.stderr, "uv unblock: %v\n", err)
			return exitFailure
		}
		url = built
	}
	parsed, err := proto.ParseVLESSUrl(url)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv unblock: %v\n", err)
		return exitFailure
	}
	if !strings.EqualFold(parsed.Domain, request.Domain) {
		fmt.Fprintf(a.stderr, "uv unblock: the URL names %q, the domain is %q\n", parsed.Domain, request.Domain)
		return exitFailure
	}
	request.BasicVLESSURL = url

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	remote := agent.Remote{BaseURL: cfg.MN.URL, CAFile: cfg.MN.CAFile}
	answer, err := remote.Unblock(ctx, st.Credentials.AgentKey, request)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv unblock: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "node:   %s\n", answer.Node.ID)
	fmt.Fprintf(a.stdout, "domain: %s\n", answer.Node.Domain)
	fmt.Fprintf(a.stdout, "state:  %s\n", answer.Node.State)
	if answer.Notice != "" {
		fmt.Fprintf(a.stdout, "notice: %s\n", answer.Notice)
	}
	if *noSync {
		return exitOK
	}
	// The node is allowed back in; converging now means the owner sees the peer set return instead
	// of waiting for the next daily cycle. The sync must read the same configuration file.
	fmt.Fprintln(a.stdout)
	if code := a.runSync([]string{"--config", *configPath}); code != exitOK {
		fmt.Fprintln(a.stderr, "uv unblock: the node was unblocked, but the first sync did not finish; the agent will retry")
	}
	return exitOK
}
