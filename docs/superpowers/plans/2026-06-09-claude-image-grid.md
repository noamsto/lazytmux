# Claude Image Grid Gallery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a thumbnail-grid contact sheet on `prefix + I` that shows all of the active Claude pane's images at once and drills into the existing v1 single-image navigator on selection.

**Architecture:** A new **gallery mode of the existing `picker/` Go binary** (`tmux-picker-generate --gallery <pane>`), built with the same bubbletea v2 + lipgloss stack the picker already uses. Images render as kitty **Unicode-placeholder virtual images** (raw graphics protocol, embedded as grapheme cells in the bubbletea `View`) on kitty/ghostty, or `chafa -f symbols` block-art text elsewhere. `Enter` hands off to the v1 navigator via `tea.ExecProcess`. The v1 viewer's outer toggle is repointed to launch the gallery; v1 inner mode gains `--start <idx>`.

**Tech Stack:** Go 1.25, `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, kitty graphics protocol (Unicode placeholders), `chafa`, bash (v1 script), Nix flake (`buildGoModule`).

**Spec:** `docs/superpowers/specs/2026-06-09-claude-image-grid-design.md`

---

## File Structure

All Go files live in the existing `picker/` module (`package main`); `buildGoModule` compiles every `.go` file automatically, so **no `flake.nix` or `go.mod` change is needed** (only stdlib + already-vendored bubbletea/lipgloss are used).

- **Create `picker/gallery_render.go`** — pure, stateless helpers: manifest parsing, backend selection, tmux passthrough, kitty transmit/delete sequence builders, the diacritics table + placeholder block builder, the chafa-symbols command. No bubbletea, no I/O beyond `os.ReadFile`/`exec` thin wrappers. This is the unit-tested core.
- **Create `picker/gallery.go`** — the bubbletea `tea.Model` (`galleryModel`): geometry, paging, cursor, selection, `Init`/`Update`/`View`, drill-in, teardown, and `runGallery(pane string) error`.
- **Create `picker/gallery_test.go`** — table-driven unit tests for everything in `gallery_render.go` plus the pure geometry/cursor math from `gallery.go`.
- **Modify `picker/main.go`** — dispatch `--gallery <pane>` to `runGallery`.
- **Modify `scripts/tmux-claude-images.sh`** — outer mode launches the gallery; inner mode (`--view`) accepts `--start <idx>`.

---

## Task 1: Manifest parser

**Files:**
- Create: `picker/gallery_render.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/gallery_test.go`:

```go
package main

import "testing"

func TestParseManifest(t *testing.T) {
	data := []byte(`{"type":"image","path":"/a/one.png","source":"Read","ts":"t","mtime":1}

  {"type":"image","path":"/b/two.png","source":"Write","ts":"t","mtime":2}
not json
{"type":"image","path":"","source":"Read"}
{"type":"image","path":"/c/three.png","source":"Screenshot"}
`)
	got := parseManifest(data)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (blank/corrupt/empty-path skipped): %+v", len(got), got)
	}
	if got[0].Path != "/a/one.png" || got[0].Source != "Read" {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[2].Path != "/c/three.png" {
		t.Errorf("entry 2 = %+v", got[2])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run TestParseManifest ./...`
Expected: FAIL — `undefined: parseManifest`.

- [ ] **Step 3: Write minimal implementation**

Create `picker/gallery_render.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"strings"
)

// imageEntry is one line of the images/<pane>.jsonl manifest (shared format
// with the v1 viewer; only the fields the grid needs are decoded).
type imageEntry struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// parseManifest decodes JSONL bytes into entries, skipping blank/unparseable
// lines and entries with no path.
func parseManifest(data []byte) []imageEntry {
	var out []imageEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e imageEntry
		if json.Unmarshal([]byte(line), &e) != nil || e.Path == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// loadManifest reads images/<pane>.jsonl for the given source pane id (leading
// % stripped) under the claude status dir.
func loadManifest(pane string) []imageEntry {
	dir := os.Getenv("CLAUDE_STATUS_DIR")
	if dir == "" {
		dir = "/tmp/claude-status"
	}
	data, err := os.ReadFile(dir + "/images/" + strings.TrimPrefix(pane, "%") + ".jsonl")
	if err != nil {
		return nil
	}
	return parseManifest(data)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run TestParseManifest ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery_render.go picker/gallery_test.go
git commit -m "feat(gallery): manifest parser for image grid (#37)"
```

---

## Task 2: Renderer backend selection

**Files:**
- Modify: `picker/gallery_render.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/gallery_test.go`:

```go
func TestChooseGridBackend(t *testing.T) {
	cases := []struct {
		term string
		want gridBackend
	}{
		{"xterm-kitty", backendKitty},
		{"xterm-ghostty", backendKitty},
		{"xterm-kitty-something", backendKitty},
		{"foot", backendSymbols},
		{"xterm-256color", backendSymbols},
		{"", backendSymbols},
	}
	for _, c := range cases {
		if got := chooseGridBackend(c.term); got != c.want {
			t.Errorf("chooseGridBackend(%q) = %v, want %v", c.term, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run TestChooseGridBackend ./...`
Expected: FAIL — `undefined: gridBackend`.

- [ ] **Step 3: Write minimal implementation**

Add to `picker/gallery_render.go`:

```go
type gridBackend int

const (
	backendKitty gridBackend = iota
	backendSymbols
)

// chooseGridBackend picks the grid renderer from the OUTER terminal name only.
// The kitty backend uses the raw graphics protocol (not `kitten icat`), so —
// unlike the v1 focus view — it does not depend on `kitten` being on PATH.
func chooseGridBackend(termname string) gridBackend {
	if strings.HasPrefix(termname, "xterm-kitty") || strings.HasPrefix(termname, "xterm-ghostty") {
		return backendKitty
	}
	return backendSymbols
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run TestChooseGridBackend ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery_render.go picker/gallery_test.go
git commit -m "feat(gallery): terminal-based grid backend selection (#37)"
```

---

## Task 3: tmux passthrough + kitty transmit/delete sequences

**Files:**
- Modify: `picker/gallery_render.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/gallery_test.go`:

```go
func TestTmuxPassthrough(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := tmuxPassthrough("\x1b_Ga=d\x1b\\"); got != "\x1b_Ga=d\x1b\\" {
		t.Errorf("no-tmux passthrough should be identity, got %q", got)
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	got := tmuxPassthrough("\x1b_Ga=d\x1b\\")
	want := "\x1bPtmux;\x1b\x1b_Ga=d\x1b\x1b\\\x1b\\"
	if got != want {
		t.Errorf("tmux passthrough = %q, want %q", got, want)
	}
}

func TestTransmitVirtual(t *testing.T) {
	t.Setenv("TMUX", "")
	// base64 of "/x.png" is "L3gucG5n"
	got := transmitVirtual(7, "/x.png", 20, 10)
	want := "\x1b_Gi=7,a=T,U=1,f=100,c=20,r=10,t=f;L3gucG5n\x1b\\"
	if got != want {
		t.Errorf("transmitVirtual = %q, want %q", got, want)
	}
}

func TestDeleteAll(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := deleteAll(); got != "\x1b_Ga=d,d=A\x1b\\" {
		t.Errorf("deleteAll = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run 'TestTmuxPassthrough|TestTransmitVirtual|TestDeleteAll' ./...`
Expected: FAIL — `undefined: tmuxPassthrough`.

- [ ] **Step 3: Write minimal implementation**

Add to `picker/gallery_render.go` (add `"encoding/base64"` and `"fmt"` to the import block):

```go
// tmuxPassthrough wraps an escape sequence so tmux forwards it to the outer
// terminal verbatim: \ePtmux;<seq with every ESC doubled>\e\\
func tmuxPassthrough(seq string) string {
	if os.Getenv("TMUX") == "" {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// transmitVirtual stores a PNG (by file path) as a virtual placement (U=1) under
// a known id, sized to cols x rows cells, emitting NO visible cells. Placeholder
// cells in the View reference it.
func transmitVirtual(id int, path string, cols, rows int) string {
	enc := base64.StdEncoding.EncodeToString([]byte(path))
	return tmuxPassthrough(fmt.Sprintf("\x1b_Gi=%d,a=T,U=1,f=100,c=%d,r=%d,t=f;%s\x1b\\",
		id, cols, rows, enc))
}

// deleteAll removes all stored images + placements (kitty graphics a=d,d=A).
func deleteAll() string { return tmuxPassthrough("\x1b_Ga=d,d=A\x1b\\") }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run 'TestTmuxPassthrough|TestTransmitVirtual|TestDeleteAll' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery_render.go picker/gallery_test.go
git commit -m "feat(gallery): kitty transmit/delete + tmux passthrough helpers (#37)"
```

---

## Task 4: Diacritics table + placeholder block builder

**Files:**
- Modify: `picker/gallery_render.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/gallery_test.go`:

```go
func TestPlaceholderBlock(t *testing.T) {
	// id=1 -> fg 0;0;1; 2 cols x 1 row. Cell = U+10EEEE + diacritic[row] + diacritic[col].
	got := placeholderBlock(1, 2, 1)
	want := "\x1b[38;2;0;0;1m" +
		"\U0010EEEE̅̅" + // row 0, col 0
		"\U0010EEEE̅̍" + // row 0, col 1
		"\x1b[39m"
	if got != want {
		t.Errorf("placeholderBlock(1,2,1) =\n%q\nwant\n%q", got, want)
	}
}

func TestPlaceholderBlockTwoRows(t *testing.T) {
	got := placeholderBlock(1, 1, 2)
	want := "\x1b[38;2;0;0;1m\U0010EEEE̅̅\x1b[39m\n" +
		"\x1b[38;2;0;0;1m\U0010EEEE̍̅\x1b[39m"
	if got != want {
		t.Errorf("placeholderBlock(1,1,2) =\n%q\nwant\n%q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run 'TestPlaceholderBlock' ./...`
Expected: FAIL — `undefined: placeholderBlock`.

- [ ] **Step 3: Write minimal implementation**

Add to `picker/gallery_render.go`:

```go
// placeholderRune is kitty's Unicode image placeholder (U+10EEEE).
const placeholderRune = "\U0010EEEE"

// rowColDiacritics are kitty's rowcolumn-diacritics (combining class 230). The
// Nth entry encodes row/column index N. Source: kitty
// share/doc/kitty/.../rowcolumn-diacritics.txt (full list is 297 entries; this
// prefix covers any thumbnail cell extent we produce — block dims are clamped to
// len(rowColDiacritics)).
var rowColDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
}

// placeholderBlock builds h lines of w placeholder cells referencing image id.
// The image id is encoded as the 24-bit cell foreground color, set once per row.
// w and h are clamped to len(rowColDiacritics) by the caller (geometry).
func placeholderBlock(id, w, h int) string {
	var b strings.Builder
	for r := 0; r < h; r++ {
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
		for c := 0; c < w; c++ {
			b.WriteString(placeholderRune)
			b.WriteRune(rowColDiacritics[r])
			b.WriteRune(rowColDiacritics[c])
		}
		b.WriteString("\x1b[39m")
		if r < h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// maxCellDim is the largest thumbnail cell extent placeholderBlock supports.
var maxCellDim = len(rowColDiacritics)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run 'TestPlaceholderBlock' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery_render.go picker/gallery_test.go
git commit -m "feat(gallery): kitty unicode-placeholder block builder (#37)"
```

---

## Task 5: chafa-symbols block builder

**Files:**
- Modify: `picker/gallery_render.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

The exec wrapper isn't unit-testable, but the arg construction is. Add to `picker/gallery_test.go`:

```go
func TestSymbolsArgs(t *testing.T) {
	got := symbolsArgs("/a/b.png", 20, 10)
	// No --clear: chafa's --clear wipes the whole screen, which would erase the
	// rest of the grid on every per-cell render.
	want := []string{"-f", "symbols", "--size", "20x10", "/a/b.png"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run TestSymbolsArgs ./...`
Expected: FAIL — `undefined: symbolsArgs`.

- [ ] **Step 3: Write minimal implementation**

Add to `picker/gallery_render.go` (add `"os/exec"` to imports):

```go
// symbolsArgs builds the chafa arg list for a block-art thumbnail of the given
// cell size. No --clear (that would clear the whole screen per file).
func symbolsArgs(path string, w, h int) []string {
	return []string{"-f", "symbols", "--size", fmt.Sprintf("%dx%d", w, h), path}
}

// symbolsBlock renders a path to chafa symbols text sized to w x h cells, or a
// textual placeholder on failure. chafa wraps output in cursor hide/show
// (\e[?25l … \e[?25h); strip them so they don't leak into the TUI's frame.
func symbolsBlock(path string, w, h int) string {
	out, err := exec.Command("chafa", symbolsArgs(path, w, h)...).Output()
	if err != nil {
		return "[img]"
	}
	s := string(out)
	s = strings.ReplaceAll(s, "\x1b[?25l", "")
	s = strings.ReplaceAll(s, "\x1b[?25h", "")
	return strings.TrimRight(s, "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run TestSymbolsArgs ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery_render.go picker/gallery_test.go
git commit -m "feat(gallery): chafa symbols block builder (#37)"
```

---

## Task 6: Grid geometry

**Files:**
- Create: `picker/gallery.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/gallery_test.go`:

```go
func TestComputeGrid(t *testing.T) {
	// 100 wide / targetCellWidth(20) = 5 cols. 40 tall, status row reserved.
	g := computeGrid(100, 40, 30)
	if g.cols != 5 {
		t.Errorf("cols = %d, want 5", g.cols)
	}
	if g.perPage != g.cols*g.rows {
		t.Errorf("perPage = %d, want cols*rows = %d", g.perPage, g.cols*g.rows)
	}
	if g.cellW > maxCellDim || g.imgH > maxCellDim {
		t.Errorf("cell dims must clamp to %d: cellW=%d imgH=%d", maxCellDim, g.cellW, g.imgH)
	}
	if g.cols < 1 || g.rows < 1 {
		t.Errorf("cols/rows must be >= 1: %+v", g)
	}
}

func TestComputeGridNarrow(t *testing.T) {
	g := computeGrid(10, 6, 1) // tiny pane, 1 image
	if g.cols < 1 || g.rows < 1 || g.perPage < 1 {
		t.Errorf("degenerate pane must still yield a 1x1 grid: %+v", g)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run TestComputeGrid ./...`
Expected: FAIL — `undefined: computeGrid`.

- [ ] **Step 3: Write minimal implementation**

Create `picker/gallery.go`:

```go
package main

const (
	targetCellWidth = 20 // desired thumbnail width in cells
	targetCellRows  = 5  // desired rows per screenful before paging
	labelRows       = 1  // one label line per cell
)

// grid is the computed layout for a given pane size and image count.
type grid struct {
	cols, rows   int // visible columns / rows of cells
	cellW, cellH int // cell box in cells (cellH includes the label row)
	imgH         int // image rows inside a cell (cellH - labelRows)
	perPage      int
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// computeGrid derives the grid layout. paneH-1 reserves the bottom status row.
func computeGrid(paneW, paneH, imageCount int) grid {
	cols := clamp(paneW/targetCellWidth, 1, maxCellDim)
	body := paneH - 1
	rows := clamp(targetCellRows, 1, maxRowsThatFit(body))
	cellW := clamp(paneW/cols, 1, maxCellDim)
	cellH := clamp(body/rows, labelRows+1, maxCellDim+labelRows)
	imgH := clamp(cellH-labelRows, 1, maxCellDim)
	return grid{cols: cols, rows: rows, cellW: cellW, cellH: cellH, imgH: imgH, perPage: cols * rows}
}

// maxRowsThatFit caps rows so each cell keeps at least a label row + 1 image row.
func maxRowsThatFit(body int) int {
	if body < labelRows+1 {
		return 1
	}
	return body / (labelRows + 1)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run TestComputeGrid ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery.go picker/gallery_test.go
git commit -m "feat(gallery): grid geometry computation (#37)"
```

---

## Task 7: Cursor / paging math

**Files:**
- Modify: `picker/gallery.go`
- Test: `picker/gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/gallery_test.go`:

```go
func TestPageOf(t *testing.T) {
	if p := pageOf(0, 6); p != 0 {
		t.Errorf("pageOf(0,6) = %d, want 0", p)
	}
	if p := pageOf(6, 6); p != 1 {
		t.Errorf("pageOf(6,6) = %d, want 1", p)
	}
	if p := pageOf(13, 6); p != 2 {
		t.Errorf("pageOf(13,6) = %d, want 2", p)
	}
}

func TestPageCount(t *testing.T) {
	if n := pageCount(0, 6); n != 1 {
		t.Errorf("empty -> 1 page, got %d", n)
	}
	if n := pageCount(6, 6); n != 1 {
		t.Errorf("exactly one page, got %d", n)
	}
	if n := pageCount(7, 6); n != 2 {
		t.Errorf("7/6 -> 2 pages, got %d", n)
	}
}

func TestMoveCursor(t *testing.T) {
	// 5 images, 2 cols. cursor 0, move right -> 1; left from 0 clamps to 0.
	if c := moveCursor(0, 1, 5); c != 1 {
		t.Errorf("right = %d, want 1", c)
	}
	if c := moveCursor(0, -1, 5); c != 0 {
		t.Errorf("left from 0 = %d, want 0 (clamp)", c)
	}
	if c := moveCursor(4, 1, 5); c != 4 {
		t.Errorf("right at end = %d, want 4 (clamp)", c)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run 'TestPageOf|TestPageCount|TestMoveCursor' ./...`
Expected: FAIL — `undefined: pageOf`.

- [ ] **Step 3: Write minimal implementation**

Add to `picker/gallery.go`:

```go
func pageOf(index, perPage int) int { return index / perPage }

func pageCount(n, perPage int) int {
	if n <= 0 {
		return 1
	}
	return (n + perPage - 1) / perPage
}

// moveCursor shifts the selected index by delta, clamped to [0, count-1].
func moveCursor(index, delta, count int) int {
	if count == 0 {
		return 0
	}
	return clamp(index+delta, 0, count-1)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run 'TestPageOf|TestPageCount|TestMoveCursor' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery.go picker/gallery_test.go
git commit -m "feat(gallery): cursor and paging math (#37)"
```

---

## Task 8: Mini-smoke-test — page-swap + resize re-transmit (eyeball gate)

This de-risks the one unverified path before building the full model (spec §"Risks": page-swap in bubbletea, and resize re-transmit). It is a throwaway, not committed.

**Files:**
- Create (throwaway): `picker/galleryspike/main.go`

- [ ] **Step 1: Write the spike**

Create `picker/galleryspike/main.go` — transmits TWO different images store-only (ids 1 and 2) sized to the current pane, and a bubbletea model whose `View` shows page 1 (id 1's placeholder block) or page 2 (id 2's block), toggled with `n`/`p`. On `r`, re-transmit both at a smaller size and rebuild. `q` deletes all and quits. Reuse `transmitVirtual`, `placeholderBlock`, `deleteAll` by copying them into the spike package, OR import logic by building from the picker dir. Simplest: copy the three helpers + the diacritics prefix into the spike `main.go`. Write the store-only transmits to `/dev/tty` (open `os.OpenFile("/dev/tty", os.O_WRONLY, 0)`), not stdout, so they don't interleave with bubbletea's frame writes.

```go
// package main — throwaway. Key points to exercise:
//  - transmit id 1 and id 2 store-only to /dev/tty BEFORE tea.Run
//  - View returns placeholderBlock(currentID, w, h) inside a lipgloss border
//  - n/p switch currentID between 1 and 2 (page swap)
//  - r: re-transmit both at (w-4)x(h-2) to /dev/tty, shrink the block (resize)
//  - q: write deleteAll() to /dev/tty, then tea.Quit
```

- [ ] **Step 2: Build and run**

Run:
```bash
cd picker && go build -o /tmp/gallery-spike ./galleryspike && /tmp/gallery-spike
```

- [ ] **Step 3: Eyeball — page swap**

Press `n` then `p`. **Expected:** image 1 shows, `n` swaps to image 2 with **no bleed-through** of image 1, `p` swaps back cleanly. If image 1 ghosts under image 2, the View-swap is insufficient → in the real model, write a per-cell clear (overwrite the old page's placeholder cells with spaces) before emitting the new page.

- [ ] **Step 4: Eyeball — resize re-transmit**

Press `r`. **Expected:** both images redraw at the smaller size, correctly scaled (not clipped to a sub-region). If clipped, the virtual placement c/r is authoritative and the re-transmit path is correct as designed; if corrupted, fall back to relaunching the gallery on resize.

- [ ] **Step 5: Eyeball — teardown**

Press `q`. **Expected:** clean screen, no lingering placeholder glyphs in the prompt.

- [ ] **Step 6: Record findings + remove the spike**

Note the page-swap and resize outcomes in the PR description (they determine whether Task 10 needs the per-cell clear). Then:

```bash
gtrash put picker/galleryspike
```

No commit (throwaway).

---

## Task 9: Gallery model skeleton — load, transmit, render page 1 (eyeball)

**Files:**
- Modify: `picker/gallery.go`

- [ ] **Step 1: Implement the model + `runGallery`**

Add to `picker/gallery.go` (imports: `fmt`, `os`, `os/signal`, `syscall`, `path/filepath`, `tea "charm.land/bubbletea/v2"`, `"charm.land/lipgloss/v2"`):

```go
type galleryModel struct {
	pane    string
	images  []imageEntry
	backend gridBackend
	theme   string
	g       grid
	cursor  int  // selected image index (absolute)
	width   int
	height  int
	tty     *os.File // raw graphics sink (bypasses bubbletea's stdout)
	ready   bool
}

func (m galleryModel) Init() tea.Cmd { return nil }

// transmitPage stores the current page's images store-only (kitty backend),
// ids 1..perPage, sized to the cell box. Writes to /dev/tty so the APC bytes
// never interleave with bubbletea's frame output.
func (m *galleryModel) transmitPage() {
	if m.backend != backendKitty || m.tty == nil {
		return
	}
	fmt.Fprint(m.tty, deleteAll())
	page := pageOf(m.cursor, m.g.perPage)
	start := page * m.g.perPage
	for slot := 0; slot < m.g.perPage; slot++ {
		idx := start + slot
		if idx >= len(m.images) {
			break
		}
		fmt.Fprint(m.tty, transmitVirtual(slot+1, m.images[idx].Path, m.g.cellW, m.g.imgH))
	}
}

func (m galleryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.g = computeGrid(m.width, m.height, len(m.images))
		m.ready = true
		m.transmitPage()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m galleryModel) View() tea.View {
	content := "Loading..."
	if m.ready {
		content = m.renderGrid()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// runGallery is the entry point dispatched from main for `--gallery <pane>`.
func runGallery(pane string) error {
	tty, _ := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	m := galleryModel{
		pane:    pane,
		images:  loadManifest(pane),
		backend: chooseGridBackend(termName()),
		theme:   detectTheme(),
		tty:     tty,
	}
	// Teardown on pane-kill (toggle-off SIGTERM/SIGHUP), not just q.
	if tty != nil {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-sig
			fmt.Fprint(tty, deleteAll())
			os.Exit(0)
		}()
	}
	_, err := tea.NewProgram(m).Run()
	if tty != nil {
		fmt.Fprint(tty, deleteAll())
		_ = tty.Close()
	}
	return err
}

// termName returns the outer client terminal name (for backend selection).
func termName() string {
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_termname}").Output()
	if err != nil {
		return os.Getenv("TERM")
	}
	return strings.TrimSpace(string(out))
}
```

Add a temporary minimal `renderGrid` so it compiles (replaced in Task 10):

```go
func (m galleryModel) renderGrid() string {
	if len(m.images) == 0 {
		return "no images"
	}
	page := pageOf(m.cursor, m.g.perPage)
	start := page * m.g.perPage
	var cells []string
	for slot := 0; slot < m.g.perPage && start+slot < len(m.images); slot++ {
		idx := start + slot
		label := fmt.Sprintf("[%d] %s", idx+1, filepath.Base(m.images[idx].Path))
		var thumb string
		if m.backend == backendKitty {
			thumb = placeholderBlock(slot+1, m.g.cellW, m.g.imgH)
		} else {
			thumb = symbolsBlock(m.images[idx].Path, m.g.cellW, m.g.imgH)
		}
		cells = append(cells, lipgloss.JoinVertical(lipgloss.Left, label, thumb))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}
```

`exec` and `strings` are already imported by `gallery_render.go` in the same package; if `go build` complains about unused/missing imports in `gallery.go`, add them there too.

- [ ] **Step 2: Build**

Run: `cd picker && go build ./...`
Expected: builds clean.

- [ ] **Step 3: Run the unit tests (no regressions)**

Run: `cd picker && go test ./...`
Expected: PASS.

- [ ] **Step 4: Eyeball page 1 (manual, needs the dispatch from Task 12 OR a temporary call)**

Temporarily exercise via a tiny throwaway main flag isn't needed yet — defer the visual check to Task 12's end-to-end, OR add the dispatch (Task 11) first. Mark this step done once `go build ./...` and `go test ./...` are green; the visual confirmation happens in Task 13.

- [ ] **Step 5: Commit**

```bash
git add picker/gallery.go
git commit -m "feat(gallery): bubbletea model, transmit, teardown, page-1 render (#37)"
```

---

## Task 10: Grid View — labels, columns, rows, selection highlight, status line

**Files:**
- Modify: `picker/gallery.go`

- [ ] **Step 1: Replace `renderGrid` with the full layout**

Replace the temporary `renderGrid` in `picker/gallery.go` with the full version. Selection is a **dimension-neutral** highlight on the label row (inverse via lipgloss `Reverse(true)`), never a border (a border would change cell size and shift the grid):

```go
func (m galleryModel) renderGrid() string {
	if len(m.images) == 0 {
		return "no images"
	}
	page := pageOf(m.cursor, m.g.perPage)
	start := page * m.g.perPage
	labelStyle := lipgloss.NewStyle().Width(m.g.cellW)
	selStyle := labelStyle.Reverse(true)

	var rows []string
	for r := 0; r < m.g.rows; r++ {
		var cols []string
		for c := 0; c < m.g.cols; c++ {
			slot := r*m.g.cols + c
			idx := start + slot
			if idx >= len(m.images) {
				cols = append(cols, lipgloss.NewStyle().Width(m.g.cellW).Height(m.g.cellH).Render(""))
				continue
			}
			label := fmt.Sprintf("[%d] %s", idx+1, filepath.Base(m.images[idx].Path))
			if len(label) > m.g.cellW {
				label = label[:m.g.cellW]
			}
			if idx == m.cursor {
				label = selStyle.Render(label)
			} else {
				label = labelStyle.Render(label)
			}
			var thumb string
			if m.backend == backendKitty {
				thumb = placeholderBlock(slot+1, m.g.cellW, m.g.imgH)
			} else {
				thumb = symbolsBlock(m.images[idx].Path, m.g.cellW, m.g.imgH)
			}
			cols = append(cols, lipgloss.JoinVertical(lipgloss.Left, label, thumb))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cols...))
	}
	status := fmt.Sprintf("page %d/%d · %d images · ↵ open · n/p page · q quit",
		page+1, pageCount(len(m.images), m.g.perPage), len(m.images))
	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinVertical(lipgloss.Left, rows...), status)
}
```

- [ ] **Step 2: Build + test**

Run: `cd picker && go build ./... && go test ./...`
Expected: builds clean, tests PASS.

- [ ] **Step 3: Commit**

```bash
git add picker/gallery.go
git commit -m "feat(gallery): full grid layout with selection highlight + status (#37)"
```

---

## Task 11: main.go — `--gallery <pane>` dispatch

**Files:**
- Modify: `picker/main.go:91-99` (the `main` function)

- [ ] **Step 1: Replace `main` to parse `--gallery <pane>`**

Replace the `main` function in `picker/main.go`:

```go
func main() {
	args := os.Args[1:]
	for i, a := range args {
		if a == "--gallery" {
			pane := ""
			if i+1 < len(args) {
				pane = args[i+1]
			}
			if err := runGallery(pane); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	flags := map[string]bool{}
	for _, a := range args {
		flags[a] = true
	}
	if err := runTUI(flags["--windows"], flags["--claude"]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build + test**

Run: `cd picker && go build ./... && go test ./...`
Expected: builds clean, tests PASS.

- [ ] **Step 3: Verify dispatch (no images → graceful)**

Run: `cd picker && go run . --gallery %999`
Expected: enters the alt-screen TUI showing `no images` (pane 999 has no manifest); press `q` to exit cleanly.

- [ ] **Step 4: Commit**

```bash
git add picker/main.go
git commit -m "feat(gallery): dispatch --gallery <pane> in picker main (#37)"
```

---

## Task 12: Drill-in handoff via `tea.ExecProcess`

**Files:**
- Modify: `picker/gallery.go` (the `Update` `KeyPressMsg` switch)

- [ ] **Step 1: Add Enter/navigation keys to `Update`**

Extend the `KeyPressMsg` switch in `Update`:

```go
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "right", "l":
			m.cursor = m.moveAndMaybeTransmit(1)
		case "left", "h":
			m.cursor = m.moveAndMaybeTransmit(-1)
		case "down", "j":
			m.cursor = m.moveAndMaybeTransmit(m.g.cols)
		case "up", "k":
			m.cursor = m.moveAndMaybeTransmit(-m.g.cols)
		case "n":
			m.cursor = m.pageJump(1)
		case "p":
			m.cursor = m.pageJump(-1)
		case "r":
			m.images = loadManifest(m.pane)
			m.cursor = clamp(m.cursor, 0, max(0, len(m.images)-1))
			m.transmitPage()
		case "enter":
			return m, m.drillIn()
		default:
			if n := digitKey(msg.String()); n >= 1 {
				page := pageOf(m.cursor, m.g.perPage)
				idx := page*m.g.perPage + (n - 1)
				if idx < len(m.images) {
					m.cursor = idx
				}
			}
		}
```

Add the helpers + drill-in to `picker/gallery.go`:

```go
// moveAndMaybeTransmit moves the cursor and, if the move crossed a page
// boundary, re-transmits the new page (kitty).
func (m *galleryModel) moveAndMaybeTransmit(delta int) int {
	old := pageOf(m.cursor, m.g.perPage)
	c := moveCursor(m.cursor, delta, len(m.images))
	if pageOf(c, m.g.perPage) != old {
		m.cursor = c
		m.transmitPage()
	}
	return c
}

func (m *galleryModel) pageJump(dir int) int {
	pages := pageCount(len(m.images), m.g.perPage)
	page := clamp(pageOf(m.cursor, m.g.perPage)+dir, 0, pages-1)
	c := clamp(page*m.g.perPage, 0, max(0, len(m.images)-1))
	if c != m.cursor {
		m.cursor = c
		m.transmitPage()
	}
	return c
}

// digitKey maps "1".."9" to 1..9, else 0.
func digitKey(s string) int {
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		return int(s[0] - '0')
	}
	return 0
}

// drillIn hands the pane to the v1 navigator opened at the selected image.
func (m galleryModel) drillIn() tea.Cmd {
	if len(m.images) == 0 {
		return nil
	}
	idx := m.cursor
	cmd := exec.Command("tmux-claude-images.sh", "--view", m.pane, "--start", fmt.Sprint(idx))
	return tea.ExecProcess(cmd, func(error) tea.Msg { return retransmitMsg{} })
}

type retransmitMsg struct{}
```

Handle `retransmitMsg` in `Update` (re-transmit after the child returns, since v1 used the terminal's graphics):

```go
	case retransmitMsg:
		m.transmitPage()
		return m, nil
```

`max` is a Go 1.21+ builtin (module is go 1.25).

- [ ] **Step 2: Build + test**

Run: `cd picker && go build ./... && go test ./...`
Expected: builds clean, tests PASS.

- [ ] **Step 3: Commit**

```bash
git add picker/gallery.go
git commit -m "feat(gallery): navigation, paging, number-jump, drill-in to v1 (#37)"
```

---

## Task 13: v1 script — `--start <idx>` + outer launches the gallery

**Files:**
- Modify: `scripts/tmux-claude-images.sh`

- [ ] **Step 1: Add `--start` to inner mode**

In `scripts/tmux-claude-images.sh`, the inner block begins with `if [[ ${1:-} == --view ]]; then` / `src_pane="$2"`. After `src_pane="$2"`, parse an optional `--start`:

```bash
	src_pane="$2"
	start_idx=0
	if [[ ${3:-} == --start && -n ${4:-} ]]; then
		start_idx="$4"
	fi
	manifest="$IMAGES_DIR/${src_pane#%}.jsonl"
```

Then change the navigator's initial index from `i=0` to use `start_idx`, clamped after the manifest loads:

```bash
	i=$start_idx
	((i >= 0)) || i=0
	((i < n)) || i=$((n - 1))
	prev=-1
```

(Place the clamp immediately after the existing `load_manifest` / `((n > 0))` guard, before the render loop.)

- [ ] **Step 2: Repoint outer mode to the gallery**

In the outer block, replace the viewer split command:

```bash
viewer="$(tmux split-window -h -P -F '#{pane_id}' "tmux-picker-generate --gallery '$src_pane'")"
tmux set-option -p -t "$viewer" @claude_img_src "$src_pane"
```

(Was `"'$SELF' --view '$src_pane'"`.)

- [ ] **Step 3: Lint the script**

Run: `shellcheck scripts/tmux-claude-images.sh`
Expected: clean (no new warnings).

- [ ] **Step 4: Commit**

```bash
git add scripts/tmux-claude-images.sh
git commit -m "feat(gallery): v1 --start flag + outer toggle launches grid (#37)"
```

---

## Task 14: Build, end-to-end, and display verification

**Files:** none (verification only).

- [ ] **Step 1: Full Go test + vet**

Run: `cd picker && go test ./... && go vet ./...`
Expected: PASS, no vet warnings.

- [ ] **Step 2: Build the flake**

Run: `nix build .#default 2>&1 | tail -5`
Expected: builds `./result/bin/tmux` with no errors (picker recompiles with the new files; `vendorHash` unchanged because no deps changed).

- [ ] **Step 3: Reload tmux + manual end-to-end on kitty**

In a kitty + tmux session with a Claude pane that has images in its manifest:
- Reload config (`prefix + r`), then press `prefix + I`.
- **Expected:** a split pane shows a grid of thumbnails with `[n] name` labels; the selected cell's label is inverse-highlighted.
- Move with `h/j/k/l` / arrows; selection moves, **columns stay aligned** (no shifting).
- `n`/`p` page; **old thumbnails fully clear** (no ghosting).
- Press a digit `1`–`9`; selection jumps to that cell on the page.
- Press `Enter`; the v1 full-pane navigator opens **at the selected image**. Navigate, then `q` — **returns to the grid** (not the shell), grid redraws.
- Press `prefix + I` again (toggle off): pane closes, and **no placeholder glyphs linger** in any pane (SIGTERM teardown fired).
- Open the grid again and press `q`: clean exit.

- [ ] **Step 4: Symbols fallback**

Force the symbols backend (run in a non-kitty terminal, or temporarily make `chooseGridBackend` return `backendSymbols`): **Expected:** a block-art grid renders with aligned columns and working paging/selection. Revert any temporary change.

- [ ] **Step 5: Commit (only if Step 4 required a code tweak; otherwise skip)**

```bash
git add -A
git commit -m "fix(gallery): <whatever Step 3/4 surfaced> (#37)"
```

---

## Self-Review notes (addressed in this plan)

- **Spec coverage:** manifest loader (T1), backend selection (T2), transmit/teardown (T3), placeholder builder (T4), symbols builder (T5), geometry (T6), cursor/paging (T7), page-swap+resize de-risk (T8), model+transmit+signal-teardown (T9), full View + dimension-neutral highlight (T10), `--gallery` dispatch (T11), drill-in via `tea.ExecProcess` + number-jump (T12), v1 `--start` + outer repoint (T13), build/e2e/display (T14). All spec §components and §"implementation phases" map to a task.
- **Type consistency:** `gridBackend`/`backendKitty`/`backendSymbols`, `imageEntry`, `grid`{cols,rows,cellW,cellH,imgH,perPage}, `galleryModel`, and helper names (`transmitVirtual`, `deleteAll`, `placeholderBlock`, `chooseGridBackend`, `computeGrid`, `moveCursor`, `pageOf`, `pageCount`) are used consistently across tasks.
- **Teardown:** delete-all fires on `q` (T9 `runGallery`), on SIGTERM/SIGHUP toggle-off (T9 signal handler), and a fresh `deleteAll` precedes each page transmit (T9 `transmitPage`).
- **Open verification (T8) gates two assumptions** (bubbletea page-swap clearing; virtual-placement resize re-transmit) before the model is built; fallbacks are documented inline.
```
