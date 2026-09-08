# Plan: stamp `@window_icon_padded` on every window (#580)

## Mechanism

`tmux-update-icons` is invoked from `status-format[0]` as
`tmux-update-icons #{qs:session_name} …`. It enumerates panes with
`list-panes -s -t "$SESSION"` and only stamps that session.

tmux evaluates `#()` only for a client that is drawing a status line. A
session with no attached client never gets a pass. Measured discriminator:
`padded_len` empty (`@window_icon_padded` unset) vs 8-char empty pad
(`MAX_ICONS * 3 + 2` spaces after a pass that found no icons).

`aeye:2` / `scratch-lazytmux:1` match this: live panes, never visited by
the attached client's tick. Not MAX_ICONS, not dead-agent veto, not
alphabetical abort.

## Approach

Keep one invocation (the attached session's tick) but stamp **every**
window on the server in that pass.

Files:

- `scripts/tmux-update-icons.sh` — enumerate all sessions/windows; stamp
  icons for each; keep git-branch polling at ~1 fork/tick on the
  **invoking** session's active window (plus unseeded `@branch` anywhere).
  Per-session `@active_pane_icon` / `@claude_session_fg`. Kick reflow per
  session whose labels actually changed. `|`-delimited formats only.
- `tests/update-icons-all-windows.bats` — private tmux server: session A
  (invoked) + session B (not invoked); after one `bash update-icons A`,
  every `list-windows -a` row has `@window_icon_padded` set (length of the
  empty pad). A window with `pane_current_command=claude` in B still gets
  a non-empty `@window_icon_display` when ICON_MAP includes claude.
- `flake.nix` — register the bats check like `update-icons-resume-guard-tests`.
- Preserve brain/state split: do not change ICON_MAP or `read_pane_state`.
- Do not touch `picker/statusline` (#575).

## Steps

1. Reproduce on a scratch server: two sessions, invoke with A only, confirm
   B's `@window_icon_padded` is empty (red).
2. Change enumeration so one pass covers `list-windows -a`.
3. Green the bats invariant + existing update-icons bats.
4. `shellcheck` the script.

## Validation

- bats: `tests/update-icons-all-windows.bats` plus existing update-icons suites
- `nix flake check` (scoped: the new + existing update-icons checks)
- `shellcheck scripts/tmux-update-icons.sh`
