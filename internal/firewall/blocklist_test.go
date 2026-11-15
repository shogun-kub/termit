package firewall

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const sampleList = `# a comment
192.0.2.0/24
198.51.100.0/24   # trailing comment
2001:db8::/32

192.0.2.0/24
203.0.113.7
`

// TestParseBlocklist reads what these lists actually look like: comments, blanks, duplicates, a bare
// address.
func TestParseBlocklist(t *testing.T) {
	t.Parallel()
	prefixes, err := ParseBlocklist(strings.NewReader(sampleList))
	if err != nil {
		t.Fatalf("ParseBlocklist: %v", err)
	}
	got := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		got = append(got, prefix.String())
	}
	want := []string{"192.0.2.0/24", "198.51.100.0/24", "2001:db8::/32", "203.0.113.7/32"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("parsed %v, want %v", got, want)
	}
}

// TestParseBlocklistRefusesGarbage: a truncated download ends mid-line, and that has to be an error
// rather than a shorter list nobody notices.
func TestParseBlocklistRefusesGarbage(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"cut mid-line":    "192.0.2.0/24\n198.51.100",
		"bad prefix":      "192.0.2.0/33\n",
		"a word":          "not-a-network\n",
		"html error page": "<html><body>404</body></html>\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseBlocklist(strings.NewReader(body)); err == nil {
				t.Error("the list was accepted")
			}
		})
	}
}

// TestRenderBlocklistRoundTrips: the file is read back by the next refresh and by `firewall on`, so the
// two directions have to agree, and the same set of networks has to render to the same bytes.
func TestRenderBlocklistRoundTrips(t *testing.T) {
	t.Parallel()
	prefixes, err := ParseBlocklist(strings.NewReader(sampleList))
	if err != nil {
		t.Fatalf("ParseBlocklist: %v", err)
	}
	first := RenderBlocklist(prefixes)
	shuffled := []netip.Prefix{prefixes[3], prefixes[1], prefixes[0], prefixes[2]}
	second := RenderBlocklist(shuffled)
	if string(first) != string(second) {
		t.Errorf("the render depends on the order:\n%s\n%s", first, second)
	}
	back, err := ParseBlocklist(strings.NewReader(string(first)))
	if err != nil {
		t.Fatalf("ParseBlocklist: %v", err)
	}
	if len(back) != len(prefixes) {
		t.Errorf("the round trip changed the list: %d -> %d", len(prefixes), len(back))
	}
}

// TestSaveBlocklistRefusesEmpty: an empty file would be read back as "no list" and silently unblock
// everything that was blocked.
func TestSaveBlocklistRefusesEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "blocklist.txt")
	if err := SaveBlocklist(path, nil); err == nil {
		t.Fatal("an empty list was written")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file exists anyway: %v", err)
	}
}

// TestLoadBlocklistMissingIsEmpty: a node that never fetched a list has no file.
func TestLoadBlocklistMissingIsEmpty(t *testing.T) {
	t.Parallel()
	prefixes, at, err := LoadBlocklist(filepath.Join(t.TempDir(), "blocklist.txt"))
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if len(prefixes) != 0 || !at.IsZero() {
		t.Errorf("loaded %d networks, fetched at %v", len(prefixes), at)
	}
}

// TestFetchFileURL is the lab's transport, and the reason the config accepts file://.
func TestFetchFileURL(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// file:///C:/... does not parse into a path any OS here can open; the transport is for the
		// Linux host, and the lab test exercises it there.
		t.Skip("file URLs are a Linux path form")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(path, []byte("192.0.2.0/24\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fetcher := Fetcher{}
	body, notModified, err := fetcher.Fetch(context.Background(), "file://"+path, time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if notModified || !strings.Contains(string(body), "192.0.2.0/24") {
		t.Errorf("body = %q, notModified = %v", body, notModified)
	}
	// The second call, with the file's own time, is unchanged.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	_, notModified, err = fetcher.Fetch(context.Background(), "file://"+path, info.ModTime())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !notModified {
		t.Error("an unchanged file was fetched again")
	}
}

// TestFetchRefusesWhatItDoesNotSpeak: no git, no rsync, no relative paths.
func TestFetchRefusesWhatItDoesNotSpeak(t *testing.T) {
	t.Parallel()
	fetcher := Fetcher{}
	for _, rawURL := range []string{"git+https://x/y", "/etc/passwd", "ftp://example.com/list"} {
		if _, _, err := fetcher.Fetch(context.Background(), rawURL, time.Time{}); err == nil {
			t.Errorf("%q was accepted", rawURL)
		}
	}
}

// TestFetchHTTP pins the conditional request, the status handling and the size cap.
func TestFetchHTTP(t *testing.T) {
	t.Parallel()
	var gotConditional string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConditional = r.Header.Get("If-Modified-Since")
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "192.0.2.0/24\n")
		case "/same":
			w.WriteHeader(http.StatusNotModified)
		case "/broken":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/huge":
			fmt.Fprint(w, strings.Repeat("192.0.2.0/24\n", 1000))
		}
	}))
	defer server.Close()

	fetcher := Fetcher{MaxBytes: 128}
	body, notModified, err := fetcher.Fetch(context.Background(), server.URL+"/ok", time.Time{})
	if err != nil || notModified || !strings.Contains(string(body), "192.0.2.0/24") {
		t.Fatalf("fetch ok: body %q, notModified %v, err %v", body, notModified, err)
	}
	if gotConditional != "" {
		t.Errorf("the first request was conditional: %q", gotConditional)
	}
	_, notModified, err = fetcher.Fetch(context.Background(), server.URL+"/same", time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	if err != nil || !notModified {
		t.Errorf("304: notModified %v, err %v", notModified, err)
	}
	if gotConditional == "" {
		t.Error("the conditional request carried no If-Modified-Since")
	}
	if _, _, err := fetcher.Fetch(context.Background(), server.URL+"/broken", time.Time{}); err == nil {
		t.Error("a 500 was accepted")
	}
	if _, _, err := fetcher.Fetch(context.Background(), server.URL+"/huge", time.Time{}); err == nil {
		t.Error("a body past the cap was accepted")
	}
}

// TestReplaceBlockedIsOneBatch: the flush and the elements travel together, so the host is never
// without a blocklist, and an empty family still gets flushed.
func TestReplaceBlockedIsOneBatch(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	prefixes, err := ParseBlocklist(strings.NewReader(sampleList))
	if err != nil {
		t.Fatalf("ParseBlocklist: %v", err)
	}
	if err := (Applier{Runner: runner}).ReplaceBlocked(context.Background(), NodeTable, prefixes); err != nil {
		t.Fatalf("ReplaceBlocked: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "nft -f -" {
		t.Fatalf("calls = %v, want one batch", runner.calls)
	}
	sent := runner.stdin[0]
	for _, want := range []string{
		"flush set inet termit_uv blocked4",
		"add element inet termit_uv blocked4 { 192.0.2.0/24, 198.51.100.0/24, 203.0.113.7/32 }",
		"flush set inet termit_uv blocked6",
		"add element inet termit_uv blocked6 { 2001:db8::/32 }",
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("the batch is missing %q:\n%s", want, sent)
		}
	}
}

// TestReplaceBlockedChunksALongList: a real list does not fit in one statement, and the chunks must be
// separate statements in the same batch.
func TestReplaceBlockedChunksALongList(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	prefixes := make([]netip.Prefix, 0, blocklistChunk+10)
	for i := 0; i < blocklistChunk+10; i++ {
		prefixes = append(prefixes, netip.MustParsePrefix(fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)))
	}
	if err := (Applier{Runner: runner}).ReplaceBlocked(context.Background(), NodeTable, prefixes); err != nil {
		t.Fatalf("ReplaceBlocked: %v", err)
	}
	if got := strings.Count(runner.stdin[0], "add element inet termit_uv blocked4"); got != 2 {
		t.Errorf("the list went out in %d statements, want 2", got)
	}
}
