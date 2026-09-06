# Spike: OSC 9;4 progress passthrough (#521)

**Status: executed.** Findings and verdict:
[2026-09-06-osc-9-4-progress-passthrough-findings.md](../specs/2026-09-06-osc-9-4-progress-passthrough-findings.md).
No code shipped — the findings doc explains why and what a follow-up needs.

## Goal

Determine, empirically, whether tmux passes OSC 9;4 (ConEmu/Windows-Terminal
progress sequence) through to the outer terminal, and whether kitty (the house
terminal) honours it. Ship code only if both answers are good; otherwise write
up the negative result on the issue.

## Steps

- [x] **Step 1: probe tmux passthrough on a scratch server**
  Start `tmux -L osc-probe` (isolated, doesn't touch the daily session). From
  inside a pane, emit `printf '\e]9;4;1;50\a'` (set 50%) and
  `printf '\e]9;4;0;0\a'` (clear) directly via `printf` to the pane's tty, and
  observe whether the *outer* terminal (kitty) receives/reacts to the sequence
  when tmux is in between vs. attached with `-CC` (control mode) vs a plain
  attach. Also test emitting it through `tmux send-keys` and through a pane
  running a real command. Try both raw OSC 9;4 and the tmux passthrough wrapper
  `\ePtmux;\e\e]9;4;1;50\a\e\\` to see if the raw form alone survives tmux's
  parser (tmux is known to consume many OSC sequences into its own state
  rather than passing them upstream).
  Record exact commands + observed terminal behavior (taskbar icon change,
  tab progress bar, or nothing, or literal escape text painted into the pane).

- [x] **Step 2: test kitty's own handling** (only if Step 1 shows passthrough,
  or to establish the negative-terminal baseline regardless)
  Run the same raw OSC 9;4 sequence directly in a kitty tab with **no tmux**
  in between, confirm kitty's tab/taskbar reacts (or doesn't — kitty's own doc
  support for OSC 9;4 needs checking, it's not universally implemented).
  Also test what a terminal that does **not** understand the sequence does
  with the raw bytes (does it print literal escape garbage, or silently
  swallow unknown OSC codes) — this matters because a stray unrecognised
  sequence painted as text would be a visible regression risk on any terminal
  lacking support.

- [x] **Step 3: decision point**
  Both mechanisms check out (tmux forwards when opted in via
  `terminal-features`, kitty honors it, unsupported terminals see zero bytes
  by construction) — but a real open question (does tmux resend progress on
  focus switch?) determines the emission architecture and wasn't resolved.
  See the findings doc for the full writeup and why that one gap was enough
  to stop here rather than guess at an architecture.

- [ ] **Step 4 (not executed): implement additively**
  Deferred to a follow-up ticket per the findings doc's recommendation —
  the open question above needs an answer before the emission design (one-shot
  vs. periodic re-emit) can be chosen correctly.
  - Add OSC 9;4 emission to `claude-status-update`, the sole state-writing
    entry point (already does `bridge_stamp` per write).
  - Set progress on `processing`/`compacting` states; clear (`\e]9;4;0;0\a`)
    on every path out of processing — enumerate explicitly: `done`, `idle`,
    `waiting`, `error`, `denied`, the derived `interrupted` reclassification
    in `read_pane_state`, and the dead-agent withdrawal
    (`claudeStatus.assumeDeadAfter`).
  - Decide explicitly whether the remote-bridge case (mirror pane, OSC has to
    cross the control stream like the graphics proxy does for kitty APCs) is
    in scope or deferred; state which, in the PR body.
  - Do not touch the spinner, state machine, priority ordering, or status-file
    format — additive only.

## Gate

`nix build .#default` and `nix flake check` only apply if code under
`scripts/`, `config/`, `modules/`, or `picker/` changed — it didn't here, this
is docs-only. `nix build .#lint` still runs regardless: it's the CI `lint` job
(typos, trailing-whitespace, merge-conflict markers) and applies to committed
markdown too.
