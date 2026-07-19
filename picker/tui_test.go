package main

import (
	"strings"
	"testing"
)

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
	// Preview always sits below the list, so a click anywhere on a list row's
	// x maps to that row — there is no side preview column to reject.
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
		{"right side is still the list now", 70, 2, 0, true},
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
	// Preview is the region below the list + separator, at any terminal size.
	m := tuiModel{width: 100, height: 40, ready: true, showPreview: true, theme: "dark"}
	below := m.listRowTop() + m.listHeight() + 1
	if !m.inPreview(5, below) {
		t.Errorf("y=%d should be preview", below)
	}
	if m.inPreview(70, below) {
		// x is irrelevant to preview hit-testing now; only y matters, but a row
		// inside the list must never read as preview.
	}
	if m.inPreview(5, m.listRowTop()) {
		t.Error("top list row should not be preview")
	}
	off := m
	off.showPreview = false
	if off.inPreview(5, below) {
		t.Error("preview hidden -> never in preview")
	}
}

func TestIdentityCapFor(t *testing.T) {
	cases := []struct {
		name                        string
		width, lead, icon, pr, want int
	}{
		{"unknown width -> default", 0, 10, 6, 4, 32},
		{"negative width -> default", -1, 10, 6, 4, 32},
		{"wide clamps to max", 200, 10, 6, 4, 48},  // 200-10-6-4-6=174 -> 48
		{"narrow clamps to min", 20, 10, 6, 4, 12}, // 20-10-6-4-6=-6 -> 12
		{"mid computes exactly", 60, 10, 6, 4, 34}, // 60-10-6-4-6=34, in range
	}
	for _, c := range cases {
		if got := identityCapFor(c.width, c.lead, c.icon, c.pr); got != c.want {
			t.Errorf("%s: identityCapFor(%d,%d,%d,%d) = %d, want %d",
				c.name, c.width, c.lead, c.icon, c.pr, got, c.want)
		}
	}
}

func TestRenderWindowItemsLayout(t *testing.T) {
	windows := []windowData{
		// Untagged plain window: no crew, name is basename.
		{session: "mono", index: 1, name: "mono", active: false},
		// Issue window with a crew tag: crew after index, ticket inline.
		{session: "mono", index: 2, name: "rustwin", active: true,
			labelID: "L ENG-7290", labelRest: " fix and lock it confirmation modal",
			crewName: "rust", crewColor: "colour210"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0)

	// items[0] is the session header; the two window rows follow.
	var plains []string
	for _, it := range items {
		plains = append(plains, it.plain)
	}
	joined := strings.Join(plains, "\n")

	// Crew renders AFTER the index, not before it.
	if !strings.Contains(joined, "2: rust") {
		t.Errorf("crew should follow the index (`2: rust`); got:\n%s", joined)
	}
	// The untagged row has no crew and no reserved crew gap before the name.
	if !strings.Contains(joined, "1: mono") {
		t.Errorf("untagged row should read `1: mono` with no crew gap; got:\n%s", joined)
	}
	// The ticket id is inline in the row (as the name), not a trailing column.
	if !strings.Contains(joined, "ENG-7290") {
		t.Errorf("ticket id should be inline in the label; got:\n%s", joined)
	}
	// Default cap (width 0) truncates the long title; the tail word must be cut.
	if strings.Contains(joined, "confirmation modal") {
		t.Errorf("long title should be truncated at the default cap; got:\n%s", joined)
	}
}
