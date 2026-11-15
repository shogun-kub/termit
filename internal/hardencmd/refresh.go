package hardencmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// firewallRefresh fetches the blocklist and puts it in force: the file first, because it is what the
// next start rewrites the kernel from, then the kernel's sets.
//
// A fetch that finds the source unchanged still reloads the kernel: the file is the record of truth and
// the kernel may have lost the sets to a reboot or a failed load. That is what makes the command safe to
// run on a timer.
func firewallRefresh(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall refresh", `Fetches security.firewall.blocklist_url, validates it, replaces the list in force and loads it
into the table. A source that has not changed is still re-loaded into the kernel; a source that is broken
or empty is refused and the list in force stays.`)
	configFlag := flags.String("config", "", "path to config.yaml")
	override := flags.String("url", "", "fetch this URL instead of security.firewall.blocklist_url")
	dryRun := flags.Bool("dry-run", false, "fetch and validate, but change nothing")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
		return ExitFailure
	}
	rawURL := component.Security.Firewall.BlocklistURL
	if *override != "" {
		rawURL = *override
	}
	if rawURL == "" {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: no list to fetch: set security.firewall.blocklist_url or pass --url\n", name)
		return ExitUsage
	}

	path := component.Paths.Blocklist
	known, since, err := firewall.LoadBlocklist(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
		return ExitFailure
	}
	fetcher := firewall.Fetcher{HTTP: &http.Client{Timeout: 2 * time.Minute}}
	body, notModified, err := fetcher.Fetch(context.Background(), rawURL, since)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
		fmt.Fprintf(env.Stderr, "%s firewall refresh: the list in force (%d networks) is untouched\n", name, len(known))
		return ExitFailure
	}
	if notModified {
		fmt.Fprintf(env.Stdout, "blocklist: %d networks, the source has not changed since %s\n",
			len(known), since.UTC().Format(time.RFC3339))
	} else {
		prefixes, err := firewall.ParseBlocklist(bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
			fmt.Fprintf(env.Stderr, "%s firewall refresh: the list in force (%d networks) is untouched\n", name, len(known))
			return ExitFailure
		}
		if len(prefixes) == 0 {
			fmt.Fprintf(env.Stderr, "%s firewall refresh: the source holds no networks; a list that blocks\n", name)
			fmt.Fprintf(env.Stderr, "nothing is worse than the one in force, so nothing was changed\n")
			return ExitFailure
		}
		if *dryRun {
			fmt.Fprintf(env.Stdout, "blocklist: would replace %d networks with %d from %s\n", len(known), len(prefixes), rawURL)
			return ExitOK
		}
		if err := firewall.SaveBlocklist(path, prefixes); err != nil {
			fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
			return ExitFailure
		}
		known = prefixes
	}

	if *dryRun {
		tableName, _ := env.table()
		fmt.Fprintf(env.Stdout, "blocklist: %d networks would be loaded into inet %s\n", len(known), tableName)
		return ExitOK
	}
	// The kernel is loaded whether or not the source changed: the file is what it has to match.
	tableName, _ := env.table()
	ctx := context.Background()
	present, err := env.applier().Present(ctx, tableName)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
		return ExitFailure
	}
	if !present {
		fmt.Fprintf(env.Stdout, "blocklist: %d networks in force; %s is not applied, so the kernel was left alone\n",
			len(known), tableName)
		return ExitOK
	}
	if err := env.applier().ReplaceBlocked(ctx, tableName, known); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall refresh: %v\n", name, err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "blocklist: %d networks, loaded into inet %s\n", len(known), tableName)
	return ExitOK
}
