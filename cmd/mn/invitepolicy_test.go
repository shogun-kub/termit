package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/config"
)

// policyConfig writes a loadable configuration whose enrollment.mode line carries a comment, the way an
// operator's file does, and returns its path.
func policyConfig(t *testing.T) (string, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Domain = "mn.example.com"
	cfg.DataDir = t.TempDir()
	body, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	handEdited := strings.Replace(string(body), "  mode: ", "  # the rule a new node meets\n  mode: ", 1)
	if handEdited == string(body) {
		t.Fatalf("the marshalled configuration has no enrollment.mode line:\n%s", body)
	}
	if err := os.WriteFile(path, []byte(handEdited), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path, handEdited
}

func runEnrollmentPolicy(t *testing.T, path string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := runInvitePolicy(append([]string{"--config", path}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// The show without a mode names the rule in force and the file it came from, and changes nothing.
func TestInvitePolicyShowsTheMode(t *testing.T) {
	t.Parallel()
	path, before := policyConfig(t)
	code, out, errOut := runEnrollmentPolicy(t, path)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "enrollment.mode: invite+approval") {
		t.Errorf("output = %q, want the mode in force", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != before {
		t.Error("showing the mode rewrote the file")
	}
}

// Changing it keeps the operator's comment, and says that the running server has not noticed yet.
func TestInvitePolicyChangesAndKeepsComments(t *testing.T) {
	t.Parallel()
	path, _ := policyConfig(t)
	code, out, errOut := runEnrollmentPolicy(t, path, "--mode", config.ModeInvite)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "invite+approval -> invite") {
		t.Errorf("output = %q, want the change", out)
	}
	if !strings.Contains(out, "systemctl restart termit-mn") {
		t.Errorf("output = %q, want the restart the change needs", out)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(edited), "mode: invite") {
		t.Errorf("the file does not carry the new mode:\n%s", edited)
	}
	if !strings.Contains(string(edited), "# the rule a new node meets") {
		t.Errorf("the operator's comment was lost:\n%s", edited)
	}
	// The rest of the file is what it was: the edit touches one value.
	if !strings.Contains(string(edited), "domain: mn.example.com") {
		t.Errorf("the edit changed something else:\n%s", edited)
	}
}

// open is the mode that needs no code and no approval, so it is refused unless the operator says so and
// the refusal leaves the file alone.
func TestInvitePolicyRefusesOpenWithoutForce(t *testing.T) {
	t.Parallel()
	path, before := policyConfig(t)
	code, _, errOut := runEnrollmentPolicy(t, path, "--mode", config.ModeOpen)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "--force") {
		t.Errorf("stderr = %q, want it to name the flag", errOut)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != before {
		t.Error("a refused change rewrote the file")
	}

	code, out, errOut := runEnrollmentPolicy(t, path, "--mode", config.ModeOpen, "--force")
	if code != exitOK {
		t.Fatalf("forced exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "open") {
		t.Errorf("output = %q, want the new mode", out)
	}
}

func TestInvitePolicyRejectsAnUnknownMode(t *testing.T) {
	t.Parallel()
	path, before := policyConfig(t)
	code, _, errOut := runEnrollmentPolicy(t, path, "--mode", "everyone")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "unknown mode") {
		t.Errorf("stderr = %q, want it to name the problem", errOut)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != before {
		t.Error("an unusable answer rewrote the file")
	}
}

func TestInvitePolicySaysWhenNothingChanges(t *testing.T) {
	t.Parallel()
	path, _ := policyConfig(t)
	code, out, _ := runEnrollmentPolicy(t, path, "--mode", config.ModeInviteApproval)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "already invite+approval") {
		t.Errorf("output = %q, want it to say the mode is already in force", out)
	}
}

// The menu turns the three answers into the command line, and passes --force only after the question that
// widens the door.
func TestInvitePolicyMenu(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answers string
		want    string
	}{
		"by invite":   {"1\n", "invite policy --mode invite"},
		"by approval": {"2\n", "invite policy --mode invite+approval"},
		"open":        {"3\ny\n", "invite policy --mode open --force"},
	}
	for name, tc := range cases {
		m, _, _ := newMenu("")
		plan, err := runEntry(t, m, childOf(t, m, "invites", "enrollment rules"), tc.answers)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.Join(plan[0], " "); got != tc.want {
			t.Errorf("%s: runs %q, want %q", name, got, tc.want)
		}
	}

	declined, _, _ := newMenu("")
	plan, err := runEntry(t, declined, childOf(t, declined, "invites", "enrollment rules"), "3\nn\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan != nil {
		t.Errorf("declining to open enrollment produced %q", plan)
	}
}

// The block the operator forwards has to carry everything one paste needs: the server, its prefix and the
// code, in the one command the installer understands.
func TestInviteBlockIsOnePaste(t *testing.T) {
	t.Parallel()
	block := inviteBlock("mn.example.com", "/6rfjdqfn5nno", "ABCD-EFGH-IJKL", "anna")
	if !strings.Contains(block, "sh uv.sh --mn https://mn.example.com/6rfjdqfn5nno --invite ABCD-EFGH-IJKL") {
		t.Errorf("block = %q, want the whole command on one line", block)
	}
	if !strings.Contains(block, "for: anna") {
		t.Errorf("block = %q, want the note as the person's name", block)
	}
	if strings.Contains(inviteBlock("mn.example.com", "/p", "CODE", ""), "for:") {
		t.Error("a code with no note grew a name")
	}
}
