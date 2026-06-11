# Splash / Welcome Buffer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a Ghostty/Amp-style animated welcome buffer — a shimmering ASCII sleepy-cat mascot plus an editable keybind cheatsheet — once in a tmux popup when a fresh, empty session is first reached.

**Architecture:** A new `tmux-splash` bubbletea binary added as a *second main package* inside the existing `picker/` Go module (shared `vendorHash`). A bash gate (`tmux-splash-maybe`) fired by indexed `client-attached` / `client-session-changed` tmux hooks decides eligibility and opens `display-popup -E -B -w 100% -h 100% tmux-splash`. The cheatsheet is a Nix module option codegen'd into the binary (mirroring the picker's `icons_generated.go`).

**Tech Stack:** Go + `charm.land/bubbletea/v2` + `charm.land/lipgloss/v2`; Nix (`buildGoModule`, home-manager module); bash + `bats`; tmux 3.6a.

---

## File Structure

**Create:**
- `picker/splash/main.go` — entrypoint: detect theme, build model, run program.
- `picker/splash/art.go` — `//go:embed` the two art files; `parseArt`/`pickArt`.
- `picker/splash/gradient.go` — pure gradient math (hex↔rgb, lerp, `buildGradient`, `paletteIndex`).
- `picker/splash/theme.go` — `detectTheme()` (copied from picker; the splash package can't import picker's `main`).
- `picker/splash/model.go` — bubbletea model (`Init`/`Update`/`View`), `tip` type, rendering.
- `picker/splash/tips_generated.go` — **committed default**, overwritten by Nix at build (like `icons_generated.go`).
- `picker/splash/assets/cat.txt`, `picker/splash/assets/cat-small.txt` — the mascot art.
- `picker/splash/gradient_test.go`, `theme_test.go`, `art_test.go`, `model_test.go`.
- `scripts/tmux-splash-maybe.sh` — the eligibility gate + popup launcher.
- `tests/splash.bats` — gate-logic tests (stubbed `tmux`).

**Modify:**
- `picker/default.nix` — build both binaries (`subPackages`), codegen `tips_generated.go`, rename outputs.
- `config/tmux.conf.nix` — new args (`splashEnable`/`splashTips`/`splashTimeout`); pass to picker import; add `tmux-splash-maybe` script + splash binary ref; add gated hooks.
- `modules/home-manager.nix` — `programs.lazytmux.splash.{enable,timeout,tips}` options; thread into `tmuxConfig`.
- `flake.nix` — add `splash-tests` check running `tests/splash.bats`.
- `CLAUDE.md` — document the new script + binary roles.

---

## Task 1: Gradient math (pure)

**Files:**
- Create: `picker/splash/gradient.go`
- Create: `picker/splash/main.go` (temporary stub so the package builds)
- Test: `picker/splash/gradient_test.go`

- [ ] **Step 1: Write the failing test**

`picker/splash/gradient_test.go`:
```go
package main

import "testing"

func TestBuildGradientLengthAndStart(t *testing.T) {
	g := buildGradient([]string{"#000000", "#ffffff"}, 24)
	if len(g) != 24 {
		t.Fatalf("len = %d, want 24", len(g))
	}
	if g[0] != "#000000" {
		t.Errorf("g[0] = %q, want #000000", g[0])
	}
}

func TestPaletteIndexWraps(t *testing.T) {
	n := 5
	if got := paletteIndex(0, 0, 0, n); got != 0 {
		t.Errorf("(0,0,0) = %d, want 0", got)
	}
	// x+y-t negative must wrap into [0,n)
	if got := paletteIndex(0, 0, 3, n); got != 2 {
		t.Errorf("(0,0,3) = %d, want 2", got)
	}
	if got := paletteIndex(4, 4, 0, n); got != 3 {
		t.Errorf("(4,4,0) = %d, want 3", got)
	}
	if got := paletteIndex(0, 0, 0, 0); got != 0 {
		t.Errorf("n=0 guard = %d, want 0", got)
	}
}

func TestHexRoundTrip(t *testing.T) {
	r, g, b := hexToRGB("#89b4fa")
	if r != 0x89 || g != 0xb4 || b != 0xfa {
		t.Fatalf("hexToRGB = %d,%d,%d", r, g, b)
	}
	if got := rgbToHex(r, g, b); got != "#89b4fa" {
		t.Errorf("rgbToHex = %q, want #89b4fa", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run 'Gradient|Palette|Hex' -v`
Expected: FAIL — `undefined: buildGradient` (package doesn't compile).

- [ ] **Step 3: Write the implementation**

`picker/splash/gradient.go`:
```go
package main

import (
	"fmt"
	"strconv"
)

// Catppuccin gradient anchors per theme: blue → sapphire → lavender → mauve.
var gradientAnchors = map[string][]string{
	"dark":  {"#89b4fa", "#74c7ec", "#b4befe", "#cba6f7"},
	"light": {"#1e66f5", "#209fb5", "#7287fd", "#8839ef"},
}

func hexToRGB(hex string) (int, int, int) {
	r, _ := strconv.ParseInt(hex[1:3], 16, 0)
	g, _ := strconv.ParseInt(hex[3:5], 16, 0)
	b, _ := strconv.ParseInt(hex[5:7], 16, 0)
	return int(r), int(g), int(b)
}

func rgbToHex(r, g, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func lerp(a, b float64, t float64) float64 { return a + (b-a)*t }

// buildGradient interpolates the anchors into `steps` evenly-spaced colors,
// looping back to the first anchor so the ripple is seamless.
func buildGradient(anchors []string, steps int) []string {
	loop := append(append([]string{}, anchors...), anchors[0])
	segs := len(loop) - 1
	out := make([]string, steps)
	for i := 0; i < steps; i++ {
		p := float64(i) / float64(steps) * float64(segs)
		seg := int(p)
		if seg >= segs {
			seg = segs - 1
		}
		frac := p - float64(seg)
		r1, g1, b1 := hexToRGB(loop[seg])
		r2, g2, b2 := hexToRGB(loop[seg+1])
		out[i] = rgbToHex(
			int(lerp(float64(r1), float64(r2), frac)),
			int(lerp(float64(g1), float64(g2), frac)),
			int(lerp(float64(b1), float64(b2), frac)),
		)
	}
	return out
}

// paletteIndex maps cell (x,y) at frame t onto a gradient index, wrapping.
func paletteIndex(x, y, t, n int) int {
	if n <= 0 {
		return 0
	}
	i := (x + y - t) % n
	if i < 0 {
		i += n
	}
	return i
}
```

`picker/splash/main.go` (temporary stub — replaced in Task 5):
```go
package main

func main() {}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run 'Gradient|Palette|Hex' -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add picker/splash/gradient.go picker/splash/gradient_test.go picker/splash/main.go
nix develop -c git commit -m "feat(splash): gradient ripple color math"
```

---

## Task 2: Theme detection

**Files:**
- Create: `picker/splash/theme.go`
- Test: `picker/splash/theme_test.go`

- [ ] **Step 1: Write the failing test**

`picker/splash/theme_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectThemeFromFixture(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "theme-state.json"), []byte(`{"theme":"light"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", dir)
	if got := detectTheme(); got != "light" {
		t.Errorf("detectTheme = %q, want light", got)
	}
}

func TestDetectThemeMissingFileDefaultsDark(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := detectTheme(); got != "dark" {
		t.Errorf("detectTheme = %q, want dark", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run Theme -v`
Expected: FAIL — `undefined: detectTheme`.

- [ ] **Step 3: Write the implementation**

`picker/splash/theme.go`:
```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// detectTheme reads $XDG_STATE_HOME/theme-state.json (same contract as the
// shell scripts and the picker), defaulting to "dark" when absent/unparseable.
func detectTheme() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		xdg = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	data, err := os.ReadFile(filepath.Join(xdg, "theme-state.json"))
	if err != nil {
		return "dark"
	}
	var cfg struct {
		Theme string `json:"theme"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.Theme == "" {
		return "dark"
	}
	return cfg.Theme
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run Theme -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add picker/splash/theme.go picker/splash/theme_test.go
nix develop -c git commit -m "feat(splash): theme detection from theme-state.json"
```

---

## Task 3: Art assets + sizing

**Files:**
- Create: `picker/splash/assets/cat.txt`
- Create: `picker/splash/assets/cat-small.txt`
- Create: `picker/splash/art.go`
- Test: `picker/splash/art_test.go`

- [ ] **Step 1: Create the art files**

`picker/splash/assets/cat.txt` (curled sleeping cat — leading spaces are significant):
```
            z
          z
        z
        |\      _,,,---,,_
   ZZZzz /,`.-'`'    -.  ;-;;,_
        |,4-  ) )-,_..;\ (  `'-'
       '---''(_/--'  `-'\_)
```

`picker/splash/assets/cat-small.txt` (compact loaf):
```
   /\_/\   z
  ( -.- )  z
   > ^ <
```

- [ ] **Step 2: Write the failing test**

`picker/splash/art_test.go`:
```go
package main

import "testing"

func TestParseArtDimensions(t *testing.T) {
	a := parseArt("ab\ncdef\n")
	if a.h != 2 {
		t.Errorf("h = %d, want 2", a.h)
	}
	if a.w != 4 {
		t.Errorf("w = %d, want 4 (widest line)", a.w)
	}
}

func TestPickArt(t *testing.T) {
	full := artGrid{w: 40, h: 8}
	small := artGrid{w: 12, h: 3}
	// reserve: lines used by wordmark + cheatsheet + hint
	const reserve = 10

	if a, show := pickArt(full, small, 80, 24); !show || a.w != full.w {
		t.Errorf("roomy viewport should pick full, got show=%v w=%d", show, a.w)
	}
	if a, show := pickArt(full, small, 20, 24); !show || a.w != small.w {
		t.Errorf("narrow viewport should pick small, got show=%v w=%d", show, a.w)
	}
	if _, show := pickArt(full, small, 8, reserve+1); show {
		t.Error("tiny viewport should drop the mascot (show=false)")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run Art -v`
Expected: FAIL — `undefined: parseArt`.

- [ ] **Step 4: Write the implementation**

`picker/splash/art.go`:
```go
package main

import (
	_ "embed"
	"strings"
	"unicode/utf8"
)

//go:embed assets/cat.txt
var catFull string

//go:embed assets/cat-small.txt
var catSmall string

type artGrid struct {
	lines []string
	w, h  int
}

func parseArt(s string) artGrid {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	w := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > w {
			w = n
		}
	}
	return artGrid{lines: lines, w: w, h: len(lines)}
}

// reserveLines is the vertical space the non-mascot rows (zzz spacer, wordmark,
// cheatsheet, dismiss hint) need; the mascot is dropped if it can't coexist.
const reserveLines = 10

// pickArt returns the largest art that fits the viewport, and whether to show a
// mascot at all (false → wordmark + cheatsheet only).
func pickArt(full, small artGrid, vw, vh int) (artGrid, bool) {
	if full.w <= vw && full.h+reserveLines <= vh {
		return full, true
	}
	if small.w <= vw && small.h+reserveLines <= vh {
		return small, true
	}
	return artGrid{}, false
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run Art -v`
Expected: PASS (2 tests).

- [ ] **Step 6: Commit**

```bash
git add picker/splash/art.go picker/splash/art_test.go picker/splash/assets/
nix develop -c git commit -m "feat(splash): embed mascot art + viewport-aware sizing"
```

---

## Task 4: Model, rendering, and quit behavior

**Files:**
- Create: `picker/splash/model.go`
- Create: `picker/splash/tips_generated.go` (committed default; Nix overwrites)
- Modify: `picker/splash/main.go` (replace the stub)
- Test: `picker/splash/model_test.go`

- [ ] **Step 1: Create the committed default `tips_generated.go`**

`picker/splash/tips_generated.go`:
```go
package main

// Generated by Nix from programs.lazytmux.splash.*. Committed defaults here are
// overwritten at build time. Do not rely on editing this file directly.

var splashTips = []tip{
	{Key: "prefix + s", Label: "Sessions"},
	{Key: "prefix + w", Label: "Windows"},
	{Key: "prefix + a", Label: "Claude windows"},
	{Key: "prefix + i", Label: "Issues / PRs"},
	{Key: "prefix + g", Label: "LazyGit"},
	{Key: "prefix + b", Label: "btop"},
	{Key: "prefix + R", Label: "Restore snapshot"},
	{Key: "prefix + u", Label: "Undo close"},
}

var splashPrefix = "`"

var splashTimeoutSec = 10
```

- [ ] **Step 2: Write the failing test**

`picker/splash/model_test.go`:
```go
package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestTimeoutQuits(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10)
	_, cmd := m.Update(timeoutMsg{})
	if !quits(cmd) {
		t.Error("timeoutMsg should produce tea.Quit")
	}
}

func TestFrameAdvances(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10)
	next, cmd := m.Update(frameMsg{})
	if next.(model).frame != 1 {
		t.Errorf("frame = %d, want 1", next.(model).frame)
	}
	if cmd == nil {
		t.Error("frameMsg should re-arm the frame tick")
	}
}

func TestRenderSubstitutesPrefix(t *testing.T) {
	m := newModel("dark", []tip{{Key: "prefix + s", Label: "Sessions"}}, "C-a", 10)
	m.width, m.height = 80, 24
	out := m.View().String()
	if !contains(out, "C-a + s") {
		t.Errorf("rendered tips should substitute prefix; got:\n%s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

> Note: key-dismiss is verified manually in Task 10 — constructing a v2
> `tea.KeyPressMsg` literal is API-fragile, so we don't unit-test it here.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd picker && nix develop ../ -c go test ./splash/ -run 'Timeout|Frame|Render' -v`
Expected: FAIL — `undefined: newModel` / `tip`.

- [ ] **Step 4: Write the implementation**

`picker/splash/model.go`:
```go
package main

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type tip struct {
	Key   string
	Label string
}

type frameMsg struct{}
type timeoutMsg struct{}

type model struct {
	theme         string
	tips          []tip
	prefix        string
	timeoutSec    int
	gradient      []string
	frame         int
	width, height int
	full, small   artGrid
}

func newModel(theme string, tips []tip, prefix string, timeoutSec int) model {
	anchors, ok := gradientAnchors[theme]
	if !ok {
		anchors = gradientAnchors["dark"]
	}
	return model{
		theme:      theme,
		tips:       tips,
		prefix:     prefix,
		timeoutSec: timeoutSec,
		gradient:   buildGradient(anchors, 48),
		full:       parseArt(catFull),
		small:      parseArt(catSmall),
	}
}

func frameCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return frameMsg{} })
}

func timeoutCmd(sec int) tea.Cmd {
	return tea.Tick(time.Duration(sec)*time.Second, func(time.Time) tea.Msg { return timeoutMsg{} })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(frameCmd(), timeoutCmd(m.timeoutSec))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		_ = msg
		return m, tea.Quit
	case timeoutMsg:
		return m, tea.Quit
	case frameMsg:
		m.frame++
		return m, frameCmd()
	}
	return m, nil
}

func (m model) accent() string {
	if m.theme == "light" {
		return "#7287fd"
	}
	return "#b4befe"
}

func (m model) subtext() string {
	if m.theme == "light" {
		return "#6c6f85"
	}
	return "#a6adc8"
}

// colorizeArt applies the moving gradient to each non-space glyph.
func (m model) colorizeArt(a artGrid) string {
	var sb strings.Builder
	for y, line := range a.lines {
		x := 0
		for _, r := range line {
			if r == ' ' {
				sb.WriteRune(' ')
				x++
				continue
			}
			hex := m.gradient[paletteIndex(x, y, m.frame, len(m.gradient))]
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(string(r)))
			x++
		}
		if y < len(a.lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (m model) renderTips() string {
	if len(m.tips) == 0 {
		return ""
	}
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.accent())).Bold(true)
	lblStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.subtext()))

	// Longest key (post-substitution) sets the column width.
	keyOf := func(t tip) string { return strings.ReplaceAll(t.Key, "prefix", m.prefix) }
	keyW := 0
	for _, t := range m.tips {
		if n := lipgloss.Width(keyOf(t)); n > keyW {
			keyW = n
		}
	}

	half := (len(m.tips) + 1) / 2
	var rows []string
	for i := 0; i < half; i++ {
		left := m.tipCell(m.tips[i], keyOf, keyStyle, lblStyle, keyW)
		right := ""
		if j := i + half; j < len(m.tips) {
			right = m.tipCell(m.tips[j], keyOf, keyStyle, lblStyle, keyW)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m model) tipCell(t tip, keyOf func(tip) string, keyStyle, lblStyle lipgloss.Style, keyW int) string {
	key := keyStyle.Width(keyW).Render(keyOf(t))
	return lipgloss.JoinHorizontal(lipgloss.Top, key, "  ", lblStyle.Render(t.Label))
}

func (m model) render() string {
	var blocks []string
	if art, show := pickArt(m.full, m.small, m.width, m.height); show {
		blocks = append(blocks, m.colorizeArt(art))
	}
	wordmark := lipgloss.NewStyle().Foreground(lipgloss.Color(m.accent())).Bold(true).Render("l a z y t m u x")
	blocks = append(blocks, wordmark, "")
	if tips := m.renderTips(); tips != "" {
		blocks = append(blocks, tips, "")
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.subtext())).Italic(true).Render("press any key to dismiss")
	blocks = append(blocks, hint)
	return lipgloss.JoinVertical(lipgloss.Center, blocks...)
}

func (m model) View() tea.View {
	w, h := m.width, m.height
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	content := lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, m.render())
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
```

`picker/splash/main.go` (replace the stub):
```go
package main

import (
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	m := newModel(detectTheme(), splashTips, splashPrefix, splashTimeoutSec)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd picker && nix develop ../ -c go test ./splash/ -v`
Expected: PASS (all splash tests, including the earlier tasks).

- [ ] **Step 6: Commit**

```bash
git add picker/splash/model.go picker/splash/main.go picker/splash/tips_generated.go picker/splash/model_test.go
nix develop -c git commit -m "feat(splash): bubbletea model, ripple render, quit-on-key/timeout"
```

---

## Task 5: Nix packaging — build both binaries + codegen tips

**Files:**
- Modify: `picker/default.nix`

- [ ] **Step 1: Update `picker/default.nix`**

Replace the file with (changes: new params `splashTips`/`splashTimeout`/`prefix`; generate `splash/tips_generated.go`; `subPackages`; rename both binaries):
```nix
{
  pkgs,
  lib,
  processIcons,
  fallbackIcon,
  maxIconsPicker,
  splashTips ? [],
  splashTimeout ? 10,
  prefix ? "`",
}: let
  iconsGo = pkgs.writeText "icons_generated.go" ''
    package main

    // Generated by Nix from process-icons.nix and lib-claude.sh constants.
    // Do not edit manually.

    var iconMap = map[string]string{
    ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "	${builtins.toJSON k}: ${builtins.toJSON v},") processIcons)}
    }

    var fallbackIcon = ${builtins.toJSON fallbackIcon}
    var maxIconsPicker = ${maxIconsPicker}

    // Claude status icons (from lib-claude.sh)
    var claudeSpinnerFrames = []string{"󰪞", "󰪟", "󰪠", "󰪡", "󰪢", "󰪣", "󰪤", "󰪥"}
    var claudeIconWaiting = "󰔟"
    var claudeIconCompacting = "󰡍"
    var claudeIconDone = "󰸞"
    var claudeIconIdle = "󰒲"
    var claudeIconError = "󰅚"
    var claudeIconDenied = "󰔟" // same clock as waiting, different color

    // Default icons (overridden by env vars or tmux options at runtime)
    var iconSession = ""
    var iconDir = ""
    var iconBranch = ""
  '';

  tipsGo = pkgs.writeText "tips_generated.go" ''
    package main

    // Generated by Nix from programs.lazytmux.splash.*. Do not edit manually.

    var splashTips = []tip{
    ${lib.concatMapStringsSep "\n" (t: "	{Key: ${builtins.toJSON t.key}, Label: ${builtins.toJSON t.label}},") splashTips}
    }

    var splashPrefix = ${builtins.toJSON prefix}

    var splashTimeoutSec = ${toString splashTimeout}
  '';

  src = pkgs.runCommand "picker-src" {} ''
    cp -r ${lib.cleanSource ./.} $out
    chmod -R u+w $out
    cp ${iconsGo} $out/icons_generated.go
    cp ${tipsGo} $out/splash/tips_generated.go
    rm -f $out/default.nix
  '';
in
  pkgs.buildGoModule {
    pname = "lazytmux-go-tools";
    version = "0.1.0";
    inherit src;
    vendorHash = "sha256-gGs19TTxci8a3adA4CJOKs/IHDhqojjpxH9WueDInLk=";
    subPackages = ["." "splash"];
    ldflags = ["-s" "-w"];
    postInstall = ''
      mv $out/bin/picker $out/bin/tmux-picker-generate
      mv $out/bin/splash $out/bin/tmux-splash
    '';
  }
```

> If `vendorHash` mismatches after adding the subpackage (it shouldn't — no new
> modules), copy the `got:` hash from the build error into `vendorHash`.

- [ ] **Step 2: Build the package to verify both binaries appear**

Run: `nix build .#default 2>&1 | tail -20`
Expected: build succeeds.

- [ ] **Step 3: Confirm the splash binary exists and runs**

Run: `nix build .#default && test -x result/bin/* ; ./result/bin/tmux 2>/dev/null; ls $(nix path-info .#default 2>/dev/null)/bin 2>/dev/null || rg -l tmux-splash result/`
Then directly: `nix eval --raw .#default` is not needed — instead verify via the wrapped PATH in later tasks.
Practical check: `nix build .#default` succeeding implies `subPackages` produced `tmux-splash` (postInstall would fail on a missing `$out/bin/splash`).

- [ ] **Step 4: Commit**

```bash
git add picker/default.nix
nix develop -c git commit -m "build(splash): package tmux-splash + codegen tips_generated.go"
```

---

## Task 6: Gate script + wire splash binary into the tmux PATH

**Files:**
- Create: `scripts/tmux-splash-maybe.sh`
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Write the gate script**

`scripts/tmux-splash-maybe.sh`:
```bash
#!/usr/bin/env bash
# Show the welcome splash once, only on a fresh, empty session.
# Fired (backgrounded) from client-attached / client-session-changed hooks.
# $1 = target session name (#{hook_session}); falls back to the current one.
set -euo pipefail

session="${1:-}"
[ -n "$session" ] || session="$(tmux display-message -p '#{session_name}')"

# Once per session.
[ "$(tmux show-option -t "$session" -qv @splash_shown)" = "1" ] && exit 0

# Only a brand-new, single-pane session.
[ "$(tmux display-message -t "$session" -p '#{session_windows}')" = "1" ] || exit 0
[ "$(tmux display-message -t "$session" -p '#{window_panes}')" = "1" ] || exit 0

# Only when the pane is sitting at an interactive shell — never cover a
# tmux-state–restored program/editor.
case "$(tmux display-message -t "$session" -p '#{pane_current_command}')" in
	fish | bash | zsh | sh | dash | nu) ;;
	*) exit 0 ;;
esac

tmux set-option -t "$session" @splash_shown 1
tmux display-popup -E -B -w 100% -h 100% -t "$session" @tmux_splash@
```

- [ ] **Step 2: Lint the script**

Run: `shellcheck scripts/tmux-splash-maybe.sh`
Expected: no warnings or errors.

- [ ] **Step 3: Wire it into `config/tmux.conf.nix`**

In `config/tmux.conf.nix`, add the splash binary reference next to `picker-generate-bin` (after line ~170):
```nix
  picker-splash-bin = "${picker-generate}/bin/tmux-splash";
```

Pass the new args into the picker import (modify the `picker-generate = import ../picker { ... }` block, ~166):
```nix
  picker-generate = import ../picker {
    inherit pkgs lib processIcons fallbackIcon;
    inherit maxIconsPicker;
    inherit splashTips splashTimeout prefix;
  };
```

Add `tmux-splash-maybe` to `scriptNames` (the list at ~172):
```nix
    "tmux-pr-enrich"
    "tmux-splash-maybe"
  ];
```

Add a dedicated builder + `script` branch. After `mkScript` (~148) add:
```nix
  mkScriptSplash = name:
    pkgs.writeShellScriptBin name (
      builtins.replaceStrings ["@tmux_splash@"] [picker-splash-bin]
      (builtins.readFile ../scripts/${name}.sh)
    );
```
And in the `script = lib.genAttrs scriptNames (name: ...)` chain (~239), add a branch before the final `else mkScript name`:
```nix
    else if name == "tmux-splash-maybe"
    then mkScriptSplash name
    else mkScript name);
```

Add the new function args to the header (`config/tmux.conf.nix` arg set, ~13–25):
```nix
  # Welcome-buffer splash (threaded from the home-manager module).
  splashEnable ? true,
  splashTips ? [],
  splashTimeout ? 10,
```

- [ ] **Step 4: Build to verify the gate packages and substitutes**

Run: `nix build .#default 2>&1 | tail -20`
Expected: build succeeds (proves `picker-splash-bin` resolves and the script substitution is valid).

- [ ] **Step 5: Commit**

```bash
git add scripts/tmux-splash-maybe.sh config/tmux.conf.nix
nix develop -c git commit -m "feat(splash): eligibility gate + tmux PATH wiring"
```

---

## Task 7: Splash hooks (gated)

**Files:**
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Add the hooks after the reflow hook block**

In `config/tmux.conf.nix`, immediately after the reflow `set-hook -g client-session-changed ...` line (~500), add:
```nix
    ${lib.optionalString splashEnable ''
      # Welcome buffer: indexed ([50]) so it coexists with the reflow hooks'
      # index-0 bindings on the same events (a bare set-hook would clobber them).
      set-hook -g client-attached[50]        'run-shell -b "${script.tmux-splash-maybe}/bin/tmux-splash-maybe #{hook_session}"'
      set-hook -g client-session-changed[50] 'run-shell -b "${script.tmux-splash-maybe}/bin/tmux-splash-maybe #{hook_session}"'
    ''}
```

> The idempotent `set-hook -gu client-session-changed` clears at lines ~480–492
> run *before* this block on every source, then reflow re-sets index 0 and this
> re-sets index 50 — so re-sourcing stays consistent.

- [ ] **Step 2: Build and inspect the generated config**

Run: `nix build .#default && rg -n 'splash-maybe|@splash_shown|client-attached\[50\]' $(readlink -f result)/share/tmux/* 2>/dev/null || nix build .#default 2>&1 | tail -5`
Expected: build succeeds; the splash hooks appear in the generated `tmux.conf`.
(If the config path differs, just confirm `nix build .#default` succeeds — the `${script.tmux-splash-maybe}` reference would fail evaluation if wired wrong.)

- [ ] **Step 3: Commit**

```bash
git add config/tmux.conf.nix
nix develop -c git commit -m "feat(splash): fire welcome buffer via indexed client hooks"
```

---

## Task 8: Home-manager module options

**Files:**
- Modify: `modules/home-manager.nix`

- [ ] **Step 1: Add the `splash` option block**

In `modules/home-manager.nix`, alongside the other option blocks (e.g. after the `enrich` block, ~280), add:
```nix
    splash = {
      enable =
        lib.mkEnableOption "the animated welcome-buffer splash on fresh sessions"
        // {default = true;};
      timeout = lib.mkOption {
        type = lib.types.ints.between 1 120;
        default = 10;
        description = "Seconds before the splash auto-dismisses (also dismissed by any key).";
      };
      tips = lib.mkOption {
        type = lib.types.listOf (lib.types.submodule {
          options = {
            key = lib.mkOption {
              type = lib.types.str;
              description = "Key hint; the token `prefix` is replaced with the real prefix.";
            };
            label = lib.mkOption {
              type = lib.types.str;
              description = "What the key does.";
            };
          };
        });
        default = [
          {key = "prefix + s"; label = "Sessions";}
          {key = "prefix + w"; label = "Windows";}
          {key = "prefix + a"; label = "Claude windows";}
          {key = "prefix + i"; label = "Issues / PRs";}
          {key = "prefix + g"; label = "LazyGit";}
          {key = "prefix + b"; label = "btop";}
          {key = "prefix + R"; label = "Restore snapshot";}
          {key = "prefix + u"; label = "Undo close";}
        ];
        description = "Keybind cheatsheet shown in the welcome buffer. Empty = mascot only.";
      };
    };
```

- [ ] **Step 2: Thread the options into `tmuxConfig`**

In the `tmuxConfig = import ../config/tmux.conf.nix { ... }` call (~98), add:
```nix
    splashEnable = cfg.splash.enable;
    splashTips = cfg.splash.tips;
    splashTimeout = cfg.splash.timeout;
```

- [ ] **Step 3: Build with the module to verify evaluation**

Run: `nix build .#default 2>&1 | tail -20`
Expected: build succeeds (the flake's `packages.default` doesn't exercise the module, so also dry-eval the module if a host imports it; the option types are checked at `nix flake check` in Task 9/10).

- [ ] **Step 4: Commit**

```bash
git add modules/home-manager.nix
nix develop -c git commit -m "feat(splash): programs.lazytmux.splash module options"
```

---

## Task 9: Gate logic bats test + flake check

**Files:**
- Create: `tests/splash.bats`
- Modify: `flake.nix`

- [ ] **Step 1: Write the failing test**

`tests/splash.bats` (stubs `tmux` on PATH; records whether `display-popup` ran):
```bash
#!/usr/bin/env bats

# A fake `tmux` driven by env vars. It logs `display-popup` invocations to
# $POPUP_LOG so the test can assert whether the splash would have opened.
setup() {
	STUBDIR="$(mktemp -d)"
	POPUP_LOG="$STUBDIR/popup.log"
	export POPUP_LOG
	cat >"$STUBDIR/tmux" <<-'EOF'
		#!/usr/bin/env bash
		case "$1" in
		show-option) echo "${FAKE_SHOWN:-}";;
		display-message)
			# last arg is the -p format string
			fmt="${@: -1}"
			case "$fmt" in
			'#{session_name}') echo "s";;
			'#{session_windows}') echo "${FAKE_WINDOWS:-1}";;
			'#{window_panes}') echo "${FAKE_PANES:-1}";;
			'#{pane_current_command}') echo "${FAKE_CMD:-fish}";;
			esac;;
		set-option) ;;
		display-popup) echo "called" >>"$POPUP_LOG";;
		esac
	EOF
	chmod +x "$STUBDIR/tmux"
	PATH="$STUBDIR:$PATH"
	# Substitute the @tmux_splash@ placeholder the way Nix does at build time.
	GATE="$STUBDIR/gate.sh"
	sed 's#@tmux_splash@#/bin/true#' scripts/tmux-splash-maybe.sh >"$GATE"
	chmod +x "$GATE"
}

teardown() { rm -rf "$STUBDIR"; }

@test "fresh single-shell session opens the popup" {
	FAKE_SHOWN="" FAKE_WINDOWS=1 FAKE_PANES=1 FAKE_CMD=fish run "$GATE" s
	[ "$status" -eq 0 ]
	[ -s "$POPUP_LOG" ]
}

@test "already-shown session does not open the popup" {
	FAKE_SHOWN=1 run "$GATE" s
	[ "$status" -eq 0 ]
	[ ! -s "$POPUP_LOG" ]
}

@test "multi-pane session does not open the popup" {
	FAKE_SHOWN="" FAKE_WINDOWS=1 FAKE_PANES=2 run "$GATE" s
	[ "$status" -eq 0 ]
	[ ! -s "$POPUP_LOG" ]
}

@test "session running a program (not a shell) does not open the popup" {
	FAKE_SHOWN="" FAKE_WINDOWS=1 FAKE_PANES=1 FAKE_CMD=nvim run "$GATE" s
	[ "$status" -eq 0 ]
	[ ! -s "$POPUP_LOG" ]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop -c bats tests/splash.bats`
Expected: tests run; the "program (not a shell)" / "multi-pane" cases pass, but they will only pass once the gate exits early correctly. If the gate is correct from Task 6, they pass immediately — in that case treat Step 1 as a regression guard and proceed. (If any fail, fix `scripts/tmux-splash-maybe.sh`.)

- [ ] **Step 3: Add the flake check**

In `flake.nix`, inside the `checks = { ... }` attrset (after `claude-images-tests`), add:
```nix
          splash-tests =
            pkgs.runCommand "splash-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/splash.bats
              touch $out
            '';
```

- [ ] **Step 4: Run the check in isolation**

Run: `nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').splash-tests 2>&1 | tail -20`
Expected: build succeeds (all 4 bats tests pass).

- [ ] **Step 5: Commit**

```bash
git add tests/splash.bats flake.nix
nix develop -c git commit -m "test(splash): gate eligibility bats + flake check"
```

---

## Task 10: Docs, full check, and manual verification

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Document the new pieces in `CLAUDE.md`**

In the **Script Roles** table, add a row:
```markdown
| `tmux-splash-maybe` | `client-attached[50]` / `client-session-changed[50]` hooks (backgrounded) | Eligibility gate for the welcome buffer: shows the splash once (`@splash_shown`) on a fresh 1-window/1-pane session whose pane is at a shell; opens `tmux-splash` in a fullscreen popup. |
```

In **Architecture**, add a short subsection after **PR + Issue Enrichment**:
```markdown
### Welcome Buffer (splash)

`tmux-splash` (a bubbletea binary, second main package in the `picker/` Go
module) renders an animated sleepy-cat mascot with a gradient ripple plus a
keybind cheatsheet, shown once per fresh session via `display-popup`. Enabled by
default through `programs.lazytmux.splash.enable`.

- **Trigger:** indexed `client-attached[50]` / `client-session-changed[50]`
  hooks fire `tmux-splash-maybe`, which gates on `@splash_shown` +
  1-window/1-pane + `pane_current_command` being a shell.
- **Cheatsheet:** `splash.tips` (list of `{ key; label; }`) is codegen'd into the
  binary at build time (`picker/splash/tips_generated.go`, like the picker's
  `icons_generated.go`); the `prefix` token is substituted at render time.
- **Dismiss:** any key or `splash.timeout` seconds (default 10).
```

- [ ] **Step 2: Run the full flake check**

Run: `nix flake check 2>&1 | tail -30`
Expected: all checks pass (Go tests via `buildGoModule`, all bats suites incl. `splash-tests`, pre-commit hooks).

- [ ] **Step 3: Build and reload in a live tmux to verify behavior**

Run: `nix build .#default`

Then verify manually (these are the cases unit tests can't cover):
```bash
# a) Fresh session shows the splash once, dismisses on a key and on timeout:
./result/bin/tmux -L splashcheck new-session -s a    # ripple + tips appear; press a key → prompt
# b) Picker-created session also splashes (client-session-changed):
#    inside that tmux: prefix + s → pick/create another session → splash shows
# c) No replay: split a window / open a 2nd window → no splash; detach+reattach → no splash
# d) Small terminal: resize the outer terminal narrow before attaching → compact cat / tips-only
./result/bin/tmux -L splashcheck kill-server
```
Expected: (a) ripple animates, dismisses on key and after ~10s; (b) splash on the new session; (c) no replay; (d) compact/tips-only path, no crash.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
nix develop -c git commit -m "docs(splash): document welcome buffer in CLAUDE.md"
```

- [ ] **Step 5: Open the PR**

```bash
git push -u origin feat/splash-animation
gh pr create --assignee @me --title "feat(splash): animated welcome buffer on fresh sessions" --body "Shimmering ASCII sleepy-cat mascot + editable keybind cheatsheet, shown once per fresh session via display-popup. See docs/superpowers/specs/2026-06-09-splash-animation-design.md."
```

---

## Self-Review

**Spec coverage:**
- Mascot + gradient ripple → Tasks 1, 3, 4. ✓
- Welcome-buffer layout (cat + wordmark + cheatsheet + hint) → Task 4 `render`. ✓
- Configurable cheatsheet (`splash.tips`, prefix token) → Tasks 4, 5, 8. ✓
- Two art sizes / responsive / tips-only fallback → Task 3 `pickArt`, Task 4 `render`. ✓
- Subpackage / single vendorHash → Task 5. ✓
- Theme detection (Latte/Mocha, dark fallback) → Task 2; gradient anchors → Task 1. ✓
- Truecolor degrade → handled by lipgloss/colorprofile (no task needed; noted). ✓
- Dual-hook trigger, indexed → Task 7. ✓
- Gate (`@splash_shown` + 1win/1pane + shell command) → Task 6, tested Task 9. ✓
- Auto-dismiss timeout + any-key → Task 4 (`timeoutCmd`, `KeyPressMsg`). ✓
- `-B` borderless fullscreen popup → Task 6. ✓
- Module options (`enable` default true, `timeout`) → Task 8. ✓
- Tests (Go units + bats gate) under `nix flake check` → Tasks 1–4, 9, 10. ✓
- Docs → Task 10. ✓

**Placeholder scan:** No TBDs; every code step contains complete code; art files contain real content. ✓

**Type consistency:** `tip{Key,Label}` declared in `model.go` (Task 4), produced by `tips_generated.go` (Tasks 4 default + 5 codegen) and consumed in `renderTips`; `artGrid{lines,w,h}` defined Task 3, used Task 4; `model` fields consistent across `newModel`/`Update`/`render`/`View`; `frameMsg`/`timeoutMsg` defined and matched in `Update`. ✓
