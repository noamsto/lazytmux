package main

import "testing"

func TestParseArtDimensions(t *testing.T) {
	a := parseArt("ab\ncdef\n")
	if a.h != 2 {
		t.Errorf("h = %d, want 2", a.h)
	}
	if a.w != 4 {
		t.Errorf("w = %d, want 4 (widest line)", a.w)
	}
}

func TestFits(t *testing.T) {
	a := artGrid{w: 40, h: 8}
	if !fits(a, 80, 24) {
		t.Error("roomy viewport should fit")
	}
	if fits(a, 20, 24) {
		t.Error("too-narrow viewport should not fit")
	}
	if fits(a, 80, a.h+reserveLines-1) {
		t.Error("too-short viewport should not fit")
	}
	if !fits(a, 80, a.h+reserveLines) {
		t.Error("exact-height viewport should fit")
	}
}

func TestLoadDeckNonEmpty(t *testing.T) {
	deck := loadDeck()
	if len(deck) == 0 {
		t.Fatal("embedded frame deck is empty")
	}
	h := deck[0].h
	for i, f := range deck {
		if f.h != h {
			t.Errorf("frame %d height %d != frame 0 height %d (deck must be registered)", i, f.h, h)
		}
	}
}
