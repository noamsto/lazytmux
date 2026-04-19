# Undo for Closed Sessions, Windows, and Panes

**Date:** 2026-04-19
**Status:** Approved
**Repo:** `~/Data/git/lazytmux`

## Overview

Add an "undo close" feature to lazytmux that re-opens recently closed tmux sessions, windows, and panes. Triggered by `prefix+u` (quick pop) and `prefix+U` (picker). Captures are recorded automatically on tmux close/exit hooks — covering the common case of an accidental `Ctrl+D` in the last pane, which exits the shell and can cascade to close the window and even the session.

## Motivation

- Accidental `Ctrl+D` in a shell kills the pane, which may cascade to close the window and/or session.
- Recovering manually requires recreating the session/window, `cd`-ing to the right path, and re-opening any splits — tedious and error-prone.
- Existing lazytmux pickers (`prefix+s`, `prefix+w`) only list *live* sessions and windows.

## Scope and Non-Goals

**In scope:**
- Capture and restore at three levels: pane, window, session.
- Dedup captures to the *outermost* unit that died (e.g., a session close does not produce separate entries for each of its windows and panes).
- Two keybindings: `prefix+u` (pop newest), `prefix+U` (picker).
- Working directory (`cwd`) preservation for each restored pane.
- Pane layout preservation within restored windows via tmux's `window_layout` string.
- Ephemeral stack capped at 10 entries, stored under `/tmp/lazytmux-undo/`.

**Out of scope (documented limitations):**
- Running processes are **not** restored. A shell exited via `Ctrl+D` is gone; the restored pane gets a fresh shell in the same cwd.
- Persistence across tmux or host restart. Files live in `/tmp`.
- Exact split sizes for pane-level restores (single-pane split uses tmux defaults). Window-level restores preserve full layout via `window_layout`.

## Architecture

New single script `scripts/tmux-undo.sh` exposing three subcommands:

| Subcommand | Invocation | Purpose |
|---|---|---|
| `capture pane` | tmux hook `pane-died` | Record pane death if parent window still alive. |
| `capture window` | tmux hook `window-unlinked` | Record window close if parent session still alive. |
| `capture session` | tmux hook `session-closed` | Record session close. |
| `pop` | `prefix+u` keybinding | Restore and remove newest stack entry. |
| `picker` | `prefix+U` keybinding | Open `fzf` popup to choose any entry to restore. |

The script is packaged via `writeShellScriptBin` in `config/tmux.conf.nix`, registered in the `scripts` attrset, and wired into hooks and keybindings.

### Storage Layout

```
/tmp/lazytmux-undo/
  <timestamp>.<level>        — one file per stack entry (user-visible undo stack)
  _lock                      — advisory lock for concurrent captures
  _dedup_<parent_id>         — short-lived marker (TTL ~2s) to skip child captures when a parent is being captured
  _scratch/<pane_id>         — per-pane snapshot written on pane-died; consumed at capture time
  _live/<session_id>         — shadow index of live session structure (window names, indices, layouts)
  _log                       — best-effort error log
  _corrupt/                  — quarantined malformed entry files
```

- `<timestamp>` is milliseconds since epoch (monotonic-ish ordering without relying on float precision).
- `<level>` is `session`, `window`, or `pane`.
- Entry files are simple `key=value` lines plus a nested block for child structure where needed. No `jq` dependency — parsed with bash associative arrays / `while read`.

### Entry Schema

**Pane entry (`*.pane`):**
```
level=pane
timestamp=<ms>
display_name=<session>:<window>  (~/path)
session_name=<name>
window_id=<@N>                    (tmux window ID when available)
window_name=<name>
window_index=<N>
pane_cwd=<abs path>
split_direction=<h|v|none>        (relative to the pane that remained; "none" if it was the only pane pre-death)
```

**Window entry (`*.window`):**
```
level=window
timestamp=<ms>
display_name=<session>:<window>
session_name=<name>
window_name=<name>
window_index=<N>
window_layout=<tmux layout string>
pane_count=<N>
pane_0_cwd=<abs path>
pane_1_cwd=<abs path>
...
```

**Session entry (`*.session`):**
```
level=session
timestamp=<ms>
display_name=<session name> (<N windows>)
session_name=<name>
window_count=<N>
win_0_name=<name>
win_0_index=<N>
win_0_layout=<layout string>
win_0_pane_count=<N>
win_0_pane_0_cwd=<abs>
...
```

## Capture Logic

### Hook Wiring

In `config/tmux.conf.nix`:

```tmux
set-hook -g pane-died       'run-shell "${script.tmux-undo}/bin/tmux-undo capture pane #{hook_pane} #{session_id} #{window_id}"'
set-hook -g window-unlinked 'run-shell "${script.tmux-undo}/bin/tmux-undo capture window #{hook_window} #{hook_session}"'
set-hook -g session-closed  'run-shell "${script.tmux-undo}/bin/tmux-undo capture session #{hook_session}"'
```

Hook arguments pass tmux-provided IDs so the script can query surviving state.

### Dedup ("outermost wins")

When a cascade occurs (`Ctrl+D` in last pane of last window of a session), all three hooks fire. Dedup rules, evaluated per capture invocation:

1. **session capture** writes a `_dedup_<session_id>` marker with 2-second TTL, then writes the session entry by assembling data from the shadow index and pending pane-level scratch entries (see "Capture Timing" below).
2. **window capture** checks for `_dedup_<session_id>` matching the window's parent. If present and fresh, skip. Otherwise, write window entry; set `_dedup_<window_id>`.
3. **pane capture** checks for `_dedup_<window_id>` or `_dedup_<session_id>`. If either is present and fresh, skip. Otherwise, write pane entry.

### Capture Timing Challenge

`session-closed` and `window-unlinked` fire *after* the unit is gone — format strings like `#{session_windows}` or `#{window_layout}` no longer resolve against the dead unit. But `pane-died` fires while `#{pane_current_path}` is still valid for the dying pane, and it fires *before* any cascading window/session hook. Two-part mitigation:

1. **Pane scratch entries.** Every `pane-died` writes a scratch file `/tmp/lazytmux-undo/_scratch/<pane_id>` containing `pane_current_path`, parent `window_id`, parent `session_id`, and timestamp. Scratch entries are what later get promoted, aggregated, or discarded.
2. **Shadow index for structural data.** A shadow index `/tmp/lazytmux-undo/_live/<session_id>` mirrors each live session's window names, indices, and `window_layout` strings. Refreshed on structural-change hooks: `window-linked`, `window-unlinked`, `window-renamed`, `window-layout-changed`. Does **not** track cwds (those come from scratch entries).

Resolution order for each capture level:

- **pane capture**: read the scratch file for this `pane_id`, write a pane entry, delete the scratch file.
- **window capture**: gather all scratch files whose parent matches this window, read the shadow index for the window's layout, write a window entry, delete the scratches.
- **session capture**: gather all scratch files + shadow index for this session, write a session entry, delete scratches and shadow.

This shifts the cost from "query at close time" to "record at every pane death plus update on structural changes," which is cheap — the pattern mirrors existing `/tmp/claude-status/panes/*`.

### Stack Cap

After any capture, if the entry count exceeds 10, the oldest file (lowest timestamp) is deleted. Cap is defined as a constant at the top of the script.

## Restore Logic

### `pop` (prefix+u)

1. Find newest entry by timestamp across `/tmp/lazytmux-undo/*.{pane,window,session}`.
2. Dispatch to level-specific restore.
3. Delete the entry file on success.
4. `switch-client` / `select-window` / `select-pane` to the restored unit.

### Pane restore

- If the captured `window_id` still resolves to a live window: `split-window -t <window> -c <cwd> -{h|v if known}`. If `split_direction=none`, fall back to window-level restore logic (window no longer has room for a "split relative to the last sibling," since the sibling *was* the last thing).
- If the window is gone: promote to window-level restore (create a new single-pane window).

### Window restore

- If `session_name` resolves to a live session: `new-window -t <session> -n <name> -c <pane_0_cwd>`, then for each additional pane run `split-window` with that pane's cwd. Apply the captured `window_layout` via `select-layout <layout>`.
- If the session is gone: promote to session-level restore with just this one window.

### Session restore

1. `new-session -d -s <name>` with the first window's first pane's cwd.
2. For each remaining window: `new-window -t <name>`, split panes, apply layout.
3. `switch-client -t <name>`.

If a session with that name already exists (user recreated it manually before hitting undo), append a numeric suffix (`-restored`, `-restored-2`, …).

## Picker (prefix+U)

`display-popup -E -w 80% -h 60%` running:

```
fzf --ansi --preview '<script> preview {}' --preview-window=right:50%
```

Input lines (one per entry file), sorted newest first, format:

```
<level_icon>  <level>   <display_name>                       <relative_time>
󱂬            session   foo (3 windows)                      2m ago
            window   lazytmux:main                         5m ago
           pane     ~/src/lazytmux                        7m ago
```

Preview subcommand dumps the entry file's content formatted as a tree (session → windows → panes with cwds).

On Enter: read selection, dispatch to restore, delete entry.

### Picker Implementation Note

The existing session/window pickers use `choose-tree`, which does not work for heterogeneous mixed-level lists. The undo picker uses `fzf` + `display-popup` instead. Icon set is shared with the rest of lazytmux (sourced from `lib-icons.sh`).

## Data Flow Summary

```
┌─────────────────┐  pane-died / window-unlinked / session-closed
│  tmux hooks     │────────────────────────────────┐
└─────────────────┘                                ▼
┌───────────────────────────────────────────────────────────┐
│ tmux-undo capture <level>                                 │
│   - check dedup marker                                    │
│   - read shadow index (for session-closed)                │
│   - write /tmp/lazytmux-undo/<ts>.<level>                 │
│   - enforce cap of 10                                     │
└───────────────────────────────────────────────────────────┘

┌─────────────────┐  prefix+u / prefix+U
│  keybindings    │────────────────────────────────┐
└─────────────────┘                                ▼
┌───────────────────────────────────────────────────────────┐
│ tmux-undo pop | picker                                    │
│   - select entry (newest or fzf choice)                   │
│   - dispatch to restore_<level>                           │
│   - new-session / new-window / split-window as needed     │
│   - apply layout, cd panes                                │
│   - switch-client / select-window / select-pane           │
│   - delete entry file                                     │
└───────────────────────────────────────────────────────────┘
```

## Error Handling

- All capture/restore operations wrap tmux/file I/O in best-effort logic — a failed capture must never block tmux. Errors go to `/tmp/lazytmux-undo/_log` and are silent to the user.
- Restore failures (e.g., cwd no longer exists) fall back to `$HOME` and display a tmux message via `display-message`.
- Corrupt entry files (malformed `key=value`) are logged and skipped; the file is moved to `/tmp/lazytmux-undo/_corrupt/` for later inspection rather than deleted.

## Testing

No unit tests (consistent with the rest of lazytmux — it's a Nix-wrapped shell config). Manual test plan lives in the implementation plan and covers:

1. `Ctrl+D` in single-pane window → `prefix+u` restores window with correct name/cwd.
2. `Ctrl+D` in last pane of last window of a session → `prefix+u` restores full session.
3. `kill-pane` on one of two panes in a window → `prefix+u` restores the pane as a split in the same window.
4. `prefix+U` shows all three levels, newest first, with preview.
5. Capture during active work does not visibly lag tmux (hooks run `run-shell` async).
6. Stack cap: close 11 things, verify oldest is dropped.
7. Dedup: closing a session does not produce window/pane entries for its contents.
8. `shellcheck` clean on the new script.

## Module / File Changes

| Path | Change |
|---|---|
| `scripts/tmux-undo.sh` | **New** — capture + restore + picker subcommands. |
| `config/tmux.conf.nix` | Add `tmux-undo` to `scriptsWithIcons`, wire hooks, add `bind-key u` and `bind-key U`. |
| `scripts/lib-icons.sh` | Add icons for `session`, `window`, `pane` row types in the picker (if not already present). |
| `CLAUDE.md` | Append `tmux-undo` row to the "Script Roles" table. |

## Open Questions

None at time of approval. Assumptions documented above supersede earlier alternatives (single-undo-only, jq-based storage, `choose-tree` picker).
