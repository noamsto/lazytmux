# Plan: mirrored windows never reflow because `aggressive-resize` hides them

Issue: #478. Design spec:
`docs/superpowers/specs/2026-09-02-bridge-window-size-latest-478-design.md`.

Root cause in one line: lazytmux sets `aggressive-resize on`
(`config/tmux.conf.nix:708`), so tmux sizes a remote window only from clients
whose session currently has it selected; the bridge's single control client is
"on" one window, so `refresh-client -C @N:WxH` is silently discarded for every
other mirrored window, and `converger.need` records it as asserted anyway.

Fix: opt each mirrored remote window out of `aggressive-resize`, so the existing
cap does what its doc comment already claims. Nothing is pinned.

## Step 1: the offline regression test, red first

- [ ] Add production parity to the "remote" server in
      `tests/remote-m2-integration.bats` `setup()`: append
      `set -g window-size latest` and `set -g aggressive-resize on` to
      `SRC_CONF` (line 33). Without this the harness cannot express the bug and
      any new test passes vacuously.
- [ ] Add the case `daemon converges every mirrored window, not just the
      remote's current one`: SRC with two windows, `select-window -t rem:1` so
      window 2 is the one that used to be skipped, DST at 100x30, mirror both,
      assert **every** remote window's `#{window_width}` reaches 100.
      Gate on the MIRROR settling (one renderer pane per remote pane, session
      wide), then poll the widths on the house budget — never a fixed sleep, and
      never a source-side width as the settle proxy (SRC hits 100 as soon as the
      daemon sizes its own control client, #449).
- [ ] Add a SECOND case, `daemon re-converges every mirrored window after a
      client resize`: same two-window shape, but attach a real pty-hosted client
      to DST (the `OBS` pattern the #201/#231 cases use), wait for the mirror to
      settle, **then** `resize-window` that client and assert every remote window
      follows. The first case only exercises `setupWindow`'s cap; this is the
      only one that exercises `watchResize`, which is the path a terminal-size
      change actually takes. Width only — an expected height is brittle
      (status lines + `pane-border-status`). Generous poll budget: `watchResize`
      polls once a second.
- [ ] Verify BOTH are **red** before any production change, and that each
      failure message names the straggler window rather than just failing.

Already executed as a dry run: parity is safe (all 32 existing cases pass
against the pinned next-3.8 tmux) and the new case fails with
`windows that never converged to 100: 2 120`.

## Step 2: correct the two false comments

- [ ] `picker/remotebridge/daemon/size.go:8-14` — `ConvergeCmd`'s doc claims the
      per-window form "holds even when a human client is attached to the same
      remote session and owns the window as w->latest". False as written: it
      holds only once the window participates in sizing at all. Rewrite to say
      what is true and why the opt-out is required alongside it.
- [ ] `tests/remote-m2-integration.bats:509-515` — "The per-window form is a
      clamp applied after that calculation, so it holds regardless of who is
      latest" is the exact belief this bug rests on. Correct it.

## Step 3: emit the opt-out for every mirrored window

- [ ] Add a command builder next to `ConvergeCmd` in `size.go` returning
      `set-option -w -t @N aggressive-resize off`, with a comment naming the
      mechanism (tmux `resize.c` `clients_calculate_size_skip_client`'s
      `current` branch) and #478.
- [ ] Emit it in `setupWindow` (`daemon.go:713`) immediately before the cap,
      via the fire-and-forget `send` the function already takes — there is no
      useful action on failure, and `watchResize` treats converges the same way.
      Send it **unconditionally**, not under `cv.need`, so it cannot depend on
      converger state (a `resetWindow` on a window whose size is already
      recorded would otherwise skip it).
- [ ] `setupWindow` is the single choke point — confirm by inspection that the
      initial-window loop, `%window-add`, and `resetWindow` all reach it, and
      that no other path registers a mirror window.

Once at setup is deliberate, not per tick: a `set -w` override persists for the
window's life and nothing on the remote re-asserts per-window options.

## Step 4: never record a size the remote was not told

The invariant the issue asks for: *the converger's recorded size is never ahead
of what the remote was actually told.* Today `need` records before the write.

- [ ] `converger`: split intent from record so a caller can assert only after a
      confirmed write (e.g. `need` stays a pure query and a separate `record`
      commits, or `need` takes the issue as a callback). Keep it small; do not
      redesign the type.
- [ ] `watchResize` (`daemon.go:205,230,234`): take a sender whose result it can
      observe (`func(string) bool`, which `stream.send` already returns) and
      record only when the write happened.
- [ ] `Run`'s startup client-size assertion (`daemon.go:398-400`) — the third
      call site, and the one that matters most: `watchResize` re-sends the
      `clientSizeKey` slot only on a *change*, so a lost startup write leaves
      every later remote window born at the 80-column default uncorrected.
- [ ] `cv.forget` in `setupWindow` after the opt-out, before the cap: `reg.add`
      precedes `setupWindow`, so `watchResize` can cap a window before its
      opt-out lands — tmux discards that cap, the converger records it, and
      setup's cap is then skipped, leaving a window opted out but never capped.
- [ ] `setupWindow` (`daemon.go:713`): same treatment for the `one(rt, ...)`
      issue — the second call site with the identical ordering, which the
      issue's own suggested fix would have missed.
- [ ] State the residual gap in the comment: a *written* command is still not an
      *applied* one. A cap that draws `%error` because the window vanished
      between `reg.remoteIDs()` and the send latches the same way; that is
      acceptable only because the path is followed by `closeWindow`'s
      `cv.forget` (`daemon.go:852`).

## Step 5: update the tests the setup sequence encodes

- [ ] `picker/remotebridge/daemon/setupwindow_test.go:87` and `:143` — the fake
      scripts enumerate `%begin/%end` reply blocks in ordinal order starting at
      `ConvergeCmd`. One extra command per window setup shifts every later
      ordinal, so each script needs an extra leading block.
- [ ] `picker/remotebridge/daemon/size_test.go` — add coverage for the new
      builder, and for the converger's record-on-confirmed-write behaviour
      (a failed write must leave the slot untouched so the next tick retries).
- [ ] `picker/remotebridge/daemon/daemon_test.go:465` — check and update if it
      encodes the emitted command set.
- [ ] Add a Go test asserting `watchResize` emits the cap once per registered
      window and does not record when the sender reports a failed write.

## Step 6: gate

- [ ] `nix build .#default`
- [ ] `nix flake check` — includes `remote-m2-integration-tests`, which runs the
      new case against the pinned next-3.8 tmux (**not** the 3.7c on the devShell
      PATH; a local `bats` run is not representative and was already misleading
      once here).
- [ ] `nix build .#lint` — the only thing that runs the formatter.

All three, none subsumes another. Paste real output; do not claim a pass without it.

## Step 7: hardware verification, g5 -> tp-g6

Required — this cannot be signed off from unit tests.

- [ ] Drive the **fixed** daemon (built from this worktree, not the installed
      store path) against a >= 3-window mirror, several client-size steps in both
      directions.
- [ ] Assert per step: every mirrored window's remote
      `#{window_width}x#{window_height}` follows the gesture, not just the
      remote's current one; both SIGWINCH probes log every step.
- [ ] Content predicate, not judged by eye: for each mirrored pane, after
      settle, the remote's `capture-pane -p -t %N` equals the local mirror
      pane's `capture-pane -p`.
- [ ] A mirrored Claude Code pane reflows across the steps, with captured
      before/after pane content.
- [ ] Re-measure the ~35s mirror lag observed during diagnosis on the window
      that *did* converge. If it survives the fix, report it as its own issue
      with its own evidence — do not fold it into this diff.
- [ ] Clean up the throwaway `v478` remote session and its mirror.

## Out of scope (with reasons in the spec)

No teardown restore of `aggressive-resize` (a relaxation, not a pin, and
teardown's control-stream sends are unreachable on the paths that matter). No
debounce (the fix does amplify reconcile 1 -> N per nudge, which is the cost of
correctness; acceptance measures content coherence instead). No positive-area
guard on the cap (`localArea` cannot emit `0x0` — speculative robustness). The
issue's three adjacent findings stay untouched.
