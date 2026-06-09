# Event Logging for Debugging — Design

**Date:** 2026-06-09
**Status:** Approved, pending implementation plan

## Problem

Lazytmux has no event logging. The only persisted runtime state is *current-state
snapshots* — `/tmp/claude-status/panes/<pane_id>`, `/tmp/claude-status/issues/<pane_id>`,
`/tmp/lazytmux-pr/<sha>.json` — all overwritten in place. There is no history.

So when a window shows a stuck claude badge, reflows to the wrong line count, stamps
the wrong issue, or a picker switches to (or kills) the wrong session, there is no trail
of *what happened on which window/session over time*. Debugging means reconstructing
from a live snapshot that has already moved on.

We want a correlated, time-ordered event log keyed by session/window/pane — without
taxing the fork-averse hot paths (`claude-status` runs every status render;
`tmux-update-icons` every 1s).

## Goals

- A single, time-ordered, correlated log: see a reflow and a claude transition that
  happened in the same instant, across windows/sessions.
- **Negligible cost when off.** The gate added to hot paths must not fork.
- Flip on/off **live** in a running session — no rebuild, no reload — to catch
  intermittent glitches when they appear.
- Cover the four categories that produce real bugs: claude state transitions, reflow,
  issue/PR enrichment, picker switch/create/**kill** actions.

## Non-goals

- Logging per-tick activity (icon updates every 1s, every status render). We log
  *transitions and decisions*, never ticks.
- Logging claude **staleness demotions** as discrete events — they are derived on each
  read in hot-path readers, not edge-triggered. They surface implicitly as the next
  real transition.
- Configurable verbosity levels, remote shipping, structured query tooling. YAGNI.

## Key Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Toggle | Runtime, flip live (no rebuild) |
| Scope | All four categories: claude transitions, reflow, enrichment, picker |
| Sink + format | Single file, JSON lines |
| Retention | Size-based rotation (flock-guarded) |
| Debug lifetime | Transient — per tmux-server lifetime; cleared on server start |

## Architecture

### 1. The gate (the "will it slow us down" answer)

Reading the tmux option directly (`tmux show-options -gqv @lazytmux_debug`) is a
**fork + IPC per check** — unacceptable on every render. Instead:

> **Gate = a sentinel file.** When debug is on, `$LAZYTMUX_LOG_DIR/debug.on` exists.
> Bash checks `[[ -f $sentinel ]]` — a builtin, **no fork**. Go checks `os.Stat`.

Cost added to a hot path when off is **one `[[ -f ]]` builtin (a stat, no fork)** plus
the always-paid cost of sourcing one small extra lib (`lib-log.sh`) — consistent with
the libs these scripts already source (`lib-icons`, `lib-claude`). Not literally zero,
but negligible and fork-free.

The user-facing switch stays "runtime, flip live": a `lazytmux-debug` command (bound to
a which-key chord) sets the `@lazytmux_debug` tmux option (for display / which-key
visibility) **and** creates/removes the sentinel. The sentinel is what scripts gate on,
and what `lazytmux-debug status` reports (never the option — they could diverge).

**Debug is transient.** The sentinel lives in `$XDG_STATE_HOME` (persists on disk), but
a tmux **server-start hook removes it**, so debug never silently survives a server
restart while the (reset) `@lazytmux_debug` option reads off. Debug = "I'm chasing a bug
in this server session now," not a durable setting.

> ⚠️ **Documented footgun:** a bare `tmux set -g @lazytmux_debug 1` will *not* arm
> logging — there is no tmux "option-changed" hook to mirror it to the sentinel. Flip
> via the binding or `lazytmux-debug on`.

**When on**, each logged event costs a couple of forks: the millisecond timestamp
(`date '+%FT%T.%3N'`) and the rotation size-check. Acceptable — the fork-free constraint
applies only to the *off* gate; once we are logging, debug is on and you have opted into
overhead.

### 2. Components

| New/changed | What it is |
|---|---|
| `scripts/lib-log.sh` *(new)* | Shared bash helper, built via `writeShellScript`, injected as `@lib_log@`. Exports `LAZYTMUX_LOG_DIR`, `LAZYTMUX_LOG_FILE`, `LAZYTMUX_DEBUG_SENTINEL`; provides `log_enabled` (the `[[ -f ]]` gate) and `log_event <category> <k> <v> …`. No build-time placeholders of its own — the rotation cap is a constant overridable via `LAZYTMUX_LOG_MAX_BYTES` (for tests). |
| `scripts/lazytmux-log-event.sh` *(new)* | Thin CLI: sources `lib-log` and calls `log_event "$@"`. Exists so the **Go picker** can log via `exec.Command("lazytmux-log-event", …)` instead of reimplementing the helper. Picker actions are rare and user-initiated, so one fork per action is free — and there is **one** implementation, no bash/Go drift. |
| `scripts/lazytmux-debug.sh` *(new)* | The toggle: `on \| off \| toggle \| status \| tail`. Sets `@lazytmux_debug` + creates/removes the sentinel; `tail` runs `tail -f` on the log; `status` reports on/off (from the **sentinel**) + file size. |
| `picker/tui.go`, `picker/zoxide.go` | Add `exec.Command("lazytmux-log-event", …)` at the existing action sites: `tui.go` switch (`:241`), kill-window (`:249`), kill-session (`:251`); `zoxide.go` new-session + switch (`:151–156`). Bare-name exec relies on the wrapper's PATH — consistent with how the picker already execs `tmux`/`zoxide`/`git`. **No Nix change to the picker derivation.** |
| `config/tmux.conf.nix` | Build `lib-log` (`pkgs.writeShellScript "lib-log" (readFile …)`); add `@lib_log@`→`${lib-log}` to **`mkScriptFull`** (reflow) and **`mkScriptEnrich`** (issue/PR); **give `claude-status-update` a substitution path** — it is currently built by plain `mkScript` with no substitution, so it must move to a builder that substitutes `@lib_log@`. Package `lazytmux-debug` + `lazytmux-log-event` (both source `@lib_log@`). Add a which-key chord to flip debug. Add the server-start hook that removes the sentinel. |
| `tests/log.bats` *(new)* | Unit-tests the pure logic: JSON escaping (incl. tabs/control chars), all-values-quoted output, the `from!=to` dedup helper, no-op when the sentinel is absent, and rotation at a tiny `LAZYTMUX_LOG_MAX_BYTES`. Runs under `nix flake check` like `enrich.bats`. |

**Locations:** `${XDG_STATE_HOME:-$HOME/.local/state}/lazytmux/` holds `events.log`,
`events.log.1`, and the `debug.on` sentinel.

There is now **one** logging implementation (`lib-log.sh`); the Go picker reaches it
through the `lazytmux-log-event` CLI, so there is no second source of truth to drift.

### 3. Line schema

One JSON object per line. `log_event` emits `ts` and `cat`, then the caller's
`key value` pairs.

- **Timestamp:** millisecond resolution via `date '+%FT%T.%3N'` — 1-second resolution
  would defeat the whole point of correlating events in the same instant. (A fork, but
  it only fires when debug is on.)
- **All values are quoted JSON strings.** No auto-numeric detection: the repo's numeric
  session-name gotcha (`sess=10`) would otherwise emit `sess` sometimes as a number,
  sometimes a string. Quoting everything is simpler and type-consistent.
- **Escaping:** `\` → `\\`, `"` → `\"`, `<tab>` → `\t`, `<cr>` → `\r`, newlines stripped,
  other C0 control chars stripped — so an issue/PR title can never corrupt or invalidate
  a line.
- **Multi-value fields** (e.g. reflow split points) serialize as a comma-joined string.

```json
{"ts":"2026-06-09T09:51:03.412","cat":"claude","sess":"lazytmux","win_id":"@4","win":"2","pane":"%53","event":"transition","from":"processing","to":"done"}
```

### 4. Call sites

All guarded by `log_enabled` / the CLI's own gate, so zero work when off. Each site logs
only data **already computed for its real work** — never forks to gather log-only data.
Identity is logged with **stable ids** (`pane=%53`, `win_id=@4`); the mutable window
*index* (`win=2`) is included only as a convenience, because indices are renumbered when
windows close and are useless for cross-time correlation.

| Category | Script / site | Event fields |
|---|---|---|
| **claude** | `claude-status-update.sh` — at the write, after reading prior state (prior read happens only when debug on) | `event=transition from= to= pane= win_id= win= sess=` — **logged only when `from != to`** (PreToolUse+PostToolUse both fire `processing` on every tool call, so un-deduped this floods); plus `event=issue op=add\|done\|clear id= pane=` |
| **reflow** | `tmux-reflow-windows.sh` — after the cache check | `event=recompute trigger= wins= width= splits= lines= cache=hit\|miss sess=` |
| **enrich/issue** | `tmux-issue-stamp.sh` + providers — after provider selection | `event=stamp provider= id= title= url= win_id= sess=` |
| **enrich/pr** | `tmux-pr-enrich.sh` — after the gh fetch | `event=pr branch= number= state= check= mergeable= cache=hit\|miss win_id=` |
| **picker** | `picker/tui.go` + `picker/zoxide.go` — at the action sites, via the CLI | `event=switch\|create\|kill_window\|kill_session mode=session\|window target= from=` |

Two scoping calls:

1. **Claude staleness demotions** are derived on each read in hot-path readers, not
   edge-triggered — logging them would be per-tick noise. We log only the real
   hook-driven transitions written by `claude-status-update` (edge-triggered, deduped on
   `from != to`, and not a hot path). A demotion surfaces as the next real transition.
2. **Picker** logs the *outcome* (switch/create/kill target) from the Go action sites —
   including **kill-window / kill-session**, since a window vanishing is exactly the
   "where did my window go" event a trail should capture.

### 5. Rotation & concurrency

- **Rotation:** when `log_event` fires (debug on), `stat -c%s` the file; if it exceeds
  the cap (`LAZYTMUX_LOG_MAX_BYTES`, default 5 MB), `mv events.log events.log.1`
  (overwriting any prior `.1`) and start fresh. Keeps ~2 files, bounded disk.
- **Concurrency:** multiple short-lived scripts append to one file. On Linux a single
  `printf >>` of a short line is one `O_APPEND` `write()` syscall, atomic w.r.t.
  concurrent writers — lines never interleave. The **rotation `mv` is flock-guarded**
  (the enrich code already uses flock): without the lock, two appenders that both pass
  the size check would `mv` in turn, and the second clobbers the just-rotated `.1` with
  a freshly-recreated tiny file — losing the whole prior segment, not "a few lines."

## Testing

- `tests/log.bats` (under `nix flake check`): JSON escaping incl. tabs/control chars;
  all-values-quoted output; `from != to` dedup; `log_event` is a no-op when the sentinel
  is absent; rotation triggers at a tiny `LAZYTMUX_LOG_MAX_BYTES`.
- Go: the picker exec sites are covered by existing picker tests / a small addition that
  asserts the CLI is invoked with the expected args (no new Go logging logic to test).
- Manual: `lazytmux-debug on`, exercise reflow/claude/enrich/picker (incl. killing a
  window), `lazytmux-debug tail`, confirm a correlated timeline; `lazytmux-debug off`,
  confirm appends stop; restart the tmux server, confirm the sentinel is gone.

## Open footguns (documented, not fixed)

- Manual `tmux set @lazytmux_debug 1` does not arm logging — use the binding or
  `lazytmux-debug on`.
- Leaving debug on grows the log; size rotation bounds it to ~2× the cap. `status`
  surfaces current size, and a server restart turns debug off.
