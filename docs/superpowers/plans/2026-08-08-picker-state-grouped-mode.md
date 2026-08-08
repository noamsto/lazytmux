# Picker State-Grouped Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `prefix + w` (window mode, `picker/`) gets a toggle that regroups the
same rows by `claude_priority_state` instead of by session, so "which agent
needs me right now" is answerable by glancing at the top of the list instead
of sweeping every session group for a glyph. Closes #229.

**Architecture:** `renderWindowItems` already does two passes: Pass A builds
every window's rendered pieces (icons, identity, PR badge) independent of how
rows get grouped; Pass B walks ordered groups and emits a header + tree rows
per group. Today grouping (by session, sorted by activity) is inlined into
Pass B. This plan extracts grouping into two small pure functions —
`groupWindowsBySession` (today's behavior, unchanged) and
`groupWindowsByState` (new) — selected by a `stateGrouped` flag threaded down
from a new `tuiModel.stateGrouped` field, toggled by `ctrl+g`. Pass A becomes
grouping-agnostic (keyed by `"session:index"` instead of by session), so both
modes reuse it untouched. State-grouped rows fold their session name into the
identity column (there's no session header to carry it), using the same
`identityCap` budget the column alignment already enforces — carved out of it,
never on top of it.

**Tech Stack:** Go (`github.com/noamsto/lazytmux/picker`, module in
`picker/`), bubbletea v2 + lipgloss v2, `go test ./picker/...`, `nix build
.#default` / `nix flake check` / `nix build .#lint` for the full gate.

---

## Design Decisions

The issue leaves several things open; answers below, so they don't get
re-litigated mid-implementation.

1. **Toggle key: `ctrl+g`, not `s`.** Plain `s` is not free — window mode's
   `handleKey` default case sends every unbound printable key into the search
   query (`m.query += key`), so binding bare `s` would make it impossible to
   type the letter "s" into a window-mode search. `ctrl+s` is also taken
   (scratch-only filter). `ctrl+g` ("**g**roup") is unused in both
   `handleKey` and `handleWallKey` — confirmed by grepping every `case "..."`
   arm in `picker/tui.go`. Gated to window mode only (a no-op in the session
   picker, where there's no window-level state to group by).
2. **Toggle does not persist across popup invocations.** Every `prefix + w`
   launch starts a fresh `tuiModel` from `runTUI`, and neither `claudeOnly`
   nor `scratchOnly` — the two existing toggles — persist across launches
   either (both zero-value `false` at construction, `claudeOnly` only seeded
   from the `--claude` CLI flag). Session grouping is what someone opening
   the picker expects by default; a state-grouped view that outlived the
   popup that set it would be a surprise the next time `prefix + w` opens,
   for a feature whose whole point is "check what needs me *right now*", not
   "leave the picker in a mode". Matches existing precedent, no new
   persistence mechanism needed.
3. **Session name folding: a dim `"session / "` prefix, capped at
   `identityCap/3` cells**, carved out of the *existing* identity budget
   (`identityCap`, already adaptive to terminal width) rather than added on
   top of it. The row's own identity (branch/issue/name) keeps at least 2/3
   of the budget, so a long session name degrades gracefully instead of
   crowding out the more decision-relevant text (issue id, branch).
4. **Stateless windows collapse into exactly one trailing group**, keyed by
   the empty string (the same key `claudePriority` already returns for "no
   claude state"), labeled "No agent", ordered last.
5. **Scope note:** this plan touches `picker/main.go` (+ a new
   `picker/main_test.go`) in addition to the `render_list.go`/`tui.go` pair
   the issue names. `claudePriority` (the Go counterpart of
   `lib-claude.sh`'s `claude_priority_state`) already lives there, and the
   issue explicitly warns against hardcoding a second, divergent priority
   list — the only way to avoid that is to derive the state-group order from
   the same list `claudePriority` walks, in the same file. This does not
   touch `picker/remote.go`, `picker/agentdetect/**`, or `scripts/**` — no
   overlap with the parallel #268 worker.
6. **Go's priority order omits `interrupted`.** `lib-claude.sh`'s order is
   `error > waiting > denied > compacting > interrupted > processing > done >
   idle`; the Go picker's `claudeCounts` has no `interrupted` field because
   `interrupted` is derived from a transcript-tail scrape the Go side never
   does (documented in `CLAUDE.md`'s Remote Agent Status section: "Not
   carried: `interrupted`"). `claudePriority` already omits it today — this
   plan doesn't add it, just gives its existing order a name.

---

## File Structure

- **Modify:** `picker/main.go` — `claudePriority` refactored onto a named
  `claudeStateOrder` slice (single source of truth); add `claudeStateLabel`
  (header text per state).
- **Create:** `picker/main_test.go` — `TestClaudePriority` (no test file for
  `main.go` exists yet).
- **Modify:** `picker/tui.go` — `windowGroup` type + `groupWindowsBySession`
  / `groupWindowsByState`; `listItem.groupKey` field; `tuiModel.stateGrouped`
  field; `ctrl+g` handling; `renderWindowItems`/`buildWindowItems` threaded
  with `stateGrouped`; `foldSessionPrefix`, `stateGroupHeader` helpers;
  `withFilter`'s window-mode regroup keyed on `groupKey` instead of
  `session`.
- **Modify:** `picker/tui_test.go` — existing `renderWindowItems(...)` calls
  gain a trailing `false` arg; new tests for grouping, folding, the toggle,
  and filter composition.
- **Modify:** `picker/render_list.go` — `renderHints` grows a `^g:group`
  entry, shown only in window mode, highlighted like `claude`/`scratch` when
  active.
- **Modify:** `CLAUDE.md` — one line on the `tmux-window-picker` row.

---

## Task 1: Single source of truth for the claude priority order

**Files:**
- Modify: `picker/main.go:754-777`
- Create: `picker/main_test.go`

- [ ] **Step 1: Write the failing test**

Create `picker/main_test.go`:

```go
package main

import "testing"

func TestClaudePriority(t *testing.T) {
	cases := []struct {
		name string
		c    claudeCounts
		want string
	}{
		{"error wins over everything", claudeCounts{errorCnt: 1, waiting: 1, done: 1}, "error"},
		{"waiting beats denied/compacting/processing/done/idle",
			claudeCounts{waiting: 1, denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "waiting"},
		{"denied beats compacting/processing/done/idle",
			claudeCounts{denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "denied"},
		{"compacting beats processing/done/idle",
			claudeCounts{compacting: 1, processing: 1, done: 1, idle: 1}, "compacting"},
		{"processing beats done/idle", claudeCounts{processing: 1, done: 1, idle: 1}, "processing"},
		{"done beats idle", claudeCounts{done: 1, idle: 1}, "done"},
		{"idle alone", claudeCounts{idle: 1}, "idle"},
		{"all zero", claudeCounts{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudePriority(c.c); got != c.want {
				t.Errorf("claudePriority(%+v) = %q, want %q", c.c, got, c.want)
			}
		})
	}
}
```

`claudeStateOrder` doesn't exist yet (it's introduced in Step 3) — don't
add a test for it here, or the package fails to compile before the refactor
even starts.

- [ ] **Step 2: Run the test to verify it's green**

Run: `cd picker && go test ./... -run TestClaudePriority -v`
Expected: compiles and passes today (the switch-chain already behaves this
way) — the point of this step is to lock in current behavior as a
regression net before the refactor, not to see red. Confirm it's green
before touching `claudePriority`.

- [ ] **Step 3: Refactor `claudePriority` onto a named order slice**

In `picker/main.go`, replace lines 754-777:

```go
func claudePriority(c claudeCounts) string {
	if c.errorCnt > 0 {
		return "error"
	}
	if c.waiting > 0 {
		return "waiting"
	}
	if c.denied > 0 {
		return "denied"
	}
	if c.compacting > 0 {
		return "compacting"
	}
	if c.processing > 0 {
		return "processing"
	}
	if c.done > 0 {
		return "done"
	}
	if c.idle > 0 {
		return "idle"
	}
	return ""
}
```

with:

```go
// claudeStateOrder mirrors lib-claude.sh's claude_priority_state, minus
// "interrupted" — a shell-only state derived from a transcript-tail scrape
// the Go picker never does (see CLAUDE.md's Remote Agent Status section).
// Single source of truth for both claudePriority (per-window state) and
// window mode's state-grouped header order (#229) — a second, hand-copied
// list here would silently diverge from this one.
var claudeStateOrder = []string{
	"error", "waiting", "denied", "compacting", "processing", "done", "idle",
}

// claudeStateLabel is the header text for each state in window mode's
// state-grouped view; "" (no claude state at all) is the trailing group.
var claudeStateLabel = map[string]string{
	"error": "Error", "waiting": "Waiting", "denied": "Denied",
	"compacting": "Compacting", "processing": "Processing", "done": "Done",
	"idle": "Idle", "": "No agent",
}

func claudeStateCount(c claudeCounts, state string) int {
	switch state {
	case "error":
		return c.errorCnt
	case "waiting":
		return c.waiting
	case "denied":
		return c.denied
	case "compacting":
		return c.compacting
	case "processing":
		return c.processing
	case "done":
		return c.done
	case "idle":
		return c.idle
	}
	return 0
}

func claudePriority(c claudeCounts) string {
	for _, state := range claudeStateOrder {
		if claudeStateCount(c, state) > 0 {
			return state
		}
	}
	return ""
}
```

- [ ] **Step 4: Append the state-order test**

`claudeStateOrder` now exists, so add the lock-in test for it. Append to
`picker/main_test.go`:

```go
func TestClaudeStateOrderMatchesPriority(t *testing.T) {
	// claudeStateOrder is what groupWindowsByState (Task 2) walks to decide
	// header order; it must name every state claudePriority can return, in
	// the same order, or the two would silently diverge.
	if len(claudeStateOrder) != 7 {
		t.Fatalf("claudeStateOrder has %d entries, want 7 (error/waiting/denied/compacting/processing/done/idle)",
			len(claudeStateOrder))
	}
	want := []string{"error", "waiting", "denied", "compacting", "processing", "done", "idle"}
	for i, s := range want {
		if claudeStateOrder[i] != s {
			t.Errorf("claudeStateOrder[%d] = %q, want %q", i, claudeStateOrder[i], s)
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd picker && go test ./... -run 'TestClaudePriority|TestClaudeStateOrder' -v`
Expected: both PASS (behavior-preserving refactor — Step 2's green stays
green; the new state-order test is green from the moment it's added).

- [ ] **Step 6: Commit**

```bash
git add picker/main.go picker/main_test.go
git commit -m "refactor(picker): derive claudePriority from a named state order"
```

---

## Task 2: Pure grouping functions

**Files:**
- Modify: `picker/tui.go` (insert after `identityCapFor`, ~line 1607, before
  the `renderWindowItems` doc comment)
- Modify: `picker/tui_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `picker/tui_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd picker && go test ./... -run TestGroupWindows -v`
Expected: FAIL with `undefined: groupWindowsBySession` (or `groupWindowsByState`,
or `windowGroup`) — the types/functions don't exist yet.

- [ ] **Step 3: Implement the grouping functions**

In `picker/tui.go`, insert after `identityCapFor`'s closing brace (before the
`// renderWindowItems is the pure rendering half...` comment):

```go
// windowGroup is one header's worth of window rows, in render order. key is
// either an owning session name (session-grouped mode) or a claude priority
// state ("" = no agent, state-grouped mode).
type windowGroup struct {
	key     string
	windows []*windowData
}

// groupWindowsBySession buckets windows under their owning session, ordered
// by session activity (busiest first) then name — window mode's default
// grouping, unchanged from before #229.
func groupWindowsBySession(windows []windowData, sessActivity map[string]int64) []windowGroup {
	order := []string{}
	byName := map[string]*windowGroup{}
	for i := range windows {
		w := &windows[i]
		g, ok := byName[w.session]
		if !ok {
			g = &windowGroup{key: w.session}
			byName[w.session] = g
			order = append(order, w.session)
		}
		g.windows = append(g.windows, w)
	}
	groups := make([]windowGroup, len(order))
	for i, name := range order {
		groups[i] = *byName[name]
	}
	sort.Slice(groups, func(i, j int) bool {
		ai, aj := sessActivity[groups[i].key], sessActivity[groups[j].key]
		if ai != aj {
			return ai > aj
		}
		return groups[i].key < groups[j].key
	})
	for i := range groups {
		sort.Slice(groups[i].windows, func(a, b int) bool {
			return groups[i].windows[a].index < groups[i].windows[b].index
		})
	}
	return groups
}

// groupWindowsByState buckets windows by claude_priority_state, in
// claudeStateOrder — the same priority the status bar and pane pollers use
// (#229). Windows with no claude state at all collapse into one trailing
// group (key ""), never one group per stateless window. Within a group,
// windows keep session-activity order so the busiest session's windows still
// lead — the same tiebreak groupWindowsBySession uses.
func groupWindowsByState(windows []windowData, sessActivity map[string]int64) []windowGroup {
	byKey := map[string]*windowGroup{}
	for i := range windows {
		w := &windows[i]
		key := claudePriority(w.claude)
		g, ok := byKey[key]
		if !ok {
			g = &windowGroup{key: key}
			byKey[key] = g
		}
		g.windows = append(g.windows, w)
	}
	order := append(append([]string{}, claudeStateOrder...), "")
	groups := make([]windowGroup, 0, len(order))
	for _, key := range order {
		if g, ok := byKey[key]; ok {
			groups = append(groups, *g)
		}
	}
	for i := range groups {
		gw := groups[i].windows
		sort.Slice(gw, func(a, b int) bool {
			wa, wb := gw[a], gw[b]
			if sessActivity[wa.session] != sessActivity[wb.session] {
				return sessActivity[wa.session] > sessActivity[wb.session]
			}
			if wa.session != wb.session {
				return wa.session < wb.session
			}
			return wa.index < wb.index
		})
	}
	return groups
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd picker && go test ./... -run TestGroupWindows -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add picker/tui.go picker/tui_test.go
git commit -m "feat(picker): add pure window-grouping functions for #229"
```

---

## Task 3: Wire grouping into `renderWindowItems`

This is the core rewire: Pass A (per-window rendering) becomes grouping-
agnostic, Pass B walks `windowGroup`s from Task 2, and state-grouped rows
fold their session name into the identity column.

**Files:**
- Modify: `picker/tui.go:22-40` (`listItem` struct), `:1579-1884`
  (`buildWindowItems`/`renderWindowItems`), `:210`, `:1264` (call sites)
- Modify: `picker/tui_test.go:257-388` (existing `renderWindowItems` calls)
- Modify: `picker/enrich_test.go:43` (`TestRenderWindowItemsEnriched`'s
  `renderWindowItems` call)

- [ ] **Step 1: Add `groupKey` to `listItem` and update existing call sites**

In `picker/tui.go`, add a field to the `listItem` struct (after `session`):

```go
	session         string // owning session name (for kill)
	groupKey        string // window-mode header key this row re-attaches to
	                       // when filtering: session name, or claude state
```

Change the `buildWindowItems`/`renderWindowItems` signatures to accept
`stateGrouped bool`:

```go
func buildWindowItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int, stateGrouped bool) []listItem {
	return renderWindowItems(collectWindows(), tmuxOpts, claudePanes, theme, width, stateGrouped)
}
```

```go
func renderWindowItems(windows []windowData, tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int, stateGrouped bool) []listItem {
```

(Body left as-is for this step — Step 3 rewrites it. This step is purely a
signature change so the compiler tells you every call site that needs
updating.)

Update the two production call sites:

`picker/tui.go:210` (`runTUI`):
```go
			items = buildWindowItems(opts, panes, theme, 0, false)
```
(Every popup launch starts session-grouped — Design Decision 2.)

`picker/tui.go:1264` (`refreshDataCmd`):
```go
func (m tuiModel) refreshDataCmd() tea.Cmd {
	wm := m.windowMode
	sg := m.stateGrouped
	opts := m.tmuxOpts
	theme := m.theme
	lw := m.listWidth()
	return func() tea.Msg {
		panes := collectClaudePanes()
		var items []listItem
		if wm {
			items = buildWindowItems(opts, panes, theme, lw, sg)
		} else {
			items = buildSessionItems(opts, panes, theme, true)
		}
		return refreshMsg{items: items}
	}
}
```

Add `stateGrouped bool` to `tuiModel`'s Modes group (this field is read by
`refreshDataCmd` above and written in Task 4 — declaring it now keeps this
step compiling on its own):

```go
	mode pickerMode
	wallLaunched bool
	windowMode   bool
	stateGrouped bool // window mode: group by claude state instead of session (#229)
	claudeOnly   bool
	scratchOnly  bool
```

Append `, false` to every existing `renderWindowItems(...)` call in the
package — the six in `picker/tui_test.go` (lines 262, 290, 331, 352, 375,
544) **and** the one in `picker/enrich_test.go:43`
(`TestRenderWindowItemsEnriched`) — e.g.:

```go
	items := renderWindowItems(windows, map[string]string{}, nil, "dark", 0, false)
```

Grep to confirm none are missed before moving on:
`grep -rn 'renderWindowItems(' picker/*_test.go` — every match must now pass
six args.

- [ ] **Step 2: Build to confirm the signature change compiles**

Run: `cd picker && go build ./... && go vet ./...`
Expected: clean build — this step only changed signatures and call sites,
no behavior yet.

- [ ] **Step 3: Write the failing tests for state-grouped rendering**

Add to `picker/tui_test.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd picker && go test ./... -run TestRenderWindowItemsStateGrouped -v`
Expected: FAIL — session mode still hardcodes session grouping and never
folds anything; `TestRenderWindowItemsSessionGroupedUnaffected` should
already PASS (nothing changed yet for that path).

- [ ] **Step 5: Rewrite `renderWindowItems`'s body**

Replace `picker/tui.go`'s `renderWindowItems` body (the whole function,
`:1612-1884` in the pre-change file) with:

```go
func renderWindowItems(windows []windowData, tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int, stateGrouped bool) []listItem {
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

	sessActivity := collectSessionActivity()
	var groups []windowGroup
	if stateGrouped {
		groups = groupWindowsByState(windows, sessActivity)
	} else {
		groups = groupWindowsBySession(windows, sessActivity)
	}

	// Pass A builds every fixed-width piece and the raw identity parts, and
	// tracks the column maxima (lead prefix, icons, PR). The identity cap is
	// then derived from the terminal width, and pass B truncates + aligns.
	// Keyed by "session:index" (not by group) so pass B can look a window's
	// pieces up the same way regardless of which grouping mode built groups.
	type rawIdentity struct {
		kind      int
		id, rest  string
		text      string
		leadGlyph string
	}
	type renderedWin struct {
		win         *windowData
		name        string
		icons       string
		iconDW      int
		leadPlain   string
		leadColored string
		leadDW      int
		ident       rawIdentity
		identSearch string
		prBadge     string
		prPlain     string
		crewName    string
	}
	winRows := make(map[string]renderedWin, len(windows))
	maxLeadDW, maxIconDW, maxPrDW, maxZoomDW := 0, 0, 0, 0
	for i := range windows {
		w := &windows[i]
		icons, dw := buildProcIcons(w.procs, maxIconsPicker)
		icons, dw = appendClaudeIcon(icons, dw, w.claude, theme, dim, reset)
		icons, dw = appendIssueIDs(icons, dw, w.claude.issues, cDim, reset)

		name := truncateCells(w.name, 40)

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
		} else if w.bridgeName != "" {
			ri = rawIdentity{kind: 0, text: truncateCells(w.bridgeName, 40)}
			idSearch = w.bridgeName
		} else {
			ri = rawIdentity{kind: 0, text: name}
			idSearch = name
		}

		prBadge := colorPRBadge(w.prPlain, w.prState, w.prCheck, w.prMergeable, prCols)
		prPlain := strings.TrimSpace(w.prPlain)
		prDW := iconCellWidth(prPlain)

		winRows[fmt.Sprintf("%s:%d", w.session, w.index)] = renderedWin{
			win: w, name: name, icons: icons, iconDW: dw,
			leadPlain: leadPlain, leadColored: leadColored, leadDW: leadDW,
			ident: ri, identSearch: idSearch,
			prBadge: prBadge, prPlain: prPlain, crewName: w.crewName,
		}
		maxLeadDW = max(maxLeadDW, leadDW)
		maxIconDW = max(maxIconDW, dw)
		maxPrDW = max(maxPrDW, prDW)
		if w.zoomed {
			maxZoomDW = max(maxZoomDW, iconCellWidth(" 󰁌"))
		}
	}
	iconCol := max(maxIconDW+1, 3)
	identityCap := identityCapFor(width, maxLeadDW+maxZoomDW, iconCol, maxPrDW)
	labelCol := maxLeadDW + identityCap + maxZoomDW

	// truncID renders a rawIdentity to (colored, plain) within budget cells.
	truncID := func(ri rawIdentity, budget int) (string, string) {
		switch ri.kind {
		case 1: // issue: id accent + dim title
			id := ri.id
			idW := iconCellWidth(id)
			rest := ri.rest
			if idW >= budget {
				id = truncateCells(id, budget)
				rest = ""
				idW = iconCellWidth(id)
			} else if idW+iconCellWidth(rest) > budget {
				rest = truncateCells(rest, budget-idW)
			}
			plain := id + rest
			colored := cMauve + id + reset
			if rest != "" {
				colored += cDim + rest + reset
			}
			return colored, plain
		case 2: // branch: faint, optional glyph
			br := truncateCells(ri.text, max(budget-iconCellWidth(ri.leadGlyph), 1))
			plain := ri.leadGlyph + br
			return cFaint + plain + reset, plain
		default: // name: plain
			nm := truncateCells(ri.text, budget)
			return nm, nm
		}
	}

	var items []listItem
	for _, g := range groups {
		var headerDisplay, headerPlain, headerSession string
		var headerHasClaude bool
		if stateGrouped {
			headerDisplay, headerPlain = stateGroupHeader(g.key, theme, cFaint)
			headerHasClaude = isActiveState(g.key)
		} else {
			sessHasClaude := false
			for _, w := range g.windows {
				key := fmt.Sprintf("%s:%d", w.session, w.index)
				if cc, ok := claudeByWin[key]; ok && isActiveState(claudePriority(*cc)) {
					sessHasClaude = true
					break
				}
			}
			headerDisplay = fmt.Sprintf("%s %s", cMauve+iSess+reset, cMauve+g.key+reset)
			headerPlain = fmt.Sprintf("%s %s", iSess, g.key)
			headerSession = g.key
			headerHasClaude = sessHasClaude
		}
		items = append(items, listItem{
			target:          g.key,
			display:         headerDisplay,
			plain:           headerPlain,
			searchText:      g.key,
			isHeader:        true,
			session:         headerSession,
			groupKey:        g.key,
			hasActiveClaude: headerHasClaude,
		})

		multiWin := len(g.windows) > 1
		for wi, w := range g.windows {
			r := winRows[fmt.Sprintf("%s:%d", w.session, w.index)]

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
			if wi == len(g.windows)-1 {
				tree = "╰─"
			}

			identCap := identityCap
			prefixColored, prefixPlain := "", ""
			if stateGrouped {
				prefixColored, prefixPlain, identCap = foldSessionPrefix(w.session, cDim, reset, identityCap)
			}
			idColored, idPlain := truncID(r.ident, identCap)
			idColored = prefixColored + idColored
			idPlain = prefixPlain + idPlain

			zoom := ""
			if w.zoomed {
				zoom = " 󰁌"
			}
			lead := padToWidth(r.leadColored, r.leadDW, maxLeadDW)
			leadPlainPadded := padToWidth(r.leadPlain, r.leadDW, maxLeadDW)
			labelColored := lead + idColored + zoom
			labelPlain := leadPlainPadded + idPlain + zoom
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

			search := w.session + " " + r.name
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
				target:          fmt.Sprintf("%s:%d", w.session, w.index),
				display:         display,
				plain:           plain,
				searchText:      search,
				session:         w.session,
				groupKey:        g.key,
				hasActiveClaude: isActiveState(claudePriority(w.claude)),
			})
		}
	}
	return items
}

// sessionFoldFrac caps how much of the identity budget a state-grouped row's
// session-name prefix may take: at most identityCap/sessionFoldFrac, so the
// row's own identity (branch/issue/name) keeps the majority of the column.
const sessionFoldFrac = 3

// foldSessionPrefix renders the owning session as a dim prefix for
// state-grouped rows (#229) — they have no session header to carry it. It
// reserves at most identityCap/sessionFoldFrac cells for the session name,
// carved OUT of identityCap (never added to it), and returns the cap left
// over for the row's own identity.
func foldSessionPrefix(session, cDim, reset string, identityCap int) (prefix, plain string, remainingCap int) {
	const sep = " / "
	sessCap := max(identityCap/sessionFoldFrac, 1)
	name := truncateCells(session, sessCap)
	plain = name + sep
	prefix = cDim + name + reset + sep
	remainingCap = identityCap - iconCellWidth(plain)
	if remainingCap < 1 {
		remainingCap = 1
	}
	return prefix, plain, remainingCap
}

// stateGroupHeader renders a state-grouped mode header for a claude priority
// state key ("" = the trailing no-agent group), using the same icon/color a
// window row in that state gets (claudeStateIcon, claudeColors) so the
// header reads as the same state at a glance. cNoAgent colors the trailing
// group, which has no entry in claudeColors.
func stateGroupHeader(key, theme, cNoAgent string) (display, plain string) {
	label := claudeStateLabel[key]
	icon := claudeStateIcon(key)
	color := cNoAgent
	if hex, ok := claudeColors[theme][key]; ok {
		color = ansiFg(hex)
	}
	reset := "\033[0m"
	if icon == "" {
		return color + label + reset, label
	}
	return color + icon + " " + label + reset, icon + " " + label
}
```

- [ ] **Step 6: Run the full picker test suite**

Run: `cd picker && go test ./... -v 2>&1 | tail -80`
Expected: all tests pass, including every pre-existing `TestRenderWindowItems*`
test (unchanged output for `stateGrouped=false`) and the four new tests from
Step 3.

- [ ] **Step 7: Commit**

```bash
git add picker/tui.go picker/tui_test.go picker/enrich_test.go
git commit -m "feat(picker): state-grouped rendering for window mode (#229)"
```

---

## Task 4: `ctrl+g` toggle + hint line

**Files:**
- Modify: `picker/tui.go:406-511` (`handleKey`)
- Modify: `picker/render_list.go:70-121` (`renderHints`)
- Modify: `picker/tui_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `picker/tui_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd picker && go test ./... -run TestToggleStateGrouped -v`
Expected: FAIL — `ctrl+g` currently falls through to `handleKey`'s default
case as a printable-key search character, so `stateGrouped` never flips.

- [ ] **Step 3: Add the `ctrl+g` case**

In `picker/tui.go`'s `handleKey`, add a case above `case "backspace":`
(`:494`):

```go
	case "ctrl+g":
		if !m.windowMode {
			return m, nil
		}
		m.stateGrouped = !m.stateGrouped
		m.cursor = m.firstSelectable(0)
		return m, m.refreshDataCmd()

```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd picker && go test ./... -run TestToggleStateGrouped -v`
Expected: PASS

- [ ] **Step 5: Add the hint line entry**

In `picker/render_list.go`'s `renderHints`, replace the `parts` slice
literal:

```go
	parts := []string{
		hint("^jk/↑↓", "nav"),
		hint("enter", "open"),
		hint("^x", killLabel),
		hint("^a", claudeLabel),
		hint("^s", scratchLabel),
		hint("^/", toggleLabel),
		hint("M-hjkl", "scroll"),
		hint("q", "quit"),
	}
```

with:

```go
	parts := []string{
		hint("^jk/↑↓", "nav"),
		hint("enter", "open"),
		hint("^x", killLabel),
		hint("^a", claudeLabel),
		hint("^s", scratchLabel),
	}
	if m.windowMode {
		groupLabel := "group"
		if m.stateGrouped {
			groupLabel = highlight.Render(groupLabel)
		}
		parts = append(parts, hint("^g", groupLabel))
	}
	parts = append(parts,
		hint("^/", toggleLabel),
		hint("M-hjkl", "scroll"),
		hint("q", "quit"),
	)
```

- [ ] **Step 6: Manually confirm the hint renders**

Run: `cd picker && go build ./... && go vet ./...`
Expected: clean build. (Visual confirmation of the hint text happens in
Task 6's manual verification pass.)

- [ ] **Step 7: Commit**

```bash
git add picker/tui.go picker/render_list.go picker/tui_test.go
git commit -m "feat(picker): ctrl+g toggles state-grouped window mode (#229)"
```

---

## Task 5: Filter composition (`withFilter` regroup on `groupKey`)

**Files:**
- Modify: `picker/tui.go:1093-1112` (`withFilter`)
- Modify: `picker/tui_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/tui_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd picker && go test ./... -run TestWithFilterStateGroupedKeepsGrouping -v`
Expected: FAIL — `withFilter`'s window-mode branch keys `headerMap` and
`seen` on `.session`, which is `""` on both header rows in this fixture
(they're state headers, not session headers), so no header gets pulled in
for either match.

- [ ] **Step 3: Rekey the regroup on `groupKey`**

In `picker/tui.go`'s `withFilter`, replace:

```go
	if m.windowMode {
		// Re-group under session headers, ordered by best child score
		headerMap := make(map[string]listItem)
		for _, item := range m.allItems {
			if item.isHeader {
				headerMap[item.session] = item
			}
		}
		seen := make(map[string]bool)
		var out []listItem
		for _, match := range matches {
			if !seen[match.item.session] {
				seen[match.item.session] = true
				if h, ok := headerMap[match.item.session]; ok {
					out = append(out, h)
				}
			}
			out = append(out, match.item)
		}
		m.visible = out
```

with:

```go
	if m.windowMode {
		// Re-group under headers, ordered by best child score. groupKey is
		// the session name (session-grouped) or claude state (state-grouped,
		// #229) — whichever the current render built headers on.
		headerMap := make(map[string]listItem)
		for _, item := range m.allItems {
			if item.isHeader {
				headerMap[item.groupKey] = item
			}
		}
		seen := make(map[string]bool)
		var out []listItem
		for _, match := range matches {
			if !seen[match.item.groupKey] {
				seen[match.item.groupKey] = true
				if h, ok := headerMap[match.item.groupKey]; ok {
					out = append(out, h)
				}
			}
			out = append(out, match.item)
		}
		m.visible = out
```

- [ ] **Step 4: Run the full picker test suite**

Run: `cd picker && go test ./... -v 2>&1 | tail -100`
Expected: all tests pass, including `TestWithFilterStateGroupedKeepsGrouping`
and every pre-existing `TestWithFilter*` test (session-grouped search is
`groupKey == session` today, so this is a rename, not a behavior change,
for that path).

- [ ] **Step 5: Commit**

```bash
git add picker/tui.go picker/tui_test.go
git commit -m "fix(picker): compose search filter with state-grouped mode (#229)"
```

---

## Task 6: Docs, full gate, manual check

**Files:**
- Modify: `CLAUDE.md:48`
- Create: `docs/superpowers/plans/2026-08-08-picker-state-grouped-mode.md`
  (this plan — repo convention is to commit the plan alongside the code it
  accompanies; skipping it is the single most common review finding on this
  repo's recent picker PRs)

- [ ] **Step 1: Commit the plan document**

```bash
git add docs/superpowers/plans/2026-08-08-picker-state-grouped-mode.md
git commit -m "docs(plan): picker state-grouped mode (#229)"
```

Verify it's tracked before moving on: `git ls-files docs/superpowers/plans/2026-08-08-picker-state-grouped-mode.md`
must print the path (empty output means the `git add` was run from the wrong
cwd or the path is wrong).

- [ ] **Step 2: Update the `tmux-window-picker` row**

In `CLAUDE.md`, replace:

```
| `tmux-window-picker` | `prefix + w` | Same TUI in window mode (`--tui --windows`), grouped by session. Window rows show the enrich identity (`@window_label_id`/`@window_label_rest_long`) and a PR badge (`@window_pr_plain`, tinted by `@pr_check_state`/`@pr_mergeable`); aligned columns, searchable by issue id / PR number. |
```

with:

```
| `tmux-window-picker` | `prefix + w` | Same TUI in window mode (`--tui --windows`), grouped by session (default) or, via `ctrl+g`, by claude priority state — same order as `claude_priority_state` (error > waiting > denied > compacting > processing > done > idle), stateless windows in one trailing group, session name folded into the identity column. Resets to session grouping on every popup launch. Window rows show the enrich identity (`@window_label_id`/`@window_label_rest_long`) and a PR badge (`@window_pr_plain`, tinted by `@pr_check_state`/`@pr_mergeable`); aligned columns, searchable by issue id / PR number. |
```

- [ ] **Step 3: Commit the doc update**

```bash
git add CLAUDE.md
git commit -m "docs(picker): document state-grouped window mode (#229)"
```

- [ ] **Step 4: Run the full gate**

Run each in order, from the repo root:

```bash
nix build .#default
nix flake check
nix build .#lint
```

Expected: all three succeed. If `nix build .#lint` flags anything in the new
code (e.g. a shadowed `cap`/unused var), fix it and re-run — do not skip.

- [ ] **Step 5: Manual verification**

After `nix build .#default` succeeds:

1. Reload tmux config (`prefix + r`) or attach a session using the built
   `./result/bin/tmux`.
2. Open a few windows across a couple of sessions with different claude
   states (or fake it: any window with an active Claude Code pane will
   show a state; windows with no agent are the control group).
3. `prefix + w` — confirm it opens session-grouped, as before.
4. Press `ctrl+g` — confirm rows regroup by state, most-urgent first, with
   the session name visible in each row's identity, and a trailing "No
   agent" group (if any windows lack claude state).
5. Type a few characters to filter — confirm rows stay attached to their
   own state header while state-grouped.
6. Press `ctrl+g` again — confirm it reverts to session grouping.
7. Close the popup and reopen with `prefix + w` — confirm it opens
   session-grouped again (toggle does not persist, Design Decision 2).

Report honestly which of these steps were actually run, per the task's
testing instructions — do not claim a step happened if it didn't.

- [ ] **Step 6: Push and open the PR**

```bash
git push -u origin HEAD
gh pr create --assignee @me --title "feat(picker): state-grouped mode for prefix + w" --body "$(cat <<'EOF'
## Summary
- Adds a `ctrl+g` toggle to window mode (`prefix + w`) that regroups rows by
  `claude_priority_state` (error > waiting > denied > compacting >
  processing > done > idle, stateless windows in one trailing "No agent"
  group) instead of by session.
- Session-grouped mode (the default) is unchanged; grouping is a Pass B
  concern over the same Pass A row data, so it composes with the existing
  search filter for free.
- Session name folds into each row's identity column when state-grouped,
  capped at identityCap/3 so the row's own identity keeps the majority of
  the (already width-adaptive) budget.
- `claudePriority`'s state order is now named (`claudeStateOrder`) instead
  of implicit in an if-chain, so the new grouping and the existing
  per-window state share one source of truth.

## Design decisions
- **Toggle key is `ctrl+g`, not `s`** — plain `s` types into the search
  query in window mode; `ctrl+s` is already the scratch-only filter.
- **Does not persist across popup invocations** — matches `claudeOnly`/
  `scratchOnly`, both of which also reset every `prefix + w` launch.

Closes #229

## Test plan
- [x] `go test ./picker/...` — grouping order, trailing no-agent group,
      session-grouped path unchanged, toggle behavior, filter composition
- [ ] `nix build .#default`
- [ ] `nix flake check`
- [ ] `nix build .#lint`
- [ ] Manual: `prefix + w`, `ctrl+g` toggle, search while state-grouped,
      toggle back, reopen popup and confirm it resets to session-grouped
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** toggle key chosen and justified (Design Decision 1,
  Task 4) · reuses the existing Go priority function rather than a second
  list (Task 1) · stateless windows collapse to one trailing group (Task 2
  test, Task 3 test) · composes with search filter (Task 5) · composes with
  responsive layout — `identityCap` is unchanged by grouping mode, folding
  is carved out of it (Task 3, `TestRenderWindowItemsStateGroupedRespectsColumnBudget`)
  · toggle persistence decided and justified (Design Decision 2) · session
  name folding approach decided and column-budget-tested (Design Decision 3,
  Task 3) · unit tests for the grouping function, the toggle's effect on the
  model, and filter composition (Tasks 2, 4, 5) · plan committed to
  `docs/superpowers/plans/` (this file) · scope boundary respected — no
  changes to `picker/remote.go`, `picker/agentdetect/**`, or `scripts/**`.
- **Type consistency:** `windowGroup{key, windows []*windowData}` (Task 2) is
  the type both `groupWindowsBySession` and `groupWindowsByState` return and
  `renderWindowItems` consumes (Task 3) — no divergent shape introduced later.
  `listItem.groupKey` (Task 3) is the field both the header-construction code
  and `withFilter` (Task 5) read/write — same name throughout.
