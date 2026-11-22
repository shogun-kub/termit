package options

import (
	"fmt"
	"html/template"
	"strings"

	"rsc.io/qr"
)

// qrSVG draws a client URI as an SVG QR code, for the one gesture a phone actually has.
//
// The encoder is in-process on purpose: the page may not reference an external host, and a QR service
// would also hand the node's credentials to whoever runs it. Level L is the right level for a screen —
// there is no print damage to correct — and it keeps the dense mesh URIs in a smaller grid.
//
// A URI that does not fit returns nothing rather than a corrupt code; the copy button still works. The
// quiet zone is inside the viewBox for the same reason: a code touching its border does not scan.
func qrSVG(uri string) template.HTML {
	if uri == "" {
		return ""
	}
	code, err := qr.Encode(uri, qr.L)
	if err != nil {
		return ""
	}
	const quiet = 4
	side := code.Size + 2*quiet
	var b strings.Builder
	// Inline SVG in an HTML document needs no xmlns, and the page must reference no host at all.
	b.WriteString(`<svg viewBox="0 0 `)
	fmt.Fprintf(&b, "%d %d", side, side)
	b.WriteString(`" shape-rendering="crispEdges" role="img">`)
	// One path for every dark module: a rect each would triple the markup, and the page can show
	// several codes at once. The digits are the only thing written into it.
	b.WriteString(`<path d="`)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"></path></svg>`)
	return template.HTML(b.String()) //nolint:gosec // digits and SVG syntax this file wrote
}
