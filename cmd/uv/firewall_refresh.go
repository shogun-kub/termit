package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// runFirewallRefresh fetches the blocklist and puts it in force: the file first, because it is what the
// next start rewrites the kernel from, then the kernel's sets.
//
// A fetch that finds the source unchanged still reloads the kernel: the file is the record of truth and
// the kernel may have lost the sets to a reboot or a failed load. That is what makes the command safe to
// run on a timer.
func (a *application) runFirewallRefresh(args []string) int {
	flags := a.newFlagSet("firewall refresh", `Fetches security.firewall.blocklist_url, validates it, replaces the list in force and loads it
into the table. A source that has not changed is still re-loaded into the kernel; a source that broken
or empty is refused and the list in force stays.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	override := flags.String("url", "", "fetch this URL instead of security.firewall.blocklist_url")
	dryRun := flags.Bool("dry-run", false, "fetch and validate, but change nothing")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
		return exitFailure
	}
	rawURL := cfg.Security.Firewall.BlocklistURL
	if *override != "" {
		rawURL = *override
	}
	if rawURL == "" {
		fmt.Fprintln(a.stderr, "termit-uv firewall refresh: no list to fetch: set security.firewall.blocklist_url or pass --url")
		return exitUsage
	}

	path := cfg.Paths.BlocklistPath()
	known, since, err := firewall.LoadBlocklist(path)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
		return exitFailure
	}
	fetcher := firewall.Fetcher{HTTP: &http.Client{Timeout: 2 * time.Minute}}
	body, notModified, err := fetcher.Fetch(context.Background(), rawURL, since)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: the list in force (%d networks) is untouched\n", len(known))
		return exitFailure
	}
	if notModified {
		fmt.Fprintf(a.stdout, "blocklist: %d networks, the source has not changed since %s\n",
			len(known), since.UTC().Format(time.RFC3339))
	} else {
		prefixes, err := firewall.ParseBlocklist(bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
			fmt.Fprintf(a.stderr, "termit-uv firewall refresh: the list in force (%d networks) is untouched\n", len(known))
			return exitFailure
		}
		if len(prefixes) == 0 {
			fmt.Fprintln(a.stderr, "termit-uv firewall refresh: the source holds no networks; a list that blocks")
			fmt.Fprintln(a.stderr, "nothing is worse than the one in force, so nothing was changed")
			return exitFailure
		}
		if *dryRun {
			fmt.Fprintf(a.stdout, "blocklist: would replace %d networks with %d from %s\n", len(known), len(prefixes), rawURL)
			return exitOK
		}
		if err := firewall.SaveBlocklist(path, prefixes); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
			return exitFailure
		}
		known = prefixes
	}

	if *dryRun {
		fmt.Fprintf(a.stdout, "blocklist: %d networks would be loaded into inet %s\n", len(known), firewall.NodeTable)
		return exitOK
	}
	// The kernel is loaded whether or not the source changed: the file is what it has to match.
	ctx := context.Background()
	present, err := a.nftApplier().Present(ctx, firewall.NodeTable)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
		return exitFailure
	}
	if !present {
		fmt.Fprintf(a.stdout, "blocklist: %d networks in force; %s is not applied, so the kernel was left alone\n",
			len(known), firewall.NodeTable)
		return exitOK
	}
	if err := a.nftApplier().ReplaceBlocked(ctx, firewall.NodeTable, known); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall refresh: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "blocklist: %d networks, loaded into inet %s\n", len(known), firewall.NodeTable)
	return exitOK
}

// loadBlocklistIntoKernel is what `firewall on` does after applying the table: the sets start empty, so
// the list in force is put back before the operator walks away.
func (a *application) loadBlocklistIntoKernel(cfg *config.Config) {
	prefixes, _, err := firewall.LoadBlocklist(cfg.Paths.BlocklistPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall: the blocklist was not loaded: %v\n", err)
		return
	}
	if len(prefixes) == 0 {
		return
	}
	if err := a.nftApplier().ReplaceBlocked(context.Background(), firewall.NodeTable, prefixes); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall: the blocklist was not loaded: %v\n", err)
		return
	}
	fmt.Fprintf(a.stdout, "blocklist: %d networks loaded\n", len(prefixes))
}
