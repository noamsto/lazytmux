# Stamp @ts_relaunch for Codex panes

**Issue:** noamsto/lazytmux#140
**Date:** 2026-07-06
**Status:** Design approved, pending implementation plan

## Problem

`tmux-state` restores a pane to a live agent session by exec'ing the pane's
`@ts_relaunch` option verbatim on restore. The mechanism is fully
agent-agnostic — `tmux-state` knows nothing about any specific agent.

Today only Claude panes get stamped. `tmux-update-icons.sh` derives the Claude
session UUID from the transcript basename in a Claude pane-state file and sets
`@ts_relaunch="claude --resume <uuid>"` (see the stamping block gated on
`@resume_claude`). Codex panes get no `@ts_relaunch`, so on restore they come
back as a bare shell instead of a resumed Codex session.

## Goal

Give Codex panes the same treatment: stamp `@ts_relaunch="codex resume <uuid>"`
so `tmux-state` restores a resumed Codex session. All changes are in lazytmux;
`tmux-state` needs zero changes — the `@ts_relaunch` contract already covers it.

## Facts established

- `codex resume <SESSION_ID>` is a real subcommand; `SESSION_ID` is a UUID (or
  session name). Verified against codex-cli 0.142.3.
- Sessions are stored at `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`.
  The first JSONL line is a `session_meta` record carrying `session_id` (the
  resumable thread id), `cwd`, `agent_nickname`, `agent_role`.
- `agent-detect` already recognizes codex panes (`AGENT_COMMANDS="claude codex"`,
  `picker/agentdetect/manifest/manifests/codex.toml`) for working/idle state,
  but it does **not** surface a session id.
- Codex has a hooks system (`--dangerously-bypass-hook-trust`, "enabled hooks")
  and a `notify` program config — either can run a script on session events.
- Unlike Claude, nothing currently writes a per-pane state file for Codex.

## Design

### 1. Per-pane session-id writer (new)

Chosen approach: a Codex session hook (fallback: the `notify` program) runs a
small writer script that records the pane's Codex session UUID to a file keyed
by tmux pane id — mirroring the Claude pane-state files lazytmux already reads.

- **File:** `$CODEX_PANES_DIR/<pane_id>` (new dir alongside `CLAUDE_PANES_DIR`),
  contents = the session UUID (single line). `<pane_id>` is the numeric pane id
  (`%N` without the `%`), matching the Claude convention.
- **Writer:** a new script (`scripts/codex-session-write.sh` or a subcommand of
  an existing helper) invoked by the hook. It reads the session id from the hook
  payload / rollout path and the pane id from `$TMUX_PANE`, then writes the file.
- **Wiring:** provisioned via the lazytmux Home Manager module
  (`modules/home-manager.nix`) into `~/.codex/config.toml`, gated the same way
  the Claude resume infra is gated (only when `tmux-state` is installed).

**Verification items (resolve in the plan):**
- Which Codex hook event fires at session start / carries the session id. If no
  start event exists, use the earliest event that does and write idempotently.
- Whether `$TMUX_PANE` is present in the hook's environment. If Codex does not
  propagate it, fall back to the `notify` program (which runs as a child of the
  pane process and should inherit `TMUX_PANE`), or resolve the pane from the
  hook's pid via `tmux list-panes` pid matching.
- Distinguish `session_id` (resumable thread) from the filename `id` for
  subagent sessions — `codex resume` must receive the resumable one.

### 2. Generalize the stamping loop (`tmux-update-icons.sh`)

The current resume stamping is Claude-specific: it iterates Claude pane files
and derives `claude --resume <uuid>`. Generalize:

- Add `@resume_codex` as a status-expanded option paralleling `@resume_claude`,
  passed into `main()` as a positional arg (avoids a per-tick `show-option`
  fork, same as `RESUME_CLAUDE`).
- Introduce a per-agent resume resolution: given an agent kind and its session
  UUID, produce the relaunch command:
  - claude → `claude --resume <uuid>`
  - codex  → `codex resume <uuid>`
- Iterate agent panes (already known from the batched `list-panes`; agent kind =
  `pane_current_command`). For codex panes, read the UUID from
  `$CODEX_PANES_DIR/<pane_id>`; for claude panes, keep the transcript-basename
  derivation.
- Stamp only on change (compare against `@ts_relaunch` already read in the
  batched `list-panes`), exactly as the Claude path does — a stable pane forks
  nothing per tick.

### 3. Config / module surface

- `config/tmux.conf.nix`: define `@resume_codex` (default off, mirroring
  `@resume_claude`) and thread it into the `tmux-update-icons` invocation in
  `status-format[0]`.
- `modules/home-manager.nix`: option to enable Codex resume; provisions the
  Codex hook/notify config and sets `@resume_codex on` when enabled and
  `tmux-state` is present.

## Data flow

```
session start: codex hook -> codex-session-write.sh
                 -> $CODEX_PANES_DIR/<pane_id> = <session_uuid>

each tick:     tmux-update-icons (@resume_codex on)
                 -> for codex pane: read $CODEX_PANES_DIR/<pane_id>
                 -> desired = "codex resume <uuid>"
                 -> if changed: tmux set -pq @ts_relaunch "$desired"

restore:       tmux-state reads @ts_relaunch -> exec "codex resume <uuid>"
```

## Testing

- Writer script: unit test (bats, under `tests/`) — given a hook payload and
  `TMUX_PANE`, writes the correct UUID to the correct pane file; idempotent on
  repeat events.
- `tmux-update-icons`: extend the existing `@ts_relaunch` integration fixture to
  include a codex pane with a seeded `$CODEX_PANES_DIR` file; assert
  `@ts_relaunch` becomes `codex resume <uuid>` and that a claude pane still gets
  `claude --resume <uuid>`.
- No-op-on-unchanged: assert no `tmux set` fork when the value is already
  current.

## Dependencies

- `tmux-state` `feat/7-relaunch-override` (the `@ts_relaunch` mechanism) must
  land first. No `tmux-state` code changes are required by this issue.

## Non-goals

- Changing `agent-detect` state detection.
- Resuming Codex non-interactive/`exec` sessions (only interactive TUI sessions
  are resumable via `codex resume`).
- Any Claude-side behavior change beyond refactoring the shared stamping loop.
