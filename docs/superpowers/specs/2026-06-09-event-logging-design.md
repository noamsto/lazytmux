# Event Logging for Debugging — Design

**Date:** 2026-06-09
**Status:** Approved, pending implementation plan

## Problem

Lazytmux has no event logging. The only persisted runtime state is *current-state
snapshots* — `/tmp/claude-status/panes/<pane_id>`, `/tmp/claude-status/issues/<pane_id>`,
`/tmp/lazytmux-pr/<sha>.json` — all overwritten in place. There is no history.

So when a window shows a stuck claude badge, reflows to the wrong line count, stamps
the wrong issue, or a picker switches to the wrong session, there is no trail of *what
happened on which window/session over time*. Debugging means reconstructing from a live
snapshot that has already moved on.

We want a correlated, time-ordered event log keyed by session/window/pane — without
taxing the fork-averse hot paths (`claude-status` runs every status render;
`tmux-update-icons` every 1s).

## Goals

- A single, time-ordered, correlated log: see a reflow and a claude transition that
  happened in the same instant, across windows/sessions.
- **Zero meaningful cost when off.** The gate added to hot paths must not fork.
- Flip on/off **live** in a running session — no rebuild, no reload — to catch
  intermittent glitches when they appear.
- Cover the four categories that produce real bugs: claude state transitions, reflow,
  issue/PR enrichment, picker switch/create actions.

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
| Retention | Size-based rotation |

## Architecture

### 1. The gate (the "will it slow us down" answer)

Reading the tmux option directly (`tmux show-options -gqv @lazytmux_debug`) is a
**fork + IPC per check** — unacceptable on every render. Instead:

> **Gate = a sentinel file.** When debug is on, `$LAZYTMUX_LOG_DIR/debug.on` exists.
> Bash checks `[[ -f $sentinel ]]` — a builtin, **no fork**. Go checks `os.Stat`. When
> off, the cost added to every hot path is one builtin test ≈ nothing.

The user-facing switch stays "runtime, flip live": a `lazytmux-debug` command (bound to
a which-key chord) sets the `@lazytmux_debug` tmux option (for display / which-key
visibility) **and** creates/removes the sentinel. The sentinel is what scripts gate on.

> ⚠️ **Documented footgun:** a bare `tmux set -g @lazytmux_debug 1` will *not* arm
> logging — there is no tmux "option-changed" hook to mirror it to the sentinel. Flip
> via the binding or `lazytmux-debug on`.

**When on**, each logged event costs a couple of forks: the timestamp is fork-free
(bash `printf '%(%Y-%m-%dT%H:%M:%S)T' -1`); the rotation size-check `stat` is the one
fork per event. Acceptable — you opted into overhead to debug.

### 2. Components

| New/changed | What it is |
|---|---|
| `scripts/lib-log.sh` *(new)* | Shared bash helper, built via `writeShellScript`, injected as `@lib_log@`. Exports `LAZYTMUX_LOG_DIR`, `LAZYTMUX_LOG_FILE`, `LAZYTMUX_DEBUG_SENTINEL`; provides `log_enabled` (the `[[ -f ]]` gate) and `log_event <category> <k> <v> …`. |
| `picker/log.go` *(new)* | Go twin of the same contract: `logEnabled()` (stat sentinel) + `logEvent(cat, fields)` writing the **same JSON-line schema to the same file**. Called from `picker/tui.go` at the switch/create site. |
| `scripts/lazytmux-debug.sh` *(new)* | The toggle: `on \| off \| toggle \| status \| tail`. Sets `@lazytmux_debug` + creates/removes the sentinel; `tail` runs `tail -f` on the log; `status` reports on/off + file size. |
| `config/tmux.conf.nix` | Add `@lib_log@` to the substitution list (alongside `@lib_enrich@`); package `lazytmux-debug`; add a which-key chord to flip it. The picker needs no Nix change beyond already being built — Go reads the sentinel at runtime. |
| `tests/log.bats` *(new)* | Unit-tests the pure logic: JSON escaping, numeric-vs-string field emission, no-op when the sentinel is absent. Runs under `nix flake check` like `enrich.bats`. |

**Locations:** `${XDG_STATE_HOME:-$HOME/.local/state}/lazytmux/` holds `events.log`,
`events.log.1`, and the `debug.on` sentinel.

The two implementations (bash + Go) share one schema and one file **by contract, not
code** — the only duplication, small and pinned by the bats + Go tests.

### 3. Line schema

One JSON object per line. `log_event` emits `ts` (fork-free) and `cat`, then the
caller's `key value` pairs. Values matching `^-?[0-9]+$` emit **bare** (JSON numbers);
everything else is **string-escaped** (`\` → `\\`, `"` → `\"`, newlines stripped) so an
issue title containing quotes cannot corrupt the line.

```json
{"ts":"2026-06-09T09:51:03","cat":"claude","sess":"lazytmux","win":2,"pane":"%53","event":"transition","from":"processing","to":"done"}
```

### 4. Call sites

All guarded by `log_enabled` / `logEnabled()`, so zero work when off. Each site logs
only data **already computed for its real work** — never forks to gather log-only data.

| Category | Script / site | Event fields |
|---|---|---|
| **claude** | `claude-status-update.sh` — at the write, after reading prior state (prior read happens only when debug on) | `event=transition from= to= pane= win= sess=`; plus `event=issue op=add\|done\|clear id=` |
| **reflow** | `tmux-reflow-windows.sh` — after the cache check | `event=recompute trigger= wins= width= splits= lines= cache=hit\|miss sess=` |
| **enrich/issue** | `tmux-issue-stamp.sh` + providers — after provider selection | `event=stamp provider= id= title= url= win= sess=` |
| **enrich/pr** | `tmux-pr-enrich.sh` — after the gh fetch | `event=pr branch= number= state= check= mergeable= cache=hit\|miss win=` |
| **picker** | `picker/tui.go` — at `switch-client` / `new-session` | `event=switch\|create mode=session\|window target= from=` |

### 5. Rotation & concurrency

- **Rotation:** when `log_event` fires (debug on), `stat -c%s` the file; if it exceeds
  the cap (5 MB, a clearly-named constant in `lib-log.sh`), `mv events.log events.log.1`
  (overwriting any prior `.1`) and start fresh. Keeps ~2 files, bounded disk.
- **Concurrency:** multiple short-lived scripts append to one file. Appends are kept
  short (< `PIPE_BUF` = 4096 bytes) so `>>` writes are atomic on Linux — lines never
  interleave. The rotation `mv` is itself atomic; a rotation race between two scripts is
  benign (worst case a few lines land in the just-rotated file). No flock — appropriate
  for a debug-only tool.

## Testing

- `tests/log.bats` (under `nix flake check`): `_json_escape` correctness; numeric vs
  string field emission; `log_event` is a no-op when the sentinel is absent; rotation
  triggers at the cap.
- Go: a unit test for `logEvent` schema output and the `logEnabled` gate.
- Manual: `lazytmux-debug on`, exercise reflow/claude/enrich/picker, `lazytmux-debug
  tail`, confirm a correlated timeline; `lazytmux-debug off`, confirm appends stop.

## Open footguns (documented, not fixed)

- Manual `tmux set @lazytmux_debug 1` does not arm logging — use the binding or
  `lazytmux-debug on`.
- Leaving debug on grows the log; size rotation bounds it to ~2× the cap. `status`
  surfaces current size.
