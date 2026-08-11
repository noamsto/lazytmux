# Reap Per-Pane Claude Status State Without a Working pane-exited Hook

**Issue:** [#341](https://github.com/noamsto/lazytmux/issues/341) — per-pane claude-status state
(`/tmp/claude-status/{panes,screen,interrupt,watchers}/<pane_id>`) outlives the pane it was
written for, because the cleanup hook that was supposed to remove it never fires.

## Root cause

`config/tmux.conf.nix` registered `set-hook -g pane-exited ...` (and a `-gu` clear alongside it)
to delete a pane's state files the moment the pane closed. On the pinned tmux, `pane-exited` is a
silent no-op: `show-hooks -g` after sourcing the generated config never lists it — tmux accepts the
`set-hook` call (no error, no warning) but never actually stores or fires it. There is no
`pane-died` fallback either; the hook name that would need it also doesn't fire. Nothing in the
existing test suite caught this because no test asserted a registered `set-hook -g` actually
*landed* in `show-hooks -g` output — every prior hook regression test (e.g. the
`after-select-pane[20]` test) checks the specific hook it cares about, not the general property
that registration implies storage.

## Fix

Rather than depend on a per-pane exit hook at all, fold dead-pane reaping into work the codebase
already does on a timer: `tmux-update-icons.sh`'s `arm_agent_detect` runs a full-server
`tmux list-panes -a` every 5th tick (originally only to arm `agent-detect` pipes and stamp the
dead-agent-floor liveness dir). That `rows` output is exactly the positive evidence needed to reap
— any pane id state has files for, that isn't in the current `list-panes -a`, is provably dead.

- **`scripts/lib-claude.sh`**: new `claude_reap_dead_panes(rows)`. Parses `rows` (tab-separated
  `list-panes -a` lines, pane id in column 1, e.g. `%3\tcodex\t0`) into a live-id set (stripped of
  the leading `%`), then for each of `$CLAUDE_PANES_DIR`, `$CLAUDE_SCREEN_DIR`,
  `$CLAUDE_INTERRUPT_DIR`, `$CLAUDE_WATCHERS_DIR` removes any file whose basename isn't in that
  set. An empty `rows` argument is a no-op — mirrors `claude_prune_stale_state`'s existing
  "only act on positive evidence" discipline, so a call site that races a failed `list-panes` can
  only under-reap, never wipe. Deliberately does **not** touch `$CLAUDE_LIVE_DIR` — that directory
  has its own sweep/staleness protocol (the dead-agent floor's `.sweep` marker) with different
  freshness semantics; folding it into this reap would double up two independent cleanup
  mechanisms over the same files.
- **`scripts/tmux-update-icons.sh`**: `arm_agent_detect` calls `claude_reap_dead_panes "$rows"`
  unconditionally, right after the existing `rows=$(tmux list-panes -a ...) || return 0` line —
  it already pays for the round-trip, so reaping is free once armed by either the agent-detect gate
  or the dead-agent-floor gate.
- **`config/tmux.conf.nix`**: removed the two no-op `set-hook -g`/`-gu pane-exited` lines, replaced
  with a comment explaining why (references issue #341 and that `show-hooks -g` confirms the
  no-op).
- **`CLAUDE.md`**: updated the stale claim that per-pane cleanup rides a `pane-exited` hook —
  now documents the sweep-based reap instead.

## Testing

- `tests/tmux-next38-readiness.bats`: new generic regression test — every `set-hook -g` name the
  generated config registers must appear in `show-hooks -g`'s output, or the test fails loudly.
  This guards the actual defect *class* (a hook silently accepted but never stored), not just the
  one hook name from #341 — any future hook that suffers the same silent-no-op fate now fails CI
  instead of shipping quietly broken. Also isolates `CLAUDE_STATUS_DIR` to a per-test tmpdir (like
  `tests/prune-stale-state.bats`/`tests/reflow-fanout.bats` already do) so the reap landing in
  `tmux-update-icons.sh` can't touch the real `/tmp/claude-status` from a test run.
- `tests/prune-stale-state.bats`: unit tests for `claude_reap_dead_panes` — reaps dead ids across
  panes/screen/interrupt/watchers while keeping live ones, never touches `CLAUDE_LIVE_DIR`, empty
  rows is a no-op, a live id with no files is a no-op.
- `tests/agent-liveness.bats`: integration test proving `arm_agent_detect` actually wires the reap
  through — stubs `claude_reap_dead_panes` before sourcing the raw script (the script's own
  `source @lib_claude@` is an unresolved Nix placeholder and fails silently without clobbering an
  already-defined function, a pattern this file already relies on for `AGENT_COMMANDS`/
  `CLAUDE_LIVE_DIR` injection) and asserts it's called with the fetched `rows`.
- `tests/test-display.sh`: isolates `CLAUDE_STATUS_DIR` to a temp dir for the same reason as the
  next38-readiness test, cleaned up in the existing `cleanup()` trap.

## Out of scope / follow-up

- `modules/home-manager.nix:107` registers an equivalent phantom `pane-exited[99]` hook for
  tmux-remux's `capture-event` — same silent-no-op mechanism, but a different file and a different
  consumer than #341 named. Not touched here; worth a follow-up sweep once the pattern above is
  proven out, ideally using the same generic "every registered hook is stored" assertion applied to
  that module's own hook set.
- `/tmp/claude-status` is shared machine-wide. A second tmux server on the same host reaping via its
  own `list-panes -a` would only see its own panes as "live" and could reap another server's still-
  live pane state — the same single-server assumption `claude_prune_stale_state` already carries
  (its `.server_start` marker is scoped to whichever server last ran the scan). Not new risk
  introduced by this change, just inherited from the existing design.
