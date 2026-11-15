package firewall

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

const (
	// DefaultBlocklistMaxBytes bounds one download. A list that needs more than this is not a blocklist
	// any more, and a hostile or broken server must not be able to fill the node's disk through us.
	DefaultBlocklistMaxBytes = 4 << 20
	// blocklistChunk is how many networks go into one `add element` statement. nft accepts thousands,
	// but a single statement with a hundred thousand would be a command line nobody can debug.
	blocklistChunk = 5000
)

// ParseBlocklist reads a list of networks: one per line, `#` starts a comment, blank lines are
// ignored, and a bare address means its /32 or /128. The result is normalised, sorted and free of
// duplicates, so two lists with the same networks render to the same bytes and the file's digest means
// something.
func ParseBlocklist(r io.Reader) ([]netip.Prefix, error) {
	scanner := bufio.NewScanner(r)
	// nft lists from the wild carry long lines and the default buffer is 64 KiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := make(map[netip.Prefix]bool)
	prefixes := make([]netip.Prefix, 0, 1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if comment := strings.IndexByte(text, '#'); comment >= 0 {
			text = text[:comment]
		}
		for _, field := range strings.Fields(text) {
			prefix, err := ParsePrefix(field)
			if err != nil {
				return nil, fmt.Errorf("line %d: %q is not a network", line, field)
			}
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			prefixes = append(prefixes, prefix)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the list: %w", err)
	}
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int { return strings.Compare(a.String(), b.String()) })
	return prefixes, nil
}

// RenderBlocklist writes the list the way LoadBlocklist reads it back: one network per line, sorted, no
// timestamp — the file's modification time is the timestamp.
func RenderBlocklist(prefixes []netip.Prefix) []byte {
	sorted := append([]netip.Prefix(nil), prefixes...)
	slices.SortFunc(sorted, func(a, b netip.Prefix) int { return strings.Compare(a.String(), b.String()) })
	sorted = slices.Compact(sorted)
	var b strings.Builder
	for _, prefix := range sorted {
		b.WriteString(prefix.String())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// LoadBlocklist reads the file that is in force. An absent file is an empty list and a zero time: no
// list has ever been fetched here.
func LoadBlocklist(path string) ([]netip.Prefix, time.Time, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path comes from the component's configuration.
	if errors.Is(err, os.ErrNotExist) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("firewall: read %s: %w", path, err)
	}
	prefixes, err := ParseBlocklist(strings.NewReader(string(data)))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("firewall: %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("firewall: stat %s: %w", path, err)
	}
	return prefixes, info.ModTime(), nil
}

// SaveBlocklist replaces the file atomically. The old list stays in force until the new one has parsed,
// which is the whole point: a broken download must never leave the host unblocked.
func SaveBlocklist(path string, prefixes []netip.Prefix) error {
	if len(prefixes) == 0 {
		return errors.New("firewall: refusing to write an empty blocklist")
	}
	if err := atomicfile.Write(path, RenderBlocklist(prefixes), 0o644); err != nil {
		return fmt.Errorf("firewall: write %s: %w", path, err)
	}
	return nil
}

// Fetcher downloads a blocklist. It understands http(s) and file URLs, which is what a mirror and a
// lab need; git, rsync and every other transport are deliberately out of scope.
type Fetcher struct {
	// HTTP is the client; nil means a client with a 2-minute timeout.
	HTTP *http.Client
	// MaxBytes bounds the body; zero means DefaultBlocklistMaxBytes.
	MaxBytes int64
}

// Fetch returns the list behind rawURL, and whether the source said it has not changed since the given
// time. A 304 or a file whose modification time did not move costs nothing and is not an error.
func (f Fetcher) Fetch(ctx context.Context, rawURL string, since time.Time) (body []byte, notModified bool, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, false, fmt.Errorf("firewall: %q is not a URL: %w", rawURL, err)
	}
	limit := f.MaxBytes
	if limit <= 0 {
		limit = DefaultBlocklistMaxBytes
	}
	switch parsed.Scheme {
	case "file":
		path := parsed.Path
		if path == "" {
			return nil, false, fmt.Errorf("firewall: %q has no path", rawURL)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, false, fmt.Errorf("firewall: %s: %w", path, err)
		}
		if !since.IsZero() && !info.ModTime().After(since) {
			return nil, true, nil
		}
		if info.Size() > limit {
			return nil, false, fmt.Errorf("firewall: %s is %d bytes, more than the %d allowed", path, info.Size(), limit)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- the operator names the file.
		if err != nil {
			return nil, false, fmt.Errorf("firewall: read %s: %w", path, err)
		}
		return data, false, nil
	case "http", "https":
		return f.fetchHTTP(ctx, rawURL, since, limit)
	}
	return nil, false, fmt.Errorf("firewall: %q: only http, https and file URLs are supported", rawURL)
}

// fetchHTTP performs the conditional GET.
func (f Fetcher) fetchHTTP(ctx context.Context, rawURL string, since time.Time, limit int64) ([]byte, bool, error) {
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("firewall: %q: %w", rawURL, err)
	}
	if !since.IsZero() {
		request.Header.Set("If-Modified-Since", since.UTC().Format(http.TimeFormat))
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false, fmt.Errorf("firewall: fetch %s: %w", rawURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified {
		return nil, true, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("firewall: fetch %s: %s", rawURL, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, false, fmt.Errorf("firewall: read %s: %w", rawURL, err)
	}
	if int64(len(body)) > limit {
		return nil, false, fmt.Errorf("firewall: %s is larger than the %d bytes allowed", rawURL, limit)
	}
	return body, false, nil
}

// ReplaceBlocked makes the kernel's blocklist sets match prefixes in one transaction: the flush and the
// new elements are the same batch, so the host is never without a blocklist. Callers check that the
// table exists first — this only touches sets.
func (a Applier) ReplaceBlocked(ctx context.Context, table string, prefixes []netip.Prefix) error {
	if err := validateName(table); err != nil {
		return err
	}
	if a.Runner == nil {
		return errors.New("firewall: no runner")
	}
	var b strings.Builder
	for _, family := range []struct {
		set  string
		keep func(netip.Prefix) bool
	}{
		{"blocked4", func(p netip.Prefix) bool { return p.Addr().Is4() }},
		{"blocked6", func(p netip.Prefix) bool { return p.Addr().Is6() && !p.Addr().Is4In6() }},
	} {
		fmt.Fprintf(&b, "flush set inet %s %s\n", table, family.set)
		chunk := make([]string, 0, blocklistChunk)
		flush := func() {
			if len(chunk) == 0 {
				return
			}
			fmt.Fprintf(&b, "add element inet %s %s { %s }\n", table, family.set, strings.Join(chunk, ", "))
			chunk = chunk[:0]
		}
		for _, prefix := range prefixes {
			if !family.keep(prefix) {
				continue
			}
			chunk = append(chunk, prefix.String())
			if len(chunk) == blocklistChunk {
				flush()
			}
		}
		flush()
	}
	if _, err := a.Runner.Run(ctx, a.binary(), []string{"-f", "-"}, b.String()); err != nil {
		return fmt.Errorf("firewall: load the blocklist: %w", err)
	}
	return nil
}
