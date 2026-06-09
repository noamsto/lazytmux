package main

import (
	"strings"
	"testing"
)

func TestTruncateCells(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"over", "hello world", 8, "hello w…"},
		{"tiny", "abcdef", 2, "a…"},
	}
	for _, c := range cases {
		if got := truncateCells(c.s, c.max); got != c.want {
			t.Errorf("%s: truncateCells(%q,%d) = %q, want %q", c.name, c.s, c.max, got, c.want)
		}
		if w := iconCellWidth(truncateCells(c.s, c.max)); w > c.max {
			t.Errorf("%s: result width %d exceeds max %d", c.name, w, c.max)
		}
	}
}

func TestRenderWindowItemsEnriched(t *testing.T) {
	windows := []windowData{
		{session: "proj", index: 1, name: "x", branch: "feat/eng-1-a",
			labelID: "L ENG-1", labelRest: " short title",
			prPlain: "  #10", prState: "open", prCheck: "success", prMergeable: "mergeable"},
		{session: "proj", index: 2, name: "a-much-longer-window-name", active: true, branch: "feat/eng-2-b",
			labelID: "L ENG-2", labelRest: " other",
			prPlain: "  #20", prState: "open", prCheck: "failure", prMergeable: "mergeable"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark")

	var rows []listItem
	for _, it := range items {
		if !it.isHeader {
			rows = append(rows, it)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 window rows, got %d", len(rows))
	}

	d0, d1 := stripANSI(rows[0].display), stripANSI(rows[1].display)
	for _, want := range []string{"L ENG-1", "short title", "#10"} {
		if !strings.Contains(d0, want) {
			t.Errorf("row0 missing %q in %q", want, d0)
		}
	}
	if !strings.Contains(d1, "#20") {
		t.Errorf("row1 missing PR badge in %q", d1)
	}

	// Identity column starts at the same visible offset despite very different
	// window-name lengths — the alignment guarantee.
	o0, o1 := strings.Index(d0, "L ENG-1"), strings.Index(d1, "L ENG-2")
	if o0 < 0 || o1 < 0 {
		t.Fatalf("identity not found: %q / %q", d0, d1)
	}
	if c0, c1 := visibleWidth(d0[:o0]), visibleWidth(d1[:o1]); c0 != c1 {
		t.Errorf("identity misaligned: row0 col %d, row1 col %d\n%q\n%q", c0, c1, d0, d1)
	}

	// PR badge tinted by check state.
	if !strings.Contains(rows[0].display, ansiFg("#a6e3a1")) {
		t.Error("passing PR badge not green")
	}
	if !strings.Contains(rows[1].display, ansiFg("#f38ba8")) {
		t.Error("failing PR badge not red")
	}

	// Searchable by issue id and PR number.
	if !strings.Contains(rows[0].searchText, "ENG-1") || !strings.Contains(rows[0].searchText, "#10") {
		t.Errorf("row0 not searchable by id/pr: %q", rows[0].searchText)
	}
}

func TestBranchEchoesName(t *testing.T) {
	cases := []struct {
		branch, name string
		want         bool
	}{
		{"feat/5-window-picker-enrich", "feat-5-window-picker-enrich", true}, // worktree dir
		{"mono", "mono", true},             // exact
		{"feat/login", "feat-login", true}, // slash normalized
		{"feat/login", "mono", false},      // unrelated
		{"feat/a/b", "feat-a-b", true},     // multiple slashes
	}
	for _, c := range cases {
		if got := branchEchoesName(c.branch, c.name); got != c.want {
			t.Errorf("branchEchoesName(%q,%q) = %v, want %v", c.branch, c.name, got, c.want)
		}
	}
}

func TestColorPRBadge(t *testing.T) {
	c := prColors{success: "<s>", failure: "<f>", pending: "<p>", merged: "<m>", reset: "<r>"}
	cases := []struct {
		name       string
		prPlain    string
		state      string
		check      string
		mergeable  string
		wantEmpty  bool
		wantPrefix string
	}{
		{"no pr", "", "open", "success", "mergeable", true, ""},
		{"conflict wins over success", "  #1", "open", "success", "conflicting", false, "<f>"},
		{"failing checks", "  #2", "open", "failure", "mergeable", false, "<f>"},
		{"pending checks", "  #3", "open", "pending", "mergeable", false, "<p>"},
		{"merged", "  #4", "merged", "success", "mergeable", false, "<m>"},
		{"clean success", "  #5", "open", "success", "mergeable", false, "<s>"},
	}
	for _, c2 := range cases {
		got := colorPRBadge(c2.prPlain, c2.state, c2.check, c2.mergeable, c)
		if c2.wantEmpty {
			if got != "" {
				t.Errorf("%s: want empty, got %q", c2.name, got)
			}
			continue
		}
		if !strings.HasPrefix(got, c2.wantPrefix) {
			t.Errorf("%s: got %q, want prefix %q", c2.name, got, c2.wantPrefix)
		}
		if !strings.HasSuffix(got, c.reset) {
			t.Errorf("%s: got %q, want reset suffix", c2.name, got)
		}
		// The plain badge text (minus leading space) must survive coloring.
		if !strings.Contains(got, strings.TrimSpace(c2.prPlain)) {
			t.Errorf("%s: badge text dropped: %q", c2.name, got)
		}
	}
}
