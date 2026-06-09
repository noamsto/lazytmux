package main

import (
	"fmt"
	"strconv"
)

// Catppuccin gradient anchors per theme: blue → sapphire → lavender → mauve.
var gradientAnchors = map[string][]string{
	"dark":  {"#89b4fa", "#74c7ec", "#b4befe", "#cba6f7"},
	"light": {"#1e66f5", "#209fb5", "#7287fd", "#8839ef"},
}

func hexToRGB(hex string) (int, int, int) {
	r, _ := strconv.ParseInt(hex[1:3], 16, 0)
	g, _ := strconv.ParseInt(hex[3:5], 16, 0)
	b, _ := strconv.ParseInt(hex[5:7], 16, 0)
	return int(r), int(g), int(b)
}

func rgbToHex(r, g, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func lerp(a, b float64, t float64) float64 { return a + (b-a)*t }

// buildGradient interpolates the anchors into `steps` evenly-spaced colors,
// looping back to the first anchor so the ripple is seamless.
func buildGradient(anchors []string, steps int) []string {
	loop := append(append([]string{}, anchors...), anchors[0])
	segs := len(loop) - 1
	out := make([]string, steps)
	for i := 0; i < steps; i++ {
		p := float64(i) / float64(steps) * float64(segs)
		seg := int(p)
		if seg >= segs {
			seg = segs - 1
		}
		frac := p - float64(seg)
		r1, g1, b1 := hexToRGB(loop[seg])
		r2, g2, b2 := hexToRGB(loop[seg+1])
		out[i] = rgbToHex(
			int(lerp(float64(r1), float64(r2), frac)),
			int(lerp(float64(g1), float64(g2), frac)),
			int(lerp(float64(b1), float64(b2), frac)),
		)
	}
	return out
}

// paletteIndex maps cell (x,y) at frame t onto a gradient index, wrapping.
func paletteIndex(x, y, t, n int) int {
	if n <= 0 {
		return 0
	}
	i := (x + y - t) % n
	if i < 0 {
		i += n
	}
	return i
}
