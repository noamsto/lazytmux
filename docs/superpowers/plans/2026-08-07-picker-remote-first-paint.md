# Picker Remote Rows In First Paint — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The session picker's Remote section (header + one row per
`@remote_bridge_hosts` host) renders in the **first paint**, so the ssh probe
finishing ~2.2s later only updates row *content* in place — it never inserts a
new section into an already-rendered list. Closes #312.

**Architecture:** `@remote_bridge_hosts` is static tmux config, readable
without any ssh. Split the row-building that today lives entirely in
`collectRemoteItems` (async, probe-backed) into a synchronous half —
`pendingRemoteItems`, called at `runTUI` construction time, before
`tea.NewProgram` even starts — and the existing async half, whose probed
result now *replaces* `m.remoteItems` in place rather than *introducing* it.
Both halves share row-rendering helpers so a pending row and its resolved
replacement are pixel-identical except for the trailing annotation. A new
`restoreCursor` helper re-finds the selected row by `target` after the
replace, so a host that turns out to have unbridged sessions (rows that
genuinely cannot exist before the probe runs) can't shift the cursor out from
under the user either.

**Tech Stack:** Go 1.25 (`github.com/noamsto/lazytmux/picker`), bubbletea v2,
`nix flake check` / `go test`.

## Design Decisions

The issue (#312) leaves three questions open; answers below, to be encoded in
the tasks rather than re-litigated mid-implementation.

1. **Pending-state rendering:** a dim ellipsis (`"…"`), appended the same way
   today's resolved annotations are (`"  " + cDim + note + reset`). No
   spinner — a spinner needs a periodic re-render to animate, which is more
   moving parts for a state that (per the issue's own measurement) resolves
   in ~2.2s on the slow path and near-instantly on the common one. A static
   dim glyph reads as "pending" without extra machinery or flicker risk.
2. **Zero-session hosts keep their row:** unchanged from today's resolved
   behavior (`picker/remote.go`'s existing comment: "the host row is always
   selectable"). The pending row is built the same way regardless of what
   the probe will find, so this falls out for free — there's no separate
   case to special-case.
3. **`ConnectTimeout=2` stays as-is.** The row no longer gates the section's
   *existence*, but it still gates how long a host's row displays a
   placeholder instead of its real status, which is still user-visible.
   Raising it buys nothing this task needs and isn't required by the issue.
   Left as a follow-up if a specific host later needs more grace.
4. **Bounded row growth is accepted, not eliminated.** A host with
   unbridged sessions still gains tree-child rows once the probe returns —
   those rows cannot exist before an ssh round trip tells us the session
   names. This is fundamentally different from the bug being fixed: it's
   *localized* growth appended directly after that host's own row, not a
   whole new section landing under the list. `restoreCursor` (Task 4) makes
   this growth cursor-safe regardless.

## Open Question — Needs Sign-off Before Implementation

Design Decision 4 above **narrows** a literal acceptance criterion from
`WORKER_TASK.md`:

> Row **count** does not change when `remoteMsg` arrives — only row content.

That statement is physically impossible to honor for a host that turns out to
have unbridged sessions: the tree-child rows for those sessions cannot be
known before the ssh round trip resolves, so the row count for that host
necessarily grows on the first probe. `TestRemoteItemsRowCountStableAcrossProbe`
(Task 2) only covers the always-stable case (a host with no new sessions to
reveal); no test asserts anything about the growing case's row count, and none
should — asserting stability there would assert something false.

**This needs the issue-owner's sign-off before implementation, not just a
footnote discovered after the fact.** Proposed reading, to confirm before
Task 1 starts:

> Header + per-host row count is stable across `remoteMsg`. A host's own row
> may still grow tree-child rows once its sessions are known — that growth is
> localized to the host's own position in the list (never a whole new section
> landing elsewhere) and is made cursor-safe by `restoreCursor` (Task 4).

If the issue owner instead wants the literal reading enforced (e.g. by
pre-declaring placeholder child rows before the probe, or deferring cursor
restoration until all hosts resolve), that's a different, larger design and
should be decided now rather than mid-implementation.

## Global Constraints

- Do not change `remoteProbeTimeout` (3s) or `ConnectTimeout=2` — see Design
  Decision 3.
- Do not touch `picker/render_wall.go` or `remotebridge/` (out of scope,
  issue #316 covers the wall).
- `collectRemoteItems`'s existing signature and resolved-row output must
  stay byte-for-byte identical to today for every case `picker/remote_test.go`
  already covers — those tests must pass **unmodified**.
- New tests use the existing injected-`probe` seam (`func(string) ([]string,
  error)`) — no real tmux, no real ssh, matching the style already used by
  `TestCollectRemoteItems` and friends.
- Measure timing only in a real tmux window polling `capture-pane` by
  `window_id`; never through `script`/a bare pty — that inflates first paint
  to a fake ~17s (bubbletea blocks on unanswered terminal capability
  queries there).
- `nix flake check` green before calling this done.

---

## File Structure

| File | Change |
|------|--------|
| `picker/remote.go` | Extract `remoteHeaderItem` / `remoteHostRowItem` helpers from `collectRemoteItems`; add `pendingRemoteItems` (sync, no ssh) built from the same helpers. |
| `picker/remote_test.go` | New tests: `pendingRemoteItems` row shape, row-count parity between the pending and a no-new-sessions resolved result. |
| `picker/tui.go` | Extract the pure model-assembly half of `runTUI` into `newPickerModel` (seeds `m.remoteItems` from `pendingRemoteItems` before first render, among the rest of today's assembly); `runTUI` is reduced to gathering the impure inputs and calling it. `Update`'s `remoteMsg` case preserves the cursor's target across the replace via a new `restoreCursor` helper. |
| `picker/tui_test.go` | New tests: `newPickerModel`'s first paint already contains remote header + host rows (calls the real wiring, not a hand-assembled model); `remoteMsg` doesn't move the cursor off its row; an in-progress `query` survives a `remoteMsg`. |

---

## Task 0: Confirm the acceptance-criterion reading (blocking)

**Files:** none — coordination only.

Design Decision 4 and the "Open Question — Needs Sign-off Before
Implementation" section above narrow a literal `WORKER_TASK.md` criterion.
This task exists so that narrowing is a structural gate an executor cannot
check past, not a paragraph of prose sitting outside the checkbox list that a
checkbox-driven run (subagent-driven-development / executing-plans) has no
reason to stop and read.

- [x] **Step 1: Get the issue owner's sign-off on the proposed reading**

Share the proposed reading from "Open Question — Needs Sign-off Before
Implementation" (above) with the issue owner:

> Header + per-host row count is stable across `remoteMsg`. A host's own row
> may still grow tree-child rows once its sessions are known — that growth is
> localized to the host's own position in the list (never a whole new section
> landing elsewhere) and is made cursor-safe by `restoreCursor` (Task 4).

Expected: explicit confirmation of this reading, or a decision to pursue the
literal reading instead (pre-declared placeholder child rows, or deferred
cursor restoration) — which would change Task 4's design and must be
resolved before writing any test that depends on the answer.

- [x] **Step 2: Record the outcome here before proceeding to Task 1**

> **Sign-off:** APPROVED by dispatcher (2026-08-07). Authoritative criterion,
> replacing the WORKER_TASK.md wording:
>
> 1. The Remote section header and its one-row-per-configured-host are
>    present in the first paint, and their count is invariant across
>    `remoteMsg`.
> 2. The section's position is fixed at first paint — it never appears,
>    moves, or reorders relative to other sections when the probe lands.
> 3. A host's own row may gain tree-child rows once its sessions are known,
>    localized under that host.
>
> Three hard constraints on Task 4, non-negotiable:
>
> - (a) The cursor must follow its **target**, not its index (this repo has
>   regressed index-keyed selection three times — #173/#198/#234, most
>   recently in the wall). If child rows insert *above* the cursor, an
>   index-preserving restore silently selects a different row.
> - (b) An in-progress filter query must survive, and late-arriving child
>   rows are subject to that active filter, not exempt from it.
> - (c) Remote rows stay exempt from the claude/scratch toggles in both the
>   pending and resolved state.
>
> Task 4 must add a unit test per constraint, specifically covering the
> cursor-below-a-growing-host case (the one that breaks under index-based
> restore). This is a decided spec change — state it in the PR body, not
> under "Assumptions".

---

## Task 1: Extract shared remote-row builders (no behavior change)

**Prerequisite:** Task 0 Step 2 signed off.

**Files:**
- Modify: `picker/remote.go:182-272` (`collectRemoteItems`)
- Test: `picker/remote_test.go` (existing tests only — this task adds none)

**Interfaces:**
- Produces: `remoteHeaderItem(tmuxOpts map[string]string) listItem`, `remoteHostRowItem(tmuxOpts map[string]string, host, note string) listItem` — both used by Task 2's `pendingRemoteItems` and by `collectRemoteItems` itself.

This is a pure refactor: `collectRemoteItems`'s output must not change for any
existing test. Establish the "before" baseline, apply the extraction, confirm
the "after" is identical.

- [ ] **Step 1: Run the existing remote tests to confirm a green baseline**

Run: `cd picker && go test ./... -run Remote -v`
Expected: PASS (all `TestCollectRemoteItems*`, `TestRemoteSessionsForHost`, etc.)

- [ ] **Step 2: Extract `remoteHeaderItem` and `remoteHostRowItem`, rewrite `collectRemoteItems` to use them**

Replace `picker/remote.go:182-272` with:

```go
// remoteHeaderItem is the "── Remote ──" divider row. Shared by the
// synchronous pending render (Task 2) and the probed result so both agree on
// layout — a pending header must look identical to a resolved one.
func remoteHeaderItem(tmuxOpts map[string]string) listItem {
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	reset := "\033[0m"
	rule := "── Remote " + strings.Repeat("─", 220)
	return listItem{
		display:        cDim + rule + reset,
		plain:          rule,
		isHeader:       true,
		isRemoteHeader: true,
	}
}

// remoteHostRowItem renders one host's row with the given trailing note —
// either a resolved annotation ("(no server — Enter starts one)", …) or
// remotePendingNote before the probe has run. The host row is always
// selectable: it opens the remote's most-recent session and keeps the
// section alive once every session is bridged.
func remoteHostRowItem(tmuxOpts map[string]string, host, note string) listItem {
	cPeach := ansiFg(envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387"))
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)
	reset := "\033[0m"

	display := fmt.Sprintf("%s %s", cPeach+iSess+reset, host)
	plain := fmt.Sprintf("%s %s", iSess, host)
	if note != "" {
		display += "  " + cDim + note + reset
		plain += "  " + note
	}
	return listItem{
		isRemoteRow: true,
		target:      "remote:" + host,
		remoteHost:  host,
		display:     display,
		plain:       plain,
		searchText:  host,
	}
}

// collectRemoteItems builds the "Remote" suggestion rows (header + hosts /
// sessions) by probing every configured host over ssh. Runs off the
// first-paint path; the result merges via remoteMsg, replacing
// pendingRemoteItems's synchronous placeholder (#312).
func collectRemoteItems(tmuxOpts map[string]string, localSessionNames map[string]bool, probe func(string) ([]string, error)) []listItem {
	hosts := parseRemoteHosts(envOrMap("REMOTE_BRIDGE_HOSTS", tmuxOpts, "@remote_bridge_hosts", ""))
	if len(hosts) == 0 {
		return nil
	}
	if probe == nil {
		probe = sshListRemoteSessions
	}
	if localSessionNames == nil {
		localSessionNames = map[string]bool{}
	}

	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	reset := "\033[0m"

	type hostResult struct {
		host  string
		sess  []string
		state remoteProbeState
	}
	results := make([]hostResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			sess, state := remoteSessionsForHost(h, localSessionNames, probe)
			results[i] = hostResult{host: h, sess: sess, state: state}
		}(i, h)
	}
	wg.Wait()

	items := make([]listItem, 0, len(hosts)+1)
	items = append(items, remoteHeaderItem(tmuxOpts))

	for _, r := range results {
		note := ""
		switch r.state {
		case remoteProbeUnreachable:
			// The host may be back up by the time it is picked.
			note = "(unreachable — open default)"
		case remoteProbeNoServer:
			// The launcher cold-starts the host's own startup session (#287).
			note = "(no server — Enter starts one)"
		default:
			if len(r.sess) == 0 {
				note = "(all open)"
			}
		}
		items = append(items, remoteHostRowItem(tmuxOpts, r.host, note))
		for _, sess := range r.sess {
			items = append(items, listItem{
				isRemoteRow: true,
				target:      "remote:" + r.host + ":" + sess,
				remoteHost:  r.host,
				remoteSess:  sess,
				display:     cDim + remoteTreeMid + reset + " " + sess,
				displayEnd:  cDim + remoteTreeEnd + reset + " " + sess,
				plain:       remoteTreeMid + " " + sess,
				plainEnd:    remoteTreeEnd + " " + sess,
				searchText:  r.host + "/" + sess + " " + r.host + " " + sess,
			})
		}
	}
	return items
}
```

- [ ] **Step 3: Run the same tests again to confirm the refactor is behavior-preserving**

Run: `cd picker && go test ./... -run Remote -v`
Expected: PASS, identical to Step 1 (same test names, same pass count).

- [ ] **Step 4: Commit**

```bash
git add picker/remote.go
git commit -m "refactor(picker): extract shared remote header/host row builders"
```

---

## Task 2: Add `pendingRemoteItems` (synchronous, no ssh)

**Files:**
- Modify: `picker/remote.go` (add `pendingRemoteItems` + `remotePendingNote`)
- Test: `picker/remote_test.go`

**Interfaces:**
- Consumes: `remoteHeaderItem`, `remoteHostRowItem`, `parseRemoteHosts` (all from Task 1 / existing code).
- Produces: `pendingRemoteItems(tmuxOpts map[string]string) []listItem`, `remotePendingNote` (string constant) — consumed by Task 3's `runTUI` wiring.

- [ ] **Step 1: Write the failing tests**

Append to `picker/remote_test.go`:

```go
func TestPendingRemoteItems(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	items := pendingRemoteItems(opts)
	if len(items) != 3 {
		t.Fatalf("expected header + 2 host rows, got %d: %+v", len(items), items)
	}
	if !items[0].isRemoteHeader {
		t.Fatalf("first row should be remote header")
	}
	for i, host := range []string{"lab", "dead"} {
		row := items[i+1]
		if row.remoteHost != host || row.remoteSess != "" {
			t.Errorf("row %d: got remoteHost=%q remoteSess=%q, want host=%q", i, row.remoteHost, row.remoteSess, host)
		}
		if row.target == "" {
			t.Errorf("row %d: must be selectable before the probe returns", i)
		}
		if !row.isRemoteRow {
			t.Errorf("row %d: must be flagged isRemoteRow so claude/scratch toggles can't hide it", i)
		}
		if row.searchText != host {
			t.Errorf("row %d: searchText=%q, want %q", i, row.searchText, host)
		}
		if !strings.Contains(row.plain, remotePendingNote) {
			t.Errorf("row %d: plain=%q missing pending note %q", i, row.plain, remotePendingNote)
		}
	}
}

func TestPendingRemoteItemsNoHosts(t *testing.T) {
	if items := pendingRemoteItems(nil); items != nil {
		t.Fatalf("no hosts => nil, got %v", items)
	}
}

// The row count for a host with nothing new to report must not change
// between the pending render and the resolved one — that stability is what
// stops the whole section from reflowing under the user (#312). A host that
// turns out to have unbridged sessions still gains those child rows once the
// probe returns (Task 4 makes that growth cursor-safe); this covers the
// common, reflow-causing case the bug report measured.
func TestRemoteItemsRowCountStableAcrossProbe(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	pending := pendingRemoteItems(opts)

	probe := func(host string) ([]string, error) {
		if host == "dead" {
			return nil, errors.New("unreachable")
		}
		return nil, nil // lab: reachable, nothing new to show
	}
	resolved := collectRemoteItems(opts, nil, probe)

	if len(pending) != len(resolved) {
		t.Fatalf("row count changed: pending=%d resolved=%d", len(pending), len(resolved))
	}
	for i := range pending {
		if pending[i].remoteHost != resolved[i].remoteHost || pending[i].remoteSess != resolved[i].remoteSess {
			t.Errorf("row %d identity changed: pending=%+v resolved=%+v", i, pending[i], resolved[i])
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd picker && go test ./... -run 'TestPendingRemoteItems|TestRemoteItemsRowCountStableAcrossProbe' -v`
Expected: FAIL — `undefined: pendingRemoteItems`, `undefined: remotePendingNote`

- [ ] **Step 3: Implement `pendingRemoteItems`**

Add to `picker/remote.go`, near `collectRemoteItems`:

```go
// remotePendingNote is the placeholder annotation on a host row before its
// ssh probe returns — a dim ellipsis, since no state (unreachable / no
// server / session list) is known yet (#312 design decision 1).
const remotePendingNote = "…"

// pendingRemoteItems builds the Remote section synchronously from
// @remote_bridge_hosts alone — no ssh, so it belongs on the first-paint
// path. Each host gets exactly the row collectRemoteItems would give it,
// with remotePendingNote standing in for whatever annotation the probe will
// resolve. remoteMsg (collectRemoteItems's result) replaces this slice
// wholesale once every host's probe returns.
func pendingRemoteItems(tmuxOpts map[string]string) []listItem {
	hosts := parseRemoteHosts(envOrMap("REMOTE_BRIDGE_HOSTS", tmuxOpts, "@remote_bridge_hosts", ""))
	if len(hosts) == 0 {
		return nil
	}
	items := make([]listItem, 0, len(hosts)+1)
	items = append(items, remoteHeaderItem(tmuxOpts))
	for _, h := range hosts {
		items = append(items, remoteHostRowItem(tmuxOpts, h, remotePendingNote))
	}
	return items
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd picker && go test ./... -run 'TestPendingRemoteItems|TestRemoteItemsRowCountStableAcrossProbe' -v`
Expected: PASS

- [ ] **Step 5: Run the full remote test file to confirm no regressions**

Run: `cd picker && go test ./... -run Remote -v`
Expected: PASS (all tests, old and new)

- [ ] **Step 6: Commit**

```bash
git add picker/remote.go picker/remote_test.go
git commit -m "feat(picker): add pendingRemoteItems for a synchronous Remote section"
```

---

## Task 3: Render the Remote section in the first paint

**Files:**
- Modify: `picker/tui.go:169-202` (`runTUI`) — split into a new `newPickerModel` plus a thinned `runTUI`.
- Test: `picker/tui_test.go`

**Interfaces:**
- Consumes: `pendingRemoteItems` (Task 2).
- Produces: `newPickerModel(windowMode, claudeOnly, wall bool, opts map[string]string, theme string, items []listItem) tuiModel` — the pure model-assembly half of today's `runTUI` (everything from the `tuiModel{...}` literal through `m.snapWall()`), taking already-built `items` so it never shells out to tmux itself. `runTUI` is reduced to gathering `theme`/`opts`/`panes`, building `items` via `buildWindowItems`/`buildSessionItems` (unchanged, still the only caller that touches tmux here), and calling `newPickerModel`. Task 4 builds on this.

This split exists so the actual wiring this task closes — the `if !windowMode
{ m.remoteItems = pendingRemoteItems(opts) }` gate and the `m.recombine().withFilter()`
swap — is exercised by a real unit test instead of only by Task 5's manual
tmux check. Without it, a later edit to `runTUI` that drops the `!windowMode`
guard or reorders the gate would regress #312 with zero CI signal: the old
plan's test called `pendingRemoteItems` and `recombine().withFilter()`
directly, re-deriving the wiring by hand rather than calling it.
`buildWindowItems`/`buildSessionItems` stay out of `newPickerModel` (and thus
out of the test) because they shell out to `tmux` via `collectSessions` —
pulling them in would violate the "no real tmux" constraint on picker unit
tests.

- [ ] **Step 1: Write the failing test**

Append to `picker/tui_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./... -run TestFirstPaintIncludesRemoteRows -v`
Expected: FAIL to compile — `newPickerModel` doesn't exist until Step 3. (Unlike
a pure-assertion test, the red step here is "doesn't build yet", which is
exactly the signal that no such extraction exists in `runTUI` today.)

- [ ] **Step 3: Extract `newPickerModel` and wire `pendingRemoteItems` into it**

Replace `picker/tui.go:169-202`:

```go
// newPickerModel assembles the picker's initial model from already-gathered
// inputs — no tmux/ssh/proc calls of its own — so the first-paint wiring
// (including the Remote section, #312) is exercised by a real unit test
// instead of only by a manual tmux check.
func newPickerModel(windowMode, claudeOnly, wall bool, opts map[string]string, theme string, items []listItem) tuiModel {
	m := tuiModel{
		mode:         wallMode(wall),
		windowMode:   windowMode,
		claudeOnly:   claudeOnly,
		showPreview:  layoutShowsPreview(opts),
		theme:        theme,
		tmuxOpts:     opts,
		sessionItems: items,
		wallContent:  map[string]string{},
		wallBad:      map[string]bool{},
	}
	if !windowMode {
		// Host rows are static config — render them now so the Remote section
		// exists from the first paint. remoteCmd's probe (kicked from Init)
		// fills in each row's annotation in place via remoteMsg (#312).
		m.remoteItems = pendingRemoteItems(opts)
	}
	m = m.recombine().withFilter()
	m.cursor = m.firstSelectable(0)
	if m.mode == modeWall {
		m = m.snapWall()
	}
	return m
}

func runTUI(windowMode, claudeOnly, wall bool) error {
	theme := detectTheme()
	opts := readTmuxOpts()
	panes := collectClaudePanes()

	var items []listItem
	if windowMode {
		items = buildWindowItems(opts, panes, theme, 0)
	} else {
		items = buildSessionItems(opts, panes, theme, false)
	}

	m := newPickerModel(windowMode, claudeOnly, wall, opts, theme, items)

	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
```

Note: `allItems: items` is dropped from the struct literal — `m.recombine()`
now builds `allItems` from `sessionItems` + `remoteItems` + `zoxideItems`
(the latter still empty at this point), which is what `allItems` always was
one line later anyway.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./... -run TestFirstPaintIncludesRemoteRows -v`
Expected: PASS

- [ ] **Step 5: Run the full picker test suite**

Run: `cd picker && go test ./...`
Expected: PASS, no regressions (window-mode tests are unaffected — `pendingRemoteItems` is only called when `!windowMode`, matching `Init()`'s existing `!m.windowMode` gate on `remoteCmd`).

- [ ] **Step 6: Commit**

```bash
git add picker/tui.go picker/tui_test.go
git commit -m "fix(picker): render Remote host rows in the first paint (#312)"
```

---

## Task 4: Preserve cursor and query across the probe's arrival

**Files:**
- Modify: `picker/tui.go:288-291` (`Update`'s `remoteMsg` case), plus a new `restoreCursor` method near `currentItem` (`picker/tui.go:872-877`)
- Test: `picker/tui_test.go`

**Interfaces:**
- Consumes: `tuiModel.currentTarget()`, `tuiModel.recombine()`, `tuiModel.withFilter()`, `tuiModel.firstSelectable()` (all existing).
- Produces: `(m tuiModel) restoreCursor(keep string) tuiModel` — scoped to this task; not required by any other task, but written generally enough that a future fix to the analogous `zoxideMsg` case (out of scope here) could reuse it.

- [ ] **Step 1: Write the failing tests**

Append to `picker/tui_test.go` (needs `"errors"` added to the import block — this
is the first test in the file to use it):

```go
// The row a host contributes can grow once its probe returns (a host that
// turns out to have unbridged sessions gains tree-child rows — Design
// Decision 4). That growth must not silently move the cursor onto a
// different row: this is the actual user-visible half of #312 ("anything
// the human did in that window ... gets re-laid-out underneath them").
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
	resolved := collectRemoteItems(opts, nil, probe)

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
	resolved := collectRemoteItems(opts, nil, probe)

	next, _ := m.Update(remoteMsg{items: resolved})
	nm, ok := next.(tuiModel)
	if !ok {
		t.Fatalf("Update did not return a tuiModel")
	}
	if nm.query != "laz" {
		t.Fatalf("query was reset: got %q", nm.query)
	}
}
```

Also append (constraint (b): late-arriving child rows must be subject to the
active filter, not exempt from it — a dispatcher-mandated constraint added
after this plan's original draft):

```go
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
	resolved := collectRemoteItems(opts, nil, probe)

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
```

And (constraint (c): remote rows — pending or resolved — stay exempt from the
claude/scratch toggles):

```go
// Pending rows (before any probe resolves) must be exempt from the
// claude/scratch toggles exactly like resolved rows — itemVisible checks
// isRemoteRow before either toggle, and pendingRemoteItems sets it via the
// same remoteHostRowItem helper collectRemoteItems uses, but this pins that
// down as a regression guard rather than relying on shared code alone.
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd picker && go test ./... -run 'TestRemoteMsgPreservesCursor|TestRemoteMsgPreservesQuery|TestRemoteMsgChildRowsRespectActiveQuery|TestPendingRemoteItemsSurviveModeToggles' -v`
Expected: `TestRemoteMsgPreservesCursor` FAILs ("cursor moved off the dead host row") — today's `remoteMsg` handler does a raw index-based recombine with no target restore, so growing "lab"'s rows shifts "dead" out from under the cursor. `TestRemoteMsgPreservesQuery`, `TestRemoteMsgChildRowsRespectActiveQuery`, and `TestPendingRemoteItemsSurviveModeToggles` may already PASS (query filtering and toggle exemption already apply uniformly in `withFilter`/`itemVisible`) — that's fine, they're regression guards pinning down dispatcher-mandated constraints (b) and (c), not new behavior.

- [ ] **Step 3: Add `restoreCursor` and use it in the `remoteMsg` case**

Insert after `currentItem` at `picker/tui.go:877` (right before `// activateCurrent switches...`):

```go
// restoreCursor re-finds keep (the target selected before a rebuild) in the
// new visible list, so a row's growing (new tree-child rows once its probe
// resolves) or shrinking never moves the selection out from under the user.
// Falls back to the first selectable row whenever keep is set but no longer
// present — never aliases onto whatever unrelated row now sits at the stale
// index — and also when keep was never set and the stale index is now out of
// range.
func (m tuiModel) restoreCursor(keep string) tuiModel {
	if keep != "" {
		for i, item := range m.visible {
			if item.target == keep {
				m.cursor = i
				return m
			}
		}
		m.cursor = m.firstSelectable(0)
		return m
	}
	if m.cursor >= len(m.visible) {
		m.cursor = m.firstSelectable(0)
	}
	return m
}
```

Replace `picker/tui.go:288-291`:

```go
	case remoteMsg:
		keep := m.currentTarget()
		m.remoteItems = msg.items
		m = m.recombine().withFilter()
		m = m.restoreCursor(keep)
		return m, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd picker && go test ./... -run 'TestRemoteMsgPreservesCursor|TestRemoteMsgPreservesQuery|TestRemoteMsgChildRowsRespectActiveQuery|TestPendingRemoteItemsSurviveModeToggles' -v`
Expected: PASS

- [ ] **Step 5: Run the full picker test suite**

Run: `cd picker && go test ./...`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add picker/tui.go picker/tui_test.go
git commit -m "fix(picker): keep cursor on its row when a remote probe resolves (#312)"
```

---

## Task 5: Hardware verification and final gate

**Files:** none (verification only)

- [ ] **Step 1: Build the picker**

Run: `nix build .#default`
Expected: build succeeds.

- [ ] **Step 2: Reload the running tmux and open the picker in a real window**

In a tmux session with `@remote_bridge_hosts` set to at least one reachable
and one unreachable host:

```
prefix + r
```

then open a fresh window (so `automatic-rename` doesn't fight a `-n` name —
per the issue's own measurement-technique note, target it by `window_id`,
not name) and run the picker (`prefix + s`), or drive it non-interactively:

```bash
tmux new-window -P -F '#{window_id}'
# capture the returned window_id, then in that window:
tmux send-keys -t <window_id> 'tmux-picker-generate --tui' Enter
```

- [ ] **Step 3: Poll `capture-pane` and confirm the Remote section is present from the very first non-empty capture**

```bash
for i in $(seq 1 40); do
  tmux capture-pane -t <window_id> -p
  sleep 0.05
done
```

Expected: the very first capture that shows the list body already shows the
`── Remote ──` header and one row per configured host (dim `…` on the
reachable-but-not-yet-probed one), not just the session list. A later
capture (once the ssh probe resolves) shows the same row positions with
`(unreachable — open default)` / `(no server — Enter starts one)` / session
names filled in — no new section, no shifted rows.

- [ ] **Step 4: Confirm existing remote-row search still works**

With the popup open, type a few characters of an unreachable host's name and
confirm its row still filters in (regression per acceptance criteria's
pointer to `remote_test.go:126` — "a row stays searchable by host").

- [ ] **Step 5: Run the full flake check**

Run: `nix flake check`
Expected: all checks green, including the `picker` derivation's Go test
suite (`agentdetect`, `statusline`, `remotebridge` subpackages plus the
root `picker` package's own `go test`).

- [ ] **Step 6: Final commit (if Step 5 required any fixups) and push**

```bash
git push -u origin feat/312-fix-picker-render-remote-host-rows-in-th
```

Then open the PR (assignee `@me`), referencing `Closes #312`.

---

## Self-Review Notes

- **Spec coverage:** first-paint rows (Task 3, via `newPickerModel` — the
  actual `runTUI` wiring, not a hand-assembled stand-in for it), row-count-stable
  annotation fill (Task 2's `TestRemoteItemsRowCountStableAcrossProbe`), cursor
  survival (Task 4), query survival (Task 4), existing `remote_test.go`
  untouched (Task 1's behavior-preserving refactor + Global Constraints),
  `nix flake check` green (Task 5), measurement technique honored (Task 5
  steps 2-3 use a real tmux window + `capture-pane` polling by `window_id`,
  never `script`/a bare pty). All three open design questions answered up
  front.
- **Out of scope respected:** no edits to `picker/render_wall.go` or
  `remotebridge/`; `remoteProbeTimeout` / `ConnectTimeout` left untouched
  per Design Decision 3.
- **Acceptance-criterion narrowing flagged, not buried:** Design Decision 4
  necessarily narrows the literal "row count never changes" criterion for the
  case where a host's probe reveals unbridged sessions. See "Open Question —
  Needs Sign-off Before Implementation" above. This is now a structural gate,
  not a footnote: Task 0 is a standalone task with its own checkboxes for
  getting and recording sign-off, sequenced before Task 1's prerequisite line,
  so a checkbox-driven executor can't reach Task 1 without surfacing it.
- **`runTUI`'s wiring is unit-tested, not just manually verified:** Task 3
  extracts the pure model-assembly half of `runTUI` into `newPickerModel`
  (takes already-built `items`, so it never shells out to tmux) specifically
  so `TestFirstPaintIncludesRemoteRows` calls the real `!windowMode` gate and
  `recombine().withFilter()` swap, instead of re-deriving that wiring by hand
  and leaving the real code path to Task 5's manual tmux check alone.
