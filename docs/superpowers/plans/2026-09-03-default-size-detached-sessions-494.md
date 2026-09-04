# Plan: set default-size so detached sessions are not born at 80x24 (#494)

## Symptom

Detached sessions (notably zoxide picker `new-session -d`) inherit tmux's
built-in `80x24` because `default-size` was never set. Under
`window-size latest` + `aggressive-resize on`, a window no client has displayed
keeps that size forever — shell output hard-wraps at 80 columns permanently.

## Root cause

Session birth is the only leak: `new-window` inherits the session size and
auto-corrects once the session is right. The authoritative knob is the global
session option `set -g default-size`, not the server-level side effect from
`new-session -x/-y`.

## Approach

Two halves:

1. **`tmux-default-size` helper + hooks** — on attach/resize, scan
   `list-clients -F` for the largest non-control-mode client (control clients
   are 80×? and must be excluded), floor at 80×24, `set -g default-size WxH`.
   Guards live in shell, not format syntax (`set-option` does not expand
   formats; `#{>=:}` compares strings). Hooks at `client-attached[20]` and
   `client-resized[20]` with matching `set-hook -gu …[20]` entries in the
   idempotency block so `prefix + r` stays clean (bare `-gu` on the hook name
   clears every index too; the indexed clears document the new slots).

2. **Zoxide creation site** — `createAndSwitch` passes `-x/-y` from the
   invoking client's size so the session is born correct even before the hook
   runs.

No `new-window` path — window birth already follows session size.

## Verification

- `shellcheck scripts/tmux-default-size.sh`
- `bats tests/default-size.bats` (via `default-size-tests` check)
- `default-size-conf-assertions` on generated conf (hook wiring + order)
- `go test ./picker/... -run 'FlooredClientSize|NewSessionSizeArgs'`

## Closes

#494
