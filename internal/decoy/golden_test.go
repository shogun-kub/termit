package decoy_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/shogun-kub/termit/internal/decoy"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// TestGoldenPages pins the generated markup. The golden files are large on purpose: a change in the
// page is a change a reviewer should see, and `-update` makes that explicit.
func TestGoldenPages(t *testing.T) {
	t.Parallel()
	cases := []struct{ golden, template string }{
		{"portal.html", decoy.TemplatePortal},
		{"product.html", decoy.TemplateProduct},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.template, func(t *testing.T) {
			t.Parallel()
			site, err := decoy.Generate(specFor(tc.template, "0011aabbccddeeff"))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			golden := filepath.Join("testdata", tc.golden)
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(golden, site.Files["index.html"], 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden) // #nosec G304 -- a fixed test path.
			if err != nil {
				t.Fatalf("read golden file: %v (run `go test ./internal/decoy -update`)", err)
			}
			if !bytes.Equal(want, site.Files["index.html"]) {
				t.Errorf("%s differs from the generated page; regenerate with -update and review the diff", golden)
			}
		})
	}
}
