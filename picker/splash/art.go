package main

import (
	_ "embed"
	"strings"
	"unicode/utf8"
)

//go:embed assets/cat.txt
var catFull string

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

// reserveLines is the vertical space the non-mascot rows (zzz spacer, wordmark,
// cheatsheet, dismiss hint) need; the mascot is dropped if it can't coexist.
const reserveLines = 10

// pickArt returns the largest art that fits the viewport, and whether to show a
// mascot at all (false → wordmark + cheatsheet only).
func pickArt(full, small artGrid, vw, vh int) (artGrid, bool) {
	if full.w <= vw && full.h+reserveLines <= vh {
		return full, true
	}
	if small.w <= vw && small.h+reserveLines <= vh {
		return small, true
	}
	return artGrid{}, false
}
