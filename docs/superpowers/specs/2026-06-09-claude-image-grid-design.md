# Claude image grid gallery — design

- **Date:** 2026-06-09
- **Issue:** [noamsto/lazytmux#37](https://github.com/noamsto/lazytmux/issues/37)
- **Follow-up to:** [#32](https://github.com/noamsto/lazytmux/issues/32) / [#35](https://github.com/noamsto/lazytmux/pull/35) (v1 single-image viewer)
- **Status:** smoke-tested on hardware, approved in brainstorming — ready for implementation plan

## Problem

The v1 viewer (`scripts/tmux-claude-images.sh`) is a full-pane keyboard
navigator that shows **one image at a time** (`j/k n/p`, `g/G`, `1-9` jump, `r`
reload). To survey a conversation's images you must page through them
sequentially — there's no contact-sheet view to see many at once and jump to the
one you want.

## Goal

`prefix + I` opens a **thumbnail grid** (contact sheet) of every image in the
active Claude pane's manifest: N columns of scaled thumbnails, each with a
visible index label and a movable selection cursor, paged (not pixel-scrolled).
Selecting a thumbnail **drills into the v1 navigator** for the full-pane focus
view of that image; `Esc`/`q` returns to the grid.

### Non-goals

- Smooth pixel-scrolling (terminal limitation) — the grid pages instead.
- Video/gif animation.
- Photographic thumbnails on **sixel** terminals (see "Renderer", §4): sixel
  cannot be embedded in the TUI's cell buffer. Sixel terminals get blocky
  `chafa -f symbols` thumbnails in the grid and **true sixel on drill-in** (the
  v1 focus view keeps its full fidelity ladder).
- Changing the manifest format or the v1 appender/hook (`claude-images.sh`).

## Verification (done before writing this spec)

All confirmed on real hardware (kitty 0.47.x host → tmux 3.6a → Go), not assumed.
Throwaway smoke scripts (shell grid + a bubbletea spike) are not committed.

1. **Kitty grid placement works under tmux.** `kitten icat
   --unicode-placeholder --place WxH@COLxROW` drops thumbnails at computed cell
   rects, and a plain `\033[2J\033[3J` screen-clear erases them cleanly on
   page-change — **no ghosting**, no need for per-image delete on paging. (This
   contradicts a v1 code comment claiming `--place` disables
   `--unicode-placeholder`; the current `kitten` combines them fine.)
2. **bubbletea v2 can host the graphics.** A spike put kitty Unicode-placeholder
   cells (`U+10EEEE` + row/col combining diacritics, image id in the cell
   foreground color) **inside the bubbletea `View`**: the image rendered,
   **survived full repaints**, and a `lipgloss` border around the block stayed
   **square** — i.e. `lipgloss`/`ultraviolet` measure each placeholder cell as
   width 1 and emit the runes unmodified. *Scope of this test:* it covered one
   image repainting in place. It did **not** cover swapping the `View` to a
   *different* page of images (old cells replaced by new + the id-retransmit
   ordering of §6) — that path is plausible but unverified, and phase 3 opens
   with a mini-smoke-test for it.
3. **Clean teardown.** Transmitting the image **store-only** via the raw kitty
   graphics protocol (`a=T,U=1` — virtual placement, emits zero visible cells)
   and sending **`a=d,d=A`** (delete all) on exit, both wrapped in tmux
   passthrough, leaves the screen clean with no lingering placeholder glyphs.
   (Transmitting via `kitten icat --place` instead leaks placeholder cells onto
   the primary screen, which reappear on alt-screen restore — so the gallery
   must use the store-only raw transmit.)
4. **Sixel cannot live in the `View`.** `charmbracelet/ultraviolet` (bubbletea
   v2's cell-buffer backend, at the version the picker pins) models a screen
   `Cell` as `{ Content string /* grapheme */, Style, Link, Width int }` — no
   graphics/opaque field, and the renderer emits only SGR + text + hyperlinks.
   Unicode placeholders and `chafa -f symbols` are grapheme text and embed
   cleanly; sixel is a DCS graphics escape with no text representation and
   cannot.

### Gotcha (carry into implementation)

`kitten icat` with a **non-tty stdin** counts stdin as a second (empty) image
and aborts `--place` with *"can only be used with a single image, not 2."* Any
`kitten` invocation (or `exec.Command`) must inherit the real tty stdin
(`cmd.Stdin = os.Stdin`), never `/dev/null`. The store-only raw transmit (§5)
sidesteps `kitten` entirely, but the symbols path and any `kitten` fallback must
respect this.

## Design decisions (from brainstorming)

- **Grid is the entry; v1 is the focus view.** `prefix + I` now opens the grid;
  `Enter` on a thumbnail hands off to the v1 navigator opened at that index
  (v1 gains a `--start <idx>` flag). This keeps one focus implementation and
  reuses the merged, verified v1 path.
- **bubbletea (reuse the picker), not bespoke.** The spike proved bubbletea can
  host the graphics, so the gallery reuses the `picker/` module's bubbletea v2 +
  lipgloss patterns and its non-UI helpers (tmux-option parsing, theme
  detection). A bespoke raw-mode renderer was considered and rejected: it would
  buy uniform sixel fidelity for the non-kitty minority at the cost of
  hand-rolling input/resize/terminal-state handling and an unverified
  sixel-tiling path — not worth it when those users already get sixel on
  drill-in.
- **Two grid renderer rungs, not v1's four.** Only text-representable backends
  can embed in the `View`: kitty Unicode placeholders, else `chafa -f symbols`.
  The 4-rung fidelity ladder (kitten / chafa-kitty / sixel / symbols) stays in
  the v1 focus view.
- **Packaging: a gallery mode of the picker binary.** A new flag
  (`tmux-picker-generate --gallery <src_pane>`) dispatched in `main`, with the
  grid in its own source file. Reuses the existing module/derivation; no new
  `go.mod`/`vendorHash`. `chafa` is already in the closure (v1); `kitten` stays
  optional and unbundled.

## Architecture

```
prefix + I ─> tmux-claude-images.sh (OUTER, unchanged role)
   capture active (Claude) pane = manifest source
   toggle: kill tagged viewer pane if present, else split-window running:

   tmux-picker-generate --gallery <src_pane>     (Go, bubbletea)
      load manifest images/<pane>.jsonl  ──┐
      pick renderer (termname + kitten)    │ reuse picker helpers
      grid loop:                           │ (readTmuxOpts, detectTheme)
        transmit current page's images     │
        View = lipgloss grid of            │
          [label + thumbnail cell] blocks  │
          + selection highlight + page X/Y │
        keys: move / page / number / r     │
      Enter ─> tea.Exec:                   │
        tmux-claude-images.sh --view <pane> --start <idx>   (v1 focus view)
      ─> on return, redraw grid
      q ─> delete-all images, exit ─> pane closes
```

Everything maps onto existing structure: the manifest, the outer toggle, the
v1 navigator, the picker module, and the `chafa` dependency all already exist.

## Components

Each unit has one purpose, a narrow interface, and is independently testable.

### 1. Manifest loader (Go)

- Reads `<status-dir>/images/<pane>.jsonl` (`<status-dir>` =
  `${CLAUDE_STATUS_DIR:-/tmp/claude-status}`), one JSON object per line:
  `{type,path,source,ts,mtime}`. Same file v1 reads and `claude-images.sh`
  writes — **format unchanged**.
- Returns an ordered slice of entries; skips blank/unparseable lines (mirrors
  v1's `load_manifest`). Dedup is already applied at write time.
- Pure given file contents → unit-testable.

### 2. Renderer selection (Go)

- `chooseGridBackend(termname string) → backend` → `kittyPlaceholder` for
  `xterm-kitty*`/`xterm-ghostty*`, else `chafaSymbols`. **Termname only** — the
  kitty grid uses the raw graphics protocol (§3), so unlike v1's `choose_renderer`
  it does **not** depend on `kitten` being on PATH (`kitten` matters only to the
  v1 focus view's higher-fidelity rungs). Two View-embeddable rungs.
- Termname from `tmux display-message -p '#{client_termname}'`. Pure given input
  → unit-testable.

### 3. Graphics transmit / teardown (Go, kitty backend only)

- `transmitVirtual(id, path, cols, rows) → string`: raw kitty protocol
  `\e_Gi=<id>,a=T,U=1,f=100,c=<cols>,r=<rows>,t=f;<base64(path)>\e\\`, tmux-
  passthrough-wrapped. Stores the image as a virtual placement (no visible
  cells) sized to `cols×rows`.
- `deleteImages(ids…)` / `deleteAll()`: `\e_Ga=d,d=A\e\\` (or per-id), tmux-
  passthrough-wrapped. Called on page-change (free previous page) and on exit.
- `tmuxPassthrough(seq)`: wrap as `\ePtmux;<ESC-doubled seq>\e\\` when `$TMUX`
  set, else pass through.
- **Teardown must also fire on pane-kill, not just `q`.** Toggling the pane off
  (`prefix + I` again) makes the outer script `kill-pane`, which never reaches
  the `q` path — so a `SIGTERM`/`SIGHUP` handler must emit `deleteAll()` before
  exit, or stored images accumulate in the terminal's graphics memory.
  (`transfer-mode=memory` bounds the leak via kitty eviction, but the handler is
  the correct fix; the v1 focus view likely shares this gap and is out of scope
  here.)

### 4. Placeholder block builder (Go, kitty backend only)

- `placeholderBlock(id, w, h) → string`: `h` lines of `w` cells, each
  `U+10EEEE` + `diacritic[row]` + `diacritic[col]`, with the cell foreground set
  to the 24-bit image id (`\e[38;2;…m`) once per row. The `diacritics` table is
  kitty's rowcolumn-diacritics (confirmed against the smoke output:
  `0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, …`).
- Pure → unit-testable (assert exact rune sequence for a small block).

### 5. Symbols block builder (Go, chafa backend only)

- `symbolsBlock(path, w, h) → string`: shell `chafa -f symbols --size <w>x<h>
  <path>` and capture the ANSI text. The result is colored block-art that embeds
  in the `View` like any styled string.
- Per-thumbnail invocation; results cached per `(path, w, h)` within a page to
  avoid re-running on every repaint.

### 6. Grid model (Go, bubbletea)

The `tea.Model`. Owns geometry, paging, cursor, selection, input, and `View`.

- **Geometry:** `cols` from `pane_width / targetCellWidth` (clamped ≥1;
  `targetCellWidth` a tunable constant, start ~18–22 cells); cell box
  `cellW × cellH` from pane size and cols/rows; one label row per cell
  (`[i] basename`, truncated to `cellW`). `perPage = cols × rows`.
- **Paging:** `n`/`p` change page; `View` swaps to the page's blocks (bubbletea
  diffs and repaints — no manual clear). On page change (kitty), transmit the
  new page's images (ids `1..perPage`, reused per page) and delete the previous
  page's ids. **Ordering invariant:** a reused id must be re-transmitted with the
  new image *before* its placeholder cells are emitted in the next `View`, else
  the cells reference the prior page's stored image.
- **Cursor / selection:** arrows + `h/j/k/l` move within the page (wrap to
  next/prev page at edges); a **dimension-neutral** highlight marks the focused
  cell — inverse/background on its *label row*, or a frame drawn into the
  already-reserved inter-cell gutter. **Not** a `lipgloss` border around the
  cell: a border adds a cell of width/height, so the focused cell would grow and
  shift the whole grid as the cursor moves. `r` reloads the manifest;
  `q`/`Ctrl-c` quit.
- **Jump-to-cell:** number keys jump within the page, but only address cells
  `1–9`; when `perPage > 9` the remainder are reachable by cursor movement only
  (acceptable — jump is a convenience, not the primary nav).
- **`View`:** `lipgloss` grid (`JoinHorizontal` of columns, `JoinVertical` of
  rows) of `[label / thumbnail block]` cells, a top or bottom status line
  (`page X/Y · N images · ↵ open · n/p page · q quit`), `AltScreen = true`.
- **Resize (`WindowSizeMsg`):** recompute geometry; for kitty, re-transmit the
  current page at the new `cols×rows`. *(Assumption to confirm in phase 3: a
  virtual placement's cell extent is fixed at transmit `c/r`, so a resized block
  needs a re-transmit rather than just emitting more/fewer placeholder cells.)*
  Debounce rapid resizes.

### 7. Drill-in handoff (Go)

- `Enter` runs the v1 navigator in the same pane via `tea.Exec`:
  `tmux-claude-images.sh --view <src_pane> --start <selectedIndex>`. bubbletea
  hands over the terminal; on the child's exit, the grid redraws (and re-
  transmits the current page for kitty). Quitting the child (`q`) returns to the
  grid, not the shell.

### 8. v1 change — `--start <idx>`

- `tmux-claude-images.sh` inner mode (`--view PANE`) gains an optional
  `--start <idx>` so drill-in opens at the selected image instead of index 0.
  Clamp out-of-range to the nearest valid index. The only change to v1.

### 9. Outer toggle + wiring

- `tmux-claude-images.sh` **outer mode**: repoint the `split-window` command
  from the v1 inner navigator to `tmux-picker-generate --gallery <src_pane>`.
  The toggle/kill, `@claude_img_src` pane tag, and "no images yet" guard are
  unchanged.
- `config/tmux.conf.nix`: `prefix + I` keybind unchanged (still calls the outer
  script). The picker store path is already interpolated for the session/window
  pickers; reuse it for the gallery invocation.
- `flake.nix` / `picker/`: add the gallery source file(s); no new deps (`chafa`
  bundled since v1, `kitten` optional). `--gallery <pane>` parsed in `main`
  alongside `--tui`/`--windows`.

## Error handling

- **No/empty manifest:** outer guard already flashes "no images yet" and exits
  before splitting.
- **Missing/deleted image file:** render a textual placeholder cell (`[missing:
  name]`); never crash. (v1 does the same in the focus view.)
- **`kitten` absent on a kitty term:** irrelevant to the grid — the kitty
  backend transmits via the raw graphics protocol, not `kitten icat`, so a kitty
  term gets placeholders regardless. (`kitten` only affects the v1 focus view.)
- **`chafa` failure / not on PATH:** render the textual placeholder cell.
- **tmux option / termname unresolved:** default to `chafaSymbols` (universal
  floor — never a blank grid).

## Testing

- **Go unit tests** (like `picker/tui_test.go`): manifest parse/skip-corrupt;
  geometry math (cols/rows/perPage/label truncation for several pane sizes);
  `placeholderBlock` exact rune encoding for a small block; `chooseGridBackend`
  selection table; `tmuxPassthrough` escaping. All pure functions.
- **Manual display test** (after `nix build .#default`, like
  `tests/test-display.sh`): kitty grid render + paging clears + cursor move +
  number jump + `r` reload; drill-in to v1 and back; resize reflow; `q`
  teardown leaves a clean screen. Forced `chafaSymbols` path for the block-art
  grid. Sixel terminal: symbols grid + true sixel on drill-in.

## Risks / open items

- **Page-swap in bubbletea is unverified** — the spike covered same-image
  repaint, not swapping to a different page (old placeholder cells replaced by
  new + the id-retransmit ordering of §6). Phase 3 opens with a mini-smoke-test;
  if a stale image bleeds through, fall back to an explicit per-cell clear before
  the swap.
- **Teardown on pane-kill** — `q` deletes images, but toggling the pane off
  `kill-pane`s it; a `SIGTERM`/`SIGHUP` handler must emit `deleteAll()` so stored
  images don't accumulate (§3).
- **Resize re-transmit cost** — re-transmitting a page's images on every
  `WindowSizeMsg`; mitigate by debouncing and only re-transmitting on a settled
  size change.
- **Image-id lifecycle** — ids reused per page (`1..perPage`); delete the prior
  page's ids on page-change and all on exit to bound terminal graphics memory.
- **Symbols cost** — N `chafa` calls per page; cache per `(path,w,h)` within the
  page so repaints don't re-shell.
- **Label/cell alignment** — labels and thumbnail blocks must report equal
  width to `lipgloss` so columns stay aligned (placeholder cells measure width 1,
  confirmed; symbols blocks are `--size`-bounded; labels truncated to `cellW`).

## Implementation phases (for the plan)

1. ~~Smoke tests: kitty grid placement, bubbletea hosting, store-only transmit +
   teardown~~ — **done, passed.**
2. Manifest loader + renderer selection + transmit/teardown + placeholder/symbols
   block builders, with Go unit tests.
3. **Open with a mini-smoke-test**: in the bubbletea spike shape, swap the
   `View` between two *different* pages of images (with id reuse + re-transmit)
   and confirm no stale-image bleed, and that a resized block re-transmits
   correctly. Then build the grid bubbletea model (geometry, paging, cursor,
   dimension-neutral selection, `View`) on the kitty placeholder backend;
   eyeball on kitty.
4. Symbols backend wired into the `View`; eyeball forced-symbols (and on a
   non-kitty term if available).
5. Drill-in handoff (`tea.Exec` → v1 `--view --start`) + v1 `--start` flag;
   round-trip grid ↔ focus.
6. Outer toggle repoint + keybind/module wiring; `--gallery` dispatch in `main`.
7. Resize reflow + id-lifecycle/teardown robustness; end-to-end on kitty + a
   symbols fallback.
