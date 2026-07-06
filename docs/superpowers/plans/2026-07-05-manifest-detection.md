# Manifest-driven multi-agent status detection — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect agent status for any terminal agent by matching declarative TOML manifests against each agent pane's rendered screen, feeding the result into lazytmux's existing state-file model.

**Architecture:** A per-pane Go binary (`agent-detect`) is armed by `tmux pipe-pane` when the 1 s tick sees an agent command in a pane with no live pipe. It feeds the pane byte stream through a headless VT emulator, matches embedded manifests on a debounced screen snapshot, and writes a separate `/tmp/claude-status/screen/<id>` file. `read_pane_state` merges hook state (fresh wins) and screen state (backfills stale/absent).

**Tech Stack:** Go 1.25 (`picker/` module, `charm.land/*` v2 fork), a headless VT emulator (decided in Task 1), bash (`lib-claude.sh`, `tmux-update-icons.sh`, `claude-status-update.sh`), tmux 3.6a, Nix flake, bats.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-07-05-manifest-detection-design.md` (issue #128). Every task's requirements implicitly include it.
- **Scope B:** hook wins when fresh; screen backfills only when hook stale/absent. Claude fresh path must be **byte-for-byte unchanged**.
- **No per-tick fork on the render hot path.** Arming issues at most one `tmux` command per agent pane; the parser reads pane size+command **once** at startup.
- **State files:** `/tmp/claude-status/screen/<pane_id>`, pane id **sans `%`**, `key=value` lines (`state`, `timestamp` epoch seconds). Eight states only: `error waiting denied compacting interrupted processing done idle`.
- **Arming gate:** tmux's own `#{pane_pipe}` (0/1), never a hand-maintained option.
- **Shell scripts are bash** (run in tmux env); commit **inside `nix develop -c`** (pre-commit hooks need the materialized config). Work happens in worktree `feat/128-manifest-detection`.
- **shfmt uses tabs.** Go binaries live in `picker/`, one subdir per `main`, renamed in `postInstall`.

---

## File structure

- Create `picker/agentdetect/main.go` — the per-pane parser `main` (stdin loop, startup size/command read, wiring).
- Create `picker/agentdetect/screen/screen.go` — VT emulator adapter (stable interface, lib chosen in Task 1).
- Create `picker/agentdetect/screen/screen_test.go`.
- Create `picker/agentdetect/manifest/manifest.go` — manifest schema + embedded TOML loader.
- Create `picker/agentdetect/manifest/match.go` — the pure matcher.
- Create `picker/agentdetect/manifest/manifest_test.go`, `match_test.go`.
- Create `picker/agentdetect/debounce/debounce.go` + `debounce_test.go` — injectable-clock debounce.
- Create `picker/agentdetect/statefile/statefile.go` + `statefile_test.go` — write-on-change screen file.
- Create `config/agent-manifests/claude.toml`, `config/agent-manifests/codex.toml` (embedded via `manifest.go`).
- Create `picker/agentdetect/manifest/testdata/*.txt` — captured screen fixtures.
- Modify `picker/default.nix` — `subPackages`, `postInstall`, `vendorHash`.
- Modify `config/tmux.conf.nix` — inject `@agent_detect_bin@`; add `screen/<id>` to the `pane-exited` `rm` (line ~684).
- Modify `scripts/tmux-update-icons.sh` — arming gate.
- Modify `scripts/lib-claude.sh` — the merge in `read_pane_state`.
- Modify `scripts/claude-status-update.sh` — `screen/<id>` cleanup (lines ~59, ~380).
- Create `tests/agent-detect-merge.bats`, `tests/agent-detect-arm.bats`.

---

### Task 1: VT emulator adapter + library decision (spike)

Resolves spec R1. Locks a lib-agnostic interface so all later Go tasks are independent of the choice.

**Files:**
- Create: `picker/agentdetect/screen/screen.go`
- Test: `picker/agentdetect/screen/screen_test.go`

**Interfaces:**
- Produces: `type Screen interface { Feed(b []byte); Text() string; Title() string; AltScreen() bool }` and `func New(cols, rows int) Screen`.

- [ ] **Step 1: Decide the library.** Run the evaluation in this order and record the outcome in the PR description:
  1. `cd picker && go doc github.com/charmbracelet/ultraviolet 2>/dev/null | head -50` — does it expose a byte-stream terminal emulator (feed bytes → readable cell grid), or only rendering primitives? If it emulates, use it (already vendored, zero new ecosystem surface).
  2. Else evaluate `github.com/charmbracelet/x/vt`: `cd picker && go get github.com/charmbracelet/x/vt@latest && go build ./...`. If it builds against the `charm.land/*` fork without version conflicts, use it.
  3. Else `github.com/hinshun/vt10x`.
  Acceptance: the chosen lib can feed a byte stream and return plain screen text, the OSC title, and alt-screen state.

- [ ] **Step 2: Write the failing test** (interface is fixed regardless of lib):

```go
package screen

import "testing"

func TestFeedRendersText(t *testing.T) {
	s := New(80, 24)
	s.Feed([]byte("hello \x1b[31mworld\x1b[0m"))
	if got := s.Text(); !contains(got, "hello world") {
		t.Fatalf("Text() = %q, want it to contain %q", got, "hello world")
	}
}

func TestCapturesOSCTitle(t *testing.T) {
	s := New(80, 24)
	s.Feed([]byte("\x1b]2;⠐ working\x07")) // OSC 2 set-title, braille glyph
	if got := s.Title(); got != "⠐ working" {
		t.Fatalf("Title() = %q, want %q", got, "⠐ working")
	}
}

func TestAltScreenDetected(t *testing.T) {
	s := New(80, 24)
	s.Feed([]byte("\x1b[?1049h")) // enter alt screen
	if !s.AltScreen() {
		t.Fatal("AltScreen() = false, want true after ?1049h")
	}
}

func contains(h, n string) bool { return len(h) >= len(n) && (indexOf(h, n) >= 0) }
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 3: Run test to verify it fails.** Run: `cd picker && go test ./agentdetect/screen/ -run TestFeed -v`. Expected: FAIL (`New` undefined).

- [ ] **Step 4: Implement the adapter** over the chosen lib. Reference implementation for `charmbracelet/x/vt` (adapt method names to the lib the spike picked — the interface stays identical):

```go
// Package screen wraps a headless VT emulator behind a small stable interface,
// so the parser and manifest matcher never depend on the concrete library.
package screen

import "github.com/charmbracelet/x/vt"

type Screen interface {
	Feed(b []byte)
	Text() string
	Title() string
	AltScreen() bool
}

type vtScreen struct {
	e     *vt.Emulator
	title string
}

func New(cols, rows int) Screen {
	e := vt.NewEmulator(cols, rows)
	s := &vtScreen{e: e}
	e.SetCallbacks(vt.Callbacks{Title: func(t string) { s.title = t }})
	return s
}

func (s *vtScreen) Feed(b []byte)   { _, _ = s.e.Write(b) }
func (s *vtScreen) Text() string    { return s.e.String() }
func (s *vtScreen) Title() string   { return s.title }
func (s *vtScreen) AltScreen() bool { return s.e.IsAltScreen() }
```

- [ ] **Step 5: Run tests to verify they pass.** Run: `cd picker && go test ./agentdetect/screen/ -v`. Expected: PASS. If `Text()` includes soft-wrap artifacts, the matcher joins lines later — do not normalize here.

- [ ] **Step 6: Commit.**

```bash
cd /home/noams/Data/git/noamsto/lazytmux-worktrees/feat-128-manifest-detection
git add picker/agentdetect/screen/ picker/go.mod picker/go.sum
nix develop -c git commit -m "feat(agent-detect): VT emulator adapter with fixed interface (#128)"
```

---

### Task 2: Manifest schema + embedded TOML loader

**Files:**
- Create: `picker/agentdetect/manifest/manifest.go`
- Test: `picker/agentdetect/manifest/manifest_test.go`
- Create: `picker/agentdetect/manifest/manifests/codex.toml` (minimal, for the loader test)

> **Note on manifest location:** `//go:embed` can only reach files at or below
> the embedding package's directory, so the manifests **must** live under
> `picker/agentdetect/manifest/manifests/` — that is the single source of truth.
> (The spec's `config/agent-manifests/` path is not embeddable; do not use it.)

**Interfaces:**
- Produces:
  - `type Predicate struct { Contains []string; Regex string; Not []Predicate }`
  - `type Rule struct { State string; Priority int; Region string; Contains []string; Regex string; Not []Predicate }`
  - `type Manifest struct { ID string; MatchCommands []string; Rules []Rule }`
  - `func Load() ([]Manifest, error)` — parses all embedded `*.toml`, sorts each manifest's rules by `Priority` descending.
  - `func ForCommand(ms []Manifest, cmd string) (Manifest, bool)`.

- [ ] **Step 1: Write `picker/agentdetect/manifest/manifests/codex.toml`** (loader fixture; real rules land in Task 11):

```toml
id = "codex"
match_commands = ["codex"]

[[rules]]
state = "processing"
priority = 100
region = "whole"
contains = ["esc to interrupt"]
```

- [ ] **Step 2: Write the failing test:**

```go
package manifest

import "testing"

func TestLoadParsesAndSortsRules(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	m, ok := ForCommand(ms, "codex")
	if !ok {
		t.Fatal("ForCommand(codex) not found")
	}
	if len(m.Rules) == 0 || m.Rules[0].State != "processing" {
		t.Fatalf("unexpected rules: %+v", m.Rules)
	}
}

func TestForCommandUnknown(t *testing.T) {
	ms, _ := Load()
	if _, ok := ForCommand(ms, "fish"); ok {
		t.Fatal("ForCommand(fish) should be false")
	}
}
```

- [ ] **Step 3: Run to verify it fails.** Run: `cd picker && go test ./agentdetect/manifest/ -run TestLoad -v`. Expected: FAIL (`Load` undefined).

- [ ] **Step 4: Implement the loader:**

```go
package manifest

import (
	"embed"
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
)

//go:embed all:manifests
var manifestFS embed.FS

type Predicate struct {
	Contains []string    `toml:"contains"`
	Regex    string      `toml:"regex"`
	Not      []Predicate `toml:"not"`
}

type Rule struct {
	State    string      `toml:"state"`
	Priority int         `toml:"priority"`
	Region   string      `toml:"region"`
	Contains []string    `toml:"contains"`
	Regex    string      `toml:"regex"`
	Not      []Predicate `toml:"not"`
}

type Manifest struct {
	ID            string `toml:"id"`
	MatchCommands []string `toml:"match_commands"`
	Rules         []Rule   `toml:"rules"`
}

func Load() ([]Manifest, error) {
	entries, err := manifestFS.ReadDir("manifests")
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		b, err := manifestFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := toml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		sort.SliceStable(m.Rules, func(i, j int) bool { return m.Rules[i].Priority > m.Rules[j].Priority })
		out = append(out, m)
	}
	return out, nil
}

func ForCommand(ms []Manifest, cmd string) (Manifest, bool) {
	for _, m := range ms {
		for _, c := range m.MatchCommands {
			if c == cmd {
				return m, true
			}
		}
	}
	return Manifest{}, false
}
```

Note: `//go:embed all:manifests` reads `picker/agentdetect/manifest/manifests/`, where `codex.toml` was created in Step 1 — no move needed.

- [ ] **Step 5: Add the TOML dependency.**

```bash
cd picker && go get github.com/BurntSushi/toml@latest
```

- [ ] **Step 6: Run to verify pass.** Run: `cd picker && go test ./agentdetect/manifest/ -v`. Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add picker/agentdetect/manifest/ picker/go.mod picker/go.sum
nix develop -c git commit -m "feat(agent-detect): manifest schema + embedded TOML loader (#128)"
```

---

### Task 3: Manifest matcher (pure function)

**Files:**
- Create: `picker/agentdetect/manifest/match.go`
- Test: `picker/agentdetect/manifest/match_test.go`

**Interfaces:**
- Consumes: `Manifest`, `Rule`, `Predicate` (Task 2); `Screen` text/title (Task 1).
- Produces: `func Match(m Manifest, screenText, title string, altScreen bool) (state string, matched bool)`. Returns `("", false)` on no match; `("skip", true)` collapses to `("", false)` for the caller (hold prior). Region text resolved internally.

- [ ] **Step 1: Write the failing tests:**

```go
package manifest

import "testing"

func mkManifest(rules ...Rule) Manifest { return Manifest{ID: "t", Rules: rules} }

func TestMatchTitleRegionWins(t *testing.T) {
	m := mkManifest(
		Rule{State: "processing", Priority: 100, Region: "title", Contains: []string{"⠐"}},
		Rule{State: "idle", Priority: 10, Region: "whole", Contains: []string{"❯"}},
	)
	got, ok := Match(m, "❯ ", "⠐ doing things", false)
	if !ok || got != "processing" {
		t.Fatalf("Match = (%q,%v), want (processing,true)", got, ok)
	}
}

func TestMatchNotExcludes(t *testing.T) {
	m := mkManifest(Rule{
		State: "idle", Priority: 10, Region: "whole",
		Contains: []string{"❯"},
		Not:      []Predicate{{Contains: []string{"do you want to proceed?"}}},
	})
	if _, ok := Match(m, "❯ do you want to proceed?", "", false); ok {
		t.Fatal("Match should be excluded by not-predicate")
	}
}

func TestMatchAltScreenSuppressed(t *testing.T) {
	m := mkManifest(Rule{State: "processing", Priority: 100, Region: "whole", Contains: []string{"x"}})
	if _, ok := Match(m, "xxx", "", true); ok {
		t.Fatal("alt-screen should suppress all matches")
	}
}

func TestMatchSkipHoldsPrior(t *testing.T) {
	m := mkManifest(Rule{State: "skip", Priority: 100, Region: "whole", Contains: []string{"select model"}})
	got, ok := Match(m, "select model", "", false)
	if ok || got != "" {
		t.Fatalf("skip rule should yield (\"\",false), got (%q,%v)", got, ok)
	}
}

func TestMatchLastLines(t *testing.T) {
	m := mkManifest(Rule{State: "waiting", Priority: 100, Region: "last_lines:2", Contains: []string{"proceed?"}})
	screen := "line a\nline b\nline c\ndo you want to proceed?"
	if got, ok := Match(m, screen, "", false); !ok || got != "waiting" {
		t.Fatalf("last_lines match = (%q,%v)", got, ok)
	}
}
```

- [ ] **Step 2: Run to verify fail.** Run: `cd picker && go test ./agentdetect/manifest/ -run TestMatch -v`. Expected: FAIL (`Match` undefined).

- [ ] **Step 3: Implement the matcher:**

```go
package manifest

import (
	"regexp"
	"strings"
)

func Match(m Manifest, screenText, title string, altScreen bool) (string, bool) {
	if altScreen {
		return "", false
	}
	for _, r := range m.Rules {
		region := regionText(r.Region, screenText, title)
		if ruleMatches(r, region) {
			if r.State == "skip" {
				return "", false
			}
			return r.State, true
		}
	}
	return "", false
}

func regionText(sel, screenText, title string) string {
	switch {
	case sel == "title":
		return title
	case sel == "whole":
		return joinWrapped(screenText)
	case strings.HasPrefix(sel, "last_lines:"):
		n := atoiDefault(strings.TrimPrefix(sel, "last_lines:"), 1)
		return lastNonEmpty(screenText, n)
	default:
		return screenText
	}
}

func ruleMatches(r Rule, region string) bool {
	lc := strings.ToLower(region)
	for _, s := range r.Contains {
		if !strings.Contains(lc, strings.ToLower(s)) {
			return false
		}
	}
	if r.Regex != "" {
		if re, err := regexp.Compile("(?i)" + r.Regex); err != nil || !re.MatchString(region) {
			return false
		}
	}
	for _, n := range r.Not {
		if predMatches(n, lc) {
			return false
		}
	}
	return true
}

func predMatches(p Predicate, lc string) bool {
	for _, s := range p.Contains {
		if !strings.Contains(lc, strings.ToLower(s)) {
			return false
		}
	}
	if p.Regex != "" {
		if re, err := regexp.Compile("(?i)" + p.Regex); err != nil || !re.MatchString(lc) {
			return false
		}
	}
	for _, n := range p.Not {
		if predMatches(n, lc) {
			return false
		}
	}
	return len(p.Contains) > 0 || p.Regex != "" || len(p.Not) > 0
}

func joinWrapped(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func lastNonEmpty(s string, n int) string {
	lines := strings.Split(s, "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append([]string{lines[i]}, kept...)
		}
	}
	return strings.Join(kept, "\n")
}

func atoiDefault(s string, d int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return d
	}
	return n
}
```

Note: `whole` collapses newlines so a soft-wrapped phrase still matches (spec width-tolerance). `last_lines` keeps line structure. `Contains`/`Not` are case-insensitive; `Regex` is compiled case-insensitive.

- [ ] **Step 4: Run to verify pass.** Run: `cd picker && go test ./agentdetect/manifest/ -v`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add picker/agentdetect/manifest/match.go picker/agentdetect/manifest/match_test.go
nix develop -c git commit -m "feat(agent-detect): pure manifest matcher (#128)"
```

---

### Task 4: Debounce with injectable clock

**Files:**
- Create: `picker/agentdetect/debounce/debounce.go`
- Test: `picker/agentdetect/debounce/debounce_test.go`

**Interfaces:**
- Produces: `type Debouncer struct{...}`; `func New(window time.Duration, now func() time.Time) *Debouncer`; `func (d *Debouncer) Mark(t time.Time)` (record last-byte time); `func (d *Debouncer) Due(now time.Time) bool` (true once `window` elapsed since last Mark and not yet fired).

- [ ] **Step 1: Write the failing test:**

```go
package debounce

import (
	"testing"
	"time"
)

func TestDueOnlyAfterQuietWindow(t *testing.T) {
	base := time.Unix(1000, 0)
	d := New(80*time.Millisecond, nil)
	d.Mark(base)
	if d.Due(base.Add(50 * time.Millisecond)) {
		t.Fatal("should not be due at 50ms")
	}
	if !d.Due(base.Add(80 * time.Millisecond)) {
		t.Fatal("should be due at 80ms")
	}
	if d.Due(base.Add(120 * time.Millisecond)) {
		t.Fatal("should not re-fire without a new Mark")
	}
	d.Mark(base.Add(200 * time.Millisecond))
	if !d.Due(base.Add(300 * time.Millisecond)) {
		t.Fatal("should be due again after a new Mark + window")
	}
}
```

- [ ] **Step 2: Run to verify fail.** Run: `cd picker && go test ./agentdetect/debounce/ -v`. Expected: FAIL (`New` undefined).

- [ ] **Step 3: Implement:**

```go
package debounce

import "time"

type Debouncer struct {
	window   time.Duration
	lastMark time.Time
	fired    bool
	dirty    bool
}

func New(window time.Duration, _ func() time.Time) *Debouncer {
	return &Debouncer{window: window}
}

func (d *Debouncer) Mark(t time.Time) {
	d.lastMark = t
	d.dirty = true
	d.fired = false
}

func (d *Debouncer) Due(now time.Time) bool {
	if !d.dirty || d.fired {
		return false
	}
	if now.Sub(d.lastMark) >= d.window {
		d.fired = true
		d.dirty = false
		return true
	}
	return false
}
```

- [ ] **Step 4: Run to verify pass.** Run: `cd picker && go test ./agentdetect/debounce/ -v`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add picker/agentdetect/debounce/
nix develop -c git commit -m "feat(agent-detect): debounce with injectable clock (#128)"
```

---

### Task 5: Screen-state writer (write-on-change)

**Files:**
- Create: `picker/agentdetect/statefile/statefile.go`
- Test: `picker/agentdetect/statefile/statefile_test.go`

**Interfaces:**
- Produces: `type Writer struct{...}`; `func New(dir, paneID string) *Writer`; `func (w *Writer) Update(state string, now time.Time) (changed bool, err error)`. Writes `<dir>/<paneID>` atomically (`state=…\ntimestamp=…\n`) only when `state` differs from the last write. Empty `state` (hold) is a no-op.

- [ ] **Step 1: Write the failing test:**

```go
package statefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWritesOnChangeOnly(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")
	now := time.Unix(1000, 0)

	changed, err := w.Update("processing", now)
	if err != nil || !changed {
		t.Fatalf("first update: changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "3"))
	if string(b) != "state=processing\ntimestamp=1000\n" {
		t.Fatalf("file = %q", b)
	}

	changed, _ = w.Update("processing", now.Add(time.Second))
	if changed {
		t.Fatal("same state should not rewrite")
	}

	changed, _ = w.Update("idle", now.Add(2*time.Second))
	if !changed {
		t.Fatal("state change should rewrite")
	}
}

func TestEmptyStateIsNoop(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")
	if changed, _ := w.Update("", time.Unix(1, 0)); changed {
		t.Fatal("empty state must be a no-op")
	}
	if _, err := os.Stat(filepath.Join(dir, "3")); !os.IsNotExist(err) {
		t.Fatal("no file should be written for empty state")
	}
}
```

- [ ] **Step 2: Run to verify fail.** Run: `cd picker && go test ./agentdetect/statefile/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement:**

```go
package statefile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Writer struct {
	dir, paneID, last string
}

func New(dir, paneID string) *Writer { return &Writer{dir: dir, paneID: paneID} }

func (w *Writer) Update(state string, now time.Time) (bool, error) {
	if state == "" || state == w.last {
		return false, nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return false, err
	}
	final := filepath.Join(w.dir, w.paneID)
	tmp := final + ".tmp"
	content := fmt.Sprintf("state=%s\ntimestamp=%d\n", state, now.Unix())
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return false, err
	}
	w.last = state
	return true, nil
}
```

- [ ] **Step 4: Run to verify pass.** Run: `cd picker && go test ./agentdetect/statefile/ -v`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add picker/agentdetect/statefile/
nix develop -c git commit -m "feat(agent-detect): write-on-change screen state file (#128)"
```

---

### Task 6: `agent-detect` main (wiring)

**Files:**
- Create: `picker/agentdetect/main.go`

**Interfaces:**
- Consumes: `screen.New`, `manifest.Load/ForCommand/Match`, `debounce.New`, `statefile.New`.
- Produces: the `agent-detect <pane_id>` binary. Startup: one `tmux display -p -t %<pane_id>` reads `pane_width pane_height pane_current_command`; selects the manifest; if no manifest matches the command, exits 0 (nothing to do). Loop: read stdin → `Feed` + `Mark(now)`; a ticker checks `Due(now)` → snapshot → `Match` → `Update`. EOF → final match → exit 0.

- [ ] **Step 1: Implement main** (no unit test; covered by Task 12 integration + the unit-tested components it wires):

```go
package main

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/agentdetect/debounce"
	"github.com/noamsto/lazytmux/picker/agentdetect/manifest"
	"github.com/noamsto/lazytmux/picker/agentdetect/screen"
	"github.com/noamsto/lazytmux/picker/agentdetect/statefile"
)

const (
	debounceWindow = 80 * time.Millisecond
	stateDir       = "/tmp/claude-status/screen"
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	paneID := os.Args[1] // already sans '%'

	cols, rows, cmd := paneInfo(paneID)
	ms, err := manifest.Load()
	if err != nil {
		return
	}
	m, ok := manifest.ForCommand(ms, cmd)
	if !ok {
		return // pane isn't running a known agent; nothing to watch
	}

	scr := screen.New(cols, rows)
	deb := debounce.New(debounceWindow, nil)
	w := statefile.New(stateDir, paneID)

	bytesCh := make(chan []byte, 64)
	go readStdin(bytesCh)

	ticker := time.NewTicker(debounceWindow / 2)
	defer ticker.Stop()

	for {
		select {
		case b, open := <-bytesCh:
			if !open {
				emit(scr, m, w) // final snapshot on EOF
				return
			}
			scr.Feed(b)
			deb.Mark(time.Now())
		case <-ticker.C:
			if deb.Due(time.Now()) {
				emit(scr, m, w)
			}
		}
	}
}

func emit(scr screen.Screen, m manifest.Manifest, w *statefile.Writer) {
	state, _ := manifest.Match(m, scr.Text(), scr.Title(), scr.AltScreen())
	_, _ = w.Update(state, time.Now())
}

func readStdin(ch chan<- []byte) {
	r := bufio.NewReader(os.Stdin)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			cp := make([]byte, n)
			copy(cp, buf[:n])
			ch <- cp
		}
		if err != nil {
			close(ch)
			return
		}
	}
}

func paneInfo(paneID string) (cols, rows int, cmd string) {
	cols, rows = 80, 24
	out, err := exec.Command("tmux", "display", "-p", "-t", "%"+paneID,
		"#{pane_width} #{pane_height} #{pane_current_command}").Output()
	if err != nil {
		return
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) >= 2 {
		if c, e := strconv.Atoi(f[0]); e == nil {
			cols = c
		}
		if r, e := strconv.Atoi(f[1]); e == nil {
			rows = r
		}
	}
	if len(f) >= 3 {
		cmd = f[2]
	}
	return
}
```

- [ ] **Step 2: Build to verify it compiles.** Run: `cd picker && go build ./agentdetect/`. Expected: no output (success).

- [ ] **Step 3: Smoke test locally.** Run: `printf '\x1b]2;\xe2\xa0\x90 x\x07codex esc to interrupt' | (cd picker && go run ./agentdetect 999)` then `cat /tmp/claude-status/screen/999` — expect `state=processing`. Clean up: `rm -f /tmp/claude-status/screen/999`. (Requires codex.toml `match_commands=["codex"]`; if `paneInfo` can't reach a real pane `999`, temporarily hardcode `cmd="codex"` to smoke the pipeline, then revert.)

- [ ] **Step 4: Commit.**

```bash
git add picker/agentdetect/main.go
nix develop -c git commit -m "feat(agent-detect): parser main wiring stdin->vt->match->statefile (#128)"
```

---

### Task 7: Build wiring (Nix)

**Files:**
- Modify: `picker/default.nix` (`subPackages`, `postInstall`, `vendorHash`)
- Modify: `config/tmux.conf.nix` (inject `@agent_detect_bin@`)

**Interfaces:**
- Produces: an `agent-detect` binary on the tmux PATH and an `@agent_detect_bin@` placeholder resolving to it.

- [ ] **Step 1: Read the current build wiring.** Run: `sed -n '55,80p' picker/default.nix` and locate `subPackages` + `postInstall`.

- [ ] **Step 2: Add the subpackage + rename.** In `picker/default.nix`, add `"agentdetect"` to `subPackages` and a rename line in `postInstall` (mirror the existing `statusline`/`enrichcard` renames), e.g.:

```nix
  subPackages = [ "." "splash" "statusline" "enrichcard" "agentdetect" ];
  # in postInstall, alongside the others:
  mv $out/bin/agentdetect $out/bin/agent-detect
```

- [ ] **Step 3: Regenerate `vendorHash`.** Set `vendorHash = lib.fakeHash;` (or `""`), run `nix build .#default 2>&1 | tail`, copy the `got:` hash into `vendorHash`.

- [ ] **Step 4: Inject the placeholder.** In `config/tmux.conf.nix`, follow the `@claude_status_bin@` pattern (~lines 222-261): add `@agent_detect_bin@` → `${picker}/bin/agent-detect` substitution for `tmux-update-icons` (Task 8 consumes it).

- [ ] **Step 5: Build to verify.** Run: `nix build .#default 2>&1 | tail`. Expected: success; `ls result/bin/agent-detect` exists.

- [ ] **Step 6: Commit.**

```bash
git add picker/default.nix config/tmux.conf.nix
nix develop -c git commit -m "build(agent-detect): package binary + inject @agent_detect_bin@ (#128)"
```

---

### Task 8: Arming in `tmux-update-icons`

**Files:**
- Modify: `scripts/tmux-update-icons.sh`
- Test: `tests/agent-detect-arm.bats`

**Interfaces:**
- Consumes: `@agent_detect_bin@`. Known-agent command set is hardcoded to match the manifests (`claude`, `codex`) for v1 — a TODO notes deriving it from the binary later.

- [ ] **Step 1: Write the failing bats test** (tmux stubbed, per the reconcile-window test pattern):

```bash
#!/usr/bin/env bats

setup() {
  TMPBIN="$(mktemp -d)"
  cat >"$TMPBIN/tmux" <<'EOF'
#!/bin/sh
# record args; emulate: pane %3 runs codex with no pipe, %5 runs fish
case "$*" in
  *"list-panes"*) printf '%%3\tcodex\t0\n%%5\tfish\t0\n' ;;
  *"pipe-pane"*) echo "$@" >>"$PIPE_LOG" ;;
esac
EOF
  chmod +x "$TMPBIN/tmux"
  PATH="$TMPBIN:$PATH"
  export PIPE_LOG="$TMPBIN/pipe.log"
  : >"$PIPE_LOG"
}

teardown() { rm -rf "$TMPBIN"; }

@test "arms pipe-pane for an agent pane with no live pipe" {
  run bash -c 'source scripts/tmux-update-icons.sh; arm_agent_detect'
  grep -q 'pipe-pane.*%3.*agent-detect 3' "$PIPE_LOG"
}

@test "does not arm a non-agent pane" {
  run bash -c 'source scripts/tmux-update-icons.sh; arm_agent_detect'
  ! grep -q '%5' "$PIPE_LOG"
}
```

- [ ] **Step 2: Run to verify fail.** Run: `cd <repo> && nix develop -c bats tests/agent-detect-arm.bats`. Expected: FAIL (`arm_agent_detect` undefined).

- [ ] **Step 3: Add the arming function** to `scripts/tmux-update-icons.sh` (list panes with id, command, and pipe flag; arm when command is an agent and pipe flag is 0):

```bash
# Known agent commands (must match the shipped manifests). TODO: derive from
# `agent-detect --commands` once more agents land.
AGENT_COMMANDS="claude codex"
AGENT_DETECT_BIN="@agent_detect_bin@"

arm_agent_detect() {
	local pid cmd piped
	while IFS=$'\t' read -r pid cmd piped; do
		[[ $piped == 0 ]] || continue
		case " $AGENT_COMMANDS " in *" $cmd "*) ;; *) continue ;; esac
		tmux pipe-pane -o -t "$pid" "$AGENT_DETECT_BIN ${pid#%}"
	done < <(tmux list-panes -a -F '#{pane_id}	#{pane_current_command}	#{pane_pipe}' 2>/dev/null || true)
}
```

Call `arm_agent_detect` once per tick from the script's main flow. Two guards make this testable:
- Add `[[ $AGENT_DETECT_BIN == @* ]] && return 0` at the top of `arm_agent_detect` so a RAW (unsubstituted) build is a safe no-op; the bats test sets a real `AGENT_DETECT_BIN` via its stub env so the body runs.
- So bats can `source` the script to reach the function without executing the whole tick, wrap the script's existing top-level execution in a `main()` and end the file with `[[ ${BASH_SOURCE[0]} == "$0" ]] && main "$@"`. This is a contained refactor: the `#()` invocation executes the file (runs `main`); the test sources it (does not). Verify the reflow/icon behavior is unchanged after the wrap by building and reloading.

- [ ] **Step 4: Run to verify pass.** Run: `nix develop -c bats tests/agent-detect-arm.bats`. Expected: PASS (test defines a non-placeholder `AGENT_DETECT_BIN` via the stub, or set it in the test env).

- [ ] **Step 5: shellcheck.** Run: `nix develop -c shellcheck scripts/tmux-update-icons.sh`. Fix all findings.

- [ ] **Step 6: Commit.**

```bash
git add scripts/tmux-update-icons.sh tests/agent-detect-arm.bats
nix develop -c git commit -m "feat(agent-detect): arm pipe-pane for agent panes on the tick (#128)"
```

---

### Task 9: Merge in `read_pane_state`

**Files:**
- Modify: `scripts/lib-claude.sh` (`read_pane_state`, ~lines 54-112)
- Test: `tests/agent-detect-merge.bats`

**Interfaces:**
- Consumes: existing `CLAUDE_STALE_*` map, `CLAUDE_NOW`, the interrupt reclassifier block.
- Produces: `read_pane_state` that reads `screen/<id>` and applies the spec merge; sets the same `REPLY*` vars.

- [ ] **Step 1: Write the failing bats test** (the five spec cases):

```bash
#!/usr/bin/env bats

setup() {
  export CLAUDE_STATUS_DIR="$(mktemp -d)"
  mkdir -p "$CLAUDE_STATUS_DIR/panes" "$CLAUDE_STATUS_DIR/screen"
  source scripts/lib-claude.sh
  CLAUDE_NOW=100000            # pin the clock
}
teardown() { rm -rf "$CLAUDE_STATUS_DIR"; }

hook() { printf 'state=%s\ntimestamp=%s\n' "$2" "$3" >"$CLAUDE_STATUS_DIR/panes/$1"; }
screen() { printf 'state=%s\ntimestamp=%s\n' "$2" "$3" >"$CLAUDE_STATUS_DIR/screen/$1"; }

@test "fresh hook wins over screen" {
  hook 3 processing $((CLAUDE_NOW-5)); screen 3 idle $((CLAUDE_NOW-1))
  read_pane_state "$CLAUDE_STATUS_DIR/panes/3"
  [ "$REPLY" = processing ]
}

@test "stale hook + screen -> screen" {
  hook 3 processing $((CLAUDE_NOW-400)); screen 3 idle $((CLAUDE_NOW-1))
  read_pane_state "$CLAUDE_STATUS_DIR/panes/3"
  [ "$REPLY" = idle ]
}

@test "no hook + screen -> screen" {
  screen 7 processing $((CLAUDE_NOW-1))
  read_pane_state "$CLAUDE_STATUS_DIR/panes/7"
  [ "$REPLY" = processing ]
}

@test "idle hook never overridden by screen" {
  hook 3 idle $((CLAUDE_NOW-5000)); screen 3 processing $((CLAUDE_NOW-1))
  read_pane_state "$CLAUDE_STATUS_DIR/panes/3"
  [ "$REPLY" = idle ]
}

@test "neither -> failure" {
  run read_pane_state "$CLAUDE_STATUS_DIR/panes/999"
  [ "$status" -ne 0 ]
}
```

- [ ] **Step 2: Run to verify fail.** Run: `nix develop -c bats tests/agent-detect-merge.bats`. Expected: FAIL (screen file ignored; "no hook + screen" and "stale hook + screen" fail).

- [ ] **Step 3: Implement the merge.** In `read_pane_state`, after the existing hook parse and *before* the interrupt reclassifier, add screen read + the merge decision. Wrap the existing interrupt reclassifier so it only runs on the no-screen fallback path. Concretely: compute `max_age` from the `CLAUDE_STALE_*` map for the hook state; if hook is fresh, keep it and skip everything else; if hook stale and a `screen/<id>` state exists, adopt it (state+timestamp) and skip the reclassifier; if hook stale and no screen, run the reclassifier as today. When `panes/<id>` is absent but `screen/<id>` exists, load the screen state. Match the file's existing `key=val` read loop; guard with `[[ -f ]]`. Keep all `REPLY*` outputs identical in shape.

- [ ] **Step 4: Run to verify pass.** Run: `nix develop -c bats tests/agent-detect-merge.bats`. Expected: PASS (all five).

- [ ] **Step 5: Regression — existing behavior intact.** Run the existing suite: `nix develop -c bats tests/`. Expected: PASS (no regressions in claude-status/enrich tests).

- [ ] **Step 6: shellcheck + commit.**

```bash
nix develop -c shellcheck scripts/lib-claude.sh
git add scripts/lib-claude.sh tests/agent-detect-merge.bats
nix develop -c git commit -m "feat(agent-detect): merge screen state into read_pane_state (#128)"
```

---

### Task 10: Cleanup wiring

**Files:**
- Modify: `config/tmux.conf.nix` (~line 684, inline `pane-exited` `rm`)
- Modify: `scripts/claude-status-update.sh` (~line 59, ~line 380)

- [ ] **Step 1: Extend the inline `pane-exited` hook.** Add `/tmp/claude-status/screen/#{s/%%//:pane_id}` to the existing `rm -f` at `config/tmux.conf.nix:684`.

- [ ] **Step 2: Extend `cleanup_stale_panes`.** At `claude-status-update.sh:59`, add `"$STATE_DIR/screen/${pf##*/}"` to the `rm -f` list (define `SCREEN_DIR="$STATE_DIR/screen"` beside the other dir vars near line 11-16 for clarity).

- [ ] **Step 3: Extend the per-pane teardown** at `claude-status-update.sh:380` to also `rm -f "$SCREEN_DIR/$pane_file"` (match the surrounding removals).

- [ ] **Step 4: shellcheck.** Run: `nix develop -c shellcheck scripts/claude-status-update.sh`. Fix findings.

- [ ] **Step 5: Verify config builds.** Run: `nix build .#default 2>&1 | tail`. Expected: success.

- [ ] **Step 6: Commit.**

```bash
git add config/tmux.conf.nix scripts/claude-status-update.sh
nix develop -c git commit -m "feat(agent-detect): clean up screen/<id> on pane teardown (#128)"
```

---

### Task 11: Author the Claude and Codex manifests

**Files:**
- Modify: `picker/agentdetect/manifest/manifests/codex.toml`
- Create: `picker/agentdetect/manifest/manifests/claude.toml`
- Create: `picker/agentdetect/manifest/testdata/{claude_working.txt,claude_prompt.txt,claude_permission.txt,codex_working.txt,codex_idle.txt}`
- Test: `picker/agentdetect/manifest/fixtures_test.go`

**Interfaces:**
- Consumes: `Load`, `ForCommand`, `Match`.

- [ ] **Step 1: Capture fixtures from real sessions.** For each state, run the agent, reach the state, and save the rendered screen: `tmux capture-pane -pt <pane> > picker/agentdetect/manifest/testdata/<name>.txt`. For the title, capture separately: `tmux display -pt <pane> '#{pane_title}' >> <name>.txt` on a first line prefixed `TITLE:` (the test splits it off). Capture at least: Claude working (braille title), Claude at prompt (idle), Claude permission prompt (waiting), Codex working, Codex idle.

- [ ] **Step 2: Write the fixture-driven test:**

```go
package manifest

import (
	"os"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) (title, screen string) {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(b), "\n", 2)
	if strings.HasPrefix(lines[0], "TITLE:") {
		return strings.TrimPrefix(lines[0], "TITLE:"), lines[1]
	}
	return "", string(b)
}

func TestFixtures(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ file, cmd, want string }{
		{"claude_working.txt", "claude", "processing"},
		{"claude_prompt.txt", "claude", "idle"},
		{"claude_permission.txt", "claude", "waiting"},
		{"codex_working.txt", "codex", "processing"},
		{"codex_idle.txt", "codex", "idle"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			m, ok := ForCommand(ms, c.cmd)
			if !ok {
				t.Fatalf("no manifest for %s", c.cmd)
			}
			title, screen := loadFixture(t, c.file)
			got, _ := Match(m, screen, title, false)
			if got != c.want {
				t.Fatalf("Match(%s) = %q, want %q", c.file, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run to verify fail.** Run: `cd picker && go test ./agentdetect/manifest/ -run TestFixtures -v`. Expected: FAIL (manifests incomplete).

- [ ] **Step 4: Author `claude.toml`** using the real fixtures (derived from the spec's discussion; tune substrings to what the fixtures actually contain):

```toml
id = "claude"
match_commands = ["claude"]

[[rules]]
state = "processing"
priority = 100
region = "title"
regex = '^[\x{2800}-\x{28FF}] '   # braille spinner in the OSC title

[[rules]]
state = "skip"
priority = 90
region = "whole"
contains = ["select model", "esc to cancel"]

[[rules]]
state = "waiting"
priority = 80
region = "whole"
contains = ["do you want to proceed?"]

[[rules]]
state = "idle"
priority = 10
region = "last_lines:3"
regex = '❯'                  # the ❯ prompt marker
not = [ { contains = ["do you want to proceed?"] } ]
```

- [ ] **Step 5: Complete `codex.toml`** likewise (add an `idle` rule keyed on Codex's prompt; keep the `processing` rule on `esc to interrupt`). Tune to the captured fixtures.

- [ ] **Step 6: Run to verify pass.** Run: `cd picker && go test ./agentdetect/manifest/ -v`. Expected: PASS. Iterate rule substrings against the fixtures until green.

- [ ] **Step 7: Commit.**

```bash
git add picker/agentdetect/manifest/manifests/ picker/agentdetect/manifest/testdata/ picker/agentdetect/manifest/fixtures_test.go
nix develop -c git commit -m "feat(agent-detect): author claude + codex manifests with fixtures (#128)"
```

---

### Task 12: End-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Full flake check.** Run: `nix flake check 2>&1 | tail -30`. Expected: PASS (build + all pre-commit hooks + bats).

- [ ] **Step 2: Live smoke — Codex (the new capability).** Build (`nix build .#default`), launch a tmux using `./result/bin/tmux`, run `codex` in a pane, and confirm within ~2 s that `cat /tmp/claude-status/screen/<paneid>` shows `processing` while it works and `idle` at the prompt, and that the window icon reflects it.

- [ ] **Step 2b: Verify the arm gate.** `tmux display -pt <pane> '#{pane_pipe}'` should read `1` once armed; kill the `agent-detect` process and confirm it re-arms on the next tick (`pane_pipe` returns to `1`).

- [ ] **Step 3: Live regression — Claude fresh path unchanged.** In a Claude pane, confirm the status icon behaves exactly as before during a normal turn (hook-fresh path). Then interrupt a turn with Esc and confirm it shows `idle` (screen backfill) rather than sticking at `processing`.

- [ ] **Step 4: Push and open the PR.**

```bash
cd /home/noams/Data/git/noamsto/lazytmux-worktrees/feat-128-manifest-detection
git push -u origin feat/128-manifest-detection
gh pr create --assignee @me --title "feat: manifest-driven multi-agent status detection" --body "Implements #128. See docs/superpowers/specs/2026-07-05-manifest-detection-design.md. Records the Task 1 VT-lib decision: <fill in>."
```

---

## Notes for the implementer

- **Task 1 gates the Go tasks** but Tasks 8, 9, 10 (bash) are independent and can proceed in parallel.
- The **matcher, debounce, statefile, and merge are the correctness core** — they are fully unit/bats-tested. `main.go` is thin wiring, verified by the Task 12 live smoke.
- If the Task 1 spike picks `ultraviolet` or `vt10x`, only `screen/screen.go` changes; the `Screen` interface and every other task are unaffected.
- Keep the Claude fresh path a **strict no-op** in the merge (Task 9) — that is the guarantee the whole scope-B design rests on.
