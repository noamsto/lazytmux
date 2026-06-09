# Claude Image Gallery (Carousel) — implementation notes

> As-built record. The design of record is
> `docs/superpowers/specs/2026-06-09-claude-image-grid-design.md`. This started
> as a thumbnail *grid* and pivoted to a *carousel* (preview + filmstrip) during
> implementation — see the spec's "Pivot" section. The grid TDD lives in git
> history; this doc reflects what shipped.

**Goal:** `prefix + I` (and a Claude plugin skill) toggles a tmux split showing a
carousel — big preview of the selected image + a filmstrip — of the images the
current Claude pane has touched.

**Architecture:** A gallery mode of the existing `picker/` Go binary
(`tmux-picker-generate --gallery <pane>`), bubbletea v2 + lipgloss. kitty images
via the raw graphics protocol (Unicode-placeholder cells embedded in the
`View`); `chafa -f symbols` text elsewhere. Launched by a thin shell toggle that
targets `$TMUX_PANE`. Manifest fed by the CC-plugin PostToolUse hook.

## File structure (as built)

- `picker/gallery_render.go` — pure helpers (unit-tested): manifest parse,
  `chooseGridBackend`, `tmuxPassthrough`, `transmitVirtual` (store-only `q=2`),
  `deleteAll`, full `rowColDiacritics` table, `placeholderBlock`, `symbolsBlock`.
- `picker/gallery.go` — `galleryModel` (bubbletea): `computeLayout`,
  `stripStart`, `transmitView`, `Update` (nav/paging/`o`/`O`/`r`/`q` + resize +
  auto-refresh tick + signal teardown), centered framed `View`, `runGallery`.
- `picker/gallery_test.go` — table tests for all of the above pure logic.
- `picker/main.go` — `--gallery <pane>` dispatch.
- `scripts/tmux-claude-images.sh` — thin launcher (toggle a split running the
  gallery for `$TMUX_PANE`), invoked by `prefix + I` and the plugin skill.
- `claude-plugin/skills/image-gallery/SKILL.md` — lets Claude open the carousel.
- `config/tmux.conf.nix` — `tmux-claude-images` in `scriptsWithIcons` (so
  `@picker_generate@` resolves to the absolute picker path); `claude-image-render`
  removed.
- `tests/claude-images.bats` — manifest-appender tests (renderer-selection tests
  moved to Go).

No `flake.nix`/`go.mod`/`vendorHash` change (stdlib + already-vendored
bubbletea/lipgloss).

## Build order (how it was implemented + verified)

1. **Pure layer** (TDD): manifest parse, backend selection, passthrough +
   transmit/delete, diacritics + placeholder builder, symbols, geometry. All
   `go test` green.
2. **Smoke-tests on hardware** (throwaway): kitty placement + paging clear;
   bubbletea hosting placeholder cells (border square, survives repaints);
   store-only transmit + `delete-all` teardown.
3. **Grid model** (later replaced) → **pivot to carousel**: preview + filmstrip,
   `transmitView`, framed/centered `View`, nav, `xdg-open`/`O`, auto-refresh.
4. **`q=2` fix**: suppress kitty responses (the `OK`-as-keystrokes / runaway
   `xdg-open` loop). Verified no feedback loop after.
5. **Polish**: 16:9 preview box, bigger framed filmstrip thumbs, centered title +
   subtitle + hints, selection = colored frame.
6. **Wiring + cleanup**: thin launcher targeting `$TMUX_PANE` (Claude-invocable);
   plugin `image-gallery` skill; removed v1 `--view` navigator +
   `claude-image-render`; absolute picker path via `@picker_generate@`.

## Verification

- `cd picker && go test ./... && go vet ./...` — green.
- `nix build .#default` — green (vendorHash unchanged).
- Deployed via `nh home switch . -- --override-input lazytmux path:<worktree>`;
  exercised `prefix + I` end-to-end on kitty (preview/filmstrip, nav, `o`/`O`,
  teardown, resize reflow, auto-refresh) and confirmed no `xdg-open` loop.

## Key gotchas (don't regress)

- **`q=2` on every kitty graphics command** — else `OK` replies are parsed as
  keys → runaway opens.
- **Store-only raw transmit** (no `kitten icat` for the grid path) — avoids the
  non-tty-stdin "two images" abort and emits zero visible cells.
- **Absolute picker path** (`@picker_generate@`), not bare PATH — pane PATHs go
  stale after a lazytmux bump until server restart.
- **Write graphics to `/dev/tty`**, not stdout — keeps APC bytes out of
  bubbletea's frame buffer.
