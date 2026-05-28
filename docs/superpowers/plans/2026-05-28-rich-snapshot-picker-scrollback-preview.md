# Rich Snapshot Picker — Scrollback Preview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a scrollback preview pane to the in-flight Bubble Tea snapshot picker on tmux-state's `feat/picker-tree` branch, then merge, release, and bump lazytmux to pick it up — so `prefix + R` in lazytmux shows a 3-pane TUI (snapshot list / manifest tree / scrollback preview).

**Architecture:** The picker already has a 2-pane layout (snapshot list + manifest tree) built on Bubble Tea v2 + lipgloss v2 + bubbles/v2. We add a third pane to the right of the tree. When the tree focus lands on a `NodePane` whose `*snapshot.Pane` has a non-empty `ScrollbackSHA`, we async-load the scrollback bytes via `internal/scrollback.Store.Stream` and render them in a `bubbles/v2/viewport.Model`. Cache loaded scrollbacks by SHA in the model. Width budget: `list = width/4`, `tree = width/3`, `preview = remainder` when `width >= 120`; degrade gracefully to 2-pane (drop preview) when `80 <= width < 120` and list-only when `width < 80`.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), bubbles v2 (`charm.land/bubbles/v2/viewport`), lipgloss v2, `github.com/noamsto/tmux-state/internal/{scrollback,snapshot,store}`.

**Repo layout:** Phases 1–9 happen in `tmux-state` repo on branch `feat/picker-tree` (worktree at `/home/noams/Data/git/noamsto/tmux-state/.worktrees/feat-picker-tree`). Phases 10–12 cross repos.

---

## File Structure

**Modified (tmux-state):**
- `internal/picker/model.go` — add `scrollbacks` cache, `scrollbackStore`, viewport, `previewState`; new constructor arg.
- `internal/picker/view.go` — render 3rd pane; update `paneWidths()` to a 3-pane split.
- `internal/picker/style.go` — `previewFrame` style.
- `internal/picker/keys.go` — `PageUp`/`PageDown`/`HalfUp`/`HalfDown` bindings used by the preview viewport when tree is focused on a pane.
- `internal/picker/model_test.go` — new test cases for preview state transitions.
- `cmd/tmux-state/main.go` — wire `scrollback.Store` into `NewPickerModel` from `newPickCmd`.

**Created (tmux-state):**
- `internal/picker/preview.go` — preview-pane rendering + async load command + message type. Splits this concern out of model.go to keep that file focused.
- `internal/picker/preview_test.go` — preview-only unit tests.

**Modified (lazytmux):**
- `flake.lock` — `nix flake update tmux-state` (auto-regenerated).
- `flake.nix` — only if input URL needs to pin a tag (optional).

---

## Phase 1: Scrollback Preview in tmux-state

### Task 1: Add scrollback store to PickerModel constructor

**Files:**
- Modify: `internal/picker/model.go` (struct + `NewPickerModel`)
- Modify: `internal/picker/model_test.go` (existing call sites)

The picker today has no way to read scrollback. We thread the existing `*scrollback.Store` through the constructor so subsequent tasks can use it.

- [ ] **Step 1: Write the failing test**

Append to `internal/picker/model_test.go`:

```go
func TestNewPickerModel_AcceptsScrollbackStore(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	sb := scrollback.New(tmp)
	m := NewPickerModel(ModeSnapshot, nil, nil, sb)
	if m.ScrollbackStore() != sb {
		t.Fatalf("scrollback store not threaded through constructor")
	}
}
```

Add the import `"github.com/noamsto/tmux-state/internal/scrollback"` to the test file's import block if not present.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/noams/Data/git/noamsto/tmux-state/.worktrees/feat-picker-tree
go test ./internal/picker/ -run TestNewPickerModel_AcceptsScrollbackStore -v
```

Expected: FAIL with `too many arguments in call to NewPickerModel` (constructor still has 3 params).

- [ ] **Step 3: Update PickerModel struct + constructor**

In `internal/picker/model.go`:

1. Add import: `"github.com/noamsto/tmux-state/internal/scrollback"`.
2. Add field on `PickerModel`:
   ```go
   scrollbackStore *scrollback.Store
   ```
3. Change constructor signature:
   ```go
   func NewPickerModel(mode Mode, events []store.Event, running map[string]bool, sb *scrollback.Store) PickerModel {
   ```
4. Set the field in the returned struct: `scrollbackStore: sb,`.
5. Add accessor:
   ```go
   // ScrollbackStore returns the scrollback store passed to the constructor.
   // Exported for tests; production code does not call this.
   func (m PickerModel) ScrollbackStore() *scrollback.Store { return m.scrollbackStore }
   ```

- [ ] **Step 4: Update existing test call sites**

In `internal/picker/model_test.go`, every existing `NewPickerModel(mode, evs, running)` call gains a 4th argument. Pass `nil` (no scrollback access needed by those tests):

```bash
rg -n "NewPickerModel\(" internal/picker/model_test.go
```

For each match, append `, nil` before the closing paren. The new test from Step 1 passes `sb` (a real store) — leave that one.

- [ ] **Step 5: Update the cobra caller**

In `cmd/tmux-state/main.go`, `newPickCmd`, the line currently reads:

```go
m := picker.NewPickerModel(mode, evs, runningSet)
```

Change to:

```go
sb := scrollback.New(cfg.ScrollbackDir)
m := picker.NewPickerModel(mode, evs, runningSet, sb)
```

`cfg` and the `scrollback` import are already available in that file.

- [ ] **Step 6: Run tests, expect pass**

```bash
go test ./internal/picker/ ./cmd/tmux-state/ -v
```

Expected: PASS, including the new `TestNewPickerModel_AcceptsScrollbackStore`.

- [ ] **Step 7: Commit**

```bash
git add internal/picker/model.go internal/picker/model_test.go cmd/tmux-state/main.go
git commit -m "feat(picker): thread scrollback.Store through constructor"
```

---

### Task 2: Async scrollback load command + message

**Files:**
- Create: `internal/picker/preview.go`
- Create: `internal/picker/preview_test.go`

Add the `tea.Cmd` that reads scrollback bytes off the UI goroutine and emits a result message. Keep it isolated in `preview.go` so model.go doesn't grow.

- [ ] **Step 1: Write the failing test**

Create `internal/picker/preview_test.go`:

```go
package picker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noamsto/tmux-state/internal/scrollback"
)

func TestLoadScrollbackCmd_ReturnsContent(t *testing.T) {
	tmp := t.TempDir()
	sb := scrollback.New(tmp)
	sha, _, err := sb.Put(context.Background(), []byte("hello scrollback"))
	if err != nil {
		t.Fatalf("seed scrollback: %v", err)
	}

	cmd := loadScrollbackCmd(sb, sha)
	if cmd == nil {
		t.Fatal("loadScrollbackCmd returned nil")
	}
	msg := cmd()
	loaded, ok := msg.(scrollbackLoadedMsg)
	if !ok {
		t.Fatalf("expected scrollbackLoadedMsg, got %T", msg)
	}
	if loaded.sha != sha {
		t.Errorf("sha mismatch: got %q want %q", loaded.sha, sha)
	}
	if loaded.err != nil {
		t.Errorf("unexpected err: %v", loaded.err)
	}
	if !strings.Contains(string(loaded.content), "hello scrollback") {
		t.Errorf("content mismatch: got %q", loaded.content)
	}
}

func TestLoadScrollbackCmd_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	sb := scrollback.New(tmp)
	const missing = "0000000000000000000000000000000000000000000000000000000000000000"

	cmd := loadScrollbackCmd(sb, missing)
	msg := cmd()
	loaded, ok := msg.(scrollbackLoadedMsg)
	if !ok {
		t.Fatalf("expected scrollbackLoadedMsg, got %T", msg)
	}
	if loaded.err == nil {
		t.Fatal("expected err for missing scrollback, got nil")
	}
	if !errors.Is(loaded.err, scrollback.ErrNotFound) && !strings.Contains(loaded.err.Error(), "no such file") {
		t.Logf("missing-file error: %v (acceptable as long as err != nil)", loaded.err)
	}
}
```

Note: if `scrollback.ErrNotFound` doesn't exist in the package (check `internal/scrollback/store.go`), the second assertion's `errors.Is` branch is unreachable — the `strings.Contains` fallback handles the raw fs.PathError text. Either way, the test passes as long as `loaded.err != nil`.

- [ ] **Step 2: Verify it fails**

```bash
go test ./internal/picker/ -run TestLoadScrollbackCmd -v
```

Expected: FAIL with `undefined: loadScrollbackCmd` and `undefined: scrollbackLoadedMsg`.

- [ ] **Step 3: Implement preview.go**

Create `internal/picker/preview.go`:

```go
package picker

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/noamsto/tmux-state/internal/scrollback"
)

// scrollbackLoadedMsg is emitted by loadScrollbackCmd when the scrollback read
// completes (successfully or not). The model handles it by populating the
// scrollback cache and refreshing the viewport if the cursor still points at
// the same SHA.
type scrollbackLoadedMsg struct {
	sha     string
	content []byte
	err     error
}

// loadScrollbackCmd returns a tea.Cmd that reads the scrollback for sha off
// the UI goroutine. Returns nil if sb is nil or sha is empty (caller short-
// circuits and never schedules a load).
func loadScrollbackCmd(sb *scrollback.Store, sha string) tea.Cmd {
	if sb == nil || sha == "" {
		return nil
	}
	return func() tea.Msg {
		rc, err := sb.Stream(context.Background(), sha)
		if err != nil {
			return scrollbackLoadedMsg{sha: sha, err: err}
		}
		defer func() { _ = rc.Close() }()
		buf, err := io.ReadAll(rc)
		return scrollbackLoadedMsg{sha: sha, content: buf, err: err}
	}
}
```

- [ ] **Step 4: Verify tests pass**

```bash
go test ./internal/picker/ -run TestLoadScrollbackCmd -v
```

Expected: PASS for both subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/picker/preview.go internal/picker/preview_test.go
git commit -m "feat(picker): async loadScrollbackCmd + scrollbackLoadedMsg"
```

---

### Task 3: Cache loaded scrollbacks + handle the msg

**Files:**
- Modify: `internal/picker/model.go`
- Modify: `internal/picker/preview_test.go`

Wire the new message into `Update`: store the bytes in a per-SHA cache, remember errors, and trigger a redraw.

- [ ] **Step 1: Write the failing test**

Append to `internal/picker/preview_test.go`:

```go
func TestPickerModel_HandlesScrollbackLoadedMsg(t *testing.T) {
	m := NewPickerModel(ModeSnapshot, nil, nil, nil)
	msg := scrollbackLoadedMsg{sha: "deadbeef", content: []byte("hi"), err: nil}
	updated, _ := m.Update(msg)
	final := updated.(PickerModel)
	got, ok := final.ScrollbackFor("deadbeef")
	if !ok {
		t.Fatalf("cache miss for sha after loaded msg")
	}
	if string(got) != "hi" {
		t.Errorf("content mismatch: got %q want %q", got, "hi")
	}
}

func TestPickerModel_RemembersScrollbackError(t *testing.T) {
	m := NewPickerModel(ModeSnapshot, nil, nil, nil)
	wantErr := errors.New("boom")
	msg := scrollbackLoadedMsg{sha: "deadbeef", err: wantErr}
	updated, _ := m.Update(msg)
	final := updated.(PickerModel)
	if err := final.ScrollbackError("deadbeef"); err == nil {
		t.Fatal("expected cached error, got nil")
	}
}
```

(The `errors` import is already added by the earlier test.)

- [ ] **Step 2: Verify fail**

```bash
go test ./internal/picker/ -run TestPickerModel_HandlesScrollbackLoadedMsg -v
```

Expected: FAIL with `final.ScrollbackFor undefined`.

- [ ] **Step 3: Add cache fields + handler**

In `internal/picker/model.go`:

1. Add fields to `PickerModel`:
   ```go
   scrollbacks      map[string][]byte // sha → bytes
   scrollbackErrors map[string]error  // sha → load error
   loadingSHAs      map[string]bool   // sha → in-flight load
   ```
2. Initialize them in `NewPickerModel`:
   ```go
   scrollbacks:      make(map[string][]byte),
   scrollbackErrors: make(map[string]error),
   loadingSHAs:      make(map[string]bool),
   ```
3. In `Update`, add a case for `scrollbackLoadedMsg` before the `tea.KeyPressMsg` case:
   ```go
   case scrollbackLoadedMsg:
       delete(m.loadingSHAs, msg.sha)
       if msg.err != nil {
           m.scrollbackErrors[msg.sha] = msg.err
       } else {
           m.scrollbacks[msg.sha] = msg.content
       }
       return m, nil
   ```
4. Add the accessors used by tests + view:
   ```go
   // ScrollbackFor returns the cached scrollback bytes for sha and whether the
   // entry was present.
   func (m PickerModel) ScrollbackFor(sha string) ([]byte, bool) {
       b, ok := m.scrollbacks[sha]
       return b, ok
   }

   // ScrollbackError returns the cached load error for sha, or nil.
   func (m PickerModel) ScrollbackError(sha string) error { return m.scrollbackErrors[sha] }
   ```

- [ ] **Step 4: Verify pass**

```bash
go test ./internal/picker/ -v
```

Expected: PASS, including both new tests and all existing picker tests.

- [ ] **Step 5: Commit**

```bash
git add internal/picker/model.go internal/picker/preview_test.go
git commit -m "feat(picker): cache loaded scrollbacks + remember errors"
```

---

### Task 4: Trigger load when tree cursor lands on a pane

**Files:**
- Modify: `internal/picker/model.go`
- Modify: `internal/picker/preview_test.go`

When the focused tree node is a `NodePane` with a `ScrollbackSHA`, schedule a `loadScrollbackCmd` (once per SHA — dedupe via `loadingSHAs`).

- [ ] **Step 1: Write the failing test**

Append to `internal/picker/preview_test.go`:

```go
func TestPickerModel_FocusedPaneTriggersLoad(t *testing.T) {
	// Build a minimal manifest with one pane carrying a scrollback SHA.
	man := snapshot.Manifest{
		V: 1,
		Sessions: []snapshot.Session{{
			Name: "s1",
			Windows: []snapshot.Window{{
				Index: 0, Name: "w1",
				Panes: []snapshot.Pane{{Index: 0, Cwd: "/tmp", Command: "bash", ScrollbackSHA: "abc123"}},
			}},
		}},
	}
	raw, _ := json.Marshal(man)
	ev := store.Event{ID: 7, Kind: "snapshot", ManifestJSON: string(raw)}

	tmp := t.TempDir()
	sb := scrollback.New(tmp)

	m := NewPickerModel(ModeSnapshot, []store.Event{ev}, nil, sb)
	m.Bootstrap()
	// Focus tree, then walk cursor down session → window → pane.
	m.focus = focusTree
	m.treeCursor = 2 // session(0) → window(1) → pane(2)

	cmd := m.PreviewCmd()
	if cmd == nil {
		t.Fatal("PreviewCmd returned nil for a pane with scrollback")
	}
}

func TestPickerModel_NoLoadWhenAlreadyCached(t *testing.T) {
	man := snapshot.Manifest{
		V: 1,
		Sessions: []snapshot.Session{{
			Windows: []snapshot.Window{{
				Panes: []snapshot.Pane{{ScrollbackSHA: "abc123"}},
			}},
		}},
	}
	raw, _ := json.Marshal(man)
	ev := store.Event{ID: 7, Kind: "snapshot", ManifestJSON: string(raw)}
	tmp := t.TempDir()
	sb := scrollback.New(tmp)

	m := NewPickerModel(ModeSnapshot, []store.Event{ev}, nil, sb)
	m.Bootstrap()
	m.focus = focusTree
	m.treeCursor = 2
	m.scrollbacks["abc123"] = []byte("cached")

	if cmd := m.PreviewCmd(); cmd != nil {
		t.Fatal("PreviewCmd should be nil when SHA already cached")
	}
}
```

Add imports as needed: `encoding/json`, `github.com/noamsto/tmux-state/internal/snapshot`, `github.com/noamsto/tmux-state/internal/store` (the store import may already be present).

- [ ] **Step 2: Verify fail**

```bash
go test ./internal/picker/ -run TestPickerModel_FocusedPaneTriggersLoad -v
```

Expected: FAIL with `m.PreviewCmd undefined`.

- [ ] **Step 3: Implement PreviewCmd**

Add to `internal/picker/model.go`:

```go
// PreviewCmd returns a tea.Cmd that loads the scrollback for the currently
// focused tree-pane node, or nil if no load is needed (wrong focus, no SHA,
// cached, already loading, or no scrollback store).
func (m *PickerModel) PreviewCmd() tea.Cmd {
    sha := m.focusedPaneSHA()
    if sha == "" || m.scrollbackStore == nil {
        return nil
    }
    if _, cached := m.scrollbacks[sha]; cached {
        return nil
    }
    if _, errored := m.scrollbackErrors[sha]; errored {
        return nil
    }
    if m.loadingSHAs[sha] {
        return nil
    }
    m.loadingSHAs[sha] = true
    return loadScrollbackCmd(m.scrollbackStore, sha)
}

// focusedPaneSHA returns the ScrollbackSHA of the currently focused tree-pane
// node, or "" if focus is not on the tree, the node is not a pane, or the
// pane has no scrollback.
func (m PickerModel) focusedPaneSHA() string {
    if m.mode != ModeSnapshot || m.focus != focusTree {
        return ""
    }
    nodes := m.visibleNodes()
    if m.treeCursor < 0 || m.treeCursor >= len(nodes) {
        return ""
    }
    n := nodes[m.treeCursor]
    if n.Kind != NodePane {
        return ""
    }
    p, ok := n.Ref.(*snapshot.Pane)
    if !ok || p == nil {
        return ""
    }
    return p.ScrollbackSHA
}
```

- [ ] **Step 4: Wire PreviewCmd into the existing key handlers**

In `internal/picker/model.go` `handleKey`, every place that moves `m.treeCursor` (the `focusTree`/Up, Down, Left, Right cases — lines ~125–151) should now return `(m, m.PreviewCmd())` instead of `(m, nil)`. Same for the list-cursor cases that set `m.treeCursor = 0` (Up/Down at lines ~155–168) and the `Tab` case (the switch to focusTree should also trigger a load if the new focus lands on a pane).

Concrete replacements:

```go
case key.Matches(msg, m.keys.Up):
    if m.treeCursor > 0 {
        m.treeCursor--
    }
    return m, m.PreviewCmd()
```

…and the same `m.PreviewCmd()` substitution in the Down, Left, Right tree-focus branches, the Up/Down list-focus branches that touch treeCursor, and the Tab branch.

- [ ] **Step 5: Verify pass**

```bash
go test ./internal/picker/ -v
```

Expected: PASS, all picker tests including the two new ones.

- [ ] **Step 6: Commit**

```bash
git add internal/picker/model.go internal/picker/preview_test.go
git commit -m "feat(picker): schedule scrollback load on tree-cursor focus"
```

---

### Task 5: 3-pane width split

**Files:**
- Modify: `internal/picker/view.go`
- Create: tests inside `internal/picker/preview_test.go`

Change `paneWidths()` from 2-pane to 3-pane. Existing layout breakpoints:
- `width < 80` → list-only
- `width >= 80` → list + tree

New breakpoints:
- `width < 80` → list-only (unchanged)
- `80 <= width < 120` → list + tree (preview hidden — current behavior, keeps narrow terminals working)
- `width >= 120` → list + tree + preview

- [ ] **Step 1: Write the failing test**

Append to `internal/picker/preview_test.go`:

```go
func TestPaneWidths_ThreePane(t *testing.T) {
	m := PickerModel{mode: ModeSnapshot, width: 160}
	l, tr, pv := m.paneWidthsThree()
	if l+tr+pv != 160 {
		t.Errorf("widths must sum to total: got %d+%d+%d != 160", l, tr, pv)
	}
	if l < 28 || tr < 32 || pv < 40 {
		t.Errorf("min widths violated: l=%d tr=%d pv=%d", l, tr, pv)
	}
}

func TestPaneWidths_NarrowFallsBackToTwoPane(t *testing.T) {
	m := PickerModel{mode: ModeSnapshot, width: 100}
	l, tr, pv := m.paneWidthsThree()
	if pv != 0 {
		t.Errorf("preview should be 0 at width=100, got %d", pv)
	}
	if l+tr != 100 {
		t.Errorf("widths must sum to total: got %d+%d != 100", l, tr)
	}
}
```

- [ ] **Step 2: Verify fail**

```bash
go test ./internal/picker/ -run TestPaneWidths -v
```

Expected: FAIL with `m.paneWidthsThree undefined`.

- [ ] **Step 3: Add paneWidthsThree**

In `internal/picker/view.go`, replace `paneWidths` with `paneWidthsThree` (keep the old function name as a thin wrapper if any out-of-tree caller exists — `rg paneWidths internal/picker` to confirm none does, then just rename and adjust the one caller in `View`).

```go
// paneWidthsThree splits the available width between list, tree, and preview.
// Returns (list, tree, preview) where preview==0 means the preview pane is
// hidden at this width (or in close mode).
func (m PickerModel) paneWidthsThree() (int, int, int) {
    if m.width < 80 || m.mode == ModeClose {
        return m.width, 0, 0
    }
    if m.width < 120 {
        // Two-pane fallback (current behavior).
        listW := m.width / 3
        if listW < 28 {
            listW = 28
        }
        return listW, m.width - listW, 0
    }
    // Three-pane: 1/4 list, 1/3 tree, remainder preview, with minimums.
    listW := m.width / 4
    if listW < 28 {
        listW = 28
    }
    treeW := m.width / 3
    if treeW < 32 {
        treeW = 32
    }
    previewW := m.width - listW - treeW
    if previewW < 40 {
        // Squeeze tree to give preview its minimum.
        treeW = m.width - listW - 40
        previewW = 40
    }
    return listW, treeW, previewW
}
```

Update `View()`:

```go
listWidth, treeWidth, previewWidth := m.paneWidthsThree()
list := renderList(m, listWidth)

var content string
switch {
case m.mode == ModeClose || m.width < 80:
    content = lipgloss.JoinVertical(lipgloss.Top, list, m.renderFooter(m.width))
case previewWidth == 0:
    tree := renderTree(m, treeWidth)
    body := lipgloss.JoinHorizontal(lipgloss.Top, list, tree)
    content = lipgloss.JoinVertical(lipgloss.Top, body, m.renderFooter(m.width))
default:
    tree := renderTree(m, treeWidth)
    preview := m.renderPreview(previewWidth) // implemented in Task 6
    body := lipgloss.JoinHorizontal(lipgloss.Top, list, tree, preview)
    content = lipgloss.JoinVertical(lipgloss.Top, body, m.renderFooter(m.width))
}
```

`renderPreview` does not exist yet — Task 6 adds it. Tests will fail to compile until then; that's expected for TDD. To make this task self-contained, add a stub now:

In `internal/picker/preview.go`:

```go
// renderPreview is the stub completed in Task 6.
func (m PickerModel) renderPreview(width int) string {
    return previewFrame.Width(width).Render("(preview)")
}
```

And add to `internal/picker/style.go`:

```go
previewFrame = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colSurface1).Padding(0, 1)
```

(Place alongside `listFrame` / `treeFrame` in the existing block.)

- [ ] **Step 4: Verify pass**

```bash
go test ./internal/picker/ -v
```

Expected: PASS, all picker tests.

- [ ] **Step 5: Commit**

```bash
git add internal/picker/view.go internal/picker/preview.go internal/picker/style.go internal/picker/preview_test.go
git commit -m "feat(picker): 3-pane width split with preview stub"
```

---

### Task 6: Render preview content with all states

**Files:**
- Modify: `internal/picker/preview.go`
- Modify: `internal/picker/style.go`
- Modify: `internal/picker/preview_test.go`

Replace the stub with the real renderer. Five states:

| Condition | Render |
|---|---|
| `focus != focusTree` OR `treeCursor` not on a pane | `(focus a pane to preview)` — dim |
| Pane has no `ScrollbackSHA` | `(no scrollback recorded)` — dim |
| Loading (in `loadingSHAs`) | `(loading...)` — dim |
| Load error in cache | `(scrollback unavailable: <err>)` — warn red |
| Bytes in cache | Tail-render the last N lines that fit the pane height; ANSI passthrough |

- [ ] **Step 1: Write the failing tests**

Append to `internal/picker/preview_test.go`:

```go
func TestRenderPreview_NoPaneFocused(t *testing.T) {
	m := PickerModel{mode: ModeSnapshot, width: 160, height: 30, focus: focusList}
	got := m.renderPreview(60)
	if !strings.Contains(stripANSI(got), "focus a pane") {
		t.Errorf("expected hint, got: %q", got)
	}
}

func TestRenderPreview_PaneWithoutSHA(t *testing.T) {
	man := snapshot.Manifest{V: 1, Sessions: []snapshot.Session{{
		Windows: []snapshot.Window{{Panes: []snapshot.Pane{{}}}},
	}}}
	raw, _ := json.Marshal(man)
	ev := store.Event{ID: 1, Kind: "snapshot", ManifestJSON: string(raw)}
	m := NewPickerModel(ModeSnapshot, []store.Event{ev}, nil, nil)
	m.Bootstrap()
	m.focus = focusTree
	m.treeCursor = 2 // session → window → pane

	got := m.renderPreview(60)
	if !strings.Contains(stripANSI(got), "no scrollback recorded") {
		t.Errorf("expected hint, got: %q", got)
	}
}

func TestRenderPreview_Loaded(t *testing.T) {
	man := snapshot.Manifest{V: 1, Sessions: []snapshot.Session{{
		Windows: []snapshot.Window{{Panes: []snapshot.Pane{{ScrollbackSHA: "abc"}}}},
	}}}
	raw, _ := json.Marshal(man)
	ev := store.Event{ID: 1, Kind: "snapshot", ManifestJSON: string(raw)}
	m := NewPickerModel(ModeSnapshot, []store.Event{ev}, nil, nil)
	m.Bootstrap()
	m.focus = focusTree
	m.treeCursor = 2
	m.scrollbacks["abc"] = []byte("$ echo hi\nhi\n$ ")

	got := stripANSI(m.renderPreview(60))
	if !strings.Contains(got, "echo hi") {
		t.Errorf("expected content, got: %q", got)
	}
}

func TestRenderPreview_Error(t *testing.T) {
	man := snapshot.Manifest{V: 1, Sessions: []snapshot.Session{{
		Windows: []snapshot.Window{{Panes: []snapshot.Pane{{ScrollbackSHA: "abc"}}}},
	}}}
	raw, _ := json.Marshal(man)
	ev := store.Event{ID: 1, Kind: "snapshot", ManifestJSON: string(raw)}
	m := NewPickerModel(ModeSnapshot, []store.Event{ev}, nil, nil)
	m.Bootstrap()
	m.focus = focusTree
	m.treeCursor = 2
	m.scrollbackErrors["abc"] = errors.New("file gone")

	got := stripANSI(m.renderPreview(60))
	if !strings.Contains(got, "unavailable") {
		t.Errorf("expected error label, got: %q", got)
	}
}

// stripANSI removes ANSI escapes for assertion ergonomics.
func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
```

Add imports as needed: `regexp`, `strings`.

- [ ] **Step 2: Verify fail**

```bash
go test ./internal/picker/ -run TestRenderPreview -v
```

Expected: FAIL — all four render the placeholder text from the Task 5 stub.

- [ ] **Step 3: Implement renderPreview**

Replace the stub in `internal/picker/preview.go`:

```go
// renderPreview renders the right-most preview pane. width is the cell budget
// (including the rounded border). Height comes from m.height.
func (m PickerModel) renderPreview(width int) string {
    // Inside-frame height: total height minus footer (1) minus border top/bottom (2).
    innerHeight := m.height - 3
    if innerHeight < 3 {
        innerHeight = 3
    }
    frame := previewFrame.Width(width).Height(innerHeight)

    if m.focus != focusTree {
        return frame.Render(rowDim.Render("(focus a pane to preview)"))
    }
    nodes := m.visibleNodes()
    if m.treeCursor < 0 || m.treeCursor >= len(nodes) {
        return frame.Render(rowDim.Render("(focus a pane to preview)"))
    }
    n := nodes[m.treeCursor]
    if n.Kind != NodePane {
        return frame.Render(rowDim.Render("(focus a pane to preview)"))
    }
    p, _ := n.Ref.(*snapshot.Pane)
    if p == nil || p.ScrollbackSHA == "" {
        return frame.Render(rowDim.Render("(no scrollback recorded)"))
    }
    sha := p.ScrollbackSHA
    if err := m.scrollbackErrors[sha]; err != nil {
        return frame.Render(footerWarn.Render("(scrollback unavailable: " + err.Error() + ")"))
    }
    content, ok := m.scrollbacks[sha]
    if !ok {
        if m.loadingSHAs[sha] {
            return frame.Render(rowDim.Render("(loading...)"))
        }
        // Not loading yet — PreviewCmd will schedule on next key event.
        return frame.Render(rowDim.Render("(preview pending)"))
    }
    return frame.Render(tailLines(string(content), innerHeight))
}

// tailLines returns the last n lines of s, joined by "\n". If s has fewer
// lines, all are returned. ANSI escapes are preserved verbatim — the saved
// scrollback already encodes terminal-state for replay.
func tailLines(s string, n int) string {
    if n <= 0 {
        return ""
    }
    // Use strings.Split — cheap, no need to be clever for typical scrollback
    // sizes (≤ a few hundred KB).
    lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
    if len(lines) <= n {
        return strings.Join(lines, "\n")
    }
    return strings.Join(lines[len(lines)-n:], "\n")
}
```

Add the `strings` import to `preview.go` if not present.

- [ ] **Step 4: Verify pass**

```bash
go test ./internal/picker/ -v
```

Expected: PASS, all picker tests.

- [ ] **Step 5: Commit**

```bash
git add internal/picker/preview.go internal/picker/preview_test.go
git commit -m "feat(picker): render scrollback preview with state-aware fallbacks"
```

---

### Task 7: Update help + footer

**Files:**
- Modify: `internal/picker/view.go`
- Modify: `internal/picker/keys.go` (optional — only if adding new bindings)

Update the footer to indicate when preview is visible, and add a quick legend ("p: preview pane").

For v1 we are NOT adding viewport scrolling inside the preview pane (tail of N lines is enough). If the user later wants PageUp/PageDown to scroll the preview, that's a follow-up. So no key.go changes.

- [ ] **Step 1: Add a small visual cue to the footer when preview is visible**

In `internal/picker/view.go` `renderFooter`, add an extra part:

```go
parts := []string{
    on(m.filter.SkipIdleShells, "skip idle"),
    on(m.filter.DedupRunningServer, "dedup running"),
    on(m.dimOlderThan > 0, "age≤24h"),
    "  " + counter,
    "  ↵ restore",
}
if m.width >= 120 && m.mode == ModeSnapshot {
    parts = append(parts, "  tab: focus tree to preview")
}
```

- [ ] **Step 2: Run the existing footer test (if any) + manual smoke**

```bash
go test ./internal/picker/ -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/picker/view.go
git commit -m "feat(picker): footer hint for preview pane when wide"
```

---

### Task 8: End-to-end manual verification in tmux-state

**Files:** none — manual.

The unit tests cover the logic. Now drive the real picker once before merging.

- [ ] **Step 1: Build and install locally**

```bash
cd /home/noams/Data/git/noamsto/tmux-state/.worktrees/feat-picker-tree
go build -o /tmp/tmux-state-feat ./cmd/tmux-state
```

- [ ] **Step 2: Seed a real snapshot**

In a separate tmux server (don't disturb the user's main one):

```bash
TMUX_TMPDIR=/tmp/tmux-state-test mkdir -p /tmp/tmux-state-test
TMUX_TMPDIR=/tmp/tmux-state-test tmux new-session -d -s probe -n w1 -x 200 -y 50 'echo hello world; sleep 5'
TMUX_TMPDIR=/tmp/tmux-state-test /tmp/tmux-state-feat save --reason=manual
```

- [ ] **Step 3: Open the picker**

```bash
TMUX_TMPDIR=/tmp/tmux-state-test /tmp/tmux-state-feat pick --kind=snapshot
```

Expected: 3-pane UI at wide terminal (≥120 cols). Tab to tree, navigate to the pane node, scrollback content (`hello world`) appears in the preview pane. Confirm:

- `(focus a pane to preview)` shown when on list pane
- `(no scrollback recorded)` if pane lacked SHA (force-restart with `-x 200 -y 50` and no command if needed to reproduce)
- Loaded content shows last N lines
- Enter restores (don't actually press if you don't want to disturb your session — Esc to cancel)

- [ ] **Step 4: Clean up**

```bash
TMUX_TMPDIR=/tmp/tmux-state-test tmux kill-server
rm -rf /tmp/tmux-state-test
```

- [ ] **Step 5: Nothing to commit (manual step) — proceed to Task 9**

---

### Task 9: Run the full tmux-state check suite

**Files:** none — verification.

- [ ] **Step 1: Run everything**

```bash
cd /home/noams/Data/git/noamsto/tmux-state/.worktrees/feat-picker-tree
go test ./...
go vet ./...
nix flake check
```

Expected: all green. If `nix flake check` fails on `vendorHash`, follow the printed instructions to update it (likely a `chore: bump vendorHash` commit).

- [ ] **Step 2: If vendorHash bump needed, commit it**

```bash
git add flake.nix
git commit -m "chore(flake): bump vendorHash after preview deps"
```

---

## Phase 2: Cross-Repo Integration

### Task 10: Merge feat/picker-tree → main in tmux-state

**Files:** `main` branch in tmux-state.

- [ ] **Step 1: Push feat/picker-tree**

```bash
cd /home/noams/Data/git/noamsto/tmux-state/.worktrees/feat-picker-tree
git push -u origin feat/picker-tree
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create --assignee @me --title "feat(picker): bubbletea TUI with scrollback preview" --body "$(cat <<'EOF'
## Summary
- Replaces the fzf wrapper in `tmux-state pick` with a 3-pane Bubble Tea TUI
- Left: snapshot list with age dim. Middle: manifest tree with filter decoration. Right: scrollback preview for the focused pane.
- Catppuccin Mocha palette, lazy manifest parse, per-SHA scrollback cache.

## Test plan
- [ ] `go test ./...` green
- [ ] `nix flake check` green
- [ ] Manual: `pick --kind=snapshot` shows 3 panes at ≥120 cols; Tab focuses tree; navigating to a pane node loads scrollback into the right pane.
EOF
)"
```

- [ ] **Step 3: Wait for CI, merge**

Once CI is green and you've self-reviewed, merge via the GitHub UI (squash or rebase as the repo convention dictates).

```bash
gh pr merge --auto --squash
```

---

### Task 11: Tag a tmux-state release

**Files:** `main` branch in tmux-state.

- [ ] **Step 1: Pull main, bump Version constant**

```bash
cd /home/noams/Data/git/noamsto/tmux-state
git checkout main
git pull
```

Edit `cmd/tmux-state/main.go`:

```go
const Version = "0.2.0"
```

(Bump from `0.1.0`.)

```bash
git add cmd/tmux-state/main.go
git commit -m "chore: bump version to 0.2.0"
```

- [ ] **Step 2: Tag and push**

```bash
git tag -a v0.2.0 -m "v0.2.0 — bubbletea picker with scrollback preview"
git push origin main
git push origin v0.2.0
```

---

### Task 12: Bump lazytmux flake input + verify prefix+R

**Files:**
- Modify: `/home/noams/Data/git/noamsto/lazytmux/flake.lock` (regenerated)

- [ ] **Step 1: Update the input**

```bash
cd /home/noams/Data/git/noamsto/lazytmux
nix flake update tmux-state
```

- [ ] **Step 2: Build**

```bash
nix build .
```

Expected: clean build. If `vendorHash` mismatches, the error tells you the new hash — paste into `packages/tmux-state-pkg.nix` or wherever the package is constructed, commit.

- [ ] **Step 3: Commit the lock**

```bash
git add flake.lock
git commit -m "chore(flake): bump tmux-state to v0.2.0 (bubbletea picker)"
```

- [ ] **Step 4: Apply via home-manager**

```bash
cd /home/noams/nix-config
nh home switch
```

- [ ] **Step 5: Reload tmux config and test prefix+R**

In a running tmux session:

```
prefix + r        # reload config
prefix + R        # open snapshot picker
```

Expected: 3-pane Bubble Tea TUI (assuming terminal width ≥ 120). Tab to focus tree, navigate to a pane node, scrollback preview shows on the right.

If the popup geometry feels cramped, tweak the binding in `modules/home-manager.nix`:

```nix
bind   R    display-popup -E -w 95% -h 90% -b rounded -T " Snapshots "     'env -u FZF_DEFAULT_OPTS ${tmuxStateBin} pick --kind=snapshot'
```

- [ ] **Step 6: Commit any binding tweaks**

```bash
cd /home/noams/Data/git/noamsto/lazytmux
git add modules/home-manager.nix
git commit -m "feat(persist): widen snapshot picker popup for 3-pane layout"
```

- [ ] **Step 7: Push lazytmux**

```bash
git push
```

---

## Done When

- `prefix + R` opens a 3-pane Bubble Tea TUI in a wide terminal.
- Focusing the tree on a pane node shows that pane's saved scrollback in the right pane.
- Panes without `ScrollbackSHA` show `(no scrollback recorded)`; missing files show an error; load is async (no UI freeze).
- All states have unit tests; the picker test suite is green.
- `nix flake check` is green in both tmux-state and lazytmux.
- The lazytmux `flake.lock` points at the tagged `v0.2.0` release of tmux-state.
