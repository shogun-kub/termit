package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
)

// runAdmin dispatches `termit-mn admin …`: the operations that manage the operator credential itself,
// as opposed to the mesh.
func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "termit-mn admin: one of rotate is required")
		return exitUsage
	}
	switch args[0] {
	case "rotate":
		return runAdminRotate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "termit-mn admin: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

// runAdminRotate replaces the token this command authenticated with. The new token is printed once, or
// written to a file, and the old one stops working immediately — including in every timer and script
// that still carries it, which is the point and also the risk.
func runAdminRotate(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token, tokenFile := uvFlags("termit-mn admin rotate", stderr)
	tokenOut := flags.String("token-out", "",
		"write the new token to this file (0600) instead of printing it")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn admin rotate: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	client, err := adminClient(*configPath, *socket, *token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn admin rotate: %v\n", err)
		return exitFailure
	}
	var result admin.AdminTokenView
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(callCtx, "admin.rotate", nil, &result); err != nil {
		fmt.Fprintf(stderr, "termit-mn admin rotate: %v\n", err)
		return exitFailure
	}

	fmt.Fprintf(stdout, "rotated the token of admin %q (%s)\n", result.Name, result.ID)
	fmt.Fprintln(stdout, "the previous token stopped working the moment this one was issued: update every")
	fmt.Fprintln(stdout, "timer, script and playbook that still carries it")

	if *tokenOut != "" {
		if err := writeTokenFile(*tokenOut, result.Token); err != nil {
			// The new token exists only in this response, so losing it locks the operator out. Print it.
			fmt.Fprintf(stdout, "\nnew admin token (the file could not be written, so it is printed instead):\n  %s\n",
				result.Token)
			fmt.Fprintf(stderr, "termit-mn admin rotate: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(stdout, "\nnew admin token written to %s (mode 0600)\n", *tokenOut)
		return exitOK
	}
	fmt.Fprintf(stdout, "\nnew admin token (shown once, store it now):\n  %s\n", result.Token)
	return exitOK
}
