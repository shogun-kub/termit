// Command vulngate runs govulncheck and fails on any finding the repository has not accepted on purpose.
//
// govulncheck has no exclusion flag, and a gate nobody can pass honestly gets deleted rather than
// obeyed. This command is the honest version: every finding must be either absent or named in
// deploy/vulnerabilities.json, which also names the release that must not see it — so "allowed" is a
// deadline, not a permanent state.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/shogun-kub/termit/deploy/vulns"
)

// defaultTool is the pinned govulncheck. It is kept in one place so a bump is one line, not a hunt.
const defaultTool = "golang.org/x/vuln/cmd/govulncheck@v1.9.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("vulngate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tool := flags.String("tool", defaultTool, "govulncheck package to run")
	file := flags.String("exclusions", "deploy/vulnerabilities.json", "the accepted advisories")
	goBinary := flags.String("go", "go", "go command")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	packages := flags.Args()
	if len(packages) == 0 {
		packages = []string{"./cmd/...", "./internal/..."}
	}
	accepted, err := vulns.Load(*file)
	if err != nil {
		fmt.Fprintf(stderr, "vulngate: %v\n", err)
		return 2
	}

	var out, errOut bytes.Buffer
	command := exec.Command(*goBinary, append([]string{"run", *tool, "-json"}, packages...)...) // #nosec G204 -- the tool and the packages are flags, and this runs in CI.
	command.Stdout = &out
	command.Stderr = &errOut
	// A non-zero exit is what govulncheck does when it has findings: the output is the answer, not the
	// exit code.
	if err := command.Run(); err != nil && out.Len() == 0 {
		fmt.Fprintf(stderr, "vulngate: %v: %s\n", err, strings.TrimSpace(errOut.String()))
		return 2
	}
	return report(parseFindings(out.Bytes()), accepted, stdout, stderr)
}

// finding is one advisory as the gate cares about it: its identifier, and whether our own code can reach
// the vulnerable symbol. An advisory that is only in the module graph is not something a build can fix by
// writing code - it is a fact about the dependency tree - so it is reported, not enforced.
type finding struct {
	ID        string
	Reachable bool
}

// parseFindings decodes govulncheck's JSON stream. The stream is a sequence of pretty-printed objects,
// several lines each, so it is decoded rather than split on newlines: a line-based reader finds nothing
// at all and reports a clean build, which is the worst kind of gate.
func parseFindings(raw []byte) []finding {
	seen := map[string]bool{}
	var findings []finding
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var message struct {
			Finding struct {
				OSV   string `json:"osv"`
				Trace []struct {
					Function string `json:"function"`
				} `json:"trace"`
			} `json:"finding"`
		}
		if err := decoder.Decode(&message); err != nil {
			break
		}
		id := message.Finding.OSV
		if id == "" {
			continue
		}
		reachable := false
		for _, frame := range message.Finding.Trace {
			if frame.Function != "" {
				reachable = true
				break
			}
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		findings = append(findings, finding{ID: id, Reachable: reachable})
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings
}

// report turns the findings into an exit code: nothing found is success, an unaccepted finding is a
// failure, and an accepted one is a printed reminder.
func report(findings []finding, accepted vulns.File, stdout, stderr io.Writer) int {
	allowed := map[string]vulns.Exclusion{}
	for _, exclusion := range accepted.Exclusions {
		allowed[exclusion.ID] = exclusion
	}
	var unknown []string
	for _, item := range findings {
		exclusion, ok := allowed[item.ID]
		if !ok {
			if item.Reachable {
				unknown = append(unknown, item.ID)
			} else {
				fmt.Fprintf(stdout, "in the module graph, not called: %s\n", item.ID)
			}
			continue
		}
		fmt.Fprintf(stdout, "accepted: %s (must be gone by %s)\n", item.ID, exclusion.MustBeGoneBy)
		if exclusion.Action != "" {
			fmt.Fprintf(stdout, "  to clear it: %s\n", exclusion.Action)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintf(stderr, "vulngate: %d reachable finding(s) with no entry in the accepted list: %s\n",
			len(unknown), strings.Join(unknown, ", "))
		fmt.Fprintln(stderr, "vulngate: fix them, or add them to deploy/vulnerabilities.json with the release that must not see them")
		return 1
	}
	return 0
}
