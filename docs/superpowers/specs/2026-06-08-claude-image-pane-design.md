# Claude conversation image pane — design

- **Date:** 2026-06-08
- **Issue:** [noamsto/lazytmux#32](https://github.com/noamsto/lazytmux/issues/32)
- **Status:** approved, hardware-verified — ready for implementation plan

## Problem

Claude Code (the CLI) is input-only for images: it can *read* a screenshot you
hand it, but it can't *display* images back the way the desktop/web apps show
them inline. There's no terminal-side view of the images a conversation has
touched (screenshots it took, images it read, images it wrote). Upstream
feature requests exist (kitty graphics in the TUI) but are unshipped.

lazytmux already owns the tmux + Claude Code integration seam (`claude-plugin/`
hooks, the `claude-status*` pane-state system, `tmux-scratchpad` toggle popups),
so this belongs here, not in a consumer's personal nix-config.

## Goal

A tmux keybind toggles a **split pane** showing a browsable gallery of every
image touched by the Claude session running in the current pane. Images
accumulate over the conversation; the pane is summoned and dismissed on demand.

### Non-goals (v1)

- Non-image resources (URLs fetched, files written, diffs). The manifest
  reserves a `type` field so these are zero-rework later, but v1 is images-only.
- Live update *while the pane is open* (`fzf --listen`). The pane reads the
  manifest fresh on each toggle, so it always shows the full current set; a
  live-refresh-while-open is a later nicety.
- Thumbnail/grid layouts, cross-pane aggregation.

## Verification (done before writing this spec)

Two facts were confirmed on real hardware (kitty 0.47.1 host → tmux 3.6a →
Claude), not assumed:

1. **Floating popups are a dead end.** A tmux `display-popup` reports `0x0`
   pixel size to applications, so `kitten icat` can't size the image and draws
   nothing. This is an unfixed *tmux* bug (kitty#7165; "still broken" Jan 2025,
   nothing in tmux 3.6). Sixel doesn't rescue it on a kitty host (kitty has no
   sixel). **⇒ the viewer must be a real split pane, not a popup.**
2. **Split pane works.** In a normal split pane, tmux reports a real pixel size
   (`1250x1260` measured), and both `kitten icat` plain and
   `kitten icat --unicode-placeholder --transfer-mode=memory` render crisply
   with zero errors.

Also confirmed: tmux 3.6a here is **built with sixel** (`#{sixel_support}` = 1),
and `#{client_termname}` reliably exposes the outer terminal under tmux
(`xterm-kitty`) — the basis for renderer selection below.

## Architecture

Everything maps onto existing lazytmux structure; no new infrastructure.

| Piece | Location | Models on |
|-------|----------|-----------|
| Manifest | `${CLAUDE_STATUS_DIR:-/tmp/claude-status}/images/<pane_id>.jsonl` | sibling of `panes/`, `issues/` |
| Appender (hook) | `claude-plugin/scripts/claude-images.sh` | `claude-plugin/scripts/status.sh` |
| Viewer (keybind) | `scripts/tmux-claude-images.sh` | `scripts/tmux-scratchpad.sh` |
| Hook registration | `claude-plugin/hooks/hooks.json` (PostToolUse) | existing `status.sh` entries |
| Cleanup | extend dead-pane sweep in `scripts/claude-status-update.sh` | existing sweep |
| Keybind | `config/tmux.conf.nix` | scratchpad keybind |
| Module wiring / deps | `modules/home-manager.nix`, flake package closure | existing options |

### Data flow

```
PostToolUse hook ──> claude-images.sh add
   reads hook JSON on stdin, keyed by $TMUX_PANE
   ──> appends JSONL line to images/<pane>.jsonl

keybind ──> tmux-claude-images.sh   (outer mode)
   captures active (Claude) pane id = manifest source
   toggle: kill existing viewer pane if present, else split + run inner mode

tmux-claude-images.sh --view <source_pane>   (inner mode, in the split)
   fzf over images/<source_pane>.jsonl
   --preview '<render-script> {path}'   (renderer ladder below)
```

## Components

### 1. Manifest

- Path: `${CLAUDE_STATUS_DIR:-/tmp/claude-status}/images/<pane_id>.jsonl`, where
  `<pane_id>` is `$TMUX_PANE` with the leading `%` stripped — exactly how
  `claude-status-update.sh` keys `panes/` and `issues/`.
- One JSON object per line:
  ```json
  {"type":"image","path":"/abs/path.png","source":"Read","ts":"2026-06-08T12:00:00+03:00"}
  ```
- `type` always `"image"` in v1 (reserved for future resource kinds).
- Dedup by `path`: the appender skips a path already present in the file.

### 2. Appender — `claude-plugin/scripts/claude-images.sh`

Shell + `jq`, styled after `status.sh` / `claude-status-update.sh`.

- Reads the PostToolUse hook JSON from stdin.
- Extracts a candidate path. Per-tool handling:
  - `Read` / `Write` → `.tool_input.file_path`
  - MCP screenshot tools (Playwright `browser_take_screenshot`,
    firefox-devtools screenshot tools) → check `.tool_input.filename` /
    `.tool_input.path` / `.tool_input.output_path` and scan `.tool_response`
    for a saved path. **Exact fields to be confirmed against live payloads
    during implementation** — screenshot tools vary.
- Keeps the path only if it has an image extension
  (`png jpg jpeg gif webp bmp`) **and** the file exists.
- Resolves `<pane_id>` from `$TMUX_PANE`; silent `exit 0` if unset (non-tmux
  context) or if no image path found.
- Dedups, then appends the JSONL line. Cost is one `jq` parse + an optional
  append — negligible, fully out-of-band from Claude's context.

### 3. Viewer — `scripts/tmux-claude-images.sh`

Two-mode, mirroring `tmux-scratchpad.sh` but **pane-based, not popup-based**.

**Outer mode (from keybind):**
- Capture the active pane id — this is the Claude pane and the manifest source.
- If no images exist for it, flash a tmux message (`no images yet`) and exit.
- Toggle: if a viewer pane tagged for this source already exists in the window
  (`@claude_img_src` pane option), `kill-pane` it (toggle off). Otherwise
  `split-window` a viewer pane, tag it, and run inner mode.

**Inner mode (`--view <source_pane>`):**
- Resolve `images/<source_pane>.jsonl`.
- `fzf` over it: list shows `source · basename`; the hidden full path drives the
  preview. `--preview '<render-script> {path}'`, sized to
  `$FZF_PREVIEW_COLUMNS` × `$FZF_PREVIEW_LINES`.
- Enter on an entry opens it full-pane (clears the list, renders large).
- Missing/deleted file → preview shows a placeholder, never crashes.

### 4. Renderer ladder (the fallback — must not assume kitty)

A small render helper picks the backend from the **outer** terminal, detected
via `tmux display-message -p '#{client_termname}'`:

| `client_termname` | Renderer | Mechanism / notes |
|-------------------|----------|-------------------|
| `xterm-kitty`, `xterm-ghostty` | `kitten icat --unicode-placeholder --transfer-mode=memory` | kitty graphics; tmux can't parse it ⇒ relies on `allow-passthrough on` (already set) + unicode placeholders for redraw stability. **Verified.** |
| `foot*`, `wezterm`, `xterm*`, `contour*`, `konsole*` | `chafa -f sixel` | tmux 3.6a parses & composites sixel natively (no passthrough); host terminal renders it. |
| anything else / `chafa` missing / detection fails | `chafa -f symbols` | Unicode block-art. **Universal floor — works in every terminal, including no-graphics ones.** |

Consequences:
- `chafa` becomes a **hard dependency** (it is the fallback floor and the sixel
  renderer); not currently in the closure — added.
- `kitten` is already present.
- Worst case is always a visible (if blocky) image, never a blank pane.

### 5. Cleanup

Piggyback the existing dead-pane sweep in `claude-status-update.sh`: when it
removes `panes/<pane>` and `issues/<pane>` for a pane that no longer exists,
also remove `images/<pane>.jsonl`. No new lifecycle code or timers.

### 6. Module wiring

- Add `chafa` (and `fzf` if not already) to the wrapped-tmux package closure.
- Register the new PostToolUse command in `claude-plugin/hooks/hooks.json`
  alongside the existing `status.sh processing` entry.
- Add the keybind in `config/tmux.conf.nix` (proposed `prefix + i` — confirm
  it's free during implementation).
- **Open choice:** gate behind `programs.lazytmux.claudeImagePane.enable`
  (default `true`) vs. always-on like scratchpad. Recommend an enable option
  defaulting true, because `chafa` adds to the closure and a published flake's
  consumers should be able to opt out. Final call deferred to reading
  `modules/home-manager.nix` conventions.

## Risks / open items

- **fzf preview + kitty graphics redraw fiddliness.** Rendering kitty graphics
  in an fzf `--preview` region (which re-renders on every list move) can leave
  artifacts; `--clear` + `--transfer-mode=memory` + `--unicode-placeholder`
  mitigate. The `chafa -f symbols` floor sidesteps it entirely. To validate in
  implementation.
- **Screenshot tool payload shapes** (#2) — confirm path-extraction fields
  against real Playwright / firefox-devtools hook payloads.
- **Keybind collision** — verify `prefix + i` (or chosen key) is unbound.

## Implementation phases (for the plan)

1. ~~Smoke test: kitty graphics in a split pane~~ — **done, passed.**
2. Manifest + appender (`claude-images.sh`) + hook registration; unit-test path
   extraction against captured hook payloads.
3. Renderer helper with the 3-rung ladder + terminal detection.
4. Viewer (`tmux-claude-images.sh`) toggle + fzf integration.
5. Cleanup sweep extension.
6. Module wiring, keybind, deps; enable-option decision.
7. End-to-end test in kitty (graphics) and foot (sixel) and a symbols fallback.
