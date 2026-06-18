# Consolidated Line-0 Status Binary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three render-only `#()` subprocess calls in `status-format[0]` (`tmux-branch-display`, `tmux-dir-display`, `claude-status`) plus the inline `#{?…}` segment logic with a single Go binary `tmux-statusline`, cutting the per-redraw fork count from 5 to 3 and collapsing the unreadable nested-conditional format string into testable Go.

**Architecture:** A new `statusline` subpackage in the existing `picker/` Go module emits the entire line-0 markup string in one fork. tmux expands every `#{…}` input as a pre-expanded argv before exec, so the binary makes **zero** additional tmux IPC calls. The two remaining `#()` calls (`tmux-update-icons`, `tmux-pr-enrich --tick`) stay — they are side-effect daemons that also feed the window grid (lines 1–3), not render calls. The `claude-status` script is left in place for external callers (the `gum` sesh-picker format); only its line-0 invocation is replaced.

**Tech Stack:** Go (stdlib only — `flag`, `os`, `strings`, `strconv`, `time`, `path/filepath`), `buildGoModule` (existing `picker/default.nix`), tmux format strings, Nix.

---

## Scope boundary (read before starting)

| Current line-0 `#()` call | Kind | Fate in this plan |
|---|---|---|
| `tmux-update-icons '#{session_name}'` | side-effect (sets `@window_icon_*`, `@active_pane_icon`; feeds lines 1–3) | **unchanged** |
| `tmux-pr-enrich --tick` (enrich only) | side-effect daemon | **unchanged** |
| `tmux-branch-display` | render | folded into `tmux-statusline` |
| `tmux-dir-display` | render | folded into `tmux-statusline` |
| `claude-status --session … --format icon-color` | render (+ reads `/tmp/claude-status`) | folded into `tmux-statusline` |
| inline `#{?…}` session / issue-or-branch / PR-badge / pane-cmd logic | render | folded into `tmux-statusline` |

**Out of scope:** lines 1–3 (window grid), the reflow engine, `tmux-update-icons`, `tmux-pr-enrich`, the `claude-status` script itself, and any vertical-spacing change. No behavior change is intended — output must match the current line 0 byte-for-byte for equivalent state.

**Deliberate non-DRY:** `picker/main.go` already has a `collectClaudePanes`/`readPaneIssues` reader, but it computes a boolean `stale` rather than the 0–100 fade gradient line 0 needs, and lives in `package main` of the picker. This plan gives `statusline` its own focused claude reader rather than refactoring the working picker into a shared internal package. The duplication is ~60 lines and the two readers have genuinely different needs (winIdx resolution vs. fade gradient). A future consolidation into `picker/internal/claudestate` is a reasonable follow-up but is **not** part of this plan.

---

## File Structure

- **Create** `picker/statusline/main.go` — arg parsing (`flag`), per-segment writers, `main()` that assembles and prints the full line-0 string.
- **Create** `picker/statusline/claude.go` — theme detection, per-state catppuccin hue palettes, fade interpolation, session-scoped pane aggregation, priority state, issue-list formatting. Self-contained port of the `lib-claude.sh` pieces line 0 uses.
- **Create** `picker/statusline/main_test.go` — golden-output tests for each segment and the full line.
- **Create** `picker/statusline/claude_test.go` — fade math, staleness, priority, and session-aggregation tests against a temp `CLAUDE_STATUS_DIR`.
- **Modify** `picker/default.nix` — add `"statusline"` to `subPackages`; add the `mv` rename in `postInstall`.
- **Modify** `config/tmux.conf.nix` — add `picker-statusline-bin`; rewrite the `status-format[0]` line (line ~550).

---

## Task 1: Verification gate — does `#[align=right]` survive inside `#()` output?

The maximal fold relies on the binary emitting `#[align=right]` mid-string and tmux honoring it. If tmux does **not** re-interpret the `align` directive in `#()` command output, the right-aligned PR badge + pane-cmd would render as literal text and the plan must fall back to splitting the right side back into the tmux-owned format. (Only `align` matters — the session-name click is region-wide, not range-based, so no `#[range=…]` is involved.) Front-load this.

**Files:** none (throwaway tmux experiment against the built binary).

- [ ] **Step 1: Build the current tree**

Run: `nix build .#default`
Expected: `./result/bin/tmux` exists.

- [ ] **Step 2: Probe align + range survival in `#()` output**

Run (fish):
```fish
./result/bin/tmux -L probe new-session -d -x 80 -y 10
./result/bin/tmux -L probe set -g status-format[0] "L#(printf '%s' '#[align=right]R')"
./result/bin/tmux -L probe set -g status 1
./result/bin/tmux -L probe refresh-client -S
sleep 0.3
./result/bin/tmux -L probe capturep -p -t : -E 0 -S 0
./result/bin/tmux -L probe kill-server
```
Expected: the captured status line shows `L` flush-left and `R` flush-right (align honored). If instead it shows the literal text `#[align=right]...R`, align does **not** survive `#()` output.

- [ ] **Step 3: Record the verdict and branch the plan if needed**

If align survives (expected): proceed with Tasks 2–10 as written (one fork).
If align does **not** survive: STOP and amend the plan — keep `#[align=right]` in the tmux format and split into two `#()` calls (`tmux-statusline --side left` and `--side right`), accepting 4 forks instead of 3. Note the verdict in the PR description either way.

---

## Task 2: Claude theme + fade primitives (`claude.go`)

Port the color/fade math from `scripts/lib-claude.sh:103-161`. Pure functions, no I/O — fully unit-testable.

**Files:**
- Create: `picker/statusline/claude.go`
- Test: `picker/statusline/claude_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package main

import "testing"

func TestPaletteSelectsByTheme(t *testing.T) {
	dark := claudePalette("dark")
	if dark.waiting != "#fab387" {
		t.Fatalf("dark waiting = %q, want #fab387", dark.waiting)
	}
	light := claudePalette("light")
	if light.waiting != "#fe640b" {
		t.Fatalf("light waiting = %q, want #fe640b", light.waiting)
	}
}

func TestFadeHexEndpoints(t *testing.T) {
	if got := fadeHex("#000000", "#ffffff", 0); got != "#000000" {
		t.Fatalf("pct 0 = %q, want #000000", got)
	}
	if got := fadeHex("#000000", "#ffffff", 100); got != "#ffffff" {
		t.Fatalf("pct 100 = %q, want #ffffff", got)
	}
	if got := fadeHex("#000000", "#ffffff", 50); got != "#7f7f7f" {
		t.Fatalf("pct 50 = %q, want #7f7f7f", got)
	}
}

func TestFadedHueUnseenPinsBright(t *testing.T) {
	p := claudePalette("dark")
	// unseen pins fade to 0 → stays bright waiting hue
	if got := p.fadedHue("waiting", 100, true); got != "#fab387" {
		t.Fatalf("unseen waiting = %q, want bright #fab387", got)
	}
	// fully faded → dim idle hue
	if got := p.fadedHue("waiting", 100, false); got != p.idle {
		t.Fatalf("faded waiting = %q, want idle %q", got, p.idle)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run 'TestPalette|TestFade' -v`
Expected: FAIL (undefined: `claudePalette`, `fadeHex`).

- [ ] **Step 3: Implement the primitives**

```go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// statePalette holds the per-state catppuccin hues for one theme, mirroring
// the H_* values in lib-claude.sh setup_claude_colors.
type statePalette struct {
	waiting, compacting, processing, done, idle, errorC, denied string
}

func claudePalette(theme string) statePalette {
	if theme == "light" { // Latte
		return statePalette{
			waiting: "#fe640b", compacting: "#04a5e5", processing: "#179299",
			done: "#40a02b", idle: "#6c6f85", errorC: "#d20f39", denied: "#df8e1d",
		}
	}
	// Mocha (default)
	return statePalette{
		waiting: "#fab387", compacting: "#89dceb", processing: "#94e2d5",
		done: "#a6e3a1", idle: "#6c7086", errorC: "#f38ba8", denied: "#f9e2af",
	}
}

func (p statePalette) hue(state string) string {
	switch state {
	case "waiting":
		return p.waiting
	case "compacting":
		return p.compacting
	case "processing":
		return p.processing
	case "done":
		return p.done
	case "idle":
		return p.idle
	case "error":
		return p.errorC
	case "denied":
		return p.denied
	}
	return ""
}

// fadedHue eases a state's hue toward the dim idle hue by pct (0..100).
// unseen pins to full color. Empty string for an unknown state.
func (p statePalette) fadedHue(state string, pct int, unseen bool) string {
	base := p.hue(state)
	if base == "" {
		return ""
	}
	if unseen {
		pct = 0
	}
	if pct <= 0 {
		return base
	}
	return fadeHex(base, p.idle, pct)
}

// fadeHex linearly interpolates between two #rrggbb colors; pct 0 = from, 100 = to.
func fadeHex(from, to string, pct int) string {
	fr, fg, fb := hexBytes(from)
	tr, tg, tb := hexBytes(to)
	return fmt.Sprintf("#%02x%02x%02x",
		fr+(tr-fr)*pct/100,
		fg+(tg-fg)*pct/100,
		fb+(tb-fb)*pct/100)
}

func hexBytes(h string) (int, int, int) {
	r, _ := strconv.ParseInt(h[1:3], 16, 0)
	g, _ := strconv.ParseInt(h[3:5], 16, 0)
	b, _ := strconv.ParseInt(h[5:7], 16, 0)
	return int(r), int(g), int(b)
}

// detectTheme reads $XDG_STATE_HOME/theme-state.json (default ~/.local/state),
// returning "light" or "dark" (the default). Matches lib-claude.sh.
func detectTheme() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = os.Getenv("HOME") + "/.local/state"
	}
	data, err := os.ReadFile(stateHome + "/theme-state.json")
	if err != nil {
		return "dark"
	}
	// minimal parse: find "theme"\s*:\s*"VALUE"
	s := string(data)
	i := strings.Index(s, "\"theme\"")
	if i < 0 {
		return "dark"
	}
	rest := s[i+7:]
	c := strings.Index(rest, ":")
	if c < 0 {
		return "dark"
	}
	rest = rest[c+1:]
	q1 := strings.Index(rest, "\"")
	if q1 < 0 {
		return "dark"
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, "\"")
	if q2 < 0 {
		return "dark"
	}
	if rest[:q2] == "light" {
		return "light"
	}
	return "dark"
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -run 'TestPalette|TestFade' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/claude.go picker/statusline/claude_test.go
git commit -m "feat(statusline): claude theme + fade color primitives"
```

---

## Task 3: Claude staleness, priority, and session aggregation (`claude.go`)

Port `read_pane_state` fade computation (`lib-claude.sh:40-81`), `claude_priority_state` (`:182-201`), `format_issue_list` (`:219-232`), and the `count_for_session` loop (`claude-status.sh:69-79`). Session mode needs **no** tmux call — it filters pane files by their `session=` field.

**Files:**
- Modify: `picker/statusline/claude.go`
- Test: `picker/statusline/claude_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestPriorityState(t *testing.T) {
	cases := []struct {
		c    counts
		want string
	}{
		{counts{errorN: 1, waiting: 1}, "error"},
		{counts{waiting: 1, processing: 3}, "waiting"},
		{counts{denied: 1, compacting: 1}, "denied"},
		{counts{processing: 2, done: 1}, "processing"},
		{counts{idle: 1}, "idle"},
		{counts{}, ""},
	}
	for _, c := range cases {
		if got := c.c.priorityState(); got != c.want {
			t.Errorf("%+v = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestFadePct(t *testing.T) {
	now := int64(1000)
	// waiting threshold 30, fade duration 45
	if got := fadePct("waiting", now, now-10); got != 0 {
		t.Errorf("fresh waiting = %d, want 0", got)
	}
	if got := fadePct("waiting", now, now-30-45); got != 100 {
		t.Errorf("fully stale waiting = %d, want 100", got)
	}
	// halfway through the fade window: age = 30 + 22 → ~48
	if got := fadePct("waiting", now, now-52); got < 40 || got > 55 {
		t.Errorf("mid-fade waiting = %d, want ~48", got)
	}
}

func TestFormatIssueList(t *testing.T) {
	if got := formatIssueList(3, []string{"ENG-1", "ENG-2"}); got != "ENG-1 ENG-2" {
		t.Errorf("got %q", got)
	}
	if got := formatIssueList(3, []string{"A", "B", "C", "D", "E"}); got != "A B C +2" {
		t.Errorf("got %q", got)
	}
	if got := formatIssueList(3, nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestAggregateSessionFromDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/panes", 0o755)
	os.MkdirAll(dir+"/issues", 0o755)
	now := int64(2000)
	// fresh waiting in session "work"
	os.WriteFile(dir+"/panes/1", []byte("state=waiting\ntimestamp=2000\nsession=work\n"), 0o644)
	// processing in another session — must be ignored
	os.WriteFile(dir+"/panes/2", []byte("state=processing\ntimestamp=2000\nsession=other\n"), 0o644)
	os.WriteFile(dir+"/issues/1", []byte("ENG-9\n"), 0o644)

	agg := aggregateSession(dir, "work", now)
	if agg.total != 1 {
		t.Fatalf("total = %d, want 1", agg.total)
	}
	if agg.counts.priorityState() != "waiting" {
		t.Fatalf("state = %q, want waiting", agg.counts.priorityState())
	}
	if len(agg.issues) != 1 || agg.issues[0] != "ENG-9" {
		t.Fatalf("issues = %v, want [ENG-9]", agg.issues)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run 'TestPriority|TestFadePct|TestFormatIssue|TestAggregate' -v`
Expected: FAIL (undefined: `counts`, `fadePct`, `formatIssueList`, `aggregateSession`).

- [ ] **Step 3: Implement aggregation**

```go
import (
	"path/filepath"
	"sort"
)

type counts struct {
	processing, waiting, compacting, done, idle, errorN, denied, total int
}

func (c counts) priorityState() string {
	switch {
	case c.errorN > 0:
		return "error"
	case c.waiting > 0:
		return "waiting"
	case c.denied > 0:
		return "denied"
	case c.compacting > 0:
		return "compacting"
	case c.processing > 0:
		return "processing"
	case c.done > 0:
		return "done"
	case c.idle > 0:
		return "idle"
	}
	return ""
}

func (c *counts) tally(state string) {
	c.total++
	switch state {
	case "processing":
		c.processing++
	case "waiting":
		c.waiting++
	case "compacting":
		c.compacting++
	case "done":
		c.done++
	case "idle":
		c.idle++
	case "error":
		c.errorN++
	case "denied":
		c.denied++
	}
}

// fadePct mirrors read_pane_state: bright until the state's staleness threshold,
// then linear ramp to 100 over 45s.
func fadePct(state string, now, ts int64) int {
	if ts == 0 {
		return 0
	}
	const fadeDuration = 45
	start := map[string]int64{
		"waiting": 30, "compacting": 60, "processing": 300,
		"done": 60, "error": 120, "denied": 60,
	}[state]
	if start == 0 {
		return 0
	}
	age := now - ts
	if age <= start {
		return 0
	}
	if age >= start+fadeDuration {
		return 100
	}
	return int((age - start) * 100 / fadeDuration)
}

type sessionAgg struct {
	counts  counts
	minFade int
	unseen  bool
	issues  []string
}

// aggregateSession scans <dir>/panes/*, filters by the session= field, and
// tallies state + freshest fade + issue ids. No tmux call — session mode keys
// entirely off the file's session field.
func aggregateSession(dir, session string, now int64) sessionAgg {
	agg := sessionAgg{minFade: 100}
	entries, err := os.ReadDir(filepath.Join(dir, "panes"))
	if err != nil {
		return agg
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "panes", e.Name()))
		if err != nil {
			continue
		}
		var state, sess string
		var ts int64
		var unseen bool
		for _, line := range strings.Split(string(data), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "state":
				state = v
			case "session":
				sess = v
			case "timestamp":
				ts, _ = strconv.ParseInt(v, 10, 64)
			case "unseen":
				unseen = v == "1"
			}
		}
		if state == "" || sess != session {
			continue
		}
		agg.counts.tally(state)
		if f := fadePct(state, now, ts); f < agg.minFade {
			agg.minFade = f
		}
		if unseen {
			agg.unseen = true
		}
		for _, id := range readIssueFile(filepath.Join(dir, "issues", e.Name())) {
			if id != "" && !seen[id] {
				seen[id] = true
				agg.issues = append(agg.issues, id)
			}
		}
	}
	return agg
}

func readIssueFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	line, _, _ := strings.Cut(string(data), "\n") // bash collect_pane_issues reads only the first line
	return strings.Split(strings.TrimSpace(line), ",")
}

func formatIssueList(max int, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) <= max {
		return strings.Join(ids, " ")
	}
	return strings.Join(ids[:max], " ") + " +" + strconv.Itoa(len(ids)-max)
}

var _ = sort.Strings // keep import tidy if unused after edits
```

Remove the `sort` import + the `_ =` line if `sort` ends up unused.

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -run 'TestPriority|TestFadePct|TestFormatIssue|TestAggregate' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/claude.go picker/statusline/claude_test.go
git commit -m "feat(statusline): claude session aggregation + staleness fade"
```

---

## Task 4: Claude segment renderer

Reproduce `claude-status --session … --format icon-color` exactly: empty when no panes; else `#[fg=HUE]ICON#[fg=default] ` followed by `#[fg=IDLE]ids#[fg=default] ` when issues exist. Session mode emits **no** leading space.

**Files:**
- Modify: `picker/statusline/claude.go`
- Test: `picker/statusline/claude_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestClaudeSegment(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/panes", 0o755)
	os.MkdirAll(dir+"/issues", 0o755)
	now := int64(5000)
	os.WriteFile(dir+"/panes/7", []byte("state=waiting\ntimestamp=5000\nsession=s\n"), 0o644)
	os.WriteFile(dir+"/issues/7", []byte("ENG-1\n"), 0o644)

	got := claudeSegment(dir, "s", "dark", now)
	want := "#[fg=#fab387]󰔟#[fg=default] #[fg=#6c7086]ENG-1#[fg=default] "
	if got != want {
		t.Fatalf("claudeSegment\n got %q\nwant %q", got, want)
	}

	// no panes for the session → empty
	if got := claudeSegment(dir, "absent", "dark", now); got != "" {
		t.Fatalf("absent session = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run TestClaudeSegment -v`
Expected: FAIL (undefined: `claudeSegment`).

- [ ] **Step 3: Implement**

```go
var spinnerFrames = []string{"󰪞", "󰪟", "󰪠", "󰪡", "󰪢", "󰪣", "󰪤", "󰪥"}

func stateIcon(state string, now int64) string {
	switch state {
	case "processing":
		return spinnerFrames[now%int64(len(spinnerFrames))]
	case "waiting":
		return "󰔟"
	case "compacting":
		return "󰡍"
	case "done":
		return "󰸞"
	case "idle":
		return "󰒲"
	case "error":
		return "󰅚"
	case "denied":
		return "󰔟"
	}
	return ""
}

// claudeSegment mirrors `claude-status --session <s> --format icon-color`.
func claudeSegment(dir, session, theme string, now int64) string {
	agg := aggregateSession(dir, session, now)
	if agg.counts.total == 0 {
		return ""
	}
	state := agg.counts.priorityState()
	icon := stateIcon(state, now)
	if icon == "" {
		return ""
	}
	pal := claudePalette(theme)
	hue := pal.fadedHue(state, agg.minFade, agg.unseen)
	out := "#[fg=" + hue + "]" + icon + "#[fg=default] "
	if list := formatIssueList(3, agg.issues); list != "" {
		out += "#[fg=" + pal.idle + "]" + list + "#[fg=default] "
	}
	return out
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -run TestClaudeSegment -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/claude.go picker/statusline/claude_test.go
git commit -m "feat(statusline): claude icon-color segment renderer"
```

---

## Task 5: Branch + dir segment renderers (`main.go`)

Port `tmux-branch-display.sh` and `tmux-dir-display.sh`. Both may shell out to `git` only when their cached tmux option is empty — identical to the scripts.

**Files:**
- Create: `picker/statusline/main.go`
- Test: `picker/statusline/main_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestBranchDisplay(t *testing.T) {
	if got := branchDisplay("feat/x", "/anything"); got != "feat/x" {
		t.Fatalf("got %q, want feat/x", got)
	}
}

func TestDirDisplay(t *testing.T) {
	if got := dirDisplay("/repo", "/repo"); got != "./" {
		t.Fatalf("at root = %q, want ./", got)
	}
	if got := dirDisplay("/repo/src/app", "/repo"); got != "./src/app" {
		t.Fatalf("subdir = %q, want ./src/app", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run 'TestBranch|TestDir' -v`
Expected: FAIL (undefined: `branchDisplay`, `dirDisplay`).

- [ ] **Step 3: Implement**

```go
package main

import (
	"os"
	"os/exec"
	"strings"
)

// branchDisplay mirrors tmux-branch-display.sh: prefer the cached @branch,
// else `git -C <path> branch --show-current`.
func branchDisplay(branch, panePath string) string {
	if branch != "" {
		return branch
	}
	if panePath == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", panePath, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dirDisplay mirrors tmux-dir-display.sh: path relative to git root as ./sub,
// "./" at root, else ~-collapsed absolute path.
func dirDisplay(panePath, gitRoot string) string {
	if gitRoot == "" && panePath != "" {
		out, err := exec.Command("git", "-C", panePath, "rev-parse", "--show-toplevel").Output()
		if err == nil {
			gitRoot = strings.TrimSpace(string(out))
		}
	}
	if gitRoot != "" && strings.HasPrefix(panePath, gitRoot) {
		if panePath == gitRoot {
			return "./"
		}
		return "./" + strings.TrimPrefix(panePath, gitRoot+"/")
	}
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(panePath, home) {
		return "~" + strings.TrimPrefix(panePath, home)
	}
	return panePath
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -run 'TestBranch|TestDir' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/main.go picker/statusline/main_test.go
git commit -m "feat(statusline): branch + dir segment renderers"
```

---

## Task 6: Session + issue/branch segment

Reproduce the left-edge segment from `config/tmux.conf.nix:550`: prefix-red when `client_prefix`, else `@claude_session_fg` if set, else mauve; session icon + name; then either the issue badge (when `@issue_id` set **and** `@issue_branch == @branch`) or the branch icon + branch name. No click-range markup is needed — the session-name click is handled by the region-wide `bind -T root MouseDown1StatusLeft choose-tree -Zs` (`config/tmux.conf.nix:474`), not a `#[range=…]`, so it keeps working unchanged.

**Files:**
- Modify: `picker/statusline/main.go`
- Test: `picker/statusline/main_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSessionSegmentBranchVariant(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x", panePath: "/repo",
		iconSession: "S", iconBranch: "B",
		thmRed: "#f00", thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd", claudeFg: "",
	}
	got := sessionSegment(a, false) // not in prefix
	want := "#[fg=#c6a] S work  #[fg=#89b,bold]B feat/x"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestSessionSegmentIssueVariant(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x",
		issueID: "ENG-7", issueBranch: "feat/x", issueProvider: "linear", issueTitle: "Do it",
		iconSession: "S", iconLinear: "L", iconGitHub: "G",
		thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd", claudeFg: "",
	}
	got := sessionSegment(a, false)
	want := "#[fg=#c6a] S work  #[fg=#89b,bold]L ENG-7 #[fg=#cdd,nobold]Do it"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestSessionSegmentPrefixColor(t *testing.T) {
	a := args{session: "s", iconSession: "S", thmRed: "#f00", thmMauve: "#c6a", branch: "m", iconBranch: "B", thmBlue: "#89b"}
	got := sessionSegment(a, true) // prefix active → red+bold
	if !strings.HasPrefix(got, "#[fg=#f00,bold] S s") {
		t.Fatalf("prefix variant = %q", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run TestSessionSegment -v`
Expected: FAIL (undefined: `args`, `sessionSegment`).

- [ ] **Step 3: Implement**

Add the `args` struct (all line-0 inputs) and the renderer. The `args` struct accumulates fields across Tasks 6–9; define the full struct now so later tasks only read from it.

```go
type args struct {
	session, prefix string
	issueID, issueBranch, issueProvider, issueTitle string
	branch, panePath, gitRoot string
	prNumber, prBranch, prState, prCheck, prMergeable, prTitle string
	paneIcon, paneCmd, claudeFg string

	// theme palette (passed pre-expanded from tmux @thm_* options)
	thmBg, thmRed, thmMauve, thmBlue, thmText, thmSubtext0 string
	thmOverlay0, thmOverlay1, thmPeach, thmGreen string

	// glyphs (tmux @icon_* options + Nix enrich icon set)
	iconSession, iconBranch, iconDir string
	iconLinear, iconGitHub, iconPending, iconSuccess, iconFailure, iconMerged, iconConflict string
}

// sessionSegment renders the leading session + issue-or-branch chunk.
func sessionSegment(a args, prefixActive bool) string {
	var b strings.Builder
	switch {
	case prefixActive:
		b.WriteString("#[fg=" + a.thmRed + ",bold]")
	case a.claudeFg != "":
		b.WriteString("#[fg=" + a.claudeFg + "]")
	default:
		b.WriteString("#[fg=" + a.thmMauve + "]")
	}
	b.WriteString(" " + a.iconSession + " " + a.session + "  ")

	if a.issueID != "" && a.issueBranch == a.branch {
		glyph := a.iconGitHub
		if a.issueProvider == "linear" {
			glyph = a.iconLinear
		}
		b.WriteString("#[fg=" + a.thmBlue + ",bold]" + glyph + " " + a.issueID +
			" #[fg=" + a.thmText + ",nobold]" + a.issueTitle)
	} else {
		b.WriteString("#[fg=" + a.thmBlue + ",bold]" + a.iconBranch + " " +
			branchDisplay(a.branch, a.panePath))
	}
	return b.String()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -run TestSessionSegment -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/main.go picker/statusline/main_test.go
git commit -m "feat(statusline): session + issue/branch segment"
```

---

## Task 7: PR badge segment

Port the nested PR-badge conditional from line 550: render only when `@pr_number` is set, not `none`, and `@pr_branch == @branch`. Color precedence: conflict/failure → red, pending → peach, merged → mauve, closed → overlay_0, else green. Glyph precedence: conflict, then check state (failure/pending), then state (merged), else success. Note the literal `#` in `#{@pr_number}` is emitted as `###{@pr_number}` in tmux format — but here we have the actual number, so we emit `#` + number directly.

**Files:**
- Modify: `picker/statusline/main.go`
- Test: `picker/statusline/main_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPRBadgeHidden(t *testing.T) {
	if got := prBadge(args{prNumber: "", branch: "x", prBranch: "x"}); got != "" {
		t.Fatalf("empty pr = %q, want empty", got)
	}
	if got := prBadge(args{prNumber: "none", branch: "x", prBranch: "x"}); got != "" {
		t.Fatalf("none pr = %q, want empty", got)
	}
	if got := prBadge(args{prNumber: "5", branch: "x", prBranch: "y"}); got != "" {
		t.Fatalf("branch mismatch = %q, want empty", got)
	}
}

func TestPRBadgeSuccess(t *testing.T) {
	a := args{
		prNumber: "42", branch: "x", prBranch: "x", prState: "open", prCheck: "success",
		thmGreen: "#0f0", iconSuccess: "OK", prTitle: "Title",
	}
	want := "#[fg=#0f0]OK #42 Title  "
	if got := prBadge(a); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestPRBadgeConflictWinsColorAndGlyph(t *testing.T) {
	a := args{
		prNumber: "9", branch: "x", prBranch: "x", prState: "open",
		prCheck: "success", prMergeable: "conflicting",
		thmRed: "#f00", iconConflict: "CF", prTitle: "T",
	}
	want := "#[fg=#f00]CF #9 T  "
	if got := prBadge(a); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run TestPRBadge -v`
Expected: FAIL (undefined: `prBadge`).

- [ ] **Step 3: Implement**

```go
// prBadge renders the right-side PR badge, matching the nested conditional in
// status-format[0]. Empty unless pr_number is a real number for this branch.
func prBadge(a args) string {
	if a.prNumber == "" || a.prNumber == "none" || a.prBranch != a.branch {
		return ""
	}

	var color string
	switch {
	case a.prCheck == "failure" || a.prMergeable == "conflicting":
		color = a.thmRed
	case a.prCheck == "pending":
		color = a.thmPeach
	case a.prState == "merged":
		color = a.thmMauve
	case a.prState == "closed":
		color = a.thmOverlay0
	default:
		color = a.thmGreen
	}

	var glyph string
	switch {
	case a.prMergeable == "conflicting":
		glyph = a.iconConflict
	case a.prCheck == "failure":
		glyph = a.iconFailure
	case a.prCheck == "pending":
		glyph = a.iconPending
	case a.prState == "merged":
		glyph = a.iconMerged
	default:
		glyph = a.iconSuccess
	}

	return "#[fg=" + color + "]" + glyph + " #" + a.prNumber + " " + a.prTitle + "  "
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -run TestPRBadge -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/main.go picker/statusline/main_test.go
git commit -m "feat(statusline): PR badge segment"
```

---

## Task 8: Full-line assembly + `main()` + golden test

Wire the segments into the complete line-0 string, including the dir segment, the claude segment, the `#[align=right]` pivot, and the pane-cmd tail (strip a `-wrapped` suffix and leading `.`, mirroring `#{s|^\.(.*)-wrapped$|\1|:pane_current_command}`). Parse all inputs with `flag`.

**Files:**
- Modify: `picker/statusline/main.go`
- Test: `picker/statusline/main_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRenderLineFull(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/panes", 0o755)
	os.MkdirAll(dir+"/issues", 0o755)
	os.WriteFile(dir+"/panes/1", []byte("state=processing\ntimestamp=9000\nsession=work\n"), 0o644)
	now := int64(9000)

	a := args{
		session: "work", branch: "feat/x", panePath: "/repo", gitRoot: "/repo",
		iconSession: "S", iconBranch: "B", iconDir: "D",
		thmBg: "#000", thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd",
		thmSubtext0: "#9a8", thmOverlay1: "#777", thmGreen: "#0f0",
		prNumber: "42", prBranch: "feat/x", prState: "open", prCheck: "success",
		iconSuccess: "OK", prTitle: "PR",
		paneIcon: "I", paneCmd: ".nvim-wrapped",
	}

	got := renderLine(a, dir, "dark", false, now)
	want := "#[align=left,bg=#000]" +
		"#[fg=#c6a] S work  #[fg=#89b,bold]B feat/x" +
		"  #[fg=#9a8,nobold]D ./" +
		"  #[fg=#777]#[fg=#94e2d5]󰪞#[fg=default] " +
		" #[align=right]" + // note: claude segment ends in a space AND a literal space precedes align → two spaces, matching the old format byte-for-byte
		"#[fg=#0f0]OK #42 PR  " +
		"#[fg=#9a8]I nvim "
	if got != want {
		t.Fatalf("renderLine\n got %q\nwant %q", got, want)
	}
}
```

(The spinner glyph `󰪞` is `spinnerFrames[9000%8]` = `spinnerFrames[0]`. Verify the index if the test fails on that cell.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd picker && go test ./statusline/ -run TestRenderLineFull -v`
Expected: FAIL (undefined: `renderLine`).

- [ ] **Step 3: Implement `renderLine`, `paneCmdDisplay`, and `main`**

```go
import (
	"flag"
	"regexp"
)

var wrappedRe = regexp.MustCompile(`^\.(.*)-wrapped$`)

func paneCmdDisplay(cmd string) string {
	if m := wrappedRe.FindStringSubmatch(cmd); m != nil {
		return m[1]
	}
	return cmd
}

func renderLine(a args, claudeDir, theme string, prefixActive bool, now int64) string {
	var b strings.Builder
	b.WriteString("#[align=left,bg=" + a.thmBg + "]")
	b.WriteString(sessionSegment(a, prefixActive))
	b.WriteString("  #[fg=" + a.thmSubtext0 + ",nobold]" + a.iconDir + " " + dirDisplay(a.panePath, a.gitRoot))
	b.WriteString("  #[fg=" + a.thmOverlay1 + "]" + claudeSegment(claudeDir, a.session, theme, now))
	b.WriteString(" #[align=right]") // literal space mirrors `#(claude) #[align=right]` in the old format
	b.WriteString(prBadge(a))
	b.WriteString("#[fg=" + a.thmSubtext0 + "]" + a.paneIcon + " " + paneCmdDisplay(a.paneCmd) + " ")
	return b.String()
}

func main() {
	var a args
	flag.StringVar(&a.session, "session", "", "")
	prefix := flag.String("prefix", "", "client_prefix flag")
	flag.StringVar(&a.issueID, "issue-id", "", "")
	flag.StringVar(&a.issueBranch, "issue-branch", "", "")
	flag.StringVar(&a.issueProvider, "issue-provider", "", "")
	flag.StringVar(&a.issueTitle, "issue-title", "", "")
	flag.StringVar(&a.branch, "branch", "", "")
	flag.StringVar(&a.panePath, "path", "", "")
	flag.StringVar(&a.gitRoot, "git-root", "", "")
	flag.StringVar(&a.prNumber, "pr-number", "", "")
	flag.StringVar(&a.prBranch, "pr-branch", "", "")
	flag.StringVar(&a.prState, "pr-state", "", "")
	flag.StringVar(&a.prCheck, "pr-check", "", "")
	flag.StringVar(&a.prMergeable, "pr-mergeable", "", "")
	flag.StringVar(&a.prTitle, "pr-title", "", "")
	flag.StringVar(&a.paneIcon, "pane-icon", "", "")
	flag.StringVar(&a.paneCmd, "pane-cmd", "", "")
	flag.StringVar(&a.claudeFg, "claude-fg", "", "")
	flag.StringVar(&a.thmBg, "thm-bg", "", "")
	flag.StringVar(&a.thmRed, "thm-red", "", "")
	flag.StringVar(&a.thmMauve, "thm-mauve", "", "")
	flag.StringVar(&a.thmBlue, "thm-blue", "", "")
	flag.StringVar(&a.thmText, "thm-text", "", "")
	flag.StringVar(&a.thmSubtext0, "thm-subtext0", "", "")
	flag.StringVar(&a.thmOverlay0, "thm-overlay0", "", "")
	flag.StringVar(&a.thmOverlay1, "thm-overlay1", "", "")
	flag.StringVar(&a.thmPeach, "thm-peach", "", "")
	flag.StringVar(&a.thmGreen, "thm-green", "", "")
	flag.StringVar(&a.iconSession, "icon-session", "", "")
	flag.StringVar(&a.iconBranch, "icon-branch", "", "")
	flag.StringVar(&a.iconDir, "icon-dir", "", "")
	flag.StringVar(&a.iconLinear, "icon-linear", "", "")
	flag.StringVar(&a.iconGitHub, "icon-github", "", "")
	flag.StringVar(&a.iconPending, "icon-pending", "", "")
	flag.StringVar(&a.iconSuccess, "icon-success", "", "")
	flag.StringVar(&a.iconFailure, "icon-failure", "", "")
	flag.StringVar(&a.iconMerged, "icon-merged", "", "")
	flag.StringVar(&a.iconConflict, "icon-conflict", "", "")
	flag.Parse()

	claudeDir := os.Getenv("CLAUDE_STATUS_DIR")
	if claudeDir == "" {
		claudeDir = "/tmp/claude-status"
	}
	os.Stdout.WriteString(renderLine(a, claudeDir, detectTheme(), *prefix != "" && *prefix != "0", time.Now().Unix()))
}
```

Add `"time"` to the import block.

- [ ] **Step 4: Run to verify it passes**

Run: `cd picker && go test ./statusline/ -v`
Expected: PASS (all statusline tests).

- [ ] **Step 5: Commit**

```bash
git add picker/statusline/main.go picker/statusline/main_test.go
git commit -m "feat(statusline): full line-0 assembly + flag parsing"
```

---

## Task 9: Build the binary via Nix (`picker/default.nix`)

**Files:**
- Modify: `picker/default.nix:66` (subPackages), `picker/default.nix:69-72` (postInstall)

- [ ] **Step 1: Add the subpackage + rename**

Change `subPackages = ["." "splash"];` to:

```nix
    subPackages = ["." "splash" "statusline"];
```

Change the `postInstall` block to:

```nix
    postInstall = ''
      mv $out/bin/picker $out/bin/tmux-picker-generate
      mv $out/bin/splash $out/bin/tmux-splash
      mv $out/bin/statusline $out/bin/tmux-statusline
    '';
```

- [ ] **Step 2: Build (vendorHash should be unchanged — stdlib only)**

Run: `nix build .#default 2>&1 | tail -20`
Expected: build succeeds. If it fails with a `vendorHash` mismatch, the new package pulled a dependency it shouldn't have — recheck imports are stdlib-only, then update `vendorHash` in `picker/default.nix:65` to the hash Nix reports.

- [ ] **Step 3: Smoke-test the binary directly**

Run:
```fish
./result/sw/bin/tmux-statusline --session none --branch test --path /tmp --icon-session S --thm-bg '#000' --thm-mauve '#c6a' --thm-blue '#89b' --icon-branch B --thm-subtext0 '#9a8' --icon-dir D --thm-overlay1 '#777'
```
(Adjust the binary path to wherever the wrapper exposes it; `nix build` output is under `./result`.)
Expected: a single line of tmux markup printed, no error.

- [ ] **Step 4: Commit**

```bash
git add picker/default.nix
git commit -m "build(statusline): package tmux-statusline binary"
```

---

## Task 10: Wire into `config/tmux.conf.nix` and replace line 0

**Files:**
- Modify: `config/tmux.conf.nix:215` (add `picker-statusline-bin`), `config/tmux.conf.nix:550` (rewrite `status-format[0]`)

- [ ] **Step 1: Add the binary reference**

After `config/tmux.conf.nix:215` (`picker-splash-bin = …`), add:

```nix
  picker-statusline-bin = "${picker-generate}/bin/tmux-statusline";
```

- [ ] **Step 2: Rewrite `status-format[0]`**

Replace the single line at `config/tmux.conf.nix:550` with the consolidated form. The two side-effect `#()` calls stay; the three render calls + inline conditionals collapse into one `tmux-statusline` call whose args are pre-expanded by tmux:

```nix
    set -g status-format[0] "#(${script.tmux-update-icons}/bin/tmux-update-icons '#{session_name}')${lib.optionalString enrichEnable "#(${script.tmux-pr-enrich}/bin/tmux-pr-enrich --tick)"}#(${picker-statusline-bin} --session '#{session_name}' --prefix '#{client_prefix}' --claude-fg '#{@claude_session_fg}' --issue-id '#{@issue_id}' --issue-branch '#{@issue_branch}' --issue-provider '#{@issue_provider}' --issue-title '#{@issue_title}' --branch '#{@branch}' --path '#{pane_current_path}' --git-root '#{@git_root}' --pr-number '#{@pr_number}' --pr-branch '#{@pr_branch}' --pr-state '#{@pr_state}' --pr-check '#{@pr_check_state}' --pr-mergeable '#{@pr_mergeable}' --pr-title '#{@pr_title}' --pane-icon '#{@active_pane_icon}' --pane-cmd '#{pane_current_command}' --thm-bg '#{@thm_bg}' --thm-red '#{@thm_red}' --thm-mauve '#{@thm_mauve}' --thm-blue '#{@thm_blue}' --thm-text '#{@thm_text}' --thm-subtext0 '#{@thm_subtext_0}' --thm-overlay0 '#{@thm_overlay_0}' --thm-overlay1 '#{@thm_overlay_1}' --thm-peach '#{@thm_peach}' --thm-green '#{@thm_green}' --icon-session '#{@icon_session}' --icon-branch '#{@icon_branch}' --icon-dir '#{@icon_dir}' --icon-linear '${enrichIconSet.linear}' --icon-github '${enrichIconSet.github}' --icon-pending '${enrichIconSet.pending}' --icon-success '${enrichIconSet.success}' --icon-failure '${enrichIconSet.failure}' --icon-merged '${enrichIconSet.merged}' --icon-conflict '${enrichIconSet.conflict}')"
```

Note: `enrichIconSet.*` already double-escapes `#`→`##` for tmux format safety; verify the badge still shows a single `#` after rendering (Step 4). The pane-cmd `#{s|…|…:…}` regex strip now happens in Go (`paneCmdDisplay`), so it is intentionally absent here.

- [ ] **Step 3: Build**

Run: `nix build .#default 2>&1 | tail -20`
Expected: success.

- [ ] **Step 4: Differential parity check — old vs new expanded line-0, byte-for-byte**

`display-message -p` expands a format string fully (runs every `#()`, resolves `#{?…}` / `#{s|…}` / `@options`) and prints the result with `#[…]` style codes intact as literal text — exactly what's needed to diff. A visual capture (`capturep`) is **not** enough: style codes aren't visible glyphs, so a stray-space or wrong-color bug renders identically to the eye. The two side-effect calls (`tmux-update-icons`, `tmux-pr-enrich --tick`) emit nothing and run identically in both builds, so they cancel — any diff isolates a port infidelity.

Both captures use the **same session name** (`probe`) so the claude segment (which filters pane files by session) is exercised identically; only the socket names differ.

Capture the NEW expansion:
```fish
./result/bin/tmux -L paritynew new-session -d -s probe -x 200 -y 20 -c (pwd)
./result/bin/tmux -L paritynew set -g @branch "feat/x"
./result/bin/tmux -L paritynew set -g @git_root (pwd)
# optional: seed /tmp/claude-status/panes/9 with `session=probe` + a state to exercise the claude segment
set -l fmt (./result/bin/tmux -L paritynew show -gv 'status-format[0]')
./result/bin/tmux -L paritynew display-message -p "$fmt" > /tmp/line0-new.txt
./result/bin/tmux -L paritynew kill-server
```

Capture the OLD expansion from before the rewrite (stash the config edit, rebuild, repeat, restore):
```fish
git stash push -- config/tmux.conf.nix
nix build .#default
./result/bin/tmux -L parityold new-session -d -s probe -x 200 -y 20 -c (pwd)
./result/bin/tmux -L parityold set -g @branch "feat/x"
./result/bin/tmux -L parityold set -g @git_root (pwd)
set -l fmt (./result/bin/tmux -L parityold show -gv 'status-format[0]')
./result/bin/tmux -L parityold display-message -p "$fmt" > /tmp/line0-old.txt
./result/bin/tmux -L parityold kill-server
git stash pop
nix build .#default
```

Diff:
```fish
diff /tmp/line0-old.txt /tmp/line0-new.txt
```
Expected: **no output** — byte-for-byte identical. Any diff is a port infidelity (a stray space, a wrong color, a missing conditional); fix the Go segment that produced it before committing. Re-run with the seeded variants and confirm each diffs clean: `@pr_*` set for a PR badge; a `session=probe` claude pane file; and `@issue_id`/`@issue_branch`/`@issue_title` set with an apostrophe-and-`#` title (e.g. `Don't fix #1`) to exercise Task 12's sanitization through the argv path. The prefix-active (red+bold) session branch is **not** reachable by `display-message` (`client_prefix` is client state, always `0` here) — it is covered by the `TestSessionSegmentPrefixColor` unit test (Task 6) instead (finding #7). Finally, in a normal attached session, confirm the right side is flush-right (Task 1's align verdict), the PR badge shows a single `#`, and clicking the session name still opens the picker (`MouseDown1StatusLeft`, unchanged).

- [ ] **Step 5: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(statusline): replace line-0 render forks with tmux-statusline"
```

---

## Task 11: Full check, deslop, PR

**Files:** none (verification + integration).

- [ ] **Step 1: Run the Go tests + vet**

Run: `cd picker && go test ./... && go vet ./...`
Expected: all PASS, no vet warnings.

- [ ] **Step 2: Run the full flake check**

Run: `nix flake check 2>&1 | tail -30`
Expected: PASS (gofmt, pre-commit hooks, build). Fix any `gofmt`/`statix` findings.

- [ ] **Step 3: Deslop the diff**

Invoke the `deslop` skill against the branch diff — strip any comment that restates code, speculative guards, or style drift from the surrounding Go.

- [ ] **Step 4: Open the PR (assigned to me)**

```bash
gh pr create --assignee @me --title "feat(statusline): consolidate line-0 render forks into one Go binary" --body "Replaces the three render \`#()\` calls in status-format[0] (branch, dir, claude-status) plus the inline conditional logic with a single \`tmux-statusline\` Go binary. Cuts per-redraw forks 5→3. No behavior change. Includes the Task-1 align-survival verdict."
```

---

## Task 12: Harden `sanitize_title` against shell-unsafe characters

> **Prerequisite for Task 10's argv safety — ship in the same PR, before the new format goes live.** The rewritten `status-format[0]` passes `@pr_title` / `@issue_title` as single-quoted `#()` argv. A title containing `'` breaks the shell quoting (argv mangled); `#` risks tmux format re-interpretation. `sanitize_title` currently strips only CR/LF/ESC, so `Don't fix #123` reaches the shell intact. Strip `'` and `#` at the source. Branch/path argv is unchanged — the old format already passes those through `#()`, so only titles are newly exposed.

These are status-bar hints, already hard-truncated to 50 chars; dropping `'`/`#` is cosmetically negligible and applies equally to the old inline render path (same `@option`).

**Files:**
- Modify: `scripts/lib-enrich.sh:58-63` (`sanitize_title`)
- Test: `tests/enrich.bats`

- [ ] **Step 1: Write the failing bats test**

Add to `tests/enrich.bats`:
```bash
@test "sanitize_title strips shell-unsafe quote and hash" {
	sanitize_title "Don't fix #123"
	[ "$REPLY" = "Dont fix 123" ]
}
```

- [ ] **Step 2: Run to verify it fails**

Run (inside `nix develop`): `bats tests/enrich.bats`
Expected: FAIL — `REPLY` still contains `'` and `#`.

- [ ] **Step 3: Harden the function**

```bash
sanitize_title() {
	local clean="${1//$'\r'/}"
	clean="${clean//$'\n'/}"
	clean="${clean//$'\033'/}"
	clean="${clean//\'/}" # breaks single-quoted #() argv
	clean="${clean//\#/}" # tmux format char
	REPLY="${clean:0:50}"
}
```

- [ ] **Step 4: Run to verify it passes**

Run (inside `nix develop`): `bats tests/enrich.bats`
Expected: PASS (new test + all existing enrich tests).

- [ ] **Step 5: Commit**

```bash
git add scripts/lib-enrich.sh tests/enrich.bats
git commit -m "fix(enrich): strip shell-unsafe chars from titles for #() argv safety"
```

---

## Self-Review

**Spec coverage** — every current line-0 element maps to a task: session color/icon/name + issue-or-branch (Task 6), dir (Task 5 + 8), claude icon-color + issue ids (Tasks 2–4), PR badge (Task 7), pane icon + stripped command (Task 8), assembly + align pivot (Task 8). Side-effect calls preserved (Task 10). Nix packaging (Task 9) + config wiring (Task 10) + checks (Task 11). The architecture-risk gate (align survival) is Task 1.

**Type consistency** — `args` is defined once in full (Task 6) and only read thereafter; `counts`, `sessionAgg`, `statePalette` are introduced in Tasks 2–3 and reused unchanged; `claudeSegment(dir, session, theme, now)` and `renderLine(a, claudeDir, theme, prefixActive, now)` signatures match between definition and the golden test.

**Placeholder scan** — no TBD/"handle errors"/"similar to" placeholders; every code step carries complete code and every run step an expected result.

**Parity guarantee** — the hand-written golden tests (Tasks 4–8) guide the *implementation* but cannot prove byte-for-byte equivalence with the old format, since their `want` strings are authored, not captured. The authoritative no-behavior-change guard is the **differential expanded-format diff** (Task 10, Step 4): same controlled state, old vs new, `diff` must be empty. If a golden test and the differential ever disagree, the differential wins and the golden is corrected to match the true old output.

**Adversarial review findings folded in** — #11 (free-text titles unsafe in single-quoted `#()` argv) → Task 12 hardens `sanitize_title` (strips `'` and `#`); only titles are newly exposed, since branch/path already ride `#()` argv in the old format. #9 (whole-file vs first-line issue read) → `readIssueFile` cuts at the first `\n`. #7 (prefix-active branch unreachable by the differential, since `client_prefix` is client state) → covered by the Task 6 `TestSessionSegmentPrefixColor` unit test.

**Known follow-ups (intentionally out of scope):** (1) extracting a shared `picker/internal/claudestate` to dedupe with `picker/main.go`'s reader; (2) the vertical-spacing question that started this thread — untouched, still a whole-row constraint.
```