package main

import "testing"

func TestBuildGradientLengthAndStart(t *testing.T) {
	g := buildGradient([]string{"#000000", "#ffffff"}, 24)
	if len(g) != 24 {
		t.Fatalf("len = %d, want 24", len(g))
	}
	if g[0] != "#000000" {
		t.Errorf("g[0] = %q, want #000000", g[0])
	}
}

func TestPaletteIndexWraps(t *testing.T) {
	n := 5
	if got := paletteIndex(0, 0, 0, n); got != 0 {
		t.Errorf("(0,0,0) = %d, want 0", got)
	}
	if got := paletteIndex(0, 0, 3, n); got != 2 {
		t.Errorf("(0,0,3) = %d, want 2", got)
	}
	if got := paletteIndex(4, 4, 0, n); got != 3 {
		t.Errorf("(4,4,0) = %d, want 3", got)
	}
	if got := paletteIndex(0, 0, 0, 0); got != 0 {
		t.Errorf("n=0 guard = %d, want 0", got)
	}
}

func TestHexRoundTrip(t *testing.T) {
	r, g, b := hexToRGB("#89b4fa")
	if r != 0x89 || g != 0xb4 || b != 0xfa {
		t.Fatalf("hexToRGB = %d,%d,%d", r, g, b)
	}
	if got := rgbToHex(r, g, b); got != "#89b4fa" {
		t.Errorf("rgbToHex = %q, want #89b4fa", got)
	}
}
