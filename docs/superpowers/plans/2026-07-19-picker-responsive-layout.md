# Picker Responsive Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the bubbletea pickers adapt to terminal size — preview always at the bottom, and prefix+w window rows put the worker after the index, fold the Linear/GH ticket inline as the name, and truncate adaptively.

**Architecture:** Three independent changes to `picker/tui.go`. (1) Delete the landscape/side-preview code path so the list always gets full width and preview stacks below. (2) Rewrite `renderWindowItems` so crew renders after the index (no reserved column), the enrich identity becomes the inline name (issue → branch → basename priority) with the trailing identity column removed, and a pure `identityCapFor` helper drives adaptive truncation. (3) Thread the model's width into the rebuild path so the adaptive cap tracks the real terminal.

**Tech Stack:** Go 1.22+, charmbracelet/bubbletea v2 + lipgloss + bubbles/viewport; built via Nix (`nix build .#default`, tests via `nix flake check` / `go test ./...`).

## Global Constraints

- Language: Go; run tests from `picker/` with `go test ./...` (module root is `picker/`).
- Cell-width math uses `iconCellWidth` / `truncateCells` / `padToWidth` (rune- and width-aware); never `len()` or `wc -L` for display width.
- Window options (`@window_*`, `@crew_*`, `@pr_*`) stay the single source of truth — this plan reads them via `windowData`, never writes tmux options.
- Colors come from `ansiFg`/`ansiFgTmux` + `envOrMap` theme lookups already in the file; ANSI reset is `"\033[0m"`.
- `width == 0` is the sentinel for "terminal size unknown → use the default identity cap".
- Commit style: conventional commits, scope `picker`, reference `#175`. Commit from inside the devshell so pre-commit hooks run (`nix develop -c git commit ...`).

---

## Task 1: Preview always at the bottom

Remove the side-by-side (landscape) layout. When `showPreview` is on, the preview always stacks below the list; the list always spans the full inner width. Delete `portrait()` and collapse every caller to its portrait branch.

**Files:**
- Modify: `picker/tui.go` — `View` (346-377), `renderSeparator` (411-423), `listHeight` (499-506), `listWidth` (512-526), `previewWidth` (528-533), `previewHeight` (535-540), `inPreview` (655-665), `listIndexAt` (636-653), delete `portrait` (486-488).
- Test: `picker/tui_test.go` — update `TestListIndexAt` (99-136) and `TestInPreview` (138-161).

**Interfaces:**
- Consumes: `m.innerWidth()`, `m.bodyHeight()`, `m.listHeight()`, `m.listRowTop()`, `m.showPreview`, `m.ready`.
- Produces: `listWidth()` and `previewWidth()` both return `innerWidth()`; `listHeight()` returns `bodyHeight()*50/100` when preview shown else `bodyHeight()`; `previewHeight()` returns `bodyHeight() - listHeight() - 1`; `portrait()` no longer exists.

- [ ] **Step 1: Update the two mouse-hit-testing tests to the bottom-only model**

In `picker/tui_test.go`, replace `TestListIndexAt` and `TestInPreview` (lines 99-161) with:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd picker && go test ./... -run 'TestListIndexAt|TestInPreview' -v`
Expected: FAIL — `TestListIndexAt/right_side_is_still_the_list_now` fails (current code returns `false` for x=70), and/or `portrait` still routing preview to the side.

- [ ] **Step 3: Collapse `View` to always stack vertically**

In `picker/tui.go`, replace the `if m.showPreview { ... }` block inside `View` (lines 353-362) with:

```go
		if m.showPreview {
			sep := m.renderSeparator()
			body = lipgloss.JoinVertical(lipgloss.Left, listPane, sep, m.preview.View())
		} else {
			body = listPane
		}
```

- [ ] **Step 4: Collapse the layout + separator + mouse methods**

Replace `renderSeparator` (411-423) with the horizontal-only version:

```go
func (m tuiModel) renderSeparator() string {
	sepColor := lipgloss.NewStyle().
		Foreground(m.thmColor("@thm_surface_1", "#45475a", "#9ca0b0"))
	return sepColor.Render(strings.Repeat("─", m.innerWidth()))
}
```

Delete `portrait` (486-488). Replace `listHeight`, `listWidth`, `previewWidth`, `previewHeight` (499-540) with:

```go
func (m tuiModel) listHeight() int {
	bh := m.bodyHeight()
	if !m.showPreview {
		return bh
	}
	// List gets the top ~50%, preview the bottom (minus 1 for the separator).
	return bh * 50 / 100
}

func (m tuiModel) innerWidth() int {
	return m.width // tmux popup provides the border
}

func (m tuiModel) listWidth() int {
	return m.innerWidth()
}

func (m tuiModel) previewWidth() int {
	return m.innerWidth()
}

func (m tuiModel) previewHeight() int {
	if !m.showPreview {
		return m.bodyHeight()
	}
	return m.bodyHeight() - m.listHeight() - 1 // -1 for separator
}
```

Replace the side-column guard in `listIndexAt` (645-647) — delete these lines entirely:

```go
	if !m.portrait() && m.showPreview && x >= m.listWidth() {
		return 0, false // click landed in the separator/preview column
	}
```

Replace `inPreview` (655-665) with:

```go
// inPreview reports whether screen coords fall in the preview pane, which always
// sits below the list (past the separator row).
func (m tuiModel) inPreview(x, y int) bool {
	if !m.showPreview || !m.ready {
		return false
	}
	return y >= m.listRowTop()+m.listHeight()+1 // past the separator row
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd picker && go test ./... -run 'TestListIndexAt|TestInPreview' -v`
Expected: PASS.

- [ ] **Step 6: Full build + vet to confirm no dangling `portrait()` references**

Run: `cd picker && go vet ./... && go build ./...`
Expected: no errors (confirms every `portrait()` caller was removed).

- [ ] **Step 7: Commit**

```bash
cd picker && cd .. && nix develop -c git add picker/tui.go picker/tui_test.go
nix develop -c git commit -m "feat(picker): preview always renders below the list (#175)"
```

---

## Task 2: Window row redesign + adaptive truncation (pure render)

Rewrite `renderWindowItems` to the new column order and add a pure `identityCapFor` helper that drives width-adaptive truncation. This task is pure rendering — it takes an explicit `width` and is fully unit-tested. Model wiring is Task 3.

**Files:**
- Modify: `picker/tui.go` — `buildWindowItems` (1045-1047), `renderWindowItems` (1051-1319), the `runTUI` initial build (115) and `refreshDataCmd` build (807) to pass `width` (both pass `0` for now — Task 3 supplies the real value).
- Test: `picker/tui_test.go` — add `TestIdentityCapFor` and `TestRenderWindowItemsLayout`.

**Interfaces:**
- Consumes (existing helpers, unchanged signatures): `aggregateClaudeByWindow`, `mergeClaudeWindows`, `collectSessionActivity`, `buildProcIcons(procs, maxIconsPicker) (string,int)`, `appendClaudeIcon`, `appendIssueIDs`, `colorPRBadge`, `branchEchoesName(branch,name) bool`, `claudePriority(claudeCounts) string`, `isActiveState`, `envOrMap`, `ansiFg`, `ansiFgTmux`, `iconCellWidth`, `truncateCells`, `padToWidth`, `max`.
- Produces:
  - `func identityCapFor(width, leadDW, iconDW, prDW int) int` — returns the identity truncation cap in cells. `width <= 0` → `defaultIdentityCap` (32). Otherwise `clamp(width - leadDW - iconDW - prDW - layoutGaps, minIdentityCap, maxIdentityCap)` with `minIdentityCap=12`, `maxIdentityCap=48`, `layoutGaps=6`.
  - `func buildWindowItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int) []listItem`
  - `func renderWindowItems(windows []windowData, tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int) []listItem`

- [ ] **Step 1: Write the failing tests**

Add to `picker/tui_test.go`:

```go
import "strings" // add to the existing import block if not present

func TestIdentityCapFor(t *testing.T) {
	cases := []struct {
		name                        string
		width, lead, icon, pr, want int
	}{
		{"unknown width -> default", 0, 10, 6, 4, 32},
		{"negative width -> default", -1, 10, 6, 4, 32},
		{"wide clamps to max", 200, 10, 6, 4, 48},   // 200-10-6-4-6=174 -> 48
		{"narrow clamps to min", 20, 10, 6, 4, 12},  // 20-10-6-4-6=-6 -> 12
		{"mid computes exactly", 60, 10, 6, 4, 34},  // 60-10-6-4-6=34, in range
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd picker && go test ./... -run 'TestIdentityCapFor|TestRenderWindowItemsLayout' -v`
Expected: FAIL — `identityCapFor` undefined and `renderWindowItems` has the old 5-arg signature / old layout.

- [ ] **Step 3: Add the `identityCapFor` helper and layout constants**

In `picker/tui.go`, just above `renderWindowItems` (before line 1049), add:

```go
const (
	defaultIdentityCap = 32
	minIdentityCap     = 12
	maxIdentityCap     = 48
	// layoutGaps: "tree marker " (4) + gap before icons (1) + gap before PR (1).
	layoutGaps = 6
)

// identityCapFor sizes the inline-identity column from the terminal width, so
// wider terminals show longer ticket titles and narrow ones shorten them while
// the icon and PR columns stay pinned. width<=0 (size unknown) uses the default.
func identityCapFor(width, leadDW, iconDW, prDW int) int {
	if width <= 0 {
		return defaultIdentityCap
	}
	cap := width - leadDW - iconDW - prDW - layoutGaps
	if cap < minIdentityCap {
		return minIdentityCap
	}
	if cap > maxIdentityCap {
		return maxIdentityCap
	}
	return cap
}
```

- [ ] **Step 4: Rewrite `buildWindowItems` + `renderWindowItems`**

Replace `buildWindowItems` (1045-1047) with:

```go
func buildWindowItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int) []listItem {
	return renderWindowItems(collectWindows(), tmuxOpts, claudePanes, theme, width)
}
```

Replace the entire `renderWindowItems` function (1051-1319) with:

```go
// renderWindowItems is the pure rendering half of buildWindowItems, split out so
// the enriched row layout can be unit-tested with synthetic windows. width is
// the list width in cells (0 = unknown → default identity cap).
func renderWindowItems(windows []windowData, tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int) []listItem {
	claudeByWin := aggregateClaudeByWindow(claudePanes)
	mergeClaudeWindows(windows, claudeByWin)

	thmMauve := envOrMap("THM_MAUVE", tmuxOpts, "@thm_mauve", "#cba6f7")
	thmGreen := envOrMap("THM_GREEN", tmuxOpts, "@thm_green", "#a6e3a1")
	thmRed := envOrMap("THM_RED", tmuxOpts, "@thm_red", "#f38ba8")
	thmPeach := envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387")
	thmSubtext0 := envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")
	thmOverlay1 := envOrMap("THM_OVERLAY_1", tmuxOpts, "@thm_overlay_1", "#7f849c")
	thmOverlay0 := envOrMap("THM_OVERLAY_0", tmuxOpts, "@thm_overlay_0", "#6c7086")
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)
	iBranch := envOrMap("PICKER_ICON_BRANCH", tmuxOpts, "@icon_branch", iconBranch)

	cMauve := ansiFg(thmMauve)
	cGreen := ansiFg(thmGreen)
	cDim := ansiFg(thmSubtext0)
	cFaint := ansiFg(thmOverlay1)
	reset := "\033[0m"
	dim := "\033[2m"
	prCols := prColors{success: cGreen, failure: ansiFg(thmRed), pending: ansiFg(thmPeach), merged: cMauve, closed: ansiFg(thmOverlay0), reset: reset}

	type sessGroup struct {
		name     string
		activity int64
		windows  []*windowData
	}
	groupMap := make(map[string]*sessGroup)
	for i := range windows {
		w := &windows[i]
		g, ok := groupMap[w.session]
		if !ok {
			g = &sessGroup{name: w.session}
			groupMap[w.session] = g
		}
		g.windows = append(g.windows, w)
	}

	sessActivity := collectSessionActivity()
	groups := make([]*sessGroup, 0, len(groupMap))
	for _, g := range groupMap {
		g.activity = sessActivity[g.name]
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].activity != groups[j].activity {
			return groups[i].activity > groups[j].activity
		}
		return groups[i].name < groups[j].name
	})
	for _, g := range groups {
		sort.Slice(g.windows, func(i, j int) bool {
			return g.windows[i].index < g.windows[j].index
		})
	}

	// Pass A builds every fixed-width piece and the raw identity parts, and
	// tracks the column maxima (lead prefix, icons, PR). The identity cap is
	// then derived from the terminal width, and pass B truncates + aligns.
	type rawIdentity struct {
		kind        int    // 0 none/name, 1 issue, 2 branch
		id, rest    string // issue: id (accent) + rest (dim)
		text        string // branch/name plain text
		leadGlyph   string // branch icon, already includes trailing space, or ""
	}
	type renderedWin struct {
		win      *windowData
		name     string // clean window name, for search
		icons    string
		iconDW   int
		leadPlain   string // "N: " + crew + " "  (uncolored, for width)
		leadColored string
		leadDW      int
		ident    rawIdentity
		identSearch string
		prBadge  string
		prPlain  string
		crewName string
	}
	winRows := make(map[string][]renderedWin)
	maxLeadDW, maxIconDW, maxPrDW := 0, 0, 0
	for _, g := range groups {
		for _, w := range g.windows {
			icons, dw := buildProcIcons(w.procs, maxIconsPicker)
			icons, dw = appendClaudeIcon(icons, dw, w.claude, theme, dim, reset)
			icons, dw = appendIssueIDs(icons, dw, w.claude.issues, cDim, reset)

			name := truncateCells(w.name, 40)

			// Lead: "N: " then the crew codename (after the index), only when
			// tagged — no reserved gap otherwise.
			leadPlain := fmt.Sprintf("%d: ", w.index)
			leadColored := leadPlain
			if w.crewName != "" {
				leadPlain += w.crewName + " "
				crew := w.crewName
				if c := ansiFgTmux(w.crewColor); c != "" {
					crew = c + w.crewName + reset
				}
				leadColored += crew + " "
			}
			leadDW := iconCellWidth(leadPlain)

			// Inline identity (the name): issue id+title → non-default branch →
			// repo basename. Mirrors the status bar's build_window_label priority.
			var ri rawIdentity
			var idSearch string
			if w.labelID != "" {
				ri = rawIdentity{kind: 1, id: w.labelID, rest: w.labelRest}
				idSearch = w.labelID + w.labelRest
			} else if w.branch != "" && !branchEchoesName(w.branch, w.name) && w.branch != "main" && w.branch != "master" {
				ri = rawIdentity{kind: 2, text: w.branch, leadGlyph: ""}
				if iBranch != "" {
					ri.leadGlyph = iBranch + " "
				}
				idSearch = w.branch
			} else {
				ri = rawIdentity{kind: 0, text: name}
				idSearch = name
			}

			prBadge := colorPRBadge(w.prPlain, w.prState, w.prCheck, w.prMergeable, prCols)
			prPlain := strings.TrimSpace(w.prPlain)
			prDW := iconCellWidth(prPlain)

			winRows[g.name] = append(winRows[g.name], renderedWin{
				win: w, name: name, icons: icons, iconDW: dw,
				leadPlain: leadPlain, leadColored: leadColored, leadDW: leadDW,
				ident: ri, identSearch: idSearch,
				prBadge: prBadge, prPlain: prPlain, crewName: w.crewName,
			})
			maxLeadDW = max(maxLeadDW, leadDW)
			maxIconDW = max(maxIconDW, dw)
			maxPrDW = max(maxPrDW, prDW)
		}
	}
	iconCol := max(maxIconDW+1, 3)
	identityCap := identityCapFor(width, maxLeadDW, iconCol, maxPrDW)
	// Uniform label column so the icon column lines up across every row.
	labelCol := maxLeadDW + identityCap

	// truncID renders a rawIdentity to (colored, plain) within identityCap cells.
	truncID := func(ri rawIdentity) (string, string) {
		switch ri.kind {
		case 1: // issue: id accent + dim title
			rest := ri.rest
			if iconCellWidth(ri.id+rest) > identityCap {
				rest = truncateCells(rest, max(identityCap-iconCellWidth(ri.id), 1))
			}
			plain := ri.id + rest
			colored := cMauve + ri.id + reset
			if rest != "" {
				colored += cDim + rest + reset
			}
			return colored, plain
		case 2: // branch: faint, optional glyph
			br := truncateCells(ri.text, max(identityCap-iconCellWidth(ri.leadGlyph), 1))
			plain := ri.leadGlyph + br
			return cFaint + plain + reset, plain
		default: // name: plain
			nm := truncateCells(ri.text, identityCap)
			return nm, nm
		}
	}

	var items []listItem
	for _, g := range groups {
		rows := winRows[g.name]

		sessHasClaude := false
		for _, r := range rows {
			key := fmt.Sprintf("%s:%d", r.win.session, r.win.index)
			if cc, ok := claudeByWin[key]; ok && isActiveState(claudePriority(*cc)) {
				sessHasClaude = true
				break
			}
		}

		headerDisplay := fmt.Sprintf("%s %s",
			cMauve+iSess+reset,
			cMauve+g.name+reset,
		)
		items = append(items, listItem{
			target:          g.name,
			display:         headerDisplay,
			plain:           fmt.Sprintf("%s %s", iSess, g.name),
			searchText:      g.name,
			isHeader:        true,
			session:         g.name,
			hasActiveClaude: sessHasClaude,
		})

		multiWin := len(rows) > 1
		for wi, r := range rows {
			w := r.win

			activeMarker := " "
			if w.active && multiWin {
				activeMarker = cGreen + "▸" + reset
			}

			icons := r.icons
			if icons == "" {
				icons = strings.Repeat(" ", iconCol)
			} else {
				icons = padToWidth(icons, r.iconDW, iconCol)
			}

			tree := "├─"
			if wi == len(rows)-1 {
				tree = "╰─"
			}

			idColored, idPlain := truncID(r.ident)
			zoom := ""
			if w.zoomed {
				zoom = " 󰁌" // a zoomed row may run up to 2 cells past labelCol
			}
			labelColored := r.leadColored + idColored + zoom
			labelPlain := r.leadPlain + idPlain + zoom
			labelColored = padToWidth(labelColored, iconCellWidth(labelPlain), labelCol)
			labelPlain = padToWidth(labelPlain, iconCellWidth(labelPlain), labelCol)

			display := fmt.Sprintf("%s %s %s %s",
				cDim+tree+reset, activeMarker, labelColored, icons)
			plain := fmt.Sprintf("%s %s %s %s",
				tree, strings.TrimSpace(stripANSI(activeMarker)), labelPlain, stripANSI(icons))
			if r.prBadge != "" {
				display += " " + r.prBadge
				plain += " " + r.prPlain
			}
			display = strings.TrimRight(display, " ")
			plain = strings.TrimRight(plain, " ")

			search := g.name + " " + r.name
			if r.identSearch != "" {
				search += " " + r.identSearch
			}
			if r.prPlain != "" {
				search += " " + r.prPlain
			}
			if r.crewName != "" {
				search += " " + r.crewName
			}
			items = append(items, listItem{
				target:          fmt.Sprintf("%s:%d", g.name, w.index),
				display:         display,
				plain:           plain,
				searchText:      search,
				session:         g.name,
				hasActiveClaude: isActiveState(claudePriority(w.claude)),
			})
		}
	}
	return items
}
```

- [ ] **Step 5: Update the two `buildWindowItems` call sites to pass width 0 (temporary)**

In `runTUI` (line 115): `items = buildWindowItems(opts, panes, theme, 0)`
In `refreshDataCmd` (line 807): `items = buildWindowItems(opts, panes, theme, 0)`

(Task 3 replaces both `0`s with the real width.)

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `cd picker && go test ./... -run 'TestIdentityCapFor|TestRenderWindowItemsLayout' -v`
Expected: PASS.

- [ ] **Step 7: Full package test + vet**

Run: `cd picker && go vet ./... && go test ./...`
Expected: PASS — confirms no other caller relied on the old signature or the removed `crewCell`/`identityCap` locals.

- [ ] **Step 8: Commit**

```bash
cd .. && nix develop -c git add picker/tui.go picker/tui_test.go
nix develop -c git commit -m "feat(picker): inline ticket + crew-after-index window rows (#175)"
```

---

## Task 3: Thread terminal width into the window rebuild

Wire the model's live width into the window-item rebuild so the adaptive cap tracks the real terminal. Rebuild once when the width first becomes known (and on any change), guarded so it fires ~once for a fixed-size popup.

**Files:**
- Modify: `picker/tui.go` — `refreshDataCmd` (799-814), the `WindowSizeMsg` case in `Update` (150-164).

**Interfaces:**
- Consumes: `identityCapFor` / `buildWindowItems(opts, panes, theme, width)` from Task 2; `m.listWidth()`, `m.windowMode`, `m.width`.
- Produces: `refreshDataCmd` renders window items at `m.listWidth()`; `WindowSizeMsg` triggers a rebuild when the width changes in window mode.

- [ ] **Step 1: Pass the live width from `refreshDataCmd`**

In `picker/tui.go`, replace `refreshDataCmd` (799-814) with:

```go
func (m tuiModel) refreshDataCmd() tea.Cmd {
	wm := m.windowMode
	opts := m.tmuxOpts
	theme := m.theme
	lw := m.listWidth() // capture the value; the closure runs off-thread
	return func() tea.Msg {
		panes := collectClaudePanes()
		var items []listItem
		if wm {
			items = buildWindowItems(opts, panes, theme, lw)
		} else {
			items = buildSessionItems(opts, panes, theme)
		}
		// Always send — spinners need to animate even without structural changes.
		return refreshMsg{items: items}
	}
}
```

- [ ] **Step 2: Rebuild window items when the width changes**

In `Update`, replace the `tea.WindowSizeMsg` case (150-164) with:

```go
	case tea.WindowSizeMsg:
		widthChanged := msg.Width != m.width
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.preview = viewport.New(
				viewport.WithWidth(m.previewWidth()),
				viewport.WithHeight(m.previewHeight()),
			)
			m.preview.MouseWheelEnabled = true
			m.ready = true
		} else {
			m.preview.SetWidth(m.previewWidth())
			m.preview.SetHeight(m.previewHeight())
		}
		// Window labels are truncated to the terminal width; when it changes,
		// rebuild so the adaptive identity cap tracks the real size. Guarded on
		// change so a fixed-size popup forks the rebuild ~once.
		if m.windowMode && widthChanged {
			return m, tea.Batch(m.loadPreviewCmd(), m.refreshDataCmd())
		}
		return m, m.loadPreviewCmd()
```

- [ ] **Step 3: Build + vet + full test**

Run: `cd picker && go vet ./... && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd .. && nix develop -c git add picker/tui.go
nix develop -c git commit -m "feat(picker): adaptive window-label width tracks terminal size (#175)"
```

---

## Task 4: Nix build, flake check, and manual verification

**Files:** none (verification only).

- [ ] **Step 1: Build the wrapped tmux**

Run: `nix build .#default`
Expected: succeeds, produces `./result/bin/tmux`.

- [ ] **Step 2: Flake check (includes pre-commit hooks + bats)**

Run: `nix flake check`
Expected: all checks pass.

- [ ] **Step 3: Manual — window picker on a wide terminal**

In a wide (e.g. 200-col) tmux client, open `prefix + w`. Confirm:
- Preview renders **below** the list (not on the right); list spans full width.
- Rows read `N: <crew?> <ticket-or-name>` — crew after the index, ticket (e.g. `ENG-7290 <title>`) inline as the name; no trailing `ENG-…` column.
- Untagged rows have no crew gap (`1: mono`).
- Claude icons align in a column; PR badge trails.

- [ ] **Step 4: Manual — narrow terminal + session picker**

- Shrink the client (e.g. ~80 cols) and reopen `prefix + w`: ticket titles shorten but icons + PR badge stay visible.
- Open `prefix + s`: preview also renders at the bottom.

- [ ] **Step 5: Push the branch and open the PR**

```bash
nix develop -c git push -u origin feat/175-picker-responsive
gh pr create --assignee @me --title "feat(picker): responsive layout — bottom preview + inline-identity window rows" --body "Closes #175"
```

---

## Self-Review

**Spec coverage:**
- Preview always bottom (spec §1) → Task 1. ✓
- Worker after index, no reserved column (spec §2) → Task 2 (`leadPlain`/`leadColored`, no `crewCell`). ✓
- Ticket inline as name, trailing column removed (spec §2) → Task 2 (`rawIdentity` priority + inline label; `anyPR`/`identityCol` deleted). ✓
- PR badge stays trailing (spec §2) → Task 2 (`prBadge` appended after icons). ✓
- Adaptive name width (spec §3) → Task 2 (`identityCapFor`) + Task 3 (width threading). ✓
- Tests updated for new order + width param (spec Testing) → Tasks 1, 2. ✓
- nix build + flake check + manual (spec Testing) → Task 4. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code.

**Type consistency:** `identityCapFor(width, leadDW, iconDW, prDW int) int`, `buildWindowItems(opts, panes, theme, width int)`, and `renderWindowItems(windows, opts, panes, theme, width int)` are used identically in Tasks 2 and 3. `iconCol` is passed as `iconDW` to `identityCapFor` (the reserved icon column width, intentional). `labelCol = maxLeadDW + identityCap` is the uniform pad target used in the render pass.
