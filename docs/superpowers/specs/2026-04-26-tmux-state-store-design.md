# Tmux State Store: Unified Persistence and Undo

**Date:** 2026-04-26
**Status:** Draft
**Repo:** `~/Data/git/lazytmux`

**Supersedes:**
- `2026-04-19-undo-closed-windows-design.md` (close-event undo, bash, `/tmp` storage)
- `2026-04-26-session-persistence-design.md` (resurrect/continuum replacement, bash, tar.zst storage)
- `2026-04-19-undo-closed-windows.md` (implementation plan)

## Overview

A single Go binary, `tmux-state`, replaces `tmux-resurrect`, `tmux-continuum`, and the previously-spec'd `tmux-undo` feature. It records tmux server activity to a SQLite event store with content-addressed scrollback files, exposing two complementary user-facing flows:

1. **Persistence** — periodic snapshots (every 60s + on structural-change hooks) restored automatically on tmux server start, through a smart filter that drops stale, idle, and duplicate entries.
2. **Undo** — close-event captures (`pane-died`, `window-unlinked`, `session-closed`) restored on demand via `prefix + u` (pop newest) or `prefix + U` (picker).

Both flows share one event store, one filter library, one set of tmux interaction primitives, and one binary.

## Motivation

- **Resurrect/continuum are unmaintained, slow, and dumb on restore.** Forking a subprocess per pane during save (where one batched call would do), restoring stale sessions and idle splits unconditionally, and offering no integration with the rest of the tmux config. Auto-restore is so noisy that users (including us) keep `@continuum-restore 'off'`.
- **Undo and persistence share ~70% of their mechanics.** Both record session/window/pane structure with cwd, command, layout, and (optionally) scrollback. Both restore via tmux command sequences. Implementing them as two separate bash scripts would mean writing the same primitives twice, then refactoring to share them anyway.
- **Bash + SQLite is awkward.** Once SQLite enters the picture (necessary for shared event-store semantics, fast picker queries, clean pruning, and forward-compatible schema migrations), Go becomes the obvious implementation language: real prepared statements, typed manifests, parallel `tmux capture-pane`, table-driven tests. The lazytmux dev shell already pulls in `pkgs.go`/`gopls`/`gotools`.
- **Undo currently lives in `/tmp`** (per the prior spec). A unified durable store means undo also survives reboots — you can recover a pane closed yesterday after a host restart.

## Scope and Non-Goals

**In scope:**
- Single Go binary `tmux-state` with subcommands: `save`, `capture-event`, `restore`, `undo`, `pick`, `list`, `prune`, `gc`, `version`.
- Periodic snapshot via systemd user timer + immediate snapshot on structural-change hooks.
- Close-event capture via `pane-died`, `window-unlinked`, `session-closed` hooks with "outermost wins" dedup.
- SQLite event store at `$XDG_DATA_HOME/lazytmux/state.db`.
- Content-addressed scrollback files at `$XDG_DATA_HOME/lazytmux/scrollbacks/<sha256>.zst` with refcount-based GC.
- Smart restore filter (dedup vs running server, stale-session age, idle-shell drop, idle-window drop, stale-snapshot age).
- Three restore modes: `auto`, `interactive`, `off`.
- Keybindings: `prefix + u` (undo pop), `prefix + U` (undo picker), `prefix + R` (snapshot picker), `prefix + Ctrl-s` (save now).
- Allow-list-gated command re-launch on restore (`nvim`, `htop`, `lazygit`, …).
- Home-manager options for all thresholds and modes.
- Removal of `tmux-resurrect` / `tmux-continuum` plugin loads and configuration from `config/tmux.conf.nix`.
- Test suite via `go test ./...`, including parser tests, filter tests, scrollback dedup/refcount tests, and an integration test that drives a real tmux server.

**Out of scope:**
- nvim/vim per-buffer session restoration (requires nvim-side `mksession` cooperation; future spec).
- Cross-host snapshot portability (cwds are absolute paths). Syncthing on the data dir is the user-level workaround, not a feature.
- Migration from existing `~/.local/share/tmux/resurrect/` saves. Different schema, stale within a week anyway.
- Cloud sync / remote SQLite. Wrong threat model — tmux scrollback contains paths and pasted secrets.
- Restoring arbitrary running processes. Re-launch is best-effort against the configured allow-list.
- Backwards compatibility with the prior bash-based tmux-undo design. Nothing was implemented.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ tmux server                                                 │
│  │                                                          │
│  ├─[hook] session-created/window-linked/window-unlinked/    │
│  │       client-detached                                    │
│  │     → tmux-state save --reason=hook:<name>               │
│  │                                                          │
│  ├─[hook] pane-died/window-unlinked/session-closed          │
│  │     → tmux-state capture-event <kind>                    │
│  │                                                          │
│  ├─[keybinding] prefix+u → tmux-state undo --pop            │
│  ├─[keybinding] prefix+U → tmux-state pick --kind=close     │
│  ├─[keybinding] prefix+R → tmux-state pick --kind=snapshot  │
│  └─[keybinding] prefix+Ctrl-s → tmux-state save --reason=key│
│                                                             │
│ systemd user timer (60s)                                    │
│  └─ tmux-state save --reason=timer                          │
│                                                             │
│ tmux-state binary                                           │
│  └─ SQLite DB ($XDG_DATA_HOME/lazytmux/state.db)            │
│  └─ Scrollback store (CAS, $XDG_DATA_HOME/lazytmux/scrollbacks/) │
└─────────────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | Purpose |
|---|---|
| `cmd/tmux-state/main.go` | CLI entry. Subcommand dispatch, flag parsing. |
| `internal/store` | SQLite access. Migrations, prepared statements, transactions. |
| `internal/snapshot` | Read live tmux server, build a manifest, persist as a `snapshot` event. |
| `internal/closeevent` | Handle pane/window/session close hooks with cascade dedup. |
| `internal/restore` | Read events, apply filter, plan tmux commands, execute. |
| `internal/filter` | Smart-filter predicates (dedup, idle, stale). Pure functions, easy to test. |
| `internal/scrollback` | Content-addressed compressed scrollback files. Hash, refcount, GC. |
| `internal/tmux` | Wraps `exec.Command("tmux", ...)`. Output parsing, safe argument passing. |
| `internal/picker` | `fzf` invocation and result formatting for undo/snapshot pickers. |
| `internal/config` | Config loading (env vars, CLI flags). |
| `internal/log` | Structured logging via `log/slog`. |

### Single-Writer Discipline

All write paths acquire a process-level `flock` on `$XDG_RUNTIME_DIR/lazytmux/write.lock` before opening the DB. SQLite is configured with WAL mode (`PRAGMA journal_mode=WAL`) for non-blocking reads while a write is in progress. Every write transaction uses `BEGIN IMMEDIATE` to avoid deadlocks under contention. Reads (e.g., during a picker session) do not require the lock.

The flock is *additional* protection on top of SQLite's own locking — it serializes operations that span multiple SQL statements and filesystem changes (e.g., scrollback file writes + DB inserts must succeed together).

## Storage Layout

```
$XDG_DATA_HOME/lazytmux/
  state.db                         — SQLite database
  state.db-wal                     — WAL file (auto-managed)
  state.db-shm                     — shared memory file (auto-managed)
  scrollbacks/
    <sha256[:2]>/<sha256>.zst     — sharded by first byte for filesystem sanity
  log                              — best-effort log, rotated at 1MB

$XDG_RUNTIME_DIR/lazytmux/         — transient, cleared on reboot
  write.lock                       — flock(1) advisory lock
  restore-<pid>/                   — temp dir for in-flight restore
```

Sharding scrollbacks by `<sha256[:2]>` (first 2 hex chars = 256 buckets) keeps any single directory bounded even with thousands of unique scrollbacks accumulated over months.

## Database Schema

Schema versioned via `PRAGMA user_version`. Migrations applied at startup if `user_version < latest`.

```sql
CREATE TABLE events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    ts              INTEGER NOT NULL,                -- ms epoch
    kind            TEXT    NOT NULL,                -- 'snapshot' | 'pane-died' | 'window-unlinked' | 'session-closed'
    scope           TEXT    NOT NULL,                -- 'server' | 'session' | 'window' | 'pane'
    reason          TEXT,                            -- e.g. 'timer', 'hook:session-created', 'keybinding'
    host            TEXT    NOT NULL,                -- hostname; helps if user ever syncs
    parent_event_id INTEGER REFERENCES events(id) ON DELETE SET NULL,
    manifest_json   TEXT    NOT NULL                 -- JSON; full session/window/pane tree
) STRICT;

CREATE INDEX events_kind_ts ON events(kind, ts DESC);
CREATE INDEX events_ts      ON events(ts DESC);

CREATE TABLE scrollbacks (
    sha256        TEXT PRIMARY KEY,                  -- 64 hex chars
    bytes         INTEGER NOT NULL,                  -- compressed size on disk
    refcount      INTEGER NOT NULL DEFAULT 0,
    last_used_ts  INTEGER NOT NULL                   -- updated on insert + ref
) STRICT;

CREATE TABLE event_scrollbacks (
    event_id        INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    pane_key        TEXT    NOT NULL,                -- '<session>:<window_idx>:<pane_idx>'
    scrollback_sha  TEXT    NOT NULL REFERENCES scrollbacks(sha256),
    PRIMARY KEY (event_id, pane_key)
) STRICT;

CREATE INDEX event_scrollbacks_sha ON event_scrollbacks(scrollback_sha);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
-- meta keys: schema_version, last_save_fingerprint, last_save_ts
```

### Manifest JSON

Stored as `events.manifest_json`. Per-event content depends on `kind`:

**`kind = 'snapshot'`** — full server state.

```json
{
  "v": 1,
  "host": "thinkpad-p14s-g6",
  "tmux_pid": 12345,
  "saved_at": 1745700000,
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
              "command_args": ["scripts/tmux-state-store.go"],
              "last_used": 1745699940,
              "child_count": 2,
              "scrollback_sha": "abc123..."
            }
          ]
        }
      ]
    }
  ]
}
```

**`kind = 'pane-died' | 'window-unlinked' | 'session-closed'`** — only the dying unit's state.

```json
{
  "v": 1,
  "host": "...",
  "session": {
    "name": "lazytmux",
    "windows": [
      { /* only the affected window or windows, with affected pane(s) */ }
    ]
  },
  "split_direction": "h"   // pane-died only; relative to surviving sibling, or "none"
}
```

Tolerant restore: unknown fields ignored; `v` mismatch logs a warning and skips the event.

### Cascade Dedup for Close Events

When `Ctrl+D` in the last pane of the last window of a session cascades, all three hooks fire in order. Dedup rules (mirroring the prior undo spec, but expressed as DB queries):

1. **`session-closed`** writes a session-level event row, with `manifest_json` containing all of that session's windows/panes assembled from a shadow index updated on structural-change hooks.
2. **`window-unlinked`** checks for a fresh (`ts > now() - 2000` ms) `session-closed` event whose manifest references this window's `session_id`. If present, skip.
3. **`pane-died`** checks for a fresh `session-closed` or `window-unlinked` event whose manifest references this pane's parent. If present, skip.

The shadow index lives in a small in-memory cache that the `capture-event` subcommand updates from a sidecar table:

```sql
CREATE TABLE live_index (
    session_id  TEXT NOT NULL,                       -- tmux $N
    payload     TEXT NOT NULL,                       -- JSON: windows, panes, layouts, cwds
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (session_id)
) STRICT;
```

Refreshed on `window-linked`, `window-unlinked`, `window-renamed`, `window-layout-changed`, `pane-died` (before the dying pane disappears from `list-panes`). Cleared for closed sessions after capture.

This shifts cost from "query at close time" to "record incrementally," matching the existing `/tmp/claude-status/panes/*` pattern in lazytmux.

## Save Logic (Periodic Snapshots)

### Trigger Sources

| Source | Mechanism | Throttle |
|---|---|---|
| Periodic | systemd user timer (`OnUnitActiveSec=60s`, `OnBootSec=2min`) → `tmux-state save --reason=timer` | none — timer drives baseline |
| Structural change | tmux hooks: `session-created`, `window-linked`, `client-detached` → `run-shell -b "tmux-state save --reason=hook:<name>"` | `min_save_interval` (default 30s) |
| Manual | `prefix + Ctrl-s` → `tmux-state save --reason=keybinding` | bypasses throttle |

The systemd timer drives the baseline; hooks accelerate save *toward* a baseline it would have eventually reached. Throttling is by `last_save_ts` in the `meta` table.

### Save Algorithm

1. Acquire `flock` on `write.lock`. If held: exit 0.
2. Read `last_save_ts` and `last_save_fingerprint` from `meta`.
3. Read live server: 3 batched `tmux` calls (`list-sessions -F`, `list-windows -a -F`, `list-panes -a -F`). Parse into `Manifest{Sessions[]}`.
4. Compute `fingerprint` = sha256 of canonical manifest with timestamps zeroed.
5. If `fingerprint == last_save_fingerprint` AND throttle applies AND `--reason != keybinding`: exit 0.
6. **Parallel scrollback capture** — goroutine pool of N workers (default `runtime.NumCPU()`), each running `tmux capture-pane -pJ -t <pane> -S -`. For each pane: hash content, write `scrollbacks/<sha[:2]>/<sha>.zst` if not present (insert into `scrollbacks` table with `refcount=0` if new), record sha in manifest.
7. Open SQLite tx (`BEGIN IMMEDIATE`):
   - Insert `events` row with `kind='snapshot'`, `manifest_json`.
   - For each pane scrollback: insert `event_scrollbacks` row + `UPDATE scrollbacks SET refcount = refcount + 1, last_used_ts = ? WHERE sha256 = ?`.
   - Update `meta` `last_save_ts`, `last_save_fingerprint`.
   - Commit.
8. Release flock.

Errors at step 7 leave orphan scrollback files (uploaded but not referenced); a periodic `gc` subcommand reaps these (see GC section).

### Pruning

After every save (post-commit), delete events older than the configured retention:

```sql
DELETE FROM events
WHERE kind = 'snapshot'
  AND id NOT IN (
      SELECT id FROM events
      WHERE kind = 'snapshot'
      ORDER BY ts DESC
      LIMIT ?      -- snapshot_history_limit (default 20)
  );

DELETE FROM events
WHERE kind != 'snapshot'
  AND id NOT IN (
      SELECT id FROM events
      WHERE kind != 'snapshot'
      ORDER BY ts DESC
      LIMIT ?      -- close_event_limit (default 50)
  );
```

`event_scrollbacks` rows cascade-delete via FK. Decrement `refcount` is handled by an `AFTER DELETE` trigger on `event_scrollbacks`:

```sql
CREATE TRIGGER decrement_scrollback_refcount
AFTER DELETE ON event_scrollbacks
BEGIN
    UPDATE scrollbacks
    SET refcount = refcount - 1
    WHERE sha256 = OLD.scrollback_sha;
END;
```

The orphaned scrollback files (refcount = 0) are reaped by `gc` (see below).

## Capture Logic (Close Events)

Hooks wired in `config/tmux.conf.nix`:

```tmux
set-hook -g pane-died          'run-shell -b "${tmux-state}/bin/tmux-state capture-event pane-died          --pane=#{hook_pane} --window=#{hook_window} --session=#{hook_session}"'
set-hook -g window-unlinked    'run-shell -b "${tmux-state}/bin/tmux-state capture-event window-unlinked    --window=#{hook_window} --session=#{hook_session}"'
set-hook -g session-closed     'run-shell -b "${tmux-state}/bin/tmux-state capture-event session-closed     --session=#{hook_session}"'

# live index maintenance
set-hook -g window-linked         'run-shell -b "${tmux-state}/bin/tmux-state index-update --session=#{hook_session}"'
set-hook -g window-renamed        'run-shell -b "${tmux-state}/bin/tmux-state index-update --session=#{hook_session}"'
set-hook -g window-layout-changed 'run-shell -b "${tmux-state}/bin/tmux-state index-update --session=#{hook_session}"'
```

`capture-event` reads from `live_index` (since the unit is gone or going), assembles a manifest, applies cascade dedup, and inserts an event row. No scrollback capture for close events — the pane process is dead, and `tmux capture-pane` against a missing pane fails. (We could capture scrollback eagerly in `index-update`, but every-event scrollback writes are too expensive for marginal value. Persistence covers the "I want my scrollback back" use case.)

## Restore Logic

### Auto-Restore on tmux Start

`config/tmux.conf.nix` adds, after all session/window setup:

```tmux
run-shell -b '${tmux-state}/bin/tmux-state restore --auto'
```

`restore --auto` behavior:
1. If `restore_mode == "off"`: exit 0.
2. Read most recent `kind='snapshot'` event.
3. If `now - event.ts > restore_max_snapshot_age`: log "snapshot too old, skipping" and exit 0.
4. Apply smart filter to manifest. Produce a restore plan.
5. Execute plan via `tmux` commands (see "Restore Plan Execution" below).

### `prefix + u` (Undo Pop)

`tmux-state undo --pop`:
1. Read most recent close event (`kind != 'snapshot'`).
2. Restore its manifest unconditionally (no smart filter — user explicitly asked).
3. Delete the event row on success.
4. `switch-client` / `select-window` / `select-pane` to the restored unit.

### `prefix + U` (Undo Picker) and `prefix + R` (Snapshot Picker)

Both run `tmux-state pick --kind=<close|snapshot>`, which opens a `display-popup` running `fzf` over event rows. Each row is rendered with `id`, `ts` (humanized), summary (e.g., `"session 'lazytmux' (3 windows, 7 panes)"`).

For snapshots, the smart filter is applied and the result is shown as a checklist (toggleable per session/window/pane). For close events, the user just selects an event to restore — no filter.

### Smart Filter

Configurable thresholds (defaults shown). All applied at restore time, not save time:

| Filter | Default | Applies to | Rule |
|---|---|---|---|
| `dedup_running_server` | true | session | Skip if a session with that name already exists. |
| `restore_max_session_age` | 14d | session | Skip if `now - last_attached > threshold`. |
| `restore_max_snapshot_age` | 30d | snapshot | Skip whole snapshot if older. |
| `restore_skip_idle_shells` | true | pane | Skip pane if `command ∈ {bash,fish,zsh,sh}` AND `child_count == 0`. |
| `restore_skip_idle_windows` | true | window | Skip window if every pane filtered out. |

### Restore Plan Execution

Plan is a slice of typed actions:

```go
type Action interface{ Execute(*tmux.Client) error }
type CreateSession struct{ Name, Cwd string }
type CreateWindow struct{ Session, Name string; Index int; Cwd string }
type SplitPane struct{ Target, Cwd string }
type SetLayout struct{ Window, Layout string }
type RelaunchCommand struct{ Pane, Command string; Args []string }
type RestoreScrollback struct{ Pane, Sha256 string }
```

Executed in dependency order. Errors logged but do not abort the rest of the plan — we always make best effort to restore as much as possible.

`RestoreScrollback` writes the decompressed scrollback to a tmux buffer (`load-buffer`) and pastes it into the pane (`paste-buffer -t <pane>`). The pasted content shows up in copy mode (`prefix + [`), matching resurrect's behavior. The shell prompt shows below the restored history.

## Command Allow-List

```go
var DefaultAllowList = []string{
    "nvim", "vim", "vi",
    "htop", "btop", "top",
    "less", "more", "tail", "head", "watch",
    "lazygit", "lazydocker",
    "k9s", "kubectl",
    "ssh", "mosh",
}
```

Configurable via home-manager `programs.lazytmux.persist.commandAllowList`. Anything not on the list restores as a fresh shell in the saved cwd.

## GC (Scrollback Cleanup)

Subcommand `tmux-state gc`. Run weekly via systemd timer.

1. Acquire flock.
2. `SELECT sha256, bytes FROM scrollbacks WHERE refcount <= 0`.
3. For each sha: `unlink` the file at `scrollbacks/<sha[:2]>/<sha>.zst`, then `DELETE FROM scrollbacks WHERE sha256 = ?`.
4. As a safety net: walk `scrollbacks/` directory; for any file *not* present in the `scrollbacks` table, delete it. (Catches files orphaned by mid-write crashes.)
5. `VACUUM` if DB size > 50MB.
6. Release flock.

## Configuration

### Home-Manager Options (`programs.lazytmux.persist`)

```nix
{
  enable = mkEnableOption "tmux state store" // { default = true; };

  # Save behavior
  saveInterval        = mkOption { type = types.int;  default = 60; };
  minSaveInterval     = mkOption { type = types.int;  default = 30; };
  snapshotHistoryLimit = mkOption { type = types.int; default = 20; };
  closeEventLimit     = mkOption { type = types.int;  default = 50; };
  captureScrollback   = mkOption { type = types.bool; default = true; };

  # Restore behavior
  restoreMode = mkOption { type = types.enum [ "auto" "interactive" "off" ]; default = "auto"; };
  restoreMaxSessionAge   = mkOption { type = types.int;  default = 14 * 24 * 3600; };
  restoreMaxSnapshotAge  = mkOption { type = types.int;  default = 30 * 24 * 3600; };
  restoreSkipIdleShells  = mkOption { type = types.bool; default = true; };
  restoreSkipIdleWindows = mkOption { type = types.bool; default = true; };
  dedupRunningServer     = mkOption { type = types.bool; default = true; };

  # Allow-list
  commandAllowList = mkOption {
    type = types.listOf types.str;
    default = [ "nvim" "vim" "vi" "htop" "btop" "top" "less" "more" "tail" "head" "watch" "lazygit" "lazydocker" "k9s" "kubectl" "ssh" "mosh" ];
  };

  # GC
  gcInterval = mkOption { type = types.str; default = "weekly"; };
}
```

### Per-Pane Tmux User Options

- `set -p @persist-skip on` — exclude pane from snapshots and close-event captures.
- `set -p @persist-skip-scrollback on` — capture metadata only, no scrollback bytes for this pane.

### CLI Flags

All home-manager options are mirrored as CLI flags on `tmux-state` for ad-hoc use. The systemd unit reads from `$XDG_CONFIG_HOME/lazytmux/state.toml` (generated by home-manager) for default values; CLI flags override.

## Tmux Integration

### config/tmux.conf.nix Changes

**Remove:**

```tmux
set -g @resurrect-strategy-vim 'session'
set -g @resurrect-strategy-nvim 'session'
set -g @resurrect-capture-pane-contents 'on'
set -g @continuum-restore 'on'
set -g @continuum-save-interval '10'
```

```tmux
run-shell ${tmuxPlugins.resurrect}/share/tmux-plugins/resurrect/resurrect.tmux
run-shell ${tmuxPlugins.continuum}/share/tmux-plugins/continuum/continuum.tmux
```

```
#(${tmuxPlugins.continuum}/share/tmux-plugins/continuum/scripts/continuum_save.sh)
```

(in `status-format[0]`).

**Add:**

```tmux
# tmux-state hooks (set-hook is replace-by-name on reload, no stacking)
set-hook -g session-created       'run-shell -b "${tmux-state}/bin/tmux-state save --reason=hook:session-created"'
set-hook -g window-linked         'run-shell -b "${tmux-state}/bin/tmux-state save --reason=hook:window-linked"'
set-hook -g client-detached       'run-shell -b "${tmux-state}/bin/tmux-state save --reason=hook:client-detached"'

set-hook -g pane-died             'run-shell -b "${tmux-state}/bin/tmux-state capture-event pane-died          --pane=#{hook_pane} --window=#{hook_window} --session=#{hook_session}"'
set-hook -g window-unlinked       'run-shell -b "${tmux-state}/bin/tmux-state capture-event window-unlinked    --window=#{hook_window} --session=#{hook_session}"'
set-hook -g session-closed        'run-shell -b "${tmux-state}/bin/tmux-state capture-event session-closed     --session=#{hook_session}"'

set-hook -g window-renamed        'run-shell -b "${tmux-state}/bin/tmux-state index-update --session=#{hook_session}"'
set-hook -g window-layout-changed 'run-shell -b "${tmux-state}/bin/tmux-state index-update --session=#{hook_session}"'

# Auto-restore (runs once at config load)
run-shell -b '${tmux-state}/bin/tmux-state restore --auto'

# Keybindings
bind   u    run-shell '${tmux-state}/bin/tmux-state undo --pop'
bind   U    run-shell '${tmux-state}/bin/tmux-state pick --kind=close'
bind   R    run-shell '${tmux-state}/bin/tmux-state pick --kind=snapshot'
bind C-s    run-shell '${tmux-state}/bin/tmux-state save --reason=keybinding'
```

### config/tmux.conf.nix Substitution

Add to the `scripts` attrset alongside existing entries:

```nix
tmux-state = pkgs.callPackage ../packages/tmux-state.nix { };
```

(Or inline `buildGoModule { ... }` if you prefer not to introduce a `packages/` directory. The wt-go-rewrite spec uses an external repo; for `tmux-state` we keep the source in-tree.)

### modules/home-manager.nix Changes

Update the comment at line 316 (`tmux-continuum's auto-restore` → `lazytmux state-store auto-restore`).

Add systemd units:

```nix
systemd.user.timers.lazytmux-state-save = {
  Unit.Description = "Periodic tmux state snapshot";
  Timer = {
    OnBootSec = "2min";
    OnUnitActiveSec = "${toString cfg.persist.saveInterval}s";
    Unit = "lazytmux-state-save.service";
  };
  Install.WantedBy = [ "timers.target" ];
};

systemd.user.services.lazytmux-state-save = {
  Unit.Description = "Save tmux state";
  Service = {
    Type = "oneshot";
    ExecStart = "${tmux-state}/bin/tmux-state save --reason=timer";
  };
};

systemd.user.timers.lazytmux-state-gc = {
  Unit.Description = "tmux state store GC";
  Timer = {
    OnCalendar = cfg.persist.gcInterval;     # default "weekly"
    Unit = "lazytmux-state-gc.service";
  };
  Install.WantedBy = [ "timers.target" ];
};

systemd.user.services.lazytmux-state-gc = {
  Unit.Description = "tmux state store garbage collection";
  Service = {
    Type = "oneshot";
    ExecStart = "${tmux-state}/bin/tmux-state gc";
  };
};
```

## Implementation: Go Module

### Layout

```
go.mod                                        — module github.com/noamsto/lazytmux
go.sum
cmd/tmux-state/main.go                        — CLI entry, subcommand dispatch
internal/
  config/config.go                            — env + flag loading
  config/config_test.go
  log/log.go                                  — slog setup, log file rotation
  store/store.go                              — DB open, migrations, prepared statements
  store/store_test.go
  store/migrations/0001_initial.sql
  store/migrations/embed.go                   — go:embed
  snapshot/snapshot.go                        — read live server → manifest
  snapshot/parse.go                           — parse `tmux ... -F` output
  snapshot/parse_test.go                      — table-driven on canned tmux output
  snapshot/save.go                            — save flow (fingerprint, scrollback capture, insert)
  snapshot/save_test.go
  closeevent/capture.go                       — capture-event subcommand
  closeevent/capture_test.go
  closeevent/index.go                         — live_index maintenance
  restore/plan.go                             — plan builder
  restore/plan_test.go
  restore/apply.go                            — execute plan
  restore/apply_test.go                       — uses fake tmux client
  filter/filter.go                            — pure functions, easy to test
  filter/filter_test.go
  scrollback/store.go                         — CAS, refcount, GC
  scrollback/store_test.go
  tmux/client.go                              — exec wrapper, format-string passing
  tmux/client_test.go                         — fake binary harness
  tmux/parse.go                               — output parsing helpers
  picker/picker.go                            — fzf invocation
  picker/format.go                            — row rendering for fzf
testutil/
  tmuxserver.go                               — start/stop a real tmux server in /tmp
  tmuxserver_test.go
integration_test.go                           — end-to-end: save → kill server → restore → assert
```

### Dependencies

| Module | Purpose |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite (no CGO; clean Nix build, cross-compiles trivially) |
| `github.com/spf13/cobra` | CLI framework — used in `worktrunk` precedent |
| `github.com/klauspost/compress/zstd` | Pure-Go zstd for scrollback compression |
| `github.com/google/go-cmp/cmp` | Test diffing |
| Standard library | `encoding/json`, `crypto/sha256`, `log/slog`, `os/exec`, `sync`, `context` |

`modernc.org/sqlite` over `mattn/go-sqlite3` because:
- No CGO → trivially cross-compiles in Nix's `buildGoModule`
- No system libsqlite3 dep
- Performance gap vs. CGO version is ~10-20% on inserts; immaterial for our QPS

### Nix Packaging

`packages/tmux-state.nix`:

```nix
{ buildGoModule, ... }:

buildGoModule {
  pname = "tmux-state";
  version = "0.1.0";
  src = ../.;
  vendorHash = "sha256-...";
  subPackages = [ "cmd/tmux-state" ];
  doCheck = true;                # runs go test ./... at build time
  meta.mainProgram = "tmux-state";
}
```

`flake.nix` adds:

```nix
packages = {
  default = tmuxConfig.tmux-wrapped;
  tmux-state = pkgs.callPackage ./packages/tmux-state.nix { };
};
```

`config/tmux.conf.nix` references `${tmux-state}/bin/tmux-state` via the same store-path interpolation pattern used for shell scripts.

### Go Coding Conventions

- `golangci-lint` enabled in pre-commit (already in lazytmux pre-commit infra).
- `errcheck`, `govet`, `staticcheck`, `revive`, `gosec` all on.
- Errors wrapped with `fmt.Errorf("...: %w", err)`. No `errors.New` for production paths — every error has context.
- Logging: `log/slog` with JSON handler in production, text in development. All log entries include `subcommand`, `event_id` (where applicable), `pane_key` (where applicable).
- Tests use table-driven style. Integration test guarded by `if testing.Short() { t.Skip(...) }`.
- No `init()` side effects. Everything is constructed explicitly in `main`.
- Context propagation: every blocking call accepts `context.Context`. The CLI builds a single root `context.Background()` with signal cancellation at startup.

## Survival Across Nix Rebuilds

- **systemd units** — home-manager regenerates unit files into `~/.config/systemd/user/`, runs `systemctl --user daemon-reload`, and the new unit points at the new `/nix/store/.../tmux-state` binary. In-flight `oneshot` save services killed mid-rebuild lose at most one timer tick (~60s).
- **tmux hooks** — bound to old store path until `tmux source-file` runs in the activation script (already present in `modules/home-manager.nix`). `set-hook -g` replaces by name on reload, no stacking. Old binary still on disk (its generation isn't GC'd), so old hooks keep working in the gap.
- **Database & scrollbacks** — under `$XDG_DATA_HOME/lazytmux/`, outside `/nix/store`. Survive rebuilds, generation rollbacks, `nix-collect-garbage`.
- **Schema drift** — `PRAGMA user_version` checked at every DB open. If `current > expected`, log "newer DB schema, refusing to run" and exit non-zero (forward-incompat = explicit). If `current < expected`, run migrations from `internal/store/migrations/` in order.
- **Generation rollback** — older binary opens DB; if migration was forward-only (e.g., new column), older code reads from a wider table without harm. We commit to migrations being **backward compatible at the SQL level for one minor version** so rollback works for at least one step.
- **Hook duplication on reload** — `set-hook -g <name>` replaces the named hook; no stacking. (This is one of the reasons we prefer the systemd timer over an in-tmux background loop.)

## Testing

### Unit Tests (`go test ./...`)

Per-package tests for:
- `internal/snapshot/parse_test.go` — table-driven on canned `tmux list-panes -F '...'` output strings, including weird cases (newlines in commands, unicode in window names, empty cwd).
- `internal/filter/filter_test.go` — every filter rule, every threshold edge case.
- `internal/restore/plan_test.go` — given a manifest, assert action sequence.
- `internal/scrollback/store_test.go` — write same content twice → one file; refcount + GC.
- `internal/store/store_test.go` — open, migrate, insert, query, prune; uses `:memory:` SQLite.

### Integration Test (`go test -run TestIntegration`)

A single end-to-end test in `integration_test.go`:
1. Start a fresh tmux server on a tmp socket.
2. Create 2 sessions × 3 windows × 2 panes; populate cwds and run a few commands.
3. Run `tmux-state save`.
4. Kill the server.
5. Start a new server on the same socket.
6. Run `tmux-state restore --auto`.
7. Assert: server now has the same session/window/pane structure, cwds preserved, allow-listed commands re-launched.

Skipped under `go test -short`. Wrapped with `t.TempDir()` + `defer cleanup`.

### Manual Verification Checklist

(Same as the superseded specs, plus close-event paths.)

1. Save roundtrip with `nvim` + plain shell + `htop`.
2. Idle-shell filter drops bare bash with no children, keeps `nvim`.
3. Dedup against running server: restore twice, no duplication.
4. Stale session: edit `last_attached` to >14d, verify skip.
5. Stale snapshot: edit `saved_at` to >30d, verify whole snapshot skipped.
6. Scrollback restore visible in copy mode (`prefix + [`).
7. `prefix + u` after `Ctrl+D` in last pane of a window restores the pane.
8. `prefix + u` after closing a session restores the whole session.
9. `prefix + U` shows close-event picker; selecting an entry restores it.
10. `prefix + R` shows snapshot picker with smart-filter pre-applied.
11. `prefix + Ctrl-s` triggers an immediate save; verify new event row.
12. Save throttling: open 10 windows in quick succession, verify ≤ ceil(elapsed/30) snapshot rows.
13. GC: manually delete event rows, run `tmux-state gc`, verify scrollback files reaped.
14. Rebuild survival: `home-manager switch`, verify timer + hooks still functional.
15. Generation rollback: `home-manager switch --rollback`, verify older binary operates on existing DB or fails cleanly.
16. `nix flake check` passes (Go source goes through gofmt + golangci-lint).

## Migration & Rollout

Single PR, single commit (or two — one for the binary, one for the wiring change, if review wants smaller diffs).

- One-line release note: "Replaces tmux-resurrect/continuum and tmux-undo (unimplemented spec) with `tmux-state` Go binary backed by SQLite."
- User runs `home-manager switch`; activation script reloads tmux config; first save runs ~2 min after timer fires.
- Existing `~/.local/share/tmux/resurrect/` saves are NOT read. Documented in the release note. User can manually delete the directory if desired.

## Future Work (Not In This Spec)

- nvim cooperation: companion Lua module that writes `mksession` files on `VimLeave`; `tmux-state` invokes it on save and restores via `nvim -S` on relaunch.
- Per-session restore-mode override: `set -t <session> @persist-mode interactive` to override the global `restoreMode`.
- Cross-host snapshot portability via cwd remap rules (`/home/old/path` → `/home/new/path` substitution at restore time).
- Snapshot diff/compaction: store snapshot N as a delta from snapshot N-1 to reduce DB size for users with very stable layouts.
- Web UI for browsing history (lazytmux project lite).
- Search across history: full-text search of pane scrollback contents (`scrollbacks` table + FTS5 virtual table).
