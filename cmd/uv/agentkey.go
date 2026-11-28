package main

import (
	"fmt"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// runAgentKey prints the identity this node authenticates with, or installs a replacement key.
//
// The install half is the other end of `termit-mn home handoff`: the node keeps its row, but the new home
// issues a new agent key and the old one stops working. A live box has to be able to take the new key —
// rebuilding a node that is serving is not a recovery step.
func (a *application) runAgentKey(args []string) int {
	flags := a.newFlagSet("agentkey", `Prints this node's credential identity, or installs a replacement agent key.
--install FILE reads a 0600 file holding a key a new home issued (see termit-mn home handoff), writes it into
the node's state and prints the new identity. The old key stops working: run termit-uv sync afterwards.`)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	install := flags.String("install", "", "0600 file holding the agent key to install")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv agentkey: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv agentkey: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv agentkey: %v\n", err)
		return exitFailure
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		fmt.Fprintln(a.stderr, "termit-uv agentkey: this node has no agent key; `termit-uv enroll` issues one")
		return exitFailure
	}

	if *install == "" {
		fmt.Fprintf(a.stdout, "identity: %s  (sha256 prefix of the agent key; the key itself is in %s)\n",
			state.Fingerprint(st.Credentials.AgentKey), cfg.Paths.StatePath())
		return exitOK
	}
	key, err := readAgentKeyFile(*install)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv agentkey: %v\n", err)
		return exitFailure
	}
	if key == st.Credentials.AgentKey {
		fmt.Fprintf(a.stdout, "identity: %s  (the key is already installed)\n",
			state.Fingerprint(key))
		return exitOK
	}
	before := state.Fingerprint(st.Credentials.AgentKey)
	st.Credentials.AgentKey = key
	if err := st.Save(cfg.Paths.StatePath()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv agentkey: %v\n", err)
		return exitFailure
	}
	// The old identity is printed beside the new one: after this the node answers to nobody until it syncs,
	// and a swap the operator cannot see is a swap they cannot undo.
	fmt.Fprintf(a.stdout, "identity: %s → %s  (installed; run `termit-uv sync`)\n",
		before, state.Fingerprint(key))
	return exitOK
}
