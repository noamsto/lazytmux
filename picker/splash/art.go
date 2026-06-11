package main

import (
	_ "embed"
	"strconv"
	"strings"
	"unicode/utf8"
)

// frames.txt is the breathing + drifting-Z cat loop: a header line with the
// per-frame row count, then every frame's rows concatenated (all frames the
// same height). cat-small.txt is a single static frame for small viewports.
//
//go:embed assets/frames.txt
var framesRaw string

//go:embed assets/cat-small.txt
var catSmall string

type artGrid struct {
	lines []string
	w, h  int
}

func parseArt(s string) artGrid {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	w := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > w {
			w = n
		}
	}
	return artGrid{lines: lines, w: w, h: len(lines)}
}

// loadDeck reads the header row count, then chunks the remaining rows into
// fixed-height frames. Every frame is the same height, so the deck stays
// vertically registered as it cycles.
func loadDeck() []artGrid {
	rows := strings.Split(strings.TrimRight(framesRaw, "\n"), "\n")
	if len(rows) < 2 {
		return nil
	}
	h, err := strconv.Atoi(strings.TrimSpace(rows[0]))
	if err != nil || h <= 0 {
		return nil
	}
	rows = rows[1:]
	var deck []artGrid
	for i := 0; i+h <= len(rows); i += h {
		lines := make([]string, h)
		copy(lines, rows[i:i+h])
		w := 0
		for _, l := range lines {
			if n := utf8.RuneCountInString(l); n > w {
				w = n
			}
		}
		deck = append(deck, artGrid{lines: lines, w: w, h: h})
	}
	return deck
}

// reserveLines is the vertical space the non-cat rows (wordmark + cheatsheet +
// dismiss hint below the cat) need; the mascot is dropped if it can't coexist.
const reserveLines = 11

// fits reports whether an art grid fits the viewport alongside reserveLines.
func fits(a artGrid, vw, vh int) bool {
	return a.w <= vw && a.h+reserveLines <= vh
}
