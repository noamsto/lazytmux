# Claude conversation image pane — design

- **Date:** 2026-06-08
- **Issue:** [noamsto/lazytmux#32](https://github.com/noamsto/lazytmux/issues/32)
- **Status:** approved, hardware-verified, self-reviewed — ready for implementation plan

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
- Thumbnail/grid layouts, cross-pane aggregation, live-update while the pane is
  open (the navigator re-reads the manifest each time it's toggled, so it always
  shows the full current set).

## Verification (done before writing this spec)

Confirmed on real hardware (kitty 0.47.1 host → tmux 3.6a → Claude), not assumed:

1. **Floating popups are a dead end.** A tmux `display-popup` reports `0x0`
   pixel size to applications, so `kitten icat` can't size the image and draws
   nothing (kitty#7165, unfixed in tmux 3.6; sixel doesn't rescue it on a kitty
   host). **⇒ the viewer must be a real split pane, not a popup.**
2. **Full-pane render in a split pane works.** tmux reports a real pixel size
   (`1250x1260` measured) and both `kitten icat` plain and
   `kitten icat --unicode-placeholder --transfer-mode=memory` render crisply,
   zero errors. **This is the exact mechanism the viewer uses** (see §3) — the
   verification covers the real path, not a proxy.

Also: tmux 3.6a here is **built with sixel** (`#{sixel_support}` = 1), and
`#{client_termname}` reliably exposes the outer terminal under tmux
(`xterm-kitty`) — the basis for renderer selection (§4).

## Design decisions from self-review

- **No `fzf`.** lazytmux deliberately removed its fzf picker
  (`modules/home-manager.nix:92`), and an `fzf --preview` image region under
  tmux is the one thing *not* verified (preview-pane redraw of graphics is
  finicky). The viewer is instead a **full-pane keyboard navigator** — the
  verified path — showing one image at a time.
- **`kitten` is optional**, not assumed: generic flake consumers may not have
  it. Renderer prefers `kitten` when present, else uses `chafa` for kitty
  terminals too (§4). `kitten` is *not* bundled (it's huge); `chafa` is.
- **Reuse `lib-claude.sh`** for pane-id resolution and the status-dir root, so
  the hook and viewer sides never diverge.

## Architecture

Everything maps onto existing lazytmux structure; no new infrastructure, no new
runtime dependency beyond `chafa`.

| Piece | Location | Models on / reuses |
|-------|----------|--------------------|
| Manifest | `<status-dir>/images/<pane_id>.jsonl` | sibling of `panes/`, `issues/` |
| Appender (hook) | `claude-plugin/scripts/claude-images.sh` | `status.sh`; sources `lib-claude.sh` |
| Viewer (keybind) | `scripts/tmux-claude-images.sh` | `tmux-scratchpad.sh` (split, not popup) |
| Renderer helper | `scripts/claude-image-render.sh` | new; the 3-rung ladder |
| Hook registration | `claude-plugin/hooks/hooks.json` (PostToolUse) | existing `status.sh` entries |
| Cleanup | extend dead-pane sweep in `claude-status-update.sh` | existing sweep |
| Keybind | `config/tmux.conf.nix` | scratchpad keybind |
| Module wiring / deps | `modules/home-manager.nix`, package closure | existing options |

`<status-dir>` = the same root `lib-claude.sh` resolves (`${CLAUDE_STATUS_DIR:-/tmp/claude-status}`).

### Data flow

```
PostToolUse hook ──> claude-images.sh
   sources lib-claude.sh -> resolves pane_id (same logic as status)
   reads hook JSON on stdin, extracts image path
   ──> appends JSONL line to images/<pane_id>.jsonl   (dedup by path+mtime)

keybind ──> tmux-claude-images.sh           (outer mode)
   captures active (Claude) pane id = manifest source
   toggle: kill existing viewer pane if present, else split + run viewer

tmux-claude-images.sh --view <source_pane>  (inner mode, in the split)
   loop: render current image full-pane via claude-image-render.sh
         status line "[i/N] basename · source"
         read key: n/p navigate, g/number jump, q quit; re-render
```

## Components

### 1. Manifest

- Path: `<status-dir>/images/<pane_id>.jsonl`, `<pane_id>` = `$TMUX_PANE` with
  the leading `%` stripped — same keying as `panes/`/`issues/`.
- One JSON object per line:
  ```json
  {"type":"image","path":"/abs/path.png","source":"Read","ts":"2026-06-08T12:00:00+03:00","mtime":1717837200}
  ```
- `type` always `"image"` in v1 (reserved).
- **Dedup by `(path, mtime)`**, not path alone — so a tool that reuses a fixed
  output filename (e.g. Playwright always writing `screenshot.png`) still
  records each distinct capture instead of silently keeping only the first.

### 2. Appender — `claude-plugin/scripts/claude-images.sh`

Shell + `jq`, styled after `status.sh`; **sources `lib-claude.sh`** for pane-id
resolution and the status-dir root (no reinvented `$TMUX_PANE` handling).

- Reads the PostToolUse hook JSON from stdin.
- Extracts a candidate path. Per-tool handling:
  - `Read` / `Write` → `.tool_input.file_path`
  - MCP screenshot tools (Playwright `browser_take_screenshot`,
    firefox-devtools screenshot tools) → check `.tool_input.filename` /
    `.tool_input.path` / `.tool_input.output_path` and scan `.tool_response`
    for a saved path. **Exact fields to be confirmed against captured live
    payloads during implementation** (phase 2).
- Keeps the path only if it has an image extension
  (`png jpg jpeg gif webp bmp`) **and** the file exists; stats `mtime`.
- Silent `exit 0` if pane-id unresolved (non-tmux) or no image path found.
- Dedups on `(path, mtime)`, then appends. Cost: one `jq` parse + optional
  append — negligible, out-of-band from Claude's context. (A concurrent-append
  TOCTOU can at worst duplicate a line; cosmetic, acceptable.)

### 3. Viewer — `scripts/tmux-claude-images.sh`

Two-mode, mirroring `tmux-scratchpad.sh` but **a split pane, not a popup**, and
a **full-pane navigator, not a list+preview**.

**Outer mode (from keybind):**
- Capture the active pane id — the Claude pane and manifest source.
- If `images/<pane>.jsonl` is missing/empty, flash `no images yet` and exit.
- Toggle: if a viewer pane tagged for this source exists in the window
  (`@claude_img_src` pane option), `kill-pane` it (toggle off). Otherwise
  `split-window`, tag it, run inner mode.

**Inner mode (`--view <source_pane>`):**
- Read the manifest into an ordered list (dedup already applied at write time).
- Loop: render the current entry full-pane via `claude-image-render.sh`, print a
  one-line status (`[3/7] login.png · source:Read   n/p · g# jump · q quit`),
  read a single key, navigate, re-render. `n`/`p` cycle, `g`+number jumps, `q`
  quits (and the outer toggle still kills the pane).
- Missing/deleted file → render a textual placeholder for that entry; never
  crash.

### 4. Renderer ladder — `scripts/claude-image-render.sh <path> [cols] [rows]`

Picks the backend from the **outer** terminal
(`tmux display-message -p '#{client_termname}'`) and `kitten` availability:

| Condition | Renderer | Mechanism / notes |
|-----------|----------|-------------------|
| kitty/ghostty term **and** `kitten` on PATH | `kitten icat --unicode-placeholder --transfer-mode=memory` | kitty graphics; needs `allow-passthrough on` (already set). **Verified.** |
| kitty/ghostty term, **no** `kitten` | `chafa -f kitty --passthrough tmux` | kitty graphics via chafa when kitten absent. |
| `foot*` / `wezterm` / `xterm*` / `contour*` / `konsole*` | `chafa -f sixel` | tmux 3.6a composites sixel natively (no passthrough). |
| anything else / detection fails | `chafa -f symbols` | Unicode block-art. **Universal floor — never a blank pane.** |

- `chafa` is a **hard dependency** (sixel rung, symbols floor, and kitten-absent
  kitty rung). Added to the closure.
- `kitten` is **not** bundled (size); used only when the host provides it.
- Sizing: the navigator passes the pane's `cols`/`rows`; the helper maps these
  to `--place`/`--size` so the image fits the split.

### 5. Cleanup

Extend the existing dead-pane sweep in `claude-status-update.sh`: when it removes
`panes/<pane>` and `issues/<pane>` for a vanished pane, also remove
`images/<pane>.jsonl`. No new lifecycle code or timers.

### 6. Module wiring

- Add `chafa` to the wrapped-tmux package closure. (No `fzf`; `kitten` not
  bundled.)
- Register the new PostToolUse command in `claude-plugin/hooks/hooks.json`
  alongside the existing `status.sh processing` entry.
- Add the keybind in `config/tmux.conf.nix`. `prefix + i` is taken by the enrich
  table (`bind-key i switch-client -T enrich`), so use **`prefix + I`**.
- **Resolved: always-on, no new option.** scratchpad, lazygit, btop, and yazi
  are all always-on keybinds with always-bundled deps; only enrich is gated
  because it carries external-API cost. `chafa` has no such cost (~small
  closure add), so always-on matches the dominant pattern and avoids
  module-threading. An enable option can be added later if a consumer asks.

## Risks / open items

- **Navigator redraw between images.** Clearing the previous kitty image before
  drawing the next (`--clear` / placeholder reuse) needs care to avoid
  ghosting; the `chafa` rungs are text and clear trivially. Lower risk than the
  abandoned fzf-preview path, and on the verified substrate.
- **Screenshot tool payload shapes** — confirm path-extraction fields against
  real Playwright / firefox-devtools hook payloads (phase 2).
- **Keybind collision** — verify the chosen key is unbound.

## Implementation phases (for the plan)

1. ~~Smoke test: kitty graphics full-pane in a split~~ — **done, passed.**
2. Manifest + appender (`claude-images.sh`, sourcing `lib-claude.sh`) + hook
   registration; test path extraction against captured hook payloads
   (Read/Write/screenshot), incl. `(path,mtime)` dedup.
3. Renderer helper (`claude-image-render.sh`) with the 4-condition ladder +
   terminal/kitten detection; eyeball each rung (kitty, foot/sixel, symbols).
4. Viewer (`tmux-claude-images.sh`) — toggle + full-pane navigation loop.
5. Cleanup sweep extension.
6. Module wiring, keybind, `chafa` dep; enable-option decision.
7. End-to-end test in kitty (graphics), foot (sixel), and a symbols fallback.
