package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/deploy/vulns"
)

const stream = `{"config":{"protocol_version":"v1.0.0"}}
{"finding":{"osv":"GO-2026-5288","trace":[{"module":"github.com/apernet/hysteria/core/v2","package":"github.com/apernet/hysteria/core/v2/client","function":"client.NewClient"}]}}
{"finding":{"osv":"GO-2026-5288","trace":[{"module":"github.com/apernet/hysteria/core/v2","package":"github.com/apernet/hysteria/core/v2/client","function":"client.NewClient"}]}}
{"finding":{"osv":"GO-2099-0001","trace":[{"module":"example.com/x"}]}}
`

// TestParseFindingsReadsOneIdentifierPerAdvisory: govulncheck repeats a finding for every trace, and a
// gate that counted traces would report the same advisory three times.
func TestParseFindingsReadsOneIdentifierPerAdvisory(t *testing.T) {
	t.Parallel()
	ids := parseFindings([]byte(stream))
	if len(ids) != 2 || ids[0].ID != "GO-2026-5288" || ids[1].ID != "GO-2099-0001" {
		t.Fatalf("parseFindings = %v", ids)
	}
	if !ids[0].Reachable {
		t.Error("a finding with a function in its trace is not marked reachable")
	}
}

// TestReportFailsOnAnythingNotAccepted: the gate exists to stop the build on a *new* advisory. An
// accepted one is a printed reminder instead, with the action that removes it.
func TestReportFailsOnAnythingNotAccepted(t *testing.T) {
	t.Parallel()
	accepted := vulns.File{Exclusions: []vulns.Exclusion{{
		ID: "GO-2026-5288", MustBeGoneBy: "0.6.0", Action: "bump hysteria",
	}}}

	var stdout, stderr bytes.Buffer
	if code := report([]finding{{ID: "GO-2026-5288", Reachable: true}}, accepted, &stdout, &stderr); code != 0 {
		t.Fatalf("an accepted advisory failed the gate: %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "must be gone by 0.6.0") {
		t.Errorf("the reminder is not printed: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "bump hysteria") {
		t.Errorf("the action is not printed: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := report([]finding{{ID: "GO-2026-5288", Reachable: true}, {ID: "GO-2099-0001", Reachable: true}}, accepted, &stdout, &stderr); code != 1 {
		t.Fatalf("a new advisory passed the gate: %d", code)
	}
	if !strings.Contains(stderr.String(), "GO-2099-0001") {
		t.Errorf("the new advisory is not named: %s", stderr.String())
	}
}

// TestParseFindingsReadsPrettyPrintedObjects: govulncheck writes an indented sequence of objects, several
// lines each. A reader that splits on newlines finds nothing and reports a clean build - the worst kind
// of gate, one that always passes.
func TestParseFindingsReadsPrettyPrintedObjects(t *testing.T) {
	t.Parallel()
	const pretty = `{
  "config": {
    "protocol_version": "v1.0.0"
  }
}
{
  "finding": {
    "osv": "GO-2026-5288",
    "trace": [
      {
        "module": "github.com/apernet/hysteria/core/v2"
      }
    ]
  }
}
`
	ids := parseFindings([]byte(pretty))
	if len(ids) != 1 || ids[0].ID != "GO-2026-5288" {
		t.Fatalf("parseFindings = %v, want the one advisory", ids)
	}
}

// TestReportEnforcesOnlyWhatOurCodeCanReach: an advisory in the module graph that no path of ours calls
// is a fact about the dependency tree, and blocking a build on it teaches everyone to edit the list.
// A reachable one is a real failure.
func TestReportEnforcesOnlyWhatOurCodeCanReach(t *testing.T) {
	t.Parallel()
	accepted := vulns.File{}
	var stdout, stderr bytes.Buffer
	graphOnly := []finding{{ID: "GO-2026-6860", Reachable: false}, {ID: "GO-2026-6866", Reachable: false}}
	if code := report(graphOnly, accepted, &stdout, &stderr); code != 0 {
		t.Fatalf("graph-only advisories failed the gate: %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not called") {
		t.Errorf("the graph-only advisories are not reported: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := report([]finding{{ID: "GO-2026-9999", Reachable: true}}, accepted, &stdout, &stderr); code != 1 {
		t.Fatalf("a reachable advisory passed the gate: %d", code)
	}
	if !strings.Contains(stderr.String(), "GO-2026-9999") {
		t.Errorf("the reachable advisory is not named: %s", stderr.String())
	}
}
