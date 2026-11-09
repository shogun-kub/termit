package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
	"github.com/shogun-kub/termit/internal/mn/config"
)

// adminTokenEnv lets an operator avoid putting the token in a shell history.
const adminTokenEnv = "TERMIT_MN_ADMIN_TOKEN"

// adminClient builds a client from the flags and the configuration. The token never has a default: an
// empty one produces a clear refusal from the server rather than an unauthenticated call.
func adminClient(configPath, socket, token string) (admin.Client, error) {
	if token == "" {
		token = os.Getenv(adminTokenEnv)
	}
	if socket == "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return admin.Client{}, err
		}
		socket = cfg.AdminSocket
	}
	if token == "" {
		return admin.Client{}, fmt.Errorf("an admin token is required: pass --token or set %s", adminTokenEnv)
	}
	return admin.Client{Network: "unix", Address: socket, Token: token}, nil
}

// runInvite dispatches `mn invite …`.
func runInvite(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "mn invite: one of create, list, revoke is required")
		return exitUsage
	}
	switch args[0] {
	case "create":
		return runInviteCreate(args[1:], stdout, stderr)
	case "list":
		return runInviteList(args[1:], stdout, stderr)
	case "revoke":
		return runInviteRevoke(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "mn invite: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

func inviteFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *string, *string) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "path to config.yaml")
	socket := flags.String("socket", "", "admin socket path (default: from the configuration)")
	token := flags.String("token", "", "admin token (default: "+adminTokenEnv+")")
	return flags, configPath, socket, token
}

func runInviteCreate(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := inviteFlags("invite create", stderr)
	uses := flags.Int("uses", 1, "how many nodes this code may register")
	note := flags.String("note", "", "why this code exists")
	expires := flags.Int("expires-hours", 0, "hours until the code expires; 0 means never")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn invite create: %v\n", err)
		return exitFailure
	}
	var result struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = client.Call(callCtx, "invite.create", map[string]any{
		"uses": *uses, "note": *note, "expires_hours": *expires,
	}, &result)
	if err != nil {
		fmt.Fprintf(stderr, "mn invite create: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "invite: %s\n", result.ID)
	fmt.Fprintf(stdout, "code:   %s\n", result.Code)
	fmt.Fprintf(stdout, "uses:   %d\n", *uses)
	if *expires > 0 {
		fmt.Fprintf(stdout, "expires: %s\n", time.Now().Add(time.Duration(*expires)*time.Hour).UTC().Format(time.RFC3339))
	}
	fmt.Fprintln(stdout, "the code is shown once: it is stored only as a digest")
	return exitOK
}

func runInviteList(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := inviteFlags("invite list", stderr)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn invite list: %v\n", err)
		return exitFailure
	}
	var invites []admin.InviteView
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "invite.list", nil, &invites); err != nil {
		fmt.Fprintf(stderr, "mn invite list: %v\n", err)
		return exitFailure
	}
	if len(invites) == 0 {
		fmt.Fprintln(stdout, "no invites")
		return exitOK
	}
	out := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "ID\tCREATED\tUSES\tEXPIRES\tSTATE\tNOTE")
	for _, invite := range invites {
		expires := "never"
		if invite.ExpiresAt != nil {
			expires = invite.ExpiresAt.UTC().Format("2006-01-02")
		}
		state := "usable"
		switch {
		case invite.Revoked:
			state = "revoked"
		case !invite.Usable:
			state = "spent"
		}
		fmt.Fprintf(out, "%s\t%s\t%d/%d\t%s\t%s\t%s\n", invite.ID,
			invite.CreatedAt.UTC().Format("2006-01-02"), invite.UsedCount, invite.MaxUses, expires, state, invite.Note)
	}
	_ = out.Flush()
	return exitOK
}

func runInviteRevoke(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := inviteFlags("invite revoke", stderr)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn invite revoke: one invite id is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn invite revoke: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "invite.revoke", map[string]string{"id": flags.Arg(0)}, nil); err != nil {
		fmt.Fprintf(stderr, "mn invite revoke: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "invite %s revoked\n", flags.Arg(0))
	return exitOK
}

// runUVReissue hands a node a new agent key, for an owner who lost the old one.
func runUVReissue(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv reissue", stderr)
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn uv reissue: one node id or domain is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv reissue: %v\n", err)
		return exitFailure
	}
	var result struct {
		ID       string `json:"id"`
		AgentKey string `json:"agent_key"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "uv.reissue", map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv reissue: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "node:      %s\n", result.ID)
	fmt.Fprintf(stdout, "agent key: %s\n", result.AgentKey)
	fmt.Fprintln(stdout, "the key is shown once; the previous one stopped working")
	return exitOK
}

// runUVState is the operator override for one node: block it, let it back in, or remove it. Each one
// goes through the same state table the automatic checks use.
func runUVState(verb string, args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv "+verb, stderr)
	reason := flags.String("reason", "", "why, recorded with the state change")
	if err := flags.Parse(flagsFirst(args, map[string]bool{"reason": true})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(stderr, "mn uv %s: one node id or domain is required\n", verb)
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv %s: %v\n", verb, err)
		return exitFailure
	}
	var result struct {
		Node struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"node"`
		Changed bool `json:"changed"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args2 := map[string]string{"id": flags.Arg(0)}
	if *reason != "" {
		args2["reason"] = *reason
	}
	if err := client.Call(callCtx, "uv."+verb, args2, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv %s: %v\n", verb, err)
		return exitFailure
	}
	if !result.Changed {
		fmt.Fprintf(stdout, "node %s is already %s; nothing changed\n", result.Node.ID, result.Node.State)
		return exitOK
	}
	fmt.Fprintf(stdout, "node %s is now %s\n", result.Node.ID, result.Node.State)
	return exitOK
}

// runUVForget erases a removed node, which is what frees its domain for a new enrollment.
func runUVForget(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv forget", stderr)
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn uv forget: one node id or domain is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv forget: %v\n", err)
		return exitFailure
	}
	var result struct {
		ID     string `json:"id"`
		Domain string `json:"domain"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "uv.forget", map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv forget: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "forgot %s (%s); the domain can be enrolled again\n", result.ID, result.Domain)
	return exitOK
}

// runUVAgentKey prints a node's current key: for an owner who lost it, without breaking the node.
func runUVAgentKey(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv agentkey", stderr)
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn uv agentkey: one node id or domain is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv agentkey: %v\n", err)
		return exitFailure
	}
	var result struct {
		ID       string `json:"id"`
		AgentKey string `json:"agent_key"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "uv.agentkey", map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv agentkey: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "node:      %s\n", result.ID)
	fmt.Fprintf(stdout, "agent key: %s\n", result.AgentKey)
	return exitOK
}

// runSub dispatches `mn sub …`.
func runSub(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "rotate" {
		fmt.Fprintln(stderr, "mn sub: the only subcommand in this build is rotate")
		return exitUsage
	}
	flags, configPath, socket, token := uvFlags("sub rotate", stderr)
	domain := flags.String("domain", "", "the server's public name, for the URL")
	if err := flags.Parse(flagsFirst(args[1:], map[string]bool{})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn sub rotate: one node id or domain is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn sub rotate: %v\n", err)
		return exitFailure
	}
	var result struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "sub.rotate", map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn sub rotate: %v\n", err)
		return exitFailure
	}
	host := *domain
	if host == "" {
		if cfg, err := config.Load(*configPath); err == nil {
			host = cfg.Domain
		}
	}
	fmt.Fprintf(stdout, "node:         %s\n", result.ID)
	fmt.Fprintf(stdout, "subscription: https://%s/sub/%s\n", host, result.Token)
	fmt.Fprintf(stdout, "sing-box:     https://%s/sub/%s/json\n", host, result.Token)
	fmt.Fprintln(stdout, "the token is shown once: the server keeps only its digest")
	return exitOK
}

// runUV dispatches `mn uv …`.
func runUV(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "mn uv: one of list, show, approve, reject, reissue, agentkey, block, unblock, rm, forget is required")
		return exitUsage
	}
	switch args[0] {
	case "list":
		return runUVList(args[1:], stdout, stderr)
	case "show":
		return runUVShow(args[1:], stdout, stderr)
	case "approve", "reject":
		return runUVDecide(args[0], args[1:], stdout, stderr)
	case "reissue":
		return runUVReissue(args[1:], stdout, stderr)
	case "agentkey":
		return runUVAgentKey(args[1:], stdout, stderr)
	case "block", "unblock", "rm":
		return runUVState(args[0], args[1:], stdout, stderr)
	case "forget":
		return runUVForget(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "mn uv: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

func uvFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *string, *string) {
	return inviteFlags(name, stderr)
}

func runUVList(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv list", stderr)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv list: %v\n", err)
		return exitFailure
	}
	var nodes []admin.NodeView
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "uv.list", nil, &nodes); err != nil {
		fmt.Fprintf(stderr, "mn uv list: %v\n", err)
		return exitFailure
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stdout, "no nodes")
		return exitOK
	}
	out := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "ID\tDOMAIN\tSTATE\tCREATED\tLAST SEEN\tAGENT")
	for _, node := range nodes {
		lastSeen := "never"
		if node.LastSeenAt != nil {
			lastSeen = node.LastSeenAt.UTC().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\n", node.ID, node.Domain, node.State,
			node.CreatedAt.UTC().Format("2006-01-02"), lastSeen, node.AgentVersion)
	}
	_ = out.Flush()
	return exitOK
}

func runUVShow(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv show", stderr)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn uv show: one node id or domain is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv show: %v\n", err)
		return exitFailure
	}
	var result struct {
		Node   admin.NodeView `json:"node"`
		Events []struct {
			At        time.Time `json:"at"`
			FromState string    `json:"from_state"`
			ToState   string    `json:"to_state"`
			Reason    string    `json:"reason"`
			Actor     string    `json:"actor"`
		} `json:"events"`
		Check *struct {
			At         time.Time `json:"at"`
			OK         bool      `json:"ok"`
			ErrorClass string    `json:"error_class"`
		} `json:"check"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "uv.show", map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv show: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "id:      %s\n", result.Node.ID)
	fmt.Fprintf(stdout, "domain:  %s\n", result.Node.Domain)
	fmt.Fprintf(stdout, "state:   %s\n", result.Node.State)
	if result.Node.StateReason != "" {
		fmt.Fprintf(stdout, "reason:  %s\n", result.Node.StateReason)
	}
	fmt.Fprintf(stdout, "created: %s\n", result.Node.CreatedAt.UTC().Format(time.RFC3339))
	if result.Node.ApprovedAt != nil {
		fmt.Fprintf(stdout, "approved: %s\n", result.Node.ApprovedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(stdout, "ip:      %s\n", result.Node.LastRequestIP)
	fmt.Fprintf(stdout, "agent:   %s\n", result.Node.AgentVersion)
	fmt.Fprintf(stdout, "epoch:   %d\n", result.Node.CredentialEpoch)
	fmt.Fprintf(stdout, "failed cycles: %d\n", result.Node.FailedCycles)
	if result.Check != nil {
		state := "failed (" + result.Check.ErrorClass + ")"
		if result.Check.OK {
			state = "ok"
		}
		fmt.Fprintf(stdout, "last probe: %s %s\n", result.Check.At.UTC().Format(time.RFC3339), state)
	}
	if len(result.Events) > 0 {
		fmt.Fprintln(stdout, "events:")
		for _, event := range result.Events {
			fmt.Fprintf(stdout, "  %s  %s -> %s  %s (%s)\n", event.At.UTC().Format("2006-01-02 15:04"),
				event.FromState, event.ToState, event.Reason, event.Actor)
		}
	}
	return exitOK
}

func runUVDecide(decision string, args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("uv "+decision, stderr)
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(stderr, "mn uv %s: one node id or domain is required\n", decision)
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv %s: %v\n", decision, err)
		return exitFailure
	}
	var result struct {
		Changed bool `json:"changed"`
		Node    struct {
			ID     string `json:"id"`
			Domain string `json:"domain"`
			State  string `json:"state"`
		} `json:"node"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	op := "uv." + decision
	if err := client.Call(callCtx, op, map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv %s: %v\n", decision, err)
		return exitFailure
	}
	if !result.Changed {
		fmt.Fprintf(stdout, "%s is already %s\n", result.Node.Domain, result.Node.State)
		return exitOK
	}
	fmt.Fprintf(stdout, "%s: %s -> %s\n", result.Node.Domain, decision, result.Node.State)
	return exitOK
}

// runCheck is `mn uv check now <node>`: one cycle, synchronously, with the scheduler's own error text.
func runCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "now" {
		fmt.Fprintln(stderr, "mn uv check: the only subcommand in this build is now")
		return exitUsage
	}
	flags, configPath, socket, token := uvFlags("uv check now", stderr)
	if err := flags.Parse(flagsFirst(args[1:], map[string]bool{})); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "mn uv check now: one node id or domain is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn uv check now: %v\n", err)
		return exitFailure
	}
	var result struct {
		Node     map[string]any `json:"node"`
		Event    string         `json:"event"`
		State    string         `json:"state"`
		Changed  bool           `json:"changed"`
		Reason   string         `json:"reason"`
		Attempts int            `json:"attempts"`
		Class    string         `json:"class"`
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := client.Call(callCtx, "check.now", map[string]string{"id": flags.Arg(0)}, &result); err != nil {
		fmt.Fprintf(stderr, "mn uv check now: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "event:    %s\n", result.Event)
	fmt.Fprintf(stdout, "attempts: %d\n", result.Attempts)
	fmt.Fprintf(stdout, "class:    %s\n", result.Class)
	fmt.Fprintf(stdout, "state:    %s (changed: %v)\n", result.State, result.Changed)
	fmt.Fprintf(stdout, "reason:   %s\n", result.Reason)
	return exitOK
}
