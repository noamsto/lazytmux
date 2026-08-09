package main

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
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

// remoteFixture is a Remote section as collectRemoteItems lays it out: divider,
// host row, that host's session rows, then an unselectable no-server host.
func remoteFixture() []listItem {
	return []listItem{
		{target: "lazytmux", searchText: "lazytmux", session: "lazytmux"},
		{display: "── Remote ──", isHeader: true, isRemoteHeader: true},
		{isRemoteRow: true, target: "remote:lab", remoteHost: "lab", searchText: "lab", plain: "lab"},
		{
			isRemoteRow: true,
			target:      "remote:lab:mono", remoteHost: "lab", remoteSess: "mono",
			searchText: "lab/mono lab mono",
			plain:      remoteTreeMid + " mono", plainEnd: remoteTreeEnd + " mono",
			display: remoteTreeMid + " mono", displayEnd: remoteTreeEnd + " mono",
		},
		{
			isRemoteRow: true,
			target:      "remote:lab:other", remoteHost: "lab", remoteSess: "other",
			searchText: "lab/other lab other",
			plain:      remoteTreeMid + " other", plainEnd: remoteTreeEnd + " other",
			display: remoteTreeMid + " other", displayEnd: remoteTreeEnd + " other",
		},
		{isRemoteRow: true, searchText: "dead", plain: "dead  (no tmux server)"},
	}
}

// The claude/scratch modes filter local sessions; remote rows are exempt, so
// neither toggle may take the Remote section away.
func TestWithFilterModesKeepRemote(t *testing.T) {
	for _, c := range []struct {
		name string
		m    tuiModel
	}{
		{"claude only", tuiModel{allItems: remoteFixture(), claudeOnly: true}},
		{"scratch only", tuiModel{allItems: remoteFixture(), scratchOnly: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := c.m.withFilter().visible
			remotes := 0
			for _, it := range out {
				if it.isRemoteRow {
					remotes++
				}
			}
			if remotes != 4 {
				t.Errorf("got %d remote rows, want 4 (visible: %+v)", remotes, out)
			}
		})
	}
}

// A query that only matches a session row still has to show the host it hangs
// off, and the surviving last child takes the closing glyph.
func TestWithFilterRemoteTree(t *testing.T) {
	m := tuiModel{allItems: remoteFixture(), query: "other"}
	out := m.withFilter().visible

	var plains []string
	for _, it := range out {
		plains = append(plains, it.plain)
	}
	if len(out) != 3 || !out[0].isRemoteHeader {
		t.Fatalf("want divider + host + match, got %+v", plains)
	}
	if out[1].remoteSess != "" || out[1].remoteHost != "lab" {
		t.Errorf("host row missing above the match: %+v", plains)
	}
	if out[2].plain != remoteTreeEnd+" other" {
		t.Errorf("last visible child should close the tree; got %q", out[2].plain)
	}
}

func TestWithFilterStateGroupedKeepsGrouping(t *testing.T) {
	allItems := []listItem{
		{display: "Waiting", isHeader: true, groupKey: "waiting", searchText: "waiting"},
		{target: "a:1", session: "a", groupKey: "waiting", searchText: "a alpha"},
		{display: "Done", isHeader: true, groupKey: "done", searchText: "done"},
		{target: "b:1", session: "b", groupKey: "done", searchText: "b alpha"},
	}
	m := tuiModel{allItems: allItems, windowMode: true, stateGrouped: true, query: "alpha"}
	out := m.withFilter().visible

	if len(out) != 4 {
		t.Fatalf("want 2 headers + 2 matching rows, got %d: %+v", len(out), out)
	}
	for i := 0; i < len(out); i += 2 {
		if !out[i].isHeader || out[i].groupKey != out[i+1].groupKey {
			t.Errorf("row %d is not attached to its own state header: header=%+v row=%+v",
				i, out[i], out[i+1])
		}
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
	if !m.inPreview(70, below) {
		// x is irrelevant to preview hit-testing now; only y matters, so a
		// preview-region row reads as preview at any x.
		t.Errorf("x should be irrelevant; y=%d is preview at any x", below)
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
		{"wide clamps to max", 200, 10, 6, 4, 48},  // 200-10-6-4-7=173 -> 48
		{"narrow clamps to min", 20, 10, 6, 4, 12}, // 20-10-6-4-7=-7 -> 12
		{"mid computes exactly", 60, 10, 6, 4, 33}, // 60-10-6-4-7=33, in range
	}
	for _, c := range cases {
		if got := identityCapFor(c.width, c.lead, c.icon, c.pr); got != c.want {
			t.Errorf("%s: identityCapFor(%d,%d,%d,%d) = %d, want %d",
				c.name, c.width, c.lead, c.icon, c.pr, got, c.want)
		}
	}
}

func TestRenderWindowItemsAlignment(t *testing.T) {
	windows := []windowData{
		{session: "s", index: 1, name: "a", labelID: "L ENG-1", labelRest: " first"},
		{session: "s", index: 2, name: "b", labelID: "L ENG-2", labelRest: " second", crewName: "rust", crewColor: "colour210"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)
	var rows []string
	for _, it := range items {
		if !it.isHeader {
			rows = append(rows, it.plain)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 window rows, got %d", len(rows))
	}
	// The ticket id must start at the SAME cell column in both rows despite the
	// crew tag widening one row's lead — that is what per-row lead padding buys.
	col0 := strings.Index(rows[0], "ENG-1")
	col1 := strings.Index(rows[1], "ENG-2")
	if col0 <= 0 || col0 != col1 {
		t.Errorf("identity columns misaligned: row0 ENG at %d, row1 ENG at %d\nrow0=%q\nrow1=%q", col0, col1, rows[0], rows[1])
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
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)

	var plains []string
	for _, it := range items {
		plains = append(plains, it.plain)
	}
	joined := strings.Join(plains, "\n")

	// Crew renders AFTER the index, not before it.
	if !strings.Contains(joined, "2: rust") {
		t.Errorf("crew should follow the index (`2: rust`); got:\n%s", joined)
	}
	// The untagged row's identity aligns with the crew-tagged row's identity —
	// per-row lead padding absorbs the crew-tag width difference. Compared via
	// display (not plain), since plain's active-marker trimming is a separate,
	// unrelated column-width quirk. Cell column (not byte offset) since row2's
	// active marker is a multi-byte glyph.
	d1, d2 := stripANSI(items[1].display), stripANSI(items[2].display)
	o1, o2 := strings.Index(d1, "mono"), strings.Index(d2, "L ENG-7290")
	if o1 < 0 || o2 < 0 {
		t.Fatalf("identity not found: %q / %q", d1, d2)
	}
	if col1, col2 := visibleWidth(d1[:o1]), visibleWidth(d2[:o2]); col1 != col2 {
		t.Errorf("untagged row identity should align with the crew-tagged row; row1 col %d, row2 col %d\nrow1=%q\nrow2=%q", col1, col2, d1, d2)
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

func TestRenderWindowItemsLongIDClamped(t *testing.T) {
	// Narrow width clamps identityCap to the minimum (12); a ticket id longer
	// than that must be truncated, not left to overflow the aligned column.
	windows := []windowData{
		{session: "s", index: 1, name: "x", labelID: "L PROJECT-123456", labelRest: " some title"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 20, false)
	var row string
	for _, it := range items {
		if !it.isHeader {
			row = it.plain
		}
	}
	if strings.Contains(row, "PROJECT-123456") {
		t.Errorf("long ticket id should be truncated at the min cap; got %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("expected an ellipsis from truncation; got %q", row)
	}
}

func TestRenderWindowItemsBridgeName(t *testing.T) {
	// A remote-bridge mirror window (#232): the picker must show the daemon-owned
	// @window_bridge_name, not the pane cwd basename — mirroring the status bar.
	windows := []windowData{
		{session: "s", index: 1, name: "noams", bridgeName: "ZULU"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)
	var row string
	for _, it := range items {
		if !it.isHeader {
			row = it.plain
		}
	}
	if !strings.Contains(row, "ZULU") {
		t.Errorf("bridge name should render inline; got %q", row)
	}
	if strings.Contains(row, "noams") {
		t.Errorf("cwd basename should not leak through for a bridge window; got %q", row)
	}
}

func TestRenderWindowItemsBridgeNameOutrankedByIssueAndBranch(t *testing.T) {
	// bridgeName must sit after issue and non-default-branch identity in the
	// chain, matching the status bar's priority (though a real bridge window
	// never carries either, per #182).
	windows := []windowData{
		{session: "s", index: 1, name: "noams", bridgeName: "ZULU", labelID: "L ENG-1", labelRest: " title"},
		{session: "s", index: 2, name: "other", bridgeName: "ZULU", branch: "feat/foo"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)
	var rows []string
	for _, it := range items {
		if !it.isHeader {
			rows = append(rows, it.plain)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if !strings.Contains(rows[0], "ENG-1") || strings.Contains(rows[0], "ZULU") {
		t.Errorf("issue identity should outrank bridge name; got %q", rows[0])
	}
	if !strings.Contains(rows[1], "feat/foo") || strings.Contains(rows[1], "ZULU") {
		t.Errorf("branch identity should outrank bridge name; got %q", rows[1])
	}
}

func TestPreviewToggleResizesViewport(t *testing.T) {
	m := tuiModel{width: 100, height: 40, ready: true, showPreview: true, theme: "dark",
		visible: []listItem{{target: "a", display: "a"}}}
	// Simulate a resize while preview is HIDDEN: viewport gets the full-body height.
	m.showPreview = false
	m.preview = viewport.New(viewport.WithWidth(m.previewWidth()), viewport.WithHeight(m.previewHeight()))
	hiddenH := m.preview.Height() // == bodyHeight() (full)
	// Toggling preview back ON must shrink the viewport to previewHeight().
	m2, _ := m.handleKey(tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	mm := m2.(tuiModel)
	if !mm.showPreview {
		t.Fatal("ctrl+/ should have toggled preview on")
	}
	if mm.preview.Height() != mm.previewHeight() {
		t.Errorf("viewport height = %d, want previewHeight() = %d (was %d while hidden)",
			mm.preview.Height(), mm.previewHeight(), hiddenH)
	}
}

// A query that matches nothing, or a header row, must not leave the previous
// target's content in the viewport (#286).
func TestLoadPreviewClearsWhenNothingSelectable(t *testing.T) {
	m := tuiModel{width: 100, height: 40, ready: true, showPreview: true, theme: "dark"}

	for name, model := range map[string]tuiModel{
		"empty list":     m,
		"header at rest": withVisible(m, []listItem{{display: "── Remote ──", isHeader: true}}),
	} {
		cmd := model.loadPreviewCmd()
		if cmd == nil {
			t.Fatalf("%s: expected a clearing command, got nil", name)
		}
		msg, ok := cmd().(previewMsg)
		if !ok {
			t.Fatalf("%s: expected previewMsg, got %T", name, cmd())
		}
		if msg.content != "" || msg.target != "" {
			t.Errorf("%s: want an empty preview, got target=%q content=%q", name, msg.target, msg.content)
		}
		// The handler's gate must accept it, or nothing would actually clear.
		if msg.target != model.currentTarget() {
			t.Errorf("%s: target %q would be rejected by the handler (currentTarget %q)",
				name, msg.target, model.currentTarget())
		}
	}
}

// Hiding the preview must stop the capture entirely, not just hide its output —
// that is what makes the faster tick free when it isn't being looked at.
func TestLoadPreviewSkippedWhenHidden(t *testing.T) {
	m := tuiModel{width: 100, height: 40, ready: true, showPreview: false, theme: "dark",
		visible: []listItem{{target: "a", display: "a"}}}
	if cmd := m.loadPreviewCmd(); cmd != nil {
		t.Error("preview hidden: expected no capture command")
	}
}

// Same economy in the wall: it captures its own page, so a preview nobody can
// see must not add 2.5 captures a second on top.
func TestLoadPreviewSkippedInWallMode(t *testing.T) {
	m := tuiModel{width: 100, height: 40, ready: true, showPreview: true, theme: "dark",
		mode: modeWall, visible: []listItem{{target: "a", display: "a"}}}
	if cmd := m.loadPreviewCmd(); cmd != nil {
		t.Error("wall mode: expected no preview capture command")
	}
}

// Every shape sizes itself against bodyHeight, so the View has to come out at
// exactly the terminal height — one row short costs the wall a whole tile row.
func TestViewFillsHeightExactly(t *testing.T) {
	shapes := map[string]func(tuiModel) tuiModel{
		"wall": func(m tuiModel) tuiModel { return m },
		"list+preview": func(m tuiModel) tuiModel {
			m.mode, m.showPreview = modeList, true
			return m
		},
		"list only": func(m tuiModel) tuiModel {
			m.mode, m.showPreview = modeList, false
			return m
		},
	}
	sizes := []struct{ w, h int }{{80, 24}, {80, 25}, {120, 30}, {200, 50}}

	for name, shape := range shapes {
		for _, size := range sizes {
			base := shape(wallFixture())
			base.ready = false // let the size message build the viewport
			sized, _ := base.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			m := sized.(tuiModel)

			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size.h {
				t.Errorf("%s at %dx%d: View is %d lines, want the full %d",
					name, size.w, size.h, len(lines), size.h)
			}
			hints := m.renderHints()
			if m.mode == modeList && strings.Contains(hints, "\n") {
				t.Errorf("%s at %dx%d: hints wrapped onto %d lines: %q",
					name, size.w, size.h, strings.Count(hints, "\n")+1, stripANSI(hints))
			}
		}
	}
}

func TestListRatio(t *testing.T) {
	ratio := func(raw string) int {
		return tuiModel{tmuxOpts: map[string]string{"@picker_list_ratio": raw}}.listRatio()
	}
	cases := map[string]int{
		"":       listRatioDefault, // unset
		"30":     30,
		" 70 ":   70,
		"5":      listRatioMin, // clamped
		"95":     listRatioMax,
		"banana": listRatioDefault, // not a number
	}
	for raw, want := range cases {
		if got := ratio(raw); got != want {
			t.Errorf("listRatio(%q) = %d, want %d", raw, got, want)
		}
	}

	// The ratio has to reach the layout, not just parse.
	m := tuiModel{width: 100, height: 45, ready: true, showPreview: true,
		tmuxOpts: map[string]string{"@picker_list_ratio": "30"}}
	if want := m.bodyHeight() * 30 / 100; m.listHeight() != want {
		t.Errorf("listHeight() = %d, want %d", m.listHeight(), want)
	}
	if m.previewHeight() <= m.listHeight() {
		t.Errorf("ratio 30 should give the preview the larger share; list=%d preview=%d",
			m.listHeight(), m.previewHeight())
	}
}

func withVisible(m tuiModel, items []listItem) tuiModel {
	m.visible = items
	return m
}

func TestRenderWindowItemsZoomAligned(t *testing.T) {
	// A zoomed row must not push its icon/PR column past a non-zoomed row's.
	// The id is long enough to overflow identityCap and get truncated flush to
	// it (zero slack in the label column), so an unbudgeted zoom glyph would
	// have nowhere to go but past labelCol.
	longID := "L ENG-000000000000000000000000000000000000000000000000000000"
	windows := []windowData{
		{session: "s", index: 1, name: "a", labelID: longID, labelRest: " one",
			prPlain: " #10", prState: "open", prCheck: "success"},
		{session: "s", index: 2, name: "b", labelID: longID, labelRest: " two", zoomed: true,
			prPlain: " #20", prState: "open", prCheck: "success"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 100, false)
	var rows []string
	for _, it := range items {
		if !it.isHeader {
			rows = append(rows, it.plain)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// Both PR badges must survive (zoomed row's badge not clipped).
	if !strings.Contains(rows[1], "#20") {
		t.Errorf("zoomed row PR badge clipped: %q", rows[1])
	}
	// The zoom glyph is present on the zoomed row and the icon column still aligns:
	// the PR badge ("#") must start at the same cell column in both rows.
	col0 := visibleWidth(rows[0][:strings.Index(rows[0], "#10")])
	col1 := visibleWidth(rows[1][:strings.Index(rows[1], "#20")])
	if col0 != col1 {
		t.Errorf("PR column misaligned: row0 # at cell %d, row1 # at cell %d\nrow0=%q\nrow1=%q", col0, col1, rows[0], rows[1])
	}
}

func TestLayoutShowsPreview(t *testing.T) {
	cases := map[string]bool{
		"":        true, // unset -> historical default
		"preview": true,
		"list":    false,
		"LIST":    true, // exact match only; not "list"
	}
	for v, want := range cases {
		opts := map[string]string{}
		if v != "" {
			opts["@picker_layout"] = v
		}
		if got := layoutShowsPreview(opts); got != want {
			t.Errorf("layoutShowsPreview(@picker_layout=%q) = %v, want %v", v, got, want)
		}
	}
}

// The Remote section (header + one row per host) must exist before any
// probe has run — this is what stops the section from landing mid-session
// and reflowing the list under the user (#312). Calls newPickerModel
// directly so this test exercises the real runTUI wiring, not a
// hand-assembled stand-in for it.
func TestFirstPaintIncludesRemoteRows(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	items := []listItem{{target: "lazytmux", searchText: "lazytmux"}}
	m := newPickerModel(false, false, false, opts, "dark", items)

	var sawHeader, sawLab, sawDead bool
	for _, item := range m.visible {
		switch {
		case item.isRemoteHeader:
			sawHeader = true
		case item.remoteHost == "lab" && item.remoteSess == "":
			sawLab = true
		case item.remoteHost == "dead" && item.remoteSess == "":
			sawDead = true
		}
	}
	if !sawHeader || !sawLab || !sawDead {
		t.Fatalf("first paint missing remote rows: header=%v lab=%v dead=%v, visible=%+v", sawHeader, sawLab, sawDead, m.visible)
	}
}

// The row a host contributes can grow once its probe returns (a host with
// unbridged sessions gains tree-child rows). That growth must not silently
// move the cursor onto a different row — the actual user-visible half of
// #312 ("anything the human did in that window ... gets re-laid-out
// underneath them").
func TestRemoteMsgPreservesCursor(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	m := tuiModel{
		sessionItems: []listItem{{target: "lazytmux", searchText: "lazytmux"}},
		remoteItems:  pendingRemoteItems(opts),
	}
	m = m.recombine().withFilter()

	found := false
	for i, item := range m.visible {
		if item.remoteHost == "dead" && item.remoteSess == "" {
			m.cursor = i
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("setup: no dead host row in %+v", m.visible)
	}

	// "lab" resolves with two unbridged sessions — its row grows by two tree
	// rows, shifting everything that came after "lab" in the old list,
	// including "dead"'s row.
	probe := func(host string) ([]string, error) {
		if host == "dead" {
			return nil, errors.New("unreachable")
		}
		return []string{"mono", "other"}, nil
	}
	resolved := collectRemoteItems(opts, nil, probe, noRestore)

	next, _ := m.Update(remoteMsg{items: resolved})
	nm, ok := next.(tuiModel)
	if !ok {
		t.Fatalf("Update did not return a tuiModel")
	}

	if nm.cursor < 0 || nm.cursor >= len(nm.visible) {
		t.Fatalf("cursor out of range: %d (len %d)", nm.cursor, len(nm.visible))
	}
	got := nm.visible[nm.cursor]
	if got.remoteHost != "dead" || got.remoteSess != "" {
		t.Fatalf("cursor moved off the dead host row: %+v", got)
	}
}

// A filter query the user is mid-typing must not be reset by an unrelated
// background message landing.
func TestRemoteMsgPreservesQuery(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab"}
	m := tuiModel{
		sessionItems: []listItem{{target: "lazytmux", searchText: "lazytmux"}},
		remoteItems:  pendingRemoteItems(opts),
		query:        "laz",
	}
	m = m.recombine().withFilter()

	probe := func(string) ([]string, error) { return nil, nil }
	resolved := collectRemoteItems(opts, nil, probe, noRestore)

	next, _ := m.Update(remoteMsg{items: resolved})
	nm, ok := next.(tuiModel)
	if !ok {
		t.Fatalf("Update did not return a tuiModel")
	}
	if nm.query != "laz" {
		t.Fatalf("query was reset: got %q", nm.query)
	}
}

// A filter query active when remoteMsg lands must still apply to the rows it
// adds — a newly-revealed child session that doesn't match the query must
// not bypass it just because it arrived asynchronously.
func TestRemoteMsgChildRowsRespectActiveQuery(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab"}
	m := tuiModel{
		sessionItems: []listItem{{target: "lazytmux", searchText: "lazytmux"}},
		remoteItems:  pendingRemoteItems(opts),
		query:        "lazytmux",
	}
	m = m.recombine().withFilter()

	probe := func(string) ([]string, error) { return []string{"mono"}, nil }
	resolved := collectRemoteItems(opts, nil, probe, noRestore)

	next, _ := m.Update(remoteMsg{items: resolved})
	nm, ok := next.(tuiModel)
	if !ok {
		t.Fatalf("Update did not return a tuiModel")
	}
	for _, item := range nm.visible {
		if item.remoteSess == "mono" {
			t.Fatalf("child row bypassed the active query: %+v", item)
		}
	}
}

// Pending rows (before any probe resolves) must be exempt from the
// claude/scratch toggles exactly like resolved rows — itemVisible checks
// isRemoteRow before either toggle, and pendingRemoteItems sets it via the
// same remoteHostRowItem helper collectRemoteItems uses.
func TestPendingRemoteItemsSurviveModeToggles(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	for _, c := range []struct {
		name string
		m    tuiModel
	}{
		{"claude only", tuiModel{sessionItems: []listItem{{target: "s", searchText: "s"}}, remoteItems: pendingRemoteItems(opts), claudeOnly: true}},
		{"scratch only", tuiModel{sessionItems: []listItem{{target: "s", searchText: "s"}}, remoteItems: pendingRemoteItems(opts), scratchOnly: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := c.m.recombine().withFilter().visible
			remotes := 0
			for _, it := range out {
				if it.isRemoteRow {
					remotes++
				}
			}
			if remotes != 2 {
				t.Errorf("got %d pending remote rows, want 2 (visible: %+v)", remotes, out)
			}
		})
	}
}

func TestGroupWindowsBySessionUnchanged(t *testing.T) {
	windows := []windowData{
		{session: "busy", index: 2},
		{session: "busy", index: 1},
		{session: "quiet", index: 1},
	}
	activity := map[string]int64{"busy": 100, "quiet": 1}
	groups := groupWindowsBySession(windows, activity)

	if len(groups) != 2 || groups[0].key != "busy" || groups[1].key != "quiet" {
		t.Fatalf("groups = %+v, want busy then quiet (by activity)", groups)
	}
	if groups[0].windows[0].index != 1 || groups[0].windows[1].index != 2 {
		t.Fatalf("windows within a session should stay index-ordered, got %+v", groups[0].windows)
	}
}

func TestGroupWindowsByStatePriorityOrder(t *testing.T) {
	windows := []windowData{
		{session: "a", index: 1, claude: claudeCounts{done: 1}},
		{session: "b", index: 1, claude: claudeCounts{errorCnt: 1}},
		{session: "c", index: 1}, // no claude state
		{session: "d", index: 1, claude: claudeCounts{waiting: 1}},
		{session: "e", index: 1}, // no claude state
	}
	groups := groupWindowsByState(windows, map[string]int64{})

	var gotKeys []string
	for _, g := range groups {
		gotKeys = append(gotKeys, g.key)
	}
	wantKeys := []string{"error", "waiting", "done", ""}
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("group keys = %v, want %v", gotKeys, wantKeys)
	}
	for i, k := range wantKeys {
		if gotKeys[i] != k {
			t.Errorf("group %d key = %q, want %q (full: %v)", i, gotKeys[i], k, gotKeys)
		}
	}

	// Stateless windows collapse into exactly one trailing group, not one
	// group per window.
	last := groups[len(groups)-1]
	if last.key != "" || len(last.windows) != 2 {
		t.Fatalf("trailing group = key %q with %d windows, want key \"\" with 2 windows",
			last.key, len(last.windows))
	}
}

func TestGroupWindowsByStateEmptyGroupsOmitted(t *testing.T) {
	// Only "processing" and "" are present — every other state in
	// claudeStateOrder must be absent from the output, not present-but-empty.
	windows := []windowData{
		{session: "a", index: 1, claude: claudeCounts{processing: 1}},
		{session: "b", index: 1},
	}
	groups := groupWindowsByState(windows, map[string]int64{})
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (processing, no-agent), got %d: %+v", len(groups), groups)
	}
}

func TestRenderWindowItemsStateGroupedHeaderOrder(t *testing.T) {
	windows := []windowData{
		{session: "a", index: 1, name: "a"},
		{session: "b", index: 1, name: "b"},
		{session: "c", index: 1, name: "c", claude: claudeCounts{errorCnt: 1}},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, true)

	var headerKeys []string
	for _, it := range items {
		if it.isHeader {
			headerKeys = append(headerKeys, it.groupKey)
		}
	}
	if len(headerKeys) != 2 || headerKeys[0] != "error" || headerKeys[1] != "" {
		t.Fatalf("headers = %v, want [error \"\"] (error first, one trailing no-agent group)", headerKeys)
	}
}

func TestRenderWindowItemsStateGroupedFoldsSession(t *testing.T) {
	// Identities deliberately differ from their session names — if folding
	// were silently skipped, the row's plain text would never contain
	// "alpha"/"beta" at all, so this fixture can actually fail.
	windows := []windowData{
		{session: "alpha", index: 1, name: "win-one", claude: claudeCounts{waiting: 1}},
		{session: "beta", index: 1, name: "win-two", claude: claudeCounts{done: 1}},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, true)

	var rows []listItem
	for _, it := range items {
		if !it.isHeader {
			rows = append(rows, it)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if !strings.Contains(rows[0].plain, "alpha / win-one") {
		t.Errorf("waiting row should fold the session name into its identity; got %q", rows[0].plain)
	}
	if !strings.Contains(rows[1].plain, "beta / win-two") {
		t.Errorf("done row should fold the session name into its identity; got %q", rows[1].plain)
	}

	// Negative control: the same fixtures session-grouped must NOT fold the
	// session into the row — it already has a header for that job.
	sessionGrouped := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)
	var sgRows []listItem
	for _, it := range sessionGrouped {
		if !it.isHeader {
			sgRows = append(sgRows, it)
		}
	}
	if len(sgRows) != 2 {
		t.Fatalf("want 2 session-grouped rows, got %d", len(sgRows))
	}
	if strings.Contains(sgRows[0].plain, "alpha") || strings.Contains(sgRows[1].plain, "beta") {
		t.Errorf("session-grouped rows should not carry the session name (it's in the header); got %q, %q",
			sgRows[0].plain, sgRows[1].plain)
	}
}

func TestRenderWindowItemsStateGroupedRespectsColumnBudget(t *testing.T) {
	// A long session name must not push the row wider than the shared
	// labelCol budget every other row (session-grouped or not) is aligned to.
	windows := []windowData{
		{session: "a-very-long-worktree-session-name-indeed", index: 1, name: "x",
			claude: claudeCounts{waiting: 1}, labelID: "L PROJECT-123456", labelRest: " a fairly long issue title here"},
	}
	narrow := renderWindowItems(windows, map[string]string{}, nil, "dark", 40, true)
	wide := renderWindowItems(windows, map[string]string{}, nil, "dark", 40, false)

	var narrowRow, wideRow listItem
	for _, it := range narrow {
		if !it.isHeader {
			narrowRow = it
		}
	}
	for _, it := range wide {
		if !it.isHeader {
			wideRow = it
		}
	}
	// Same width input -> same identityCap -> same overall label column width,
	// regardless of grouping mode (folding carves the session name OUT of the
	// budget, never adds to it).
	if visibleWidth(narrowRow.plain) != visibleWidth(wideRow.plain) {
		t.Errorf("state-grouped row width %d != session-grouped row width %d at the same terminal width\nstate: %q\nsession: %q",
			visibleWidth(narrowRow.plain), visibleWidth(wideRow.plain), narrowRow.plain, wideRow.plain)
	}
}

func TestRenderWindowItemsSessionGroupedUnaffected(t *testing.T) {
	// Passing stateGrouped=false must reproduce exactly today's output —
	// this is the regression net for the Pass A/B rewrite.
	windows := []windowData{
		{session: "s", index: 1, name: "a", labelID: "L ENG-1", labelRest: " first"},
		{session: "s", index: 2, name: "b", labelID: "L ENG-2", labelRest: " second", crewName: "rust", crewColor: "colour210"},
	}
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)
	var rows []string
	for _, it := range items {
		if !it.isHeader {
			rows = append(rows, it.plain)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 window rows, got %d", len(rows))
	}
	col0 := strings.Index(rows[0], "ENG-1")
	col1 := strings.Index(rows[1], "ENG-2")
	if col0 <= 0 || col0 != col1 {
		t.Errorf("identity columns misaligned: row0 ENG at %d, row1 ENG at %d\nrow0=%q\nrow1=%q", col0, col1, rows[0], rows[1])
	}
}

func TestToggleStateGrouped(t *testing.T) {
	m := tuiModel{windowMode: true, theme: "dark", tmuxOpts: map[string]string{},
		visible: []listItem{{isHeader: true, display: "h"}, {target: "s:1", display: "row"}}}

	m2, cmd := m.handleKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	mm := m2.(tuiModel)
	if !mm.stateGrouped {
		t.Fatal("ctrl+g should toggle stateGrouped on")
	}
	if cmd == nil {
		t.Fatal("ctrl+g should trigger a data refresh so the new grouping renders")
	}

	m3, _ := mm.handleKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if m3.(tuiModel).stateGrouped {
		t.Fatal("a second ctrl+g should toggle stateGrouped back off")
	}
}

func TestToggleStateGroupedNoopOutsideWindowMode(t *testing.T) {
	m := tuiModel{windowMode: false, theme: "dark"}
	m2, cmd := m.handleKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if m2.(tuiModel).stateGrouped {
		t.Fatal("ctrl+g should be a no-op in session mode (prefix + s)")
	}
	if cmd != nil {
		t.Fatal("ctrl+g should not trigger a refresh in session mode")
	}
}

func TestToggleStateGroupedNoopInWallMode(t *testing.T) {
	// windowMode is also true in the tiled wall (prefix + W, --tui --windows
	// --wall), which has no group headers or hint to explain a reorder — ctrl+g
	// must stay a no-op there, not just in session mode.
	m := tuiModel{windowMode: true, mode: modeWall, theme: "dark"}
	m2, cmd := m.handleKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if m2.(tuiModel).stateGrouped {
		t.Fatal("ctrl+g should be a no-op in the wall (prefix + W)")
	}
	if cmd != nil {
		t.Fatal("ctrl+g should not trigger a refresh in the wall")
	}
}
