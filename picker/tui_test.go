package main

import "testing"

// withFilter re-inserts the zoxide divider before the first surviving
// suggestion row. These cases pin that branch.
func TestWithFilterZoxideHeader(t *testing.T) {
	allItems := []listItem{
		{plain: "hdr"}, // column-label row: isHeader false, must not be picked as divider
		{target: "lazytmux", searchText: "lazytmux", session: "lazytmux"},
		{display: "── New session ──", isHeader: true, isZoxideHeader: true},
		{target: "/git/alpha", createPath: "/git/alpha", createName: "alpha", searchText: "alpha /git/alpha"},
	}

	cases := []struct {
		name       string
		items      []listItem
		query      string
		wantHeader bool // a zoxide header present in the result
		headerIdx  int  // expected position of header when present
	}{
		{"sessions only", allItems, "lazytmux", false, 0},
		{"mixed match", allItems, "a", true, 1},
		{"no header item", allItems[:2], "a", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := tuiModel{allItems: c.items, query: c.query}
			out := m.withFilter().visible
			headerAt := -1
			for i, it := range out {
				if it.isZoxideHeader {
					headerAt = i
				}
			}
			if c.wantHeader {
				if headerAt != c.headerIdx {
					t.Errorf("header at %d, want %d (visible: %+v)", headerAt, c.headerIdx, out)
				}
				if out[headerAt+1].createPath == "" {
					t.Error("header not immediately followed by a suggestion")
				}
			} else if headerAt != -1 {
				t.Errorf("unexpected header at %d (visible: %+v)", headerAt, out)
			}
		})
	}
}

func TestFuzzyScore(t *testing.T) {
	if got := fuzzyScore("lazytmux", ""); got != 0 {
		t.Errorf("empty pattern = %d, want 0", got)
	}
	if got := fuzzyScore("lazytmux", "ltx"); got < 0 {
		t.Errorf("subsequence ltx should match, got %d", got)
	}
	if got := fuzzyScore("lazytmux", "xyz"); got != -1 {
		t.Errorf("non-subsequence = %d, want -1", got)
	}
	// Consecutive prefix beats a scattered match
	if fuzzyScore("lazytmux", "lazy") <= fuzzyScore("lazytmux", "lzyu") {
		t.Error("consecutive prefix should outscore scattered match")
	}
}

func TestVisibleWidth(t *testing.T) {
	if got := visibleWidth("abc"); got != 3 {
		t.Errorf("plain = %d, want 3", got)
	}
	if got := visibleWidth("\033[31mabc\033[0m"); got != 3 {
		t.Errorf("ANSI-wrapped = %d, want 3", got)
	}
}

func TestPadToWidth(t *testing.T) {
	if got := padToWidth("ab", 2, 5); got != "ab   " {
		t.Errorf("padToWidth = %q, want %q", got, "ab   ")
	}
	if got := padToWidth("abcdef", 6, 5); got != "abcdef" {
		t.Errorf("wider than target should be unchanged, got %q", got)
	}
}
