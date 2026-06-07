# Zoxide Suggestions in the Session Picker

**Date:** 2026-06-07
**Issue:** [#6](https://github.com/noamsto/lazytmux/issues/6) (revised — extend our Go picker instead of sesh/gum)
**Status:** Approved

## Summary

Extend the Go bubbletea picker (`picker/`, `tmux-picker-generate`) so `prefix+s`
shows existing sessions first, then top-ranked zoxide directories. Selecting a
zoxide row creates a session at that directory and switches to it. The sesh/gum
tmux bindings (`prefix+K`, `prefix+S`) are removed from lazytmux; sesh stays in
nix-config for the `sc` CLI abbreviation and the Hyprland launcher (scope split).

## Motivation

Issue #6 wanted a sesh-style switch-or-create flow on `prefix+s`. The original
sketch used `sesh list | gum filter`, but `prefix+s` already runs our own picker
— which has fuzzy filtering, preview, claude status, and resource columns that
sesh/gum can't show. Extending it keeps one picker, drops two closure
dependencies, and the user has no `sesh.toml`, so sesh adds nothing beyond
create-or-attach + switch.

## Design

### 1. Data collection (`picker/main.go`)

- New `collectZoxide()`: runs `zoxide query -l`, preserving rank order.
- **Dedupe:** skip directories whose path equals an existing session's path
  (symlink-resolved, trailing `/` trimmed), or whose derived session name
  collides with an existing session name.
- **Top-N:** keep the first 15 after dedupe (constant; not configurable).
- **Graceful absence:** `zoxide` missing or erroring → no suggestions section;
  picker behaves exactly as today. Binary pinned via `pkgs.zoxide` in the
  tmux wrapper PATH.

### 2. List model (`picker/tui.go`)

- `listItem` gains `createPath string` — non-empty marks a zoxide row.
- `buildSessionItems` appends an unselectable header (` Suggestions`, styled
  like the window-mode session headers) followed by zoxide rows: dir icon +
  `~`-shortened path. `searchText` = basename + parent dir.
- Fuzzy filter narrows sessions and suggestions together; the existing
  `pruneOrphanHeaders` drops the header when no suggestion survives the filter.
- Session mode only — window mode (`prefix+w`) is untouched.

### 3. Actions

- **Enter** on a zoxide row:
  1. Derive session name: path basename with `.` and `:` replaced by `_`
     (tmux forbids them in session names).
  2. `tmux new-session -d -s <name> -c <path>` — skipped if the session
     already exists (attach semantics).
  3. `tmux switch-client -t <name>`.
  4. `zoxide add <path>` — new session shells never `cd`, so the rank would
     not bump otherwise.
- **Kill key** on a zoxide row: no-op.
- **Preview:** directory listing (`eza` if present in the wrapper PATH, else
  `ls`) rendered in the existing preview viewport. Session rows keep the
  pane-capture preview.

### 4. Sesh removal (lazytmux only)

- Remove `prefix+K` (gum popup) and `prefix+S` (fzf picker) bindings from
  `config/tmux.conf.nix`.
- Drop `pkgs.sesh`, `pkgs.gum`, the fzf-tmux usage, and `sesh-preview` from
  the wrapper closure. `pkgs.fzf` stays only if something else still uses it
  (verify during implementation).
- **Out of scope:** nix-config's `sc` abbreviation, sesh fish completions, and
  the Hyprland `sesh-launcher.sh` keep using nix-config's own sesh package.
  Full sesh retirement is a possible follow-up, not part of this change.

### 5. Testing

- New logic is written as pure functions (sessions + zoxide lines in, rows
  out — no `exec` inside) so it is unit-testable.
- Add `picker/picker_test.go` covering:
  - session-name derivation (dots, colons, trailing slash),
  - zoxide dedupe (path match, symlink-resolved match, name collision),
  - top-N cut,
  - regression anchors for the existing `fuzzyScore` and width helpers.
- No harness wiring needed: `buildGoModule`'s default check phase runs
  `go test ./...` on `nix build` / `nix flake check`.
- Manual verification for TUI behavior: popup, filter, create+switch, dedupe,
  preview, and the missing-zoxide degradation path.

### 6. Docs

- Update CLAUDE.md's stale claim that `prefix+s`/`prefix+w` use `choose-tree`
  (they run the Go TUI), and document the new suggestions behavior.

## Decisions Log

| Question | Decision |
|----------|----------|
| Picker engine | Extend our Go bubbletea picker, not sesh/gum |
| When suggestions show | Always, top-15, below sessions |
| Created-session naming | Dir basename, `.`/`:` → `_` |
| Duplicates | Hide zoxide dirs already covered by a session (path or name) |
| sesh bindings | Drop `prefix+K`/`prefix+S` + closure deps from lazytmux |
| `sc` CLI / Hyprland launcher | Untouched — they use nix-config's sesh (scope split) |
| Zoxide-row preview | Directory listing (eza, ls fallback) |
