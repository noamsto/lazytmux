# Claude image gallery (carousel) — design

- **Date:** 2026-06-09
- **Issue:** [noamsto/lazytmux#37](https://github.com/noamsto/lazytmux/issues/37)
- **Supersedes:** the v1 single-image navigator ([#32](https://github.com/noamsto/lazytmux/issues/32) / [#35](https://github.com/noamsto/lazytmux/pull/35)) — see "Pivot" below.
- **Status:** implemented, deployed, hardware-verified.

## Problem

The v1 viewer showed one image at a time (a full-pane keyboard navigator). To
survey a conversation's images you paged through them sequentially, with no way
to see the set or jump around, and no way to act on an image (open it, reveal
its folder).

## Goal

`prefix + I` (and a Claude-invocable plugin skill) toggles a **carousel** in a
tmux split: a large **preview** of the selected image with a **filmstrip** of
thumbnails below it. Arrow through the filmstrip and the preview updates live;
open the image or its folder in the desktop; the view auto-refreshes as the
Claude session touches new images.

### Pivot (grid → carousel)

This issue was titled "thumbnail grid gallery." During implementation the grid
(contact-sheet) was built and working, but in use a **preview-centric carousel**
proved the better UX for *looking at* images (wallpapers, screenshots): the big
preview replaces the need to drill into a separate full-screen view entirely.
We pivoted to the carousel as the primary (and only) layout, which also let us
**retire the v1 single-image navigator** — the carousel's preview is that view.
The grid exploration + its hardware verification live in git history.

### Non-goals

- Smooth pixel-scrolling (terminal limitation) — the filmstrip windows instead.
- Video/gif animation.
- Photographic thumbnails on sixel-only terminals — they get `chafa -f symbols`
  block-art (kitty/ghostty get real graphics). See "Renderer".
- Changing the manifest format or the CC-plugin appender hook.

## Verification (hardware-confirmed)

kitty 0.47.x host → tmux 3.6a → Go. Throwaway smoke scripts are not committed.

1. **Kitty placement under tmux.** `kitten icat --unicode-placeholder` (and the
   raw graphics protocol) place images at computed cells under tmux; a plain
   `\033[2J\033[3J` / View-swap clears them with no ghosting.
2. **bubbletea v2 hosts the graphics.** Unicode-placeholder cells (`U+10EEEE` +
   row/col diacritics, image id in the cell foreground) embed inside the
   bubbletea `View`: images render, survive repaints, and a `lipgloss` border
   stays square (placeholder cells measure as width 1). Sixel cannot embed
   (`ultraviolet`'s cell model has no graphics field) — hence the symbols
   fallback.
3. **Clean teardown + no input feedback loop.** Images are transmitted
   **store-only** via the raw protocol (`a=T,U=1`, zero visible cells) with
   **`q=2`** to suppress kitty's responses, and a `delete-all` (`a=d,d=A,q=2`)
   fires on quit and on `SIGTERM`/`SIGHUP` (toggle-off). **`q=2` is essential**:
   without it, kitty's `…;OK\e\\` replies land on the program's stdin and the
   literal `OK` is parsed as keystrokes (the `O` fired "open folder" → a runaway
   `xdg-open` loop).

### Gotchas (carried into the code)

- `kitten icat` with a **non-tty stdin** counts stdin as a second image and
  aborts `--place`; the store-only raw transmit sidesteps `kitten` entirely.
- Invoke the picker + scripts by **absolute store path** (the `@picker_generate@`
  placeholder), not bare PATH names — pane PATHs go stale after a lazytmux bump
  until the tmux server restarts.

## Architecture

```
prefix + I  ──┐
              ├─> tmux-claude-images (thin launcher, targets $TMUX_PANE)
Claude (skill)┘     guard: manifest non-empty
                    toggle: kill tagged viewer pane, else split + run:

   tmux-picker-generate --gallery <pane>      (Go, bubbletea; picker module)
      load manifest images/<pane>.jsonl  (populated by the CC-plugin hook)
      pick backend (kitty placeholder | chafa symbols) from $TERM
      carousel loop:
        transmit preview (1 big) + visible filmstrip thumbs, store-only, q=2,
          to /dev/tty (out of band from bubbletea's frame writes)
        View = title + subtitle(current) + framed preview + framed filmstrip
               (selected frame colored) + centered hints
        keys: h/l/g/G/digits/n/p move · o/↵ xdg-open image · O open folder ·
              r reload · q quit
        tick (1.5s): re-read manifest on mtime change (auto-refresh)
      q / SIGTERM ─> delete-all images, exit ─> pane closes
```

The CC plugin's PostToolUse hook (`claude-plugin/hooks/hooks.json → images.sh →
claude-images-update`) is what fills the manifest, so the CC → manifest →
carousel chain runs through the plugin. A plugin **skill** (`image-gallery`)
lets Claude open the carousel by running the launcher (targets its own pane).

## Components

- **Launcher** `scripts/tmux-claude-images.sh` — thin toggle: resolve the
  invoking pane (`$TMUX_PANE`, fallback active), guard on a non-empty manifest,
  kill the tagged viewer if present, else split and run the gallery by absolute
  picker path. Runnable by `prefix + I` and by Claude (Bash).
- **Gallery** `picker/gallery.go` — the bubbletea `galleryModel`: layout
  geometry (`computeLayout`: ~16:9 preview box + filmstrip window), filmstrip
  windowing (`stripStart`), transmit (`transmitView`), input/nav, `xdg-open`
  actions, auto-refresh tick, signal teardown, and the framed/centered `View`.
- **Render helpers** `picker/gallery_render.go` (pure, unit-tested):
  `parseManifest`/`loadManifest`, `chooseGridBackend` (termname-only — the kitty
  path uses the raw protocol, no `kitten`), `tmuxPassthrough`, `transmitVirtual`
  (store-only, `q=2`), `deleteAll`, `rowColDiacritics` (full 297-entry kitty
  table), `placeholderBlock`, `symbolsArgs`/`symbolsBlock` (chafa text).
- **Dispatch** `picker/main.go` — `--gallery <pane>` → `runGallery`.
- **Plugin skill** `claude-plugin/skills/image-gallery/SKILL.md` — tells Claude
  to run `tmux-claude-images` to show the user the conversation's images.
- **Removed:** the v1 inner `--view` navigator and `claude-image-render` (the
  carousel preview + its 2-rung backend replace them).

## Error handling

- No/empty manifest → launcher flashes "no images yet" and exits.
- Missing/deleted image file → kitty/chafa simply draws nothing for it; never
  crashes.
- `/dev/tty` unopenable → graphics are skipped (no transmit/teardown); the TUI
  still runs.
- Non-kitty terminal / `chafa` absent → symbols backend, or a textual fallback.

## Testing

- **Go unit tests** (`picker/gallery_test.go`): manifest parse, backend
  selection, `tmuxPassthrough`/`transmitVirtual`/`deleteAll` exact escapes (incl.
  `q=2`), `placeholderBlock` rune encoding, `computeLayout` geometry,
  `stripStart` windowing.
- **bats** (`tests/claude-images.bats`): the manifest appender (Read/Write/
  screenshot extraction, dedup, no-pane no-op, regex-DoS guard).
- **Manual**: `prefix + I` on kitty — preview/filmstrip render, nav + paging,
  `o`/`O` open, `q`/toggle-off teardown clean; resize reflows the filmstrip;
  Claude opening it via the skill; symbols fallback on a non-kitty term.

## Risks / open items

- Filmstrip thumb size vs count is a fixed trade-off (`stripThumbW`); resize
  reflows the count automatically.
- Preview box aspect (`previewBoxCols`) assumes ~16:9 / ~1:2.1 cells; very
  different image aspects still letterbox within the box (kitty preserves
  aspect).
- Transmit churn: every selection change re-transmits the preview + visible
  filmstrip; fine under tmux in practice (store-only, `q=2`).
