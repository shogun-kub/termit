package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"
)

// runPolicy is `mn policy show` and `mn policy set <key> <value>`. The knobs it changes are the ones
// worth turning while watching a mesh; everything else is in the configuration file and needs a
// restart, on purpose.
func runPolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "mn policy: show or set is required")
		return exitUsage
	}
	flags, configPath, socket, token := uvFlags("policy "+args[0], stderr)
	if err := flags.Parse(flagsFirst(args[1:], map[string]bool{})); err != nil {
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn policy: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch args[0] {
	case "show":
		var result struct {
			Policy map[string]string `json:"policy"`
			Keys   map[string]string `json:"keys"`
		}
		if err := client.Call(callCtx, "policy.show", nil, &result); err != nil {
			fmt.Fprintf(stderr, "mn policy show: %v\n", err)
			return exitFailure
		}
		keys := make([]string, 0, len(result.Policy))
		for key := range result.Policy {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := result.Policy[key]
			if value == "" {
				value = "(from the configuration)"
			}
			fmt.Fprintf(stdout, "%-16s %s\n", key, value)
			if help := result.Keys[key]; help != "" {
				fmt.Fprintf(stdout, "%-16s   %s\n", "", help)
			}
		}
		return exitOK

	case "set":
		if flags.NArg() != 2 {
			fmt.Fprintln(stderr, "mn policy set: a key and a value are required; an empty value clears the override")
			return exitUsage
		}
		var result struct {
			Key     string `json:"key"`
			Value   string `json:"value"`
			Cleared bool   `json:"cleared"`
		}
		if err := client.Call(callCtx, "policy.set",
			map[string]string{"key": flags.Arg(0), "value": flags.Arg(1)}, &result); err != nil {
			fmt.Fprintf(stderr, "mn policy set: %v\n", err)
			return exitFailure
		}
		if result.Cleared {
			fmt.Fprintf(stdout, "%s cleared; the configuration applies again\n", result.Key)
			return exitOK
		}
		fmt.Fprintf(stdout, "%s = %s; the scheduler picks it up on its next pass\n", result.Key, result.Value)
		return exitOK

	default:
		fmt.Fprintf(stderr, "mn policy: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}
