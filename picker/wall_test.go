package main

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWallGeometry(t *testing.T) {
	// tiles is the total tile count: the grid is clamped to it, so a wall with
	// three windows draws three tiles rather than a 4x3 cage of blanks.
	cases := []struct {
		name               string
		width, bodyHeight  int
		tiles              int
		wantCols, wantRows int
	}{
		{"unknown size", 0, 0, 12, 0, 0},
		{"exact minimum tile", 36, 10, 12, 1, 1},
		{"one cell under the minimum", 35, 9, 12, 0, 0},
		{"too narrow to tile", 30, 30, 12, 0, 3},
		{"too short to tile", 100, 9, 12, 2, 0},
		{"typical popup", 188, 33, 12, 4, 3},
		{"capped at 4x3", 400, 100, 99, 4, 3},
		{"no tiles at all", 188, 33, 0, 4, 3},
		{"one tile takes one cell", 188, 33, 1, 1, 1},
		{"three tiles stay one row", 188, 33, 3, 3, 1},
		{"five tiles need a second row", 188, 33, 5, 4, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cols, rows := wallGeometry(c.width, c.bodyHeight, c.tiles)
			if cols != c.wantCols || rows != c.wantRows {
				t.Errorf("wallGeometry(%d,%d,%d) = (%d,%d), want (%d,%d)",
					c.width, c.bodyHeight, c.tiles, cols, rows, c.wantCols, c.wantRows)
			}
		})
	}
}

// The minimum tile only derives the grid — the tiles then expand to fill the
// popup, or a 188x38 popup wastes 44 columns and 8 rows.
func TestWallTileSizesFillPopup(t *testing.T) {
	cases := []struct{ width, bodyHeight int }{
		{188, 33}, {80, 20}, {36, 10}, {113, 31}, {400, 100},
	}
	for _, c := range cases {
		// A full 4x3's worth of tiles, so the fill is measured against the whole
		// popup rather than a grid clamped to a smaller count.
		colWidths, rowHeights := wallTileSizes(c.width, c.bodyHeight, wallMaxCols*wallMaxRows)
		if len(colWidths) == 0 || len(rowHeights) == 0 {
			t.Fatalf("%dx%d: expected a grid, got %v/%v", c.width, c.bodyHeight, colWidths, rowHeights)
		}
		if got := sum(colWidths); got != c.width {
			t.Errorf("%dx%d: columns total %d cells, want the full %d", c.width, c.bodyHeight, got, c.width)
		}
		if got := sum(rowHeights); got != c.bodyHeight {
			t.Errorf("%dx%d: rows total %d cells, want the full %d", c.width, c.bodyHeight, got, c.bodyHeight)
		}
		for _, w := range colWidths {
			if w < wallMinTileInnerW+wallTileFrame {
				t.Errorf("%dx%d: column width %d below the minimum tile", c.width, c.bodyHeight, w)
			}
		}
		for _, h := range rowHeights {
			if h < wallMinTileInnerH+wallTileFrame {
				t.Errorf("%dx%d: row height %d below the minimum tile", c.width, c.bodyHeight, h)
			}
		}
	}

	if colWidths, rowHeights := wallTileSizes(20, 6, 12); colWidths != nil || rowHeights != nil {
		t.Errorf("too small to tile: want no grid, got %v/%v", colWidths, rowHeights)
	}
}

func sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// The wall is composed by hand, so nothing else enforces the grid: every row has
// to be the same visible width, and that width has to fit the terminal.
func TestRenderWallRowsUniformWidth(t *testing.T) {
	m := wallFixture()
	m.wallContent = map[string]string{
		"s:1": "\033[31mred\033[0m\n" + strings.Repeat("wide ", 40),
		// lipgloss measures these wider than this picker does; the tiles must
		// hold their width by the picker's own rules regardless.
		"s:2": "plain ✍️ 👨‍👩‍👧",
		"s:3": "",
	}
	m.wallBad = map[string]bool{"s:4": true}

	// Page 1 holds 2 tiles in a 4-slot grid, so it also covers the empty slots.
	for _, page := range []int{0, 1} {
		m.wallPage = page
		out := m.renderWall()
		lines := strings.Split(out, "\n")
		if len(lines) != m.bodyHeight() {
			t.Fatalf("page %d: wall has %d rows, want bodyHeight %d", page, len(lines), m.bodyHeight())
		}
		for i, line := range lines {
			if got := visibleWidth(line); got != m.width {
				t.Errorf("page %d row %d is %d cells, want %d: %q", page, i, got, m.width, line)
			}
		}
	}

	m.wallPage = 0
	out := m.renderWall()
	if !strings.Contains(out, "\033[31m") {
		t.Error("captured ANSI did not survive into the wall")
	}
	if !strings.Contains(out, "(gone)") {
		t.Error("a target capture-pane refused should render as (gone)")
	}
	if !strings.Contains(out, "2: beta") {
		t.Error("a tile's top border should carry its identity")
	}
}

// bodyHeight reserves exactly one row for the hints, and the wall's keymap is
// long enough that a lipgloss Width() wrapped it onto a second one.
func TestRenderWallHintsSingleLine(t *testing.T) {
	for _, width := range []int{40, 100, 188} {
		m := wallFixture()
		m.width = width
		hints := m.renderWallHints()
		if strings.Contains(hints, "\n") {
			t.Errorf("width %d: hints span %d lines: %q", width, strings.Count(hints, "\n")+1, hints)
		}
		if got := visibleWidth(hints); got != width {
			t.Errorf("width %d: hints are %d cells", width, got)
		}
	}

	// The two hints that would lie while the prompt is open say what applies.
	m := wallFixture()
	m.querying = true
	if hints := stripANSI(m.renderWallHints()); !strings.Contains(hints, "esc:done") {
		t.Errorf("querying hints should offer esc, got %q", hints)
	}
}

func TestRenderWallFallsBackToListWhenTooSmall(t *testing.T) {
	m := wallFixture()
	m.width, m.height = 30, 12 // narrower than one tile
	if cols, _ := wallGeometry(m.width, m.bodyHeight(), len(m.tileItems())); cols != 0 {
		t.Fatalf("fixture is not too small to tile: cols = %d", cols)
	}
	if m.renderWall() != m.renderList() {
		t.Error("a terminal too small to tile should render the list instead")
	}
	// A hint line promising tiles above a body that has none is the one thing
	// the fallback must not do.
	hints := stripANSI(m.renderWallHints())
	if !strings.Contains(hints, "too small") {
		t.Errorf("fallback hints should say why: %q", hints)
	}
	if strings.Contains(hints, "hjkl") {
		t.Errorf("fallback hints should not offer tile navigation: %q", hints)
	}
	if got := visibleWidth(m.renderWallHints()); got != m.width {
		t.Errorf("fallback hints are %d cells, want %d", got, m.width)
	}
}

// Nothing draws a preview under the fallback list, so it gets the whole body.
// Sized by listHeight() it drew half the rows at 90x14 and left the rest blank.
func TestRenderWallFallbackFillsBody(t *testing.T) {
	m := wallFixture()
	m.width, m.height = 90, 13 // two tiles wide, not one tall
	m.showPreview = true
	if _, rows := wallGeometry(m.width, m.bodyHeight(), len(m.tileItems())); rows != 0 {
		t.Fatalf("fixture is not too small to tile: rows = %d", rows)
	}
	if m.listHeight() >= m.bodyHeight() {
		t.Fatalf("fixture does not split the body: listHeight %d of %d", m.listHeight(), m.bodyHeight())
	}
	if got := strings.Count(m.renderWall(), "\n") + 1; got != m.bodyHeight() {
		t.Errorf("fallback drew %d rows, want the whole body %d", got, m.bodyHeight())
	}
}

// The index must survive and stay leading, or truncation reaches it and two
// tiles on one branch stop being distinguishable.
func TestStripTreePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"├─ ▸ 1:  #286 feat(picker): live preview", "▸ 1:  #286 feat(picker): live preview"},
		{"╰─  14: lazytmux", "14: lazytmux"},
		{"2: beta", "2: beta"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripTreePrefix(c.in); got != c.want {
			t.Errorf("stripTreePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	m := wallFixture()
	m.visible[1].plain = "├─ ▸ 1: alpha"
	label := stripANSI(m.tileLabel(m.visible[1], 30))
	if strings.ContainsAny(label, "├╰") {
		t.Errorf("tile label kept the list's tree chrome: %q", label)
	}
	if !strings.Contains(label, "▸ 1: alpha") {
		t.Errorf("tile label lost the marker or the index: %q", label)
	}
}

// A capture longer and wider than the tile is cropped to the tail, not wrapped —
// wrapping inside a hand-drawn border would shear it.
func TestRenderTileCropsCapture(t *testing.T) {
	m := wallFixture()
	const outerW, outerH = 40, 12
	innerW, innerH := outerW-wallTileFrame, outerH-wallTileFrame

	rows := make([]string, innerH*2)
	for i := range rows {
		rows[i] = "\033[32m" + strings.Repeat("x", innerW*2) + "\033[0m row"
	}
	rows[len(rows)-1] = "\033[32mlast row\033[0m"
	m.wallContent = map[string]string{"s:1": strings.Join(rows, "\n")}

	tile := m.renderTile(m.visible[m.tileItems()[0]], true, outerW, outerH)
	if len(tile) != outerH {
		t.Fatalf("tile has %d rows, want %d", len(tile), outerH)
	}
	body := tile[1 : len(tile)-1]
	if len(body) != innerH {
		t.Fatalf("tile body has %d rows, want innerHeight %d", len(body), innerH)
	}
	for i, line := range body {
		if got := visibleWidth(line); got != outerW {
			t.Errorf("body row %d is %d cells, want %d: %q", i, got, outerW, line)
		}
		if got := visibleWidth(stripTileEdges(line)); got > innerW {
			t.Errorf("body row %d content is %d cells, want <= %d", i, got, innerW)
		}
	}
	if !strings.Contains(body[len(body)-1], "last row") {
		t.Errorf("tile should show the tail of the capture, got %q", body[len(body)-1])
	}
	if !strings.Contains(strings.Join(body, "\n"), "\033[32m") {
		t.Error("capture ANSI was stripped from the tile body")
	}
}

func stripTileEdges(line string) string {
	return strings.Trim(stripANSI(line), "│")
}

// The 1s data refresh must not move the wall's selection.
func TestWallRefreshKeepsSelection(t *testing.T) {
	m := wallFixture()
	m.cursor = 5 // grid row 2, second tile

	got, _ := m.Update(refreshMsg{items: m.visible})
	gm := got.(tuiModel)

	if gm.cursor != 5 {
		t.Errorf("refresh moved the cursor to %d, want it left at 5", gm.cursor)
	}
	if gm.wallPage != 1 {
		t.Errorf("refresh moved the page to %d, want 1", gm.wallPage)
	}
}

// Regression: driving the live wall, the selection jumped to another window
// about a second after every move — the refresh re-snapped by index under a list
// that reorders. See snapWallTo.
func TestWallRefreshFollowsTargetNotIndex(t *testing.T) {
	m := wallFixture()
	m.cursor = 5 // "s:5"
	want := m.visible[m.cursor].target

	// Same rows, one session's worth of them moved to the front.
	reordered := append([]listItem{}, m.visible[4:7]...)
	reordered = append(reordered, m.visible[0:4]...)
	reordered = append(reordered, m.visible[7:]...)

	got, _ := m.Update(refreshMsg{items: reordered})
	gm := got.(tuiModel)

	if gm.cursor >= len(gm.visible) || gm.visible[gm.cursor].target != want {
		at := "out of range"
		if gm.cursor < len(gm.visible) {
			at = gm.visible[gm.cursor].target
		}
		t.Errorf("selection followed the index to %q, want it still on %q", at, want)
	}
	if pos := tilePos(gm.tileItems(), gm.cursor); gm.wallPage != pos/gm.wallPerPage() {
		t.Errorf("page %d does not show the selected tile at position %d", gm.wallPage, pos)
	}
}

func TestWallGridNav(t *testing.T) {
	// The fixture is a 2x2 grid over 6 tiles: page 0 holds visible 1-4, page 1
	// holds visible 5-6.
	base := wallFixture()
	if cols, rows := wallGeometry(base.width, base.bodyHeight(), len(base.tileItems())); cols != 2 || rows != 2 {
		t.Fatalf("fixture grid = %dx%d, want 2x2", cols, rows)
	}

	cases := []struct {
		name       string
		cursor     int
		page       int
		key        tea.KeyPressMsg
		wantCursor int
		wantPage   int
		wantCmd    bool
	}{
		{"l steps right", 1, 0, wallKey("l"), 2, 0, false},
		{"right steps right", 1, 0, wallKey("right"), 2, 0, false},
		{"h at the first tile is a no-op", 1, 0, wallKey("h"), 1, 0, false},
		{"j steps a grid row", 1, 0, wallKey("j"), 3, 0, false},
		{"down steps a grid row", 1, 0, wallKey("down"), 3, 0, false},
		{"ctrl+k steps back a grid row", 3, 0, wallKey("ctrl+k"), 1, 0, false},
		{"l past the page edge turns the page", 4, 0, wallKey("l"), 5, 1, true},
		{"h back over the page edge", 5, 1, wallKey("h"), 4, 0, true},
		{"l at the last tile is a no-op", 6, 1, wallKey("l"), 6, 1, false},
		{"] turns to the page's first tile", 1, 0, wallKey("]"), 5, 1, true},
		{"pgdown turns the page", 1, 0, wallKey("pgdown"), 5, 1, true},
		{"[ turns back", 6, 1, wallKey("["), 1, 0, true},
		{"[ on the first page is a no-op", 2, 0, wallKey("["), 2, 0, false},
		{"] past the last page is a no-op", 5, 1, wallKey("]"), 5, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := base
			m.cursor, m.wallPage = c.cursor, c.page
			got, cmd := m.handleKey(c.key)
			gm := got.(tuiModel)
			if gm.cursor != c.wantCursor || gm.wallPage != c.wantPage {
				t.Errorf("cursor/page = %d/%d, want %d/%d", gm.cursor, gm.wallPage, c.wantCursor, c.wantPage)
			}
			if (cmd != nil) != c.wantCmd {
				t.Errorf("cmd != nil = %v, want %v (a page turn must capture now)", cmd != nil, c.wantCmd)
			}
		})
	}
}

// Only rows capture-pane can read may be selected: a header, a zoxide
// suggestion and a remote bridge row have nothing to tile.
func TestWallSelectsOnlyTileableRows(t *testing.T) {
	m := wallFixture()
	tiles := m.tileItems()
	if want := []int{1, 2, 3, 4, 5, 6}; !slices.Equal(tiles, want) {
		t.Fatalf("tileItems = %v, want %v", tiles, want)
	}

	for _, start := range []int{0, 7, 8} {
		snapped := m
		snapped.cursor = start
		snapped = snapped.snapWall()
		if !slices.Contains(tiles, snapped.cursor) {
			t.Errorf("cursor %d snapped to %d, which is not tileable", start, snapped.cursor)
		}
	}

	// ^a rebuilds the list, which can leave a header under the cursor.
	onHeader := m
	onHeader.cursor = 0
	got, _ := onHeader.handleKey(wallKey("ctrl+a"))
	gm := got.(tuiModel)
	if !slices.Contains(gm.tileItems(), gm.cursor) {
		t.Errorf("ctrl+a left the cursor at %d, which is not tileable", gm.cursor)
	}
}

// listIndexAt/inPreview hit-test against list rows the wall doesn't have, so a
// click would resolve to an arbitrary row — and activate it when it matched.
func TestWallMouseInert(t *testing.T) {
	m := wallFixture()
	m.cursor = 3
	x, y := 5, m.listRowTop()+3
	// Pin what makes this a bug rather than a hypothetical: in list mode these
	// coords resolve to the cursor's own row, which activates it and quits.
	if idx, ok := m.listIndexAt(x, y); !ok || idx != m.cursor {
		t.Fatalf("listIndexAt(%d,%d) = (%d,%v), want the cursor row %d", x, y, idx, ok, m.cursor)
	}

	click, clickCmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if cm := click.(tuiModel); cm.cursor != m.cursor || clickCmd != nil {
		t.Errorf("click in wall mode: cursor %d, cmd %v; want inert", cm.cursor, clickCmd != nil)
	}
	wheel, wheelCmd := m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
	if wm := wheel.(tuiModel); wm.cursor != m.cursor || wheelCmd != nil {
		t.Errorf("wheel in wall mode: cursor %d, cmd %v; want inert", wm.cursor, wheelCmd != nil)
	}
}

// In the wall letters navigate, so the query is modal: / opens it, and until esc
// closes it every printable belongs to it — including q.
func TestWallQueryIsModal(t *testing.T) {
	m := wallFixture()
	m.cursor = m.tileItems()[0]

	typed, _ := m.handleKey(wallKey("l"))
	if tm := typed.(tuiModel); tm.query != "" || tm.querying {
		t.Errorf("l should navigate, not filter; query = %q querying = %v", tm.query, tm.querying)
	}

	opened, _ := m.handleKey(wallKey("/"))
	om := opened.(tuiModel)
	if !om.querying {
		t.Fatal("/ should open the filter prompt")
	}

	// q typing instead of quitting is the whole point of the mode — a shared
	// "q": quit branch seeing it would kill the picker mid-query.
	q, _ := om.handleKey(wallKey("q"))
	qm := q.(tuiModel)
	if qm.query != "q" {
		t.Errorf("q while querying should type, got query %q", qm.query)
	}

	done, _ := qm.handleKey(wallKey("esc"))
	dm := done.(tuiModel)
	if dm.querying {
		t.Error("esc should close the prompt")
	}
	if dm.query != "q" {
		t.Errorf("esc should keep the query, got %q", dm.query)
	}

	// Printables that aren't bindings are swallowed once the prompt is closed.
	swallowed, _ := dm.handleKey(wallKey("z"))
	if sm := swallowed.(tuiModel); sm.query != "q" {
		t.Errorf("printables must not reach the query in the wall, got %q", sm.query)
	}
}

// ^/ leaves the wall for the list, which is the only thing that re-enables the
// preview's captures.
func TestWallModeGatesCommands(t *testing.T) {
	m := wallFixture()
	if cmd := m.captureWallCmd(); cmd == nil {
		t.Error("wall mode should capture its page")
	}
	list := m
	list.mode = modeList
	if cmd := list.captureWallCmd(); cmd != nil {
		t.Error("list mode should not capture a wall page")
	}

	back, _ := m.handleKey(tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	if bm := back.(tuiModel); bm.mode != modeList {
		t.Errorf("ctrl+/ should drop back to the list, mode = %v", bm.mode)
	}
}

func TestWallModeFromFlag(t *testing.T) {
	if got := wallMode(true); got != modeWall {
		t.Errorf("wallMode(true) = %v, want modeWall", got)
	}
	if got := wallMode(false); got != modeList {
		t.Errorf("wallMode(false) = %v, want modeList", got)
	}
}

// A dead target must not blank the wall: tmux aborts the batch at the first bad
// target, so the reply is partial by design.
func TestMergeWallKeepsUnreachedTiles(t *testing.T) {
	m := wallFixture()
	m.wallContent = map[string]string{"s:1": "first", "s:2": "second"}
	m = m.mergeWall(wallMsg{content: map[string]string{"s:1": "fresh"}, bad: "s:2"})

	if m.wallContent["s:1"] != "fresh" {
		t.Errorf("s:1 = %q, want the fresh capture", m.wallContent["s:1"])
	}
	if m.wallContent["s:2"] != "second" {
		t.Errorf("s:2 = %q, want the previous capture kept", m.wallContent["s:2"])
	}
	if !m.wallBad["s:2"] {
		t.Error("the bad target should be recorded")
	}
	for _, target := range m.wallPageTargets() {
		if target == "s:2" {
			t.Error("a bad target must be dropped from the next batch")
		}
	}

	// A target that left the list takes its cached content with it.
	m.wallContent["gone:9"] = "stale"
	m.wallBad["gone:9"] = true
	m = m.mergeWall(wallMsg{})
	if _, ok := m.wallContent["gone:9"]; ok {
		t.Error("content for a target no longer tileable should be pruned")
	}
	if m.wallBad["gone:9"] {
		t.Error("bad flag for a target no longer tileable should be pruned")
	}
}

// bodyHeight over-reserved a row, which is a whole tile row's worth at the
// smallest size that affords two: 24 rows of terminal fit 2x10-row tiles.
func TestWallGeometryUsesTheWholeBody(t *testing.T) {
	m := wallFixture()
	m.width, m.height = 80, 24
	if got := m.bodyHeight(); got != 20 {
		t.Errorf("bodyHeight at 80x24 = %d, want 20 (search 2 + rule 1 + hints 1)", got)
	}
	if _, rows := wallGeometry(m.width, m.bodyHeight(), len(m.tileItems())); rows != 2 {
		t.Errorf("80x24 affords %d tile rows, want 2", rows)
	}
}

// wallPageTargets never retries a blacklisted target, so a failure tmux did not
// pin on a specific target must not blacklist one.
func TestUnattributedFailureRetriesTheTarget(t *testing.T) {
	m := wallFixture()
	m = m.mergeWall(wallMsg{content: map[string]string{"s:1": "alpha"}})

	if len(m.wallBad) != 0 {
		t.Errorf("wallBad = %v, want nothing blacklisted", m.wallBad)
	}
	if !slices.Contains(m.wallPageTargets(), "s:2") {
		t.Errorf("s:2 was dropped from the next batch: %v", m.wallPageTargets())
	}
}

// zoxideMsg and remoteMsg rebuild visible the same way refreshMsg does, so they
// have to re-snap the wall too — otherwise the indices shift under the cursor and
// the selection lands on a row that cannot be tiled.
func TestAsyncRowsResnapWall(t *testing.T) {
	for name, msg := range map[string]tea.Msg{
		"zoxide": zoxideMsg{items: []listItem{
			{target: "/tmp/a", createPath: "/tmp/a", createName: "a", display: "a", plain: "a"},
			{target: "/tmp/b", createPath: "/tmp/b", createName: "b", display: "b", plain: "b"},
		}},
		"remote": remoteMsg{items: []listItem{
			{target: "g6", remoteHost: "g6", isRemoteRow: true, display: "g6", plain: "g6"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			m := wallFixture()
			m.sessionItems = m.visible
			m.cursor = 5
			want := m.visible[m.cursor].target

			got, _ := m.Update(msg)
			gm := got.(tuiModel)

			if !slices.Contains(gm.tileItems(), gm.cursor) {
				t.Errorf("cursor %d is not a tileable row after the rebuild", gm.cursor)
			}
			if gm.visible[gm.cursor].target != want {
				t.Errorf("selection moved to %q, want it still on %q", gm.visible[gm.cursor].target, want)
			}
			if pos := tilePos(gm.tileItems(), gm.cursor); gm.wallPage != pos/gm.wallPerPage() {
				t.Errorf("page %d does not show the selected tile at position %d", gm.wallPage, pos)
			}
		})
	}
}

// ^/ out of the wall used to be one-way — nothing set modeWall back — while the
// hint bar advertised it as a toggle.
func TestWallToggleRoundTrip(t *testing.T) {
	m := wallFixture()
	m.wallLaunched = true

	list, _ := m.handleKey(wallKey("ctrl+/"))
	lm := list.(tuiModel)
	if lm.mode != modeList {
		t.Fatalf("^/ in the wall should show the list, mode = %v", lm.mode)
	}
	if hints := stripANSI(lm.renderHints()); !strings.Contains(hints, "^/:wall") {
		t.Errorf("a wall-launched popup's list hints should offer ^/:wall, got %q", hints)
	}

	back, cmd := lm.handleKey(wallKey("ctrl+/"))
	bm := back.(tuiModel)
	if bm.mode != modeWall {
		t.Errorf("^/ in a wall-launched list should return to the wall, mode = %v", bm.mode)
	}
	if !slices.Contains(bm.tileItems(), bm.cursor) {
		t.Errorf("returning to the wall left the cursor at %d, which is not tileable", bm.cursor)
	}
	if cmd == nil {
		t.Error("returning to the wall must capture now, not in 500ms")
	}
}

// A popup that was never a wall keeps ^/ as the preview toggle it has always been.
func TestPreviewToggleUnchangedWithoutWall(t *testing.T) {
	m := wallFixture()
	m.mode, m.wallLaunched, m.showPreview = modeList, false, false

	got, _ := m.handleKey(wallKey("ctrl+/"))
	gm := got.(tuiModel)
	if gm.mode != modeList || !gm.showPreview {
		t.Errorf("^/ should toggle the preview; mode = %v showPreview = %v", gm.mode, gm.showPreview)
	}
	if hints := stripANSI(gm.renderHints()); !strings.Contains(hints, "^/:preview") {
		t.Errorf("hints should still offer ^/:preview, got %q", hints)
	}
}

// wallFixture is a 2x2 wall over 6 tileable rows, with the three untileable row
// kinds mixed in.
func wallFixture() tuiModel {
	items := []listItem{
		{display: "── Windows ──", plain: "── Windows ──", isHeader: true, session: "s"},
		{target: "s:1", display: "1: alpha", plain: "1: alpha", session: "s", hasActiveClaude: true},
		{target: "s:2", display: "2: beta", plain: "2: beta", session: "s", hasActiveClaude: true},
		{target: "s:3", display: "3: gamma", plain: "3: gamma", session: "s", hasActiveClaude: true},
		{target: "s:4", display: "4: delta", plain: "4: delta", session: "s", hasActiveClaude: true},
		{target: "s:5", display: "5: epsilon", plain: "5: epsilon", session: "s", hasActiveClaude: true},
		{target: "s:6", display: "6: zeta", plain: "6: zeta", session: "s", hasActiveClaude: true},
		{target: "/tmp/dir", createPath: "/tmp/dir", createName: "dir", display: "dir", plain: "dir"},
		{target: "g5", remoteHost: "g5", isRemoteRow: true, display: "g5", plain: "g5"},
	}
	return tuiModel{
		mode: modeWall, ready: true, theme: "dark",
		width: 80, height: 25, // bodyHeight 20 -> 2 cols x 2 rows
		allItems: items, visible: items, cursor: 1,
		wallContent: map[string]string{},
		wallBad:     map[string]bool{},
	}
}

func wallKey(s string) tea.KeyPressMsg {
	switch s {
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	if ctrl, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(ctrl[0]), Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0])}
}
