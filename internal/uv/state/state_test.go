package state_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/state"
)

var fixedTime = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filename := filepath.Join(dir, "secret.json")

	if err := state.WriteFileAtomic(filename, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if err := state.WriteFileAtomic(filename, []byte("second"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "second" {
		t.Errorf("content = %q, want %q", data, "second")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only the target: %v", len(entries), names(entries))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %o, want 600", perm)
		}
	}
}

func TestLoadMissingState(t *testing.T) {
	t.Parallel()
	_, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load error = %v, want fs.ErrNotExist", err)
	}
	_, err = state.LoadInstalled(filepath.Join(t.TempDir(), "state.json"))
	if !errors.Is(err, state.ErrNotInstalled) {
		t.Fatalf("LoadInstalled error = %v, want ErrNotInstalled", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "state.json")
	st := state.New("vpn.example.com", fixedTime)
	st.Identity = state.Identity{SelfUUID: "11111111-2222-4333-8444-555555555555", XHTTPPath: "deadbeef"}
	st.Decoy = state.Decoy{Template: "portal", Seed: "00112233"}
	st.Xray = state.Xray{Version: "26.11.9", SHA256: strings.Repeat("a", 64), Path: "/usr/lib/termit/bin/xray"}
	st.Certificate = &state.Certificate{IssuedAt: fixedTime, NotAfter: fixedTime.Add(90 * 24 * time.Hour)}
	st.AddPath("/var/lib/termit/uv", state.KindDir, "dirs", fixedTime)
	if err := st.SaveAt(filename, fixedTime); err != nil {
		t.Fatalf("SaveAt: %v", err)
	}

	loaded, err := state.Load(filename)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Installed() {
		t.Error("loaded state reports not installed")
	}
	if loaded.Identity != st.Identity || loaded.Decoy != st.Decoy || loaded.Xray != st.Xray {
		t.Errorf("round trip changed state: %+v", loaded)
	}
	if loaded.Certificate == nil || !loaded.Certificate.NotAfter.Equal(st.Certificate.NotAfter) {
		t.Errorf("certificate lost: %+v", loaded.Certificate)
	}
	if len(loaded.Manifest) != 1 || loaded.Manifest[0].Kind != state.KindDir {
		t.Errorf("manifest = %+v", loaded.Manifest)
	}
	if !loaded.CreatedAt.Equal(fixedTime) {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, fixedTime)
	}
}

func TestLoadRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "state.json")
	body := `{"schema_version": 99, "domain": "vpn.example.com"}`
	if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := state.Load(filename)
	if err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("Load error = %v, want a schema version rejection", err)
	}
}

func TestManifestDeduplicates(t *testing.T) {
	t.Parallel()
	st := state.New("vpn.example.com", fixedTime)
	st.AddPath("/var/lib/termit/uv", state.KindDir, "dirs", fixedTime)
	st.AddPath("/var/lib/termit/uv/", state.KindDir, "dirs", fixedTime)
	st.AddPath("/var/lib/termit/uv/state.json", state.KindFile, "xray-binary", fixedTime)

	if len(st.Manifest) != 2 {
		t.Fatalf("manifest has %d entries, want 2: %+v", len(st.Manifest), st.Manifest)
	}
	if !st.HasPath("/var/lib/termit/uv/state.json") {
		t.Error("HasPath missed a recorded file")
	}
	st.RemovePath("/var/lib/termit/uv/state.json")
	if st.HasPath("/var/lib/termit/uv/state.json") {
		t.Error("RemovePath did not remove the entry")
	}
	if !st.Dirty() {
		t.Error("mutations did not mark the state dirty")
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	if got := state.Fingerprint(""); got != "" {
		t.Errorf("Fingerprint(\"\") = %q, want empty", got)
	}
	first := state.Fingerprint("11111111-2222-4333-8444-555555555555")
	if len(first) != 8 {
		t.Errorf("fingerprint %q has length %d, want 8", first, len(first))
	}
	if first != state.Fingerprint("11111111-2222-4333-8444-555555555555") {
		t.Error("fingerprint is not stable")
	}
	if first == state.Fingerprint("11111111-2222-4333-8444-555555555556") {
		t.Error("different secrets share a fingerprint")
	}
	if strings.Contains(first, "1111") {
		t.Error("fingerprint leaks the secret")
	}
}

func TestJournalRunLifecycle(t *testing.T) {
	t.Parallel()
	journal := &state.Journal{SchemaVersion: state.JournalSchemaVersion}
	run := journal.StartRun(fixedTime, "v1.2.3", "init")
	if !journal.Interrupted() {
		t.Fatal("a fresh run should look interrupted")
	}
	run.BeginStep("dirs", fixedTime)
	run.FinishStep("dirs", state.StepOK, "", fixedTime.Add(time.Second))
	run.Finish(state.RunOK, "", fixedTime.Add(2*time.Second))
	if journal.Interrupted() {
		t.Error("a finished run still looks interrupted")
	}

	last := journal.Last()
	if last.Result != state.RunOK || last.Version != "v1.2.3" || last.Command != "init" {
		t.Errorf("run = %+v", last)
	}
	if len(last.Steps) != 1 || last.Steps[0].Status != state.StepOK {
		t.Errorf("steps = %+v", last.Steps)
	}
	if last.Steps[0].FinishedAt == nil {
		t.Error("finished step has no FinishedAt")
	}
}

func TestJournalFinishStepIsIdempotent(t *testing.T) {
	t.Parallel()
	journal := &state.Journal{SchemaVersion: state.JournalSchemaVersion}
	run := journal.StartRun(fixedTime, "dev", "init")
	run.BeginStep("dirs", fixedTime)
	run.BeginStep("dirs", fixedTime)
	run.FinishStep("dirs", state.StepFailed, "boom", fixedTime)
	run.FinishStep("dirs", state.StepOK, "", fixedTime)
	if len(run.Steps) != 1 {
		t.Fatalf("steps = %+v, want one entry", run.Steps)
	}
	if run.Steps[0].Status != state.StepOK || run.Steps[0].Detail != "" {
		t.Errorf("final step record = %+v", run.Steps[0])
	}
}

func TestJournalIsCappedAndPersisted(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "journal.json")
	journal := &state.Journal{SchemaVersion: state.JournalSchemaVersion}
	for i := 0; i < 30; i++ {
		journal.StartRun(fixedTime.Add(time.Duration(i)*time.Minute), "dev", "init")
	}
	if len(journal.Runs) != 20 {
		t.Errorf("journal kept %d runs, want 20", len(journal.Runs))
	}
	if err := journal.Save(filename); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := state.LoadJournal(filename)
	if err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}
	if len(loaded.Runs) != 20 {
		t.Errorf("loaded %d runs, want 20", len(loaded.Runs))
	}
	if last := loaded.Last(); last == nil || !last.StartedAt.Equal(fixedTime.Add(29*time.Minute)) {
		t.Errorf("Last = %+v, want the newest run", last)
	}
}

func TestLoadJournalMissingIsEmpty(t *testing.T) {
	t.Parallel()
	journal, err := state.LoadJournal(filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}
	if journal.Last() != nil {
		t.Error("a missing journal should be empty")
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
