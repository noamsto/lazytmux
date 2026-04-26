# Session Persistence (Resurrect/Continuum Replacement)

**Date:** 2026-04-26
**Status:** Draft
**Repo:** `~/Data/git/lazytmux`

## Overview

Replace `tmux-resurrect` and `tmux-continuum` with an embedded, opinionated persistence layer in lazytmux. Periodically snapshots the running tmux server (sessions, windows, panes, layouts, cwds, commands, scrollback) to compressed archives under `$XDG_DATA_HOME/lazytmux/persist/`. On tmux server start, restores the latest snapshot through a smart filter that drops stale, idle, and duplicate entries — avoiding the "every leftover split comes back" failure mode that erodes trust in resurrect.

## Motivation

- `tmux-resurrect` and `tmux-continuum` are unmaintained, slow, and fork a subprocess per pane during save (`tmux display-message` in loops where one `list-panes -a -F` would do).
- Auto-restore is dumb: stale sessions from weeks ago, idle plain-shell splits, and duplicates of currently-running sessions all come back, training the user to keep `@continuum-restore 'off'` and lose the feature.
- Resurrect's nvim "session" strategy is unreliable in practice and requires `vim-obsession` for any real per-buffer state — neither the user nor lazytmux currently uses it.
- lazytmux already ships its own scripts, status bar, pickers, and home-manager module — adding a tpm-style plugin scaffold for our own code is pure overhead.

## Scope and Non-Goals

**In scope:**
- Session/window/pane structure preservation (names, indices, `window_layout`).
- Per-pane `cwd` and last-foreground command (allow-list re-launch on restore).
- Per-pane scrollback contents (`capture-pane -pJ`).
- Periodic save via systemd user timer + immediate save on structural-change hooks.
- Smart restore filter (dedup vs running server, stale-session age, idle-shell drop).
- Home-manager options for all thresholds + an `off` mode for users who want only manual save/restore.
- Manual triggers: `prefix + Ctrl-s` (save now), `prefix + R` (interactive restore picker).
- Single-archive snapshots with rolling history (default 20).

**Out of scope:**
- Restoring running processes verbatim. Re-launch is best-effort against an allow-list (`nvim`, `htop`, `lazygit`, …); anything else gets a fresh shell in the saved cwd.
- nvim/vim per-buffer session restoration. Documented as a future spec requiring nvim-side cooperation (`mksession` hook).
- Migration from existing `~/.local/share/tmux/resurrect/` saves. Different schema, different goals; saves go stale weekly anyway.
- Cross-host snapshot portability. cwds are absolute paths; restoring on a different host is undefined behavior (file falls back to home dir if cwd is missing).
- Reuse of mechanics from `tmux-undo`. The two features overlap conceptually but operate on different timescales (transient vs durable) and have different filter rules. Future refactor candidate, not a v1 goal.

## Architecture

Two new scripts plus systemd units.

| File | Purpose |
|---|---|
| `scripts/tmux-persist.sh` | CLI with subcommands: `save`, `restore`, `list`, `prune`, `picker`. Pure snapshot/restore logic; no scheduling. |
| `scripts/lib-persist.sh` | Shared helpers: manifest read/write, archive pack/unpack, smart-filter predicates, command allow-list. |
| `modules/home-manager.nix` | New `programs.lazytmux.persist` options block; `systemd.user.timers.lazytmux-persist`; `systemd.user.services.lazytmux-persist`. |
| `config/tmux.conf.nix` | `set-hook` wiring for immediate save; keybindings; remove `@resurrect-*` / `@continuum-*` lines and both `run-shell` invocations. |

### Component Responsibilities

- **`tmux-persist save`** — one-shot. Reads the live server via batched `list-sessions`, `list-windows -a`, `list-panes -a` calls (3 forks for structure metadata, 1 `capture-pane` fork per pane for scrollback). Writes a compressed archive to `latest.tar.zst`, atomically swaps via `mv`, copies into `history/<epoch>.tar.zst`, prunes history above `historyLimit`. Throttled: skips if no structural diff since last save AND last save < `minSaveInterval` ago.
- **`tmux-persist restore [--auto|--interactive|--from <file>]`** — reads `latest.tar.zst` (or selected history entry), unpacks manifest + scrollbacks to `$XDG_RUNTIME_DIR/lazytmux-persist/restore-<pid>/`, applies the smart filter to produce a restore plan, then issues batched tmux commands (`new-session -d`, `new-window -t`, `split-window -t -c`, `select-layout`, scrollback paste). Idempotent: rerunning is a no-op once the running server matches.
- **`tmux-persist list`** — prints history entries with timestamp, session count, window count, pane count.
- **`tmux-persist prune`** — drops history below `historyLimit`; also runs at end of every save.
- **`tmux-persist picker`** — `display-popup` running `fzf` over `list` output; selecting an entry runs `restore --from <file> --interactive`.

### Shared Library (`lib-persist.sh`)

Functions follow the existing `REPLY` convention from `lib-icons.sh` / `lib-claude.sh` (set `REPLY` instead of echoing) to avoid subshell forks in hot paths.

- `read_live_server` → populates `MANIFEST_JSON` global with current server state via 3 batched `tmux` calls.
- `write_snapshot <out_path>` → packs manifest + scrollbacks into `tar.zst` (zstd at level 3; ~5x smaller than gzip with similar speed).
- `read_snapshot <archive>` → unpacks to a temp dir, sets `RESTORE_DIR`, validates `manifest.json` `version` field.
- `should_restore_session <session_name> <last_attached_epoch>` → applies dedup + stale-session filter.
- `should_restore_pane <command> <child_count> <last_used_epoch>` → applies idle-shell + per-pane staleness filter.
- `relaunch_command_for <command>` → checks allow-list; returns the command to send to the new pane, or empty (= leave shell).

## Storage Layout

```
$XDG_DATA_HOME/lazytmux/persist/
  latest.tar.zst              — newest snapshot (atomic write target)
  latest.tar.zst.tmp          — staging file for atomic mv
  history/
    <epoch>.tar.zst           — rolling history, capped at historyLimit (20)
  log                         — best-effort save/restore log (last 1000 lines)
```

`$XDG_RUNTIME_DIR/lazytmux-persist/` (transient, cleared on reboot):

```
restore-<pid>/                — unpacked snapshot during restore
save-lock                     — flock(1) advisory lock for concurrent save attempts
last-fingerprint              — sha256 of manifest sans timestamps; used for "no change since last save" check
```

Archive contents (inside `latest.tar.zst`):

```
manifest.json
panes/<session>__<window_idx>__<pane_idx>.scrollback
```

Pane filenames use `__` as separator (no path traversal risk; tmux session names allow `/` but it's stripped to `_` for the filename).

## Manifest Schema

Single JSON file, schema versioned. Tolerant restore: unknown fields ignored, version mismatch logged and snapshot skipped.

```json
{
  "version": 1,
  "saved_at": 1745700000,
  "host": "thinkpad-p14s-g6",
  "tmux_server_pid": 12345,
  "sessions": [
    {
      "name": "lazytmux",
      "last_attached": 1745699940,
      "windows": [
        {
          "index": 1,
          "name": "main",
          "layout": "abcd,200x50,0,0,1",
          "panes": [
            {
              "index": 1,
              "cwd": "/home/noams/Data/git/lazytmux",
              "command": "nvim",
              "command_args": ["scripts/tmux-persist.sh"],
              "last_used": 1745699940,
              "child_count": 2,
              "scrollback_file": "panes/lazytmux__1__1.scrollback"
            }
          ]
        }
      ]
    }
  ]
}
```

`command_args` is best-effort: parsed from `pane_current_command` plus `/proc/<pid>/cmdline` of the foreground process when available; empty array when not. Used only for re-launch of allow-listed commands.

## Save Logic

### Triggers

1. **systemd user timer** (`lazytmux-persist.timer`) — `OnBootSec=2min`, `OnUnitActiveSec=60s`. Drives the periodic-save baseline.
2. **tmux hooks** — immediate save on structural change. Throttle inside the script (skip if last save < `minSaveInterval` seconds ago).

```tmux
set-hook -g session-created    'run-shell -b "${script.tmux-persist}/bin/tmux-persist save --reason hook:session-created"'
set-hook -g window-linked      'run-shell -b "${script.tmux-persist}/bin/tmux-persist save --reason hook:window-linked"'
set-hook -g window-unlinked    'run-shell -b "${script.tmux-persist}/bin/tmux-persist save --reason hook:window-unlinked"'
set-hook -g client-detached    'run-shell -b "${script.tmux-persist}/bin/tmux-persist save --reason hook:client-detached"'
```

`-b` (background) so hooks return immediately. `--reason` is for the log only.

### Save Algorithm

1. `flock` on `save-lock`. If held, exit 0 (concurrent save in progress).
2. Read live server into `MANIFEST_JSON` (3 batched tmux calls).
3. Compute `fingerprint` = sha256 of manifest with `saved_at` / `last_used` / `last_attached` zeroed out.
4. If `fingerprint == cat last-fingerprint` AND `now - mtime(latest.tar.zst) < minSaveInterval`: exit 0.
5. For each pane: `tmux capture-pane -pJ -t <pane> -S -` → `panes/<key>.scrollback`. (One fork per pane; the unavoidable cost.)
6. Pack manifest + scrollbacks into `latest.tar.zst.tmp`, then `mv` to `latest.tar.zst` (atomic on same filesystem).
7. Copy to `history/<epoch>.tar.zst`. Prune history above `historyLimit`.
8. Write new `last-fingerprint`. Append one line to `log`.

### Throttling

- `minSaveInterval` (default 30s) prevents storms when many hooks fire close together (e.g., opening 5 windows with a script).
- The fingerprint check makes redundant saves free in CPU but still cheap in IO (no archive write).

## Restore Logic

### Triggers

1. **Auto on tmux start** — a single `run-shell -b "tmux-persist restore --auto"` invocation in `tmux.conf.nix`, after all session/window setup. The script no-ops if any sessions already exist matching snapshot session names.
2. **Manual interactive** — `prefix + R` runs `tmux-persist picker`. User selects a snapshot from history; each session/window/pane is presented as a checklist with the smart-filter result pre-applied (toggleable).
3. **Manual targeted** — `tmux-persist restore --from <file>` for shell use; not bound to a key.

### Smart Filter

Applied to every snapshot session, window, and pane before any tmux commands run.

| Filter | Default | Rule |
|---|---|---|
| `dedupRunningServer` | on | Skip session if a session with that name is already on the running server. |
| `restoreMaxSessionAge` | 14d | Skip session if `now - last_attached > 14d`. |
| `restoreMaxSnapshotAge` | 30d | Skip *whole snapshot* if `now - saved_at > 30d` (host probably reinstalled). |
| `restoreSkipIdleShells` | true | Skip pane if `command ∈ {bash, fish, zsh, sh}` AND `child_count == 0`. |
| `restoreSkipIdleWindows` | true | Skip window if every pane was filtered out by idle-shell rule (full window assumed leftover). |

Restore mode (`programs.lazytmux.persist.restoreMode`):

- `"auto"` (default) — apply filter, restore the rest silently.
- `"interactive"` — present picker on tmux start, never auto-restore.
- `"off"` — disable auto-restore entirely; manual `prefix + R` only.

### Restore Algorithm

1. `read_snapshot latest.tar.zst` → unpacks to `restore-<pid>/`.
2. Build restore plan: walk manifest, apply smart filter, produce a list of (session_create | window_create | pane_split | scrollback_paste) actions.
3. Execute plan in dependency order:
   - For each surviving session not on running server: `new-session -d -s <name>` with first window's first pane's cwd.
   - For each surviving window: `new-window -t <session> -n <name>` (or reuse window 1 from `new-session`).
   - For each surviving pane after the first in a window: `split-window -t <session>:<win_idx> -c <cwd>`.
   - Apply `select-layout <layout>` to each window.
   - For each surviving pane with allow-listed command: `send-keys -t <pane> "<command> <args>" Enter`.
   - For each surviving pane with scrollback file: write to a tmux buffer, paste into pane (`load-buffer` + `paste-buffer`), then clear buffer. Pasted scrollback goes to the *visible* pane content; copy mode shows it as history.
4. `rm -rf restore-<pid>/`.

### Scrollback Restore Limitation

Pasting scrollback into a fresh shell produces a visual approximation, not a true tmux history-buffer restore. Scrollback shows up via copy mode (`prefix + [`) because `paste-buffer` writes to the pane's history buffer. The shell prompt that was running before save is gone; what shows above it is the previous session's output, then the new shell prompt at the bottom. This matches resurrect's behavior with `@resurrect-capture-pane-contents 'on'`.

## Command Allow-List

Re-launch on restore is gated by an allow-list to avoid running arbitrary user history (security + sanity). Default list:

```nix
commandAllowList = [
  "nvim" "vim" "vi"
  "htop" "btop" "top"
  "less" "more" "tail" "head" "watch"
  "lazygit" "lazydocker"
  "k9s" "kubectl"
  "ssh" "mosh"
];
```

Configurable via `programs.lazytmux.persist.commandAllowList`. Anything not on the list restores as a fresh shell in the saved cwd.

## Home-Manager Options

Under `programs.lazytmux.persist`:

```nix
{
  enable = mkEnableOption "session persistence" // { default = true; };
  saveInterval = mkOption { type = types.int; default = 60; description = "Seconds between periodic saves."; };
  minSaveInterval = mkOption { type = types.int; default = 30; description = "Minimum seconds between any two saves (throttle for hooks)."; };
  historyLimit = mkOption { type = types.int; default = 20; description = "Number of historical snapshots to keep."; };
  restoreMode = mkOption { type = types.enum [ "auto" "interactive" "off" ]; default = "auto"; };
  restoreMaxSessionAge = mkOption { type = types.int; default = 14 * 24 * 3600; description = "Drop session if last_attached older than this (seconds)."; };
  restoreMaxSnapshotAge = mkOption { type = types.int; default = 30 * 24 * 3600; description = "Skip whole snapshot if older than this (seconds)."; };
  restoreSkipIdleShells = mkOption { type = types.bool; default = true; };
  restoreSkipIdleWindows = mkOption { type = types.bool; default = true; };
  dedupRunningServer = mkOption { type = types.bool; default = true; };
  commandAllowList = mkOption { type = types.listOf types.str; default = [ /* see above */ ]; };
  captureScrollback = mkOption { type = types.bool; default = true; };
}
```

Per-pane override (set via tmux user options):

- `set -p @persist-skip-scrollback on` — opt out of scrollback capture for a noisy pane.
- `set -p @persist-skip on` — exclude this pane from the snapshot entirely.

## Removal of Resurrect/Continuum

In `config/tmux.conf.nix`:

- Remove lines 171-177 (`@resurrect-strategy-vim`, `@resurrect-strategy-nvim`, `@resurrect-capture-pane-contents`, `@continuum-restore`, `@continuum-save-interval`).
- Remove `run-shell ${tmuxPlugins.resurrect}/...` and `run-shell ${tmuxPlugins.continuum}/...` lines.
- Remove the `#(${tmuxPlugins.continuum}/share/tmux-plugins/continuum/scripts/continuum_save.sh)` invocation from `status-format[0]`.

In `modules/home-manager.nix`:

- Update the comment at line 316 (`tmux-continuum's auto-restore` → `lazytmux-persist's auto-restore`).
- Add the systemd timer + service blocks.

Existing `~/.local/share/tmux/resurrect/` saves are not read. Removed in same commit; user can manually delete the directory if desired.

## Survival Across Nix Rebuilds

- **Systemd units** — home-manager regenerates unit files in `~/.config/systemd/user/` and runs `systemctl --user daemon-reload`. New units point at the new `/nix/store/.../tmux-persist`. In-flight `oneshot` saves killed mid-rebuild lose at most one tick (~60s).
- **Tmux hooks** — bound to old store path until `tmux source-file` runs in the activation script (already present). `set-hook -g` replaces by hook name on reload, so no stacking.
- **Snapshots** — under `$XDG_DATA_HOME/lazytmux/persist/`, outside `/nix/store`. Survive rebuilds, generation rollbacks, and `nix-collect-garbage`.
- **Schema drift** — `version` field + tolerant restore. A future schema bump skips old snapshots with one log line; rollback to older lazytmux skips newer snapshots the same way. No crashes.

## Testing

Per project convention there are no unit tests. Manual verification checklist:

1. **Save roundtrip** — open 2 sessions × 3 windows × 2 panes mix of shells and `nvim`; trigger `prefix + Ctrl-s`; verify `latest.tar.zst` exists, `tar -tf` lists expected files.
2. **Manifest correctness** — `zstd -d < latest.tar.zst | tar -xO manifest.json | jq` shows full structure with cwds, commands, layouts.
3. **Auto restore** — `tmux kill-server`; start new tmux; verify all non-idle sessions/windows/panes restored with correct cwds and `nvim` re-launched.
4. **Idle-shell filter** — open a session with one `nvim` pane and one bare `bash` pane (no children); restore in fresh server; verify only `nvim` pane appears.
5. **Dedup** — with one session running, restore again; verify no duplicates and no errors.
6. **Stale session** — manually edit a snapshot's `last_attached` to >14 days ago; restore; verify the session is skipped.
7. **Stale snapshot** — edit `saved_at` to >30 days ago; restore; verify whole snapshot is skipped.
8. **Scrollback** — verify `prefix + [` in a restored pane shows pre-save output above the new shell prompt.
9. **Interactive picker** — `prefix + R`; verify history list, selection, multi-select toggle, smart-filter pre-applied.
10. **Throttling** — open 10 windows in quick succession; verify no more than `ceil(elapsed / minSaveInterval)` archive writes.
11. **Rebuild survival** — `home-manager switch`; verify timer still ticking, hooks still firing, restore still works.
12. **Generation rollback** — `home-manager switch --rollback`; verify older lazytmux still operates on existing snapshots (or skips with version-mismatch log).
13. **`nix flake check`** — passes shellcheck/shfmt.

## Migration & Rollout

Single PR, single commit. No phased rollout.

- One-line release note: "Replaces tmux-resurrect/continuum with embedded persistence; existing resurrect saves are not migrated."
- User runs `home-manager switch`; activation script reloads tmux config; new save runs on first systemd-timer tick (~2 min after boot or service start).

## Future Work (Not In This Spec)

- nvim cooperation: companion Lua module that writes `mksession` files on `VimLeave` and restores on launch. Requires user to opt in via nvim config.
- Cross-host portability: cwd remapping rules (`/home/old/path` → `/home/new/path`) for moving snapshots between machines.
- Unifying `tmux-undo` and `tmux-persist` into a single capture pipeline. Both record session/window/pane structure; current divergence is justified by filter rules and persistence model, but the parsing logic could be shared.
- Pane content compression options: capture only last N lines (`-S -<N>`) to bound archive size.
