package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shogun-kub/termit/internal/atomicfile"
	"github.com/shogun-kub/termit/internal/mn/config"
)

// runInvitePolicy shows or changes enrollment.mode — the rule a new node meets when it knocks: a code
// (invite), a code and an operator's approval, or nothing at all.
//
// The mode is read once at startup, so the command ends by saying that the running server keeps the old
// rule until it restarts. No command here restarts anything: that is the operator's call, and the node's
// own units are the only ones an agent may bounce.
func runInvitePolicy(args []string, stdout, stderr io.Writer) int {
	flags, configPath, _, _, _ := inviteFlags("invite policy", stderr)
	mode := flags.String("mode", "", "enrollment mode: invite, invite+approval or open")
	force := flags.Bool("force", false, "allow open, where a leaked invite becomes an account")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn invite policy: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn invite policy: %v\n", err)
		return exitFailure
	}
	current := cfg.Enrollment.Mode
	if *mode == "" {
		fmt.Fprintf(stdout, "enrollment.mode: %s  (%s)\n", current, *configPath)
		return exitOK
	}

	next := strings.TrimSpace(*mode)
	switch next {
	case config.ModeInvite, config.ModeInviteApproval:
	case config.ModeOpen:
		if !*force {
			fmt.Fprintln(stderr, "termit-mn invite policy: open registers without an invite and without an "+
				"approval, so a code that leaks is an account. Pass --force if that is the rule you want")
			return exitUsage
		}
	default:
		fmt.Fprintf(stderr, "termit-mn invite policy: unknown mode %q; one of %s, %s, %s\n",
			*mode, config.ModeInvite, config.ModeInviteApproval, config.ModeOpen)
		return exitUsage
	}
	if next == current {
		fmt.Fprintf(stdout, "enrollment.mode is already %s\n", current)
		return exitOK
	}
	if err := setEnrollmentMode(*configPath, next); err != nil {
		fmt.Fprintf(stderr, "termit-mn invite policy: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "enrollment.mode: %s -> %s\n", current, next)
	fmt.Fprintln(stdout, "the running server keeps the old rule until it restarts: systemctl restart termit-mn")
	return exitOK
}

// setEnrollmentMode rewrites one value of the file, keeping the operator's comments and the rest of the
// layout: the server's configuration is edited by hand, and a round trip through the parsed struct would
// throw both away. The result is loaded again before it replaces the file, so a broken edit cannot land.
func setEnrollmentMode(path, mode string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- the operator chooses the path.
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	root := &doc
	if len(root.Content) == 1 {
		root = root.Content[0]
	}
	enrollment := mappingValue(root, "enrollment")
	if enrollment == nil {
		return fmt.Errorf("%s: no enrollment section to change", path)
	}
	if value := mappingValue(enrollment, "mode"); value != nil {
		value.Kind = yaml.ScalarNode
		value.Tag = "!!str"
		value.Value = mode
		value.Style = 0
	} else {
		enrollment.Content = append(enrollment.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mode"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: mode})
	}

	edited, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := config.Parse(bytes.NewReader(edited)); err != nil {
		return fmt.Errorf("the edited file does not load, so nothing was written: %w", err)
	}
	return atomicfile.Write(path, edited, 0o644)
}

// mappingValue returns the value node of key in a mapping node, or nil when the key is absent.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
