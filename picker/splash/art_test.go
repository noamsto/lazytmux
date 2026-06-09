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

func TestPickArt(t *testing.T) {
	full := artGrid{w: 40, h: 8}
	small := artGrid{w: 12, h: 3}
	const reserve = 10

	if a, show := pickArt(full, small, 80, 24); !show || a.w != full.w {
		t.Errorf("roomy viewport should pick full, got show=%v w=%d", show, a.w)
	}
	if a, show := pickArt(full, small, 20, 24); !show || a.w != small.w {
		t.Errorf("narrow viewport should pick small, got show=%v w=%d", show, a.w)
	}
	if _, show := pickArt(full, small, 8, reserve+1); show {
		t.Error("tiny viewport should drop the mascot (show=false)")
	}
}
