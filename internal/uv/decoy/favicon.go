package decoy

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"strconv"
	"strings"
)

// faviconSize is the icon's edge in pixels. 32 is what a browser tab shows at 1x and 2x.
const faviconSize = 32

// faviconPNG draws the icon in the site's own accent colour. It is generated from the same seed as
// the page, so a scanner comparing the favicon with the site's palette sees them agree — and no two
// deployments share an icon.
func faviconPNG(rng *rand.Rand, colours palette) ([]byte, error) {
	accent, err := parseHexColor(colours.Accent)
	if err != nil {
		return nil, err
	}
	ink, err := parseHexColor(colours.AccentInk)
	if err != nil {
		return nil, err
	}

	img := image.NewRGBA(image.Rect(0, 0, faviconSize, faviconSize))
	const corner = 6
	for y := 0; y < faviconSize; y++ {
		for x := 0; x < faviconSize; x++ {
			if inRoundedSquare(x, y, faviconSize, corner) {
				img.Set(x, y, accent)
			}
		}
	}

	// Geometry is seed-dependent too: the motif, its offset and its thickness all vary, so two
	// deployments do not accidentally share an icon.
	jitterX := rng.IntN(5) - 2
	jitterY := rng.IntN(5) - 2
	thickness := 3 + rng.IntN(2)

	switch rng.IntN(6) {
	case 0:
		fillRect(img, 8+jitterX, 11+jitterY, 24+jitterX, 11+jitterY+thickness, ink)
		fillRect(img, 8+jitterX, 18+jitterY, 19+jitterX, 18+jitterY+thickness, ink)
	case 1:
		fillCircle(img, 16+jitterX, 16+jitterY, 7, ink)
		fillCircle(img, 16+jitterX, 16+jitterY, thickness, accent)
	case 2:
		for i := 0; i < 13; i++ {
			fillRect(img, 9+i+jitterX, 9+i+jitterY, 9+i+jitterX+thickness+1, 9+i+jitterY+thickness+1, ink)
		}
	case 3:
		fillRect(img, 9+jitterX, 12+jitterY, 9+jitterX+thickness, 21+jitterY, ink)
		fillRect(img, 9+jitterX, 9+jitterY, 23+jitterX, 9+jitterY+thickness, ink)
		fillRect(img, 20+jitterX, 9+jitterY, 20+jitterX+thickness, 18+jitterY, ink)
	case 4:
		fillCircle(img, 12+jitterX, 12+jitterY, 4, ink)
		fillCircle(img, 20+jitterX, 16+jitterY, 4, ink)
		fillRect(img, 12+jitterX, 20+jitterY, 20+jitterX, 20+jitterY+thickness, ink)
	default:
		fillRect(img, 8+jitterX, 8+jitterY, 24+jitterX, 11+jitterY, ink)
		fillRect(img, 13+jitterX, 11+jitterY, 13+jitterX+thickness, 22+jitterY, ink)
		fillRect(img, 19+jitterX, 11+jitterY, 19+jitterX+thickness, 22+jitterY, ink)
	}

	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("decoy: encode favicon: %w", err)
	}
	return buf.Bytes(), nil
}

// inRoundedSquare reports whether (x, y) is inside a square of the given size with rounded corners.
func inRoundedSquare(x, y, size, radius int) bool {
	min, max := radius, size-1-radius
	if (x >= min && x <= max) || (y >= min && y <= max) {
		return true
	}
	// Outside the cross, the pixel belongs to a corner: inside only within the corner circle.
	cx, cy := min, min
	if x > max {
		cx = max
	}
	if y > max {
		cy = max
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if image.Pt(x, y).In(img.Bounds()) {
				img.Set(x, y, c)
			}
		}
	}
}

func fillCircle(img *image.RGBA, cx, cy, radius int, c color.RGBA) {
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= radius*radius && image.Pt(x, y).In(img.Bounds()) {
				img.Set(x, y, c)
			}
		}
	}
}

// parseHexColor reads the "#rrggbb" form used by the palettes.
func parseHexColor(value string) (color.RGBA, error) {
	if len(value) != 7 || !strings.HasPrefix(value, "#") {
		return color.RGBA{}, fmt.Errorf("decoy: colour %q is not #rrggbb", value)
	}
	component := func(offset int) (uint8, error) {
		parsed, err := strconv.ParseUint(value[offset:offset+2], 16, 8)
		return uint8(parsed), err
	}
	r, err := component(1)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("decoy: colour %q is not #rrggbb", value)
	}
	g, err := component(3)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("decoy: colour %q is not #rrggbb", value)
	}
	b, err := component(5)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("decoy: colour %q is not #rrggbb", value)
	}
	return color.RGBA{R: r, G: g, B: b, A: 0xff}, nil
}
