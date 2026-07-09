package main

import "testing"

func TestAnsiFgTmux(t *testing.T) {
	cases := map[string]string{
		"colour210": "\033[38;5;210m",
		"#a6e3a1":   "\033[38;2;166;227;161m",
		"default":   "",
		"colour999": "", // out of the 0-255 palette
		"":          "",
	}
	for in, want := range cases {
		if got := ansiFgTmux(in); got != want {
			t.Errorf("ansiFgTmux(%q) = %q, want %q", in, got, want)
		}
	}
}

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

func TestListIndexAt(t *testing.T) {
	// Landscape (width >= 2*height): list on the left of listWidth, cursor at 0
	// so scrollStart is 0 and row y maps to index y-listRowTop.
	m := tuiModel{
		width: 100, height: 40, ready: true, showPreview: true, theme: "dark",
		visible: []listItem{
			{target: "a", display: "a"},
			{target: "b", display: "b"},
			{display: "hdr"}, // empty target -> not selectable
			{target: "d", display: "d"},
		},
	}
	if top := m.listRowTop(); top != 2 {
		t.Fatalf("listRowTop = %d, want 2", top)
	}
	cases := []struct {
		name    string
		x, y    int
		wantIdx int
		wantOk  bool
	}{
		{"first row", 5, 2, 0, true},
		{"second row", 5, 3, 1, true},
		{"header row not selectable", 5, 4, 0, false},
		{"row after header", 5, 5, 3, true},
		{"above list in search", 5, 1, 0, false},
		{"preview column", 70, 2, 0, false},
		{"below the list", 5, 90, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, ok := m.listIndexAt(c.x, c.y)
			if ok != c.wantOk || (ok && idx != c.wantIdx) {
				t.Errorf("listIndexAt(%d,%d) = (%d,%v), want (%d,%v)", c.x, c.y, idx, ok, c.wantIdx, c.wantOk)
			}
		})
	}
}

func TestInPreview(t *testing.T) {
	land := tuiModel{width: 100, height: 40, ready: true, showPreview: true, theme: "dark"}
	if !land.inPreview(70, 2) {
		t.Error("landscape: right of listWidth should be preview")
	}
	if land.inPreview(5, 2) {
		t.Error("landscape: left column should be the list")
	}
	off := land
	off.showPreview = false
	if off.inPreview(70, 2) {
		t.Error("preview hidden -> never in preview")
	}

	// Portrait (width < 2*height): preview sits below the list + separator.
	port := tuiModel{width: 40, height: 40, ready: true, showPreview: true, theme: "dark"}
	below := port.listRowTop() + port.listHeight() + 1
	if !port.inPreview(5, below) {
		t.Errorf("portrait: y=%d should be preview", below)
	}
	if port.inPreview(5, port.listRowTop()) {
		t.Error("portrait: top list row should not be preview")
	}
}
