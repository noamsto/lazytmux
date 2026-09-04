# #513 — read `@bridge_proc` in the pickers so mirror rows show the agent icon

## Problem

A mirrored (remote-bridge) row in `prefix + s` / `prefix + w` / `prefix + W`
shows the agent **status** indicator but no agent **process icon** (no brain
for a remote `claude`). Local rows show both.

## Cause

A mirror pane runs the bridge renderer, so its `pane_current_command` is
`fish`, not the remote's real command. The daemon already stamps the remote
pane's real command onto the mirror pane as `@bridge_proc`
(`daemon/agentstatus.go:151`). `scripts/tmux-update-icons.sh:126` already
applies the precedence rule (`@bridge_proc` overrides `pane_current_command`
when non-empty), but the Go pickers' collectors never read `@bridge_proc` at
all — confirmed by grep: its only consumers were the shell script, the daemon
that writes it, and a kill-pane confirm prompt.

Verified on two live mirrors (`halo-nix-config:1`, `tp-g6-money:1`): both
carried `@bridge_proc=claude` while `pane_current_command` read `fish`.

## Fix

- `picker/main.go` `collectPanesSnapshot` / `sessions()` — appended
  `#{@bridge_proc}` to the `list-panes -a` format (trailing field, index 8 of
  9) and applied the shell script's precedence: non-empty `@bridge_proc`
  overrides `proc` before it's fed into `si.procs`.
- `picker/main.go` `collectWindows` / `parseWindowPaneRows` — same append
  (trailing field, index 29 of 30) and same override, applied per-pane before
  dedup into `wi.procs`.
- `picker/statusline/usage.go` `agentsRunning` — same root cause: the
  agent-usage segment's live gate scanned `pane_current_command` only, so a
  host whose only agents are remote hid the segment. Added `@bridge_proc` to
  the same `list-panes -a` call and checked it too.
- `picker/statusline/main.go:44` — confirmed no change needed: it renders
  `@active_pane_icon`, which `tmux-update-icons` already stamps bridge-aware.

## Verification

- New unit tests: `TestSessionsBridgeProcOverride` (session collector) and
  `TestParseWindowPaneRowsBridgeProcOverride` (window collector), each
  asserting a mirror row resolves to the remote's proc and a local row falls
  through to `pane_current_command`. Confirmed non-vacuous by temporarily
  disabling the override in `main.go` and re-running just these two tests —
  they failed (`fish` instead of `claude`) before the fix.
- `cd picker && go test -race -count=1 ./...` — all packages green.
- `go test -v ./tmuxformat/...` — the `|`-delimiter format assertions
  (`TestPickerGoSources`) pass over the edited files.
- Live sanity check (throwaway test, removed before commit): against the two
  real mirrors on this machine, `collectSessions()`/`collectWindows()` now
  resolve `procs=[claude]` instead of `[fish]` for both `halo-nix-config` and
  `tp-g6-money`.
- Gate: `nix build .#default`, `nix flake check`, `nix build .#lint` — see PR
  description for exit codes.
