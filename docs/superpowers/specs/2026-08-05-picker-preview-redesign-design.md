# Picker preview redesign — live tiles you can type into

Issue: [#286](https://github.com/noamsto/lazytmux/issues/286)
Branch: `docs/286-picker-redesign-spec` (this spec); one branch per stage after.

## Problem

The preview shows one pane's content in the bottom ~40% of the popup, and it is
not enough to act on. Four separate things are wrong; only one of them was the
one everybody assumes.

**The preview is already live.** Measured, not read: a ticking clock in the
previewed pane advanced inside the picker while watched —
`10:08:43 … :45` at t=0, `10:08:47 … :49` at t=4s. `tickMsg` (1 s) →
`refreshDataCmd` → `refreshMsg` → `loadPreviewCmd` re-runs `capture-pane` every
second, and the viewport stays pinned at the bottom because a pane's capture has
constant line count. **Any plan whose first step is "make the preview live" is
already done.** What is actually wrong:

1. **Stale content when nothing is selected.** Filtering to zero rows leaves the
   *previous* target's content on screen — verified by typing `ticker` (no
   match) and watching an unrelated Claude session stay in the preview.
   `loadPreviewCmd` returns `nil` when there is no selectable item, so nothing
   clears what is there.
2. **It is about 9 content lines.** 50% of the body goes to the list, the rest
   to the preview, on a 90%×85% popup. The split is hardcoded.
3. **1 Hz is sluggish** for the thing the preview is for — watching a build or a
   Claude turn move.
4. **One pane at a time**, which is the substance of the original request:
   several windows' output at once, and ideally the ability to answer a prompt
   without leaving the picker.

Out of scope, found by the same probe and worth its own issue: window rows are
not searchable by window name (search follows the label priority issue → branch
→ `@window_ai_name` → `@window_task` → repo basename), so a window named
`ticker` cannot be found by typing `ticker`.

## Goals

1. The preview never shows content that does not belong to the selection.
2. The list/preview split is configurable, and the preview can take the larger
   share.
3. `prefix + s` / `prefix + w` can open list-only, per configuration.
4. `prefix + W` opens a **wall**: the same filtered list rendered as live tiles.
5. A focused tile can take keystrokes for the simple answers (`y`, `n`, Enter).

Non-goals: no changes to the status bar, window options, or the enrich/claude
state pipelines. `@window_*` / `@pr_*` / `@issue_*` stay the single source of
truth, read-only to every renderer here.

## Design

One model, three renderers. `tuiModel` grows a `mode` and a `focus`; the
renderers read the same `visible` slice, so filtering, `^a` claude-only, `^s`
scratch, claude state and PR badges work identically in every shape.

```
picker/
  tui.go            tuiModel + Update + layout math (mode, focus, page)
  render_list.go    renderList, moved out (tui.go is 1708 lines today)
  render_wall.go    new: tile grid, pagination, per-tile crop
  capture.go        new: batched capture-pane, marker parsing, send-keys relay
```

Rejected alternatives:

- **A floating pane** (`new-pane -x 95% -y 90%`, as `prefix + y` yazi uses).
  Resizable at runtime and passes escapes through, but a float belongs to the
  window that launched it, and this tool's whole job is to `switch-client`
  away — which would strand it. Escape passthrough only matters if the
  *target's* TUI must read raw escapes, and relaying via `send-keys` does not.
- **A separate wall binary.** The repo already ships extra mains (`splash`,
  `statusline`), so it would fit, but it would fork the item model — the drift
  behind #173, #198 and #234. One model, many renderers.
- **A terminal emulator per tile.** Far more machinery than `capture-pane` plus
  a crop, for a view that is glanced at.

### Constraints (probed, not assumed)

- **A wall costs one fork per tick, not one per tile.**
  `tmux capture-pane -pt A \; display-message -p MARK \; capture-pane -pt B`
  returns both panes in a single call, separated by the marker. `capture.go`
  is built on this.
- **Popups cannot be resized after creation.** next-3.8 has `resize-pane` and
  `resize-window` and nothing for popups. So each bind opens at its own size,
  the mode cycles only within the space it was given, and the wall gets its own
  launcher rather than trying to grow the list's popup.

### Stage 1 — preview correctness and size

`picker/tui.go`, `picker/tui_test.go`.

- **Clear on empty selection.** `loadPreviewCmd` currently returns `nil` when
  there is no selectable item. Instead return a command emitting
  `previewMsg{content: "", target: ""}`. The existing handler gate
  (`msg.target == m.currentTarget()`) accepts it, because `currentTarget()` is
  also `""` with nothing selected — so one path handles both, with no new
  branch in `Update`.
- **A preview tick of its own.** Add `previewTickMsg` at `previewInterval`
  (400 ms), scheduled only while `showPreview`. It fires `loadPreviewCmd` and
  nothing else. Remove the periodic `loadPreviewCmd()` calls from the
  `refreshMsg` / `zoxideMsg` / `remoteMsg` handlers: those exist because a list
  change can move what sits under the cursor, and the 400 ms tick now covers
  that within one frame. Cursor-move key handlers keep their immediate load, so
  navigation stays instant.
  Fork budget: 2.5/s while the preview is visible, **0/s when it is hidden**
  (today it is 1/s regardless).
- **Configurable split.** `listHeight()`'s hardcoded `bh * 50 / 100` reads
  `@picker_list_ratio` (default 50, clamped 20–80) via the existing `envOrMap`
  helper. New module option `programs.lazytmux.picker.listRatio`.

### Stage 2 — list-only default

`modules/home-manager.nix`, `config/tmux.conf.nix`,
`scripts/tmux-session-picker.sh`, `scripts/tmux-window-picker.sh`,
`picker/tui.go`.

- New option `programs.lazytmux.picker.layout` = `"preview"` (default, today's
  behavior) | `"list"`, surfaced as the global `@picker_layout`.
- `runTUI` seeds `showPreview` from it. `^/` still toggles for the current
  invocation; nothing persists, because the option is the only source of truth.
- The launcher scripts read `@picker_layout` and choose the popup height with
  it — `list` opens shorter (`-h 60%`) so a full-height list is not mostly
  blank, `preview` keeps `-h 85%`. This is the only way to size per mode, given
  popups cannot be resized.

### Stage 3 — the wall, read-only

`picker/render_wall.go`, `picker/capture.go`, `picker/render_list.go` (split),
`config/tmux.conf.nix`, new `scripts/tmux-window-wall.sh`.

- `prefix + W` → a popup at `-w 95% -h 90%` running
  `tmux-picker-generate --tui --windows --wall`. `w` stays the list, `W` is the
  wall; `prefix + S` keeps the scratchpad.
- **What gets a tile:** every item in `visible` with a non-empty `target` that
  is not a header, a zoxide suggestion, or a remote row — the things
  `capture-pane` can actually read. Typing filters tiles; `^a` / `^s` apply
  unchanged.
- **Geometry** (`wallGeometry(width, height) (cols, rows int)`): tile inner
  width ≥ 34 cells plus border, tile height ≥ 8 rows; `cols` capped at 4 and
  `rows` at 3, so a page is at most 12 tiles — which also bounds the batched
  tmux command's length. Below one tile's minimum, the wall renders the list
  instead and says so in the hint line, rather than drawing something unusable.
- **Tile content:** the target's capture, last `innerHeight` lines, each passed
  through the existing `truncateVisibleWidth` (ANSI-aware) and terminated with
  `\033[49m` so a pane's background cannot bleed into the padding — the same
  treatment `loadPreviewCmd` already applies per line.
- **Keys:** `hjkl` moves between tiles, `←`/`→` pages, `↵` jumps to the tile's
  target and quits, `^x` kills it. `^/` keeps one meaning across all three
  shapes — "change how pane content is shown": in list/preview it toggles the
  preview, in the wall it drops back to list+preview inside the popup it already
  has. It never resizes the popup, because it cannot.
- `capture.go` exposes `captureTargets(targets []string) (map[string]string,
  error)` built on the batched call, with the exec function injectable — the
  same seam `collectRemoteItems` uses for `probe`, so tests need no tmux.

### Stage 4 — typing into the focused tile

`picker/capture.go`, `picker/render_wall.go`.

- `tab` focuses the current tile; `esc` unfocuses. A focused tile draws its
  border in the accent color and the hint line shows what it is focused on.
- While focused, keys are relayed as `send-keys -t <target>`. **Scope is
  deliberately small:** printable characters, Enter, Escape and Backspace.
  Arrows, `C-*` and function keys stay the picker's own, because a mapping
  table for them is never complete and a half-working relay is worse than none.
  For anything richer, `↵` jumps to the real pane — full fidelity by
  definition.
- The relay refuses any target that is not a local pane (remote rows, zoxide
  rows), so a keystroke can never be sent somewhere it cannot land.

## Testing

Unit (Go, no tmux required):

- `listHeight` honors and clamps `@picker_list_ratio`.
- `loadPreviewCmd` with nothing selectable emits an empty `previewMsg`, and the
  handler clears the viewport.
- `wallGeometry` for a table of widths/heights, including the too-small case
  that must fall back to the list.
- Tile crop: a capture wider and taller than the tile yields exactly
  `innerHeight` lines, each ≤ `innerWidth` visible cells, with ANSI intact.
- `captureTargets` parses a multi-target batch through the injected exec seam,
  including a target whose content is empty and one containing the marker text
  as ordinary output.
- The relay's key mapping: in-scope keys map to `send-keys` arguments,
  out-of-scope keys are not relayed, and non-local targets are refused.

Bats: the launcher scripts pick popup height from `@picker_layout`.

Manual, after `nix build .#default`: `prefix + s` / `prefix + w` in both
layouts, `prefix + W` at a few terminal sizes including one too small to tile,
and `tests/test-display.sh` for the status line (unchanged by this work).

## Risks

- **Fork rate.** 2.5 captures/s with the preview visible, one batched call per
  tick for the wall. Both are bounded and stop when the popup closes; the wall's
  tick can be lowered if a 12-tile page proves heavy on a busy machine.
- **ANSI in tiles.** Cropping colored output is where this can look broken;
  `truncateVisibleWidth` already handles it for the preview, and the tile tests
  cover the width and height edges.
- **Stage 4's scope creep.** The relay invites "just add arrows". The spec's
  answer is that `↵` already gives full fidelity, so the relay stays small.

## Follow-ups (not in this spec)

- Window rows are not searchable by window name (found while probing; needs its
  own issue).
- Whether the session picker ever wants a tiled twin — deliberately unanswered
  until the window wall has been lived with.
