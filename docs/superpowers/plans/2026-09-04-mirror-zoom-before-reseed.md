# Plan: sequence the mirror zoom toggle before the reseed (#511)

Design: `docs/superpowers/specs/2026-09-04-mirror-zoom-before-reseed-design.md`.
Read it first — it carries the measured tmux behaviour every step below relies on.

All work is in `picker/remotebridge/daemon/` and `tests/`. Worker `fern` is
concurrently editing `reconcile.go`, `daemon.go` and `windows.go` for #409, so
every step below is scoped to the smallest hunk that does the job.

## Step 1: move the zoom toggle ahead of the dims push and the reseed

- [ ] In `reconcileLayout` (`picker/remotebridge/daemon/reconcile.go`), cut the
      zoom-toggle block (currently `:128-140` — the comment plus the
      `if local, ok := localZoomed(...)` statement) and paste it **verbatim**
      immediately after `applyLayout(cfg, w, L)` at `:94`, before the
      `FrameResize` loop.
- [ ] Do not change the condition, the target expression, or the error handling.
      It stays `localZoomed(cfg, w.localWin)` compared against `zoomed`, firing
      only on a mismatch (#420), targeting
      `localPaneAt(w, indexOf(newRemote, remoteActive))`.
- [ ] Extend the moved block's comment with the *reason* it now sits here: the
      seeds below are painted at the pane's current geometry, so the zoom is
      part of the reshape and has to land before them (#511) — and it must stay
      **after** `applyLayout`, because `select-layout` unzooms the window it
      shapes.
- [ ] Update the reseed block's own comment only if it now reads wrong; do not
      rewrite it for style.

Acceptance: `git diff` for this step is a move plus comment text, nothing else.

## Step 2: give a zoomed pane the layout cell it actually occupies

- [ ] In the `FrameResize` loop (`reconcile.go:97-101`), when `zoomed` and the
      pane's id equals `remoteActive`, send `(L.W, L.H)` instead of
      `(L.Panes[i].W, L.Panes[i].H)`.
- [ ] Name the locals `pw, ph`, not `w, h`. Nothing in the loop body references
      the `w *mirrorWindow` parameter today, so a shadow would compile and be
      harmless — but it puts a trap one line away from anyone who later reaches
      for `w` there.
- [ ] Comment it with the fact that justifies it: while zoomed,
      `#{window_layout}` reports the *saved* geometry, so `L.Panes[i]` names a
      cell the pane has left; `#{window_visible_layout}` shows the zoomed pane's
      cell is the layout root, which is `(L.W, L.H)`.
- [ ] Leave every other pane on `L.Panes[i]`: tmux's zoom drops the other panes
      from the layout without resizing them, so their capture is still at that
      size.
- [ ] Do not touch `readLayout`. No `#{window_visible_layout}` (#413).

Acceptance: the loop is the only hunk; `newRemote` is still the iteration
source, so the rule cannot select a float (#409-safe).

## Step 3: Go ordering test — zoom before the capture that feeds the seed

- [ ] Add `TestReconcileZoomsBeforeReseeding` to
      `picker/remotebridge/daemon/reconcileordering_test.go`, beside the
      existing seed-ordering test.
- [ ] Build one ordered event log written by two seams that both run on
      `reconcileLayout`'s own goroutine, so the assertion has no async in it:
      `Config.LocalTmux` appends each local argv, and the `stream`'s writer
      (via `newRoundTrip(..., newStream(logWriter))`) appends each batch of
      control commands. Call `reconcileLayout` **synchronously**, not in a
      goroutine.
- [ ] Register a real sink so the reseed actually issues `capture-pane`
      (`newOutputSink` over a `net.Pipe`, with the peer drained by
      `go io.Copy(io.Discard, peer)` so the pump never blocks).
- [ ] Drive it with a zoom-only change: same `L.Raw` as `w.layout`, remote zoom
      flag `1`, `LocalTmuxOut` returning `0` so `localZoomed` disagrees and the
      toggle fires.
- [ ] Add a sibling of `scriptedRTRouter` that takes the log writer, rather than
      editing `scriptedRTRouter` itself (it hardcodes `testStream()` =
      `newStream(io.Discard)`). Keeps the diff off lines #409 may touch.
- [ ] The script needs **4** reply blocks for a zoom-only pass with one sink:
      `readLayout`, `PaneSeeds` cursor, `PaneSeeds` capture, trailing
      `readLayout` — mirroring `reconcileordering_test.go:58-66`.
- [ ] Assert the index of the first log entry containing `resize-pane` + `-Z` is
      **less than** the index of the first containing `capture-pane`, and assert
      **both were found**. An implementation that leaves a missing entry at `-1`
      would otherwise pass vacuously when no capture is issued at all.
- [ ] Cover the **unzoom** direction too (the design's Goal 3 is both): a second
      case, or a table row, with the remote flag `0` and `LocalTmuxOut`
      returning `"1\n"` so `localZoomed` disagrees the other way. ~3 lines.
- [ ] Comment that this deliberately over-constrains: the invariant is
      zoom-before-`FrameSeed`, which zoom-before-`capture-pane` implies, and the
      stricter form is the one assertable without racing the sink pump.

Acceptance: passes on the new code; **fails** on the old (step 6).

## Step 4: Go dims test — the zoomed pane gets the layout root

- [ ] Add `TestReconcileGivesZoomedPaneTheWindowDims` (same file or a new
      `reconcilezoom_test.go`, whichever keeps the diff smaller against #409).
- [ ] Two-pane layout, remote zoomed, `remoteActive` = the first pane. Register
      a sink per pane over `net.Pipe`s. Pick a layout whose pane-0 cell differs
      from the root on at least one axis and say so in a comment, so the
      assertion cannot be vacuous — e.g.
      `4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}`: root `190x45`, cell `95x45`.
- [ ] Read frames off each peer and assert with `wire.DecodeResize`: the zoomed
      pane's `FrameResize` carries the layout root's `WxH`; the other pane's
      carries its own cell.
- [ ] The goroutine-free shape **is** available and is the one to use: call
      `reconcileLayout` synchronously to completion, then read each peer — the
      sink pumps park in `wire.WriteFrame` until read, and `enqueue` is
      non-blocking over a 4096-deep channel, so nothing deadlocks. Do not add a
      read goroutine.
- [ ] `SetDeadline` every pipe read unconditionally. Without it a dims
      regression hangs to the package timeout instead of failing.
- [ ] The script needs 6 reply blocks (`readLayout`, two panes × cursor+capture,
      trailing `readLayout`). A short script cannot hide the assertion anyway —
      the `FrameResize` frames are enqueued before `PaneSeeds` is called at all —
      but state it so the asymmetry with step 3 doesn't read as an oversight.

Acceptance: passes on the new code; **fails** on the old (step 6).

## Step 5: m2 integration test — the mirror's content matches after a zoom

- [ ] Add one `@test` to `tests/remote-m2-integration.bats`, modelled on the
      content test at `:430` (which ends in `[ "$dst_screen" = "$src_screen" ]`),
      **not** on the flag/dims-only zoom test at `:967`.
- [ ] **Append it at the end of the file, after the reconnect family.**
      WORKER_TASK's known-flake allowance is stated by test *number* (36-41 on
      aarch64-darwin, `:1941`-`:2247`); inserting ahead of them shifts the
      numbering and the allowance silently starts pointing at the wrong tests.
- [ ] Pane mapping, stated once because getting it wrong makes the test vacuous.
      Both servers run `pane-base-index 1` (`setup()`, `:23-33`); the daemon
      stamps `0` on mirror windows. `split-window -v` puts the NEW pane at
      layout index **1** (measured: original `%0` is the top cell, index 0). So:

      | | SRC (remote) | DST (mirror) | layout index |
      |---|---|---|---|
      | original interactive pane | `rem:1.1` | `host-sess:1.0` | 0 |
      | new content pane | `rem:1.2` | `host-sess:1.1` | 1 |

- [ ] Create the content pane with an **explicit command**, spelled out — no
      `send-keys` into a prompt (the pane's shell is whatever `default-shell`
      resolves to, and fish has bitten this repo repeatedly), and no ellipsis:

      ```
      $SRC split-window -v -t rem \
        "sh -c 'printf \"\\033[?1049h\"; i=1; while [ \$i -le 40 ]; do printf \"ZOOMFILL_%02d\\n\" \$i; i=\$((i+1)); done; sleep 300'"
      ```

      Every part earns its place: `\033[?1049h` puts the pane on the alternate
      screen (no history, no reflow — the only grid a wrong-geometry paint
      damages permanently); 40 lines is more than the ~11-21 row unzoomed pane
      holds, so the surviving content fills it completely; the `while` loop
      avoids depending on `seq`; and `sleep 300` is required because **`SRC_CONF`
      sets no `remain-on-exit`** (`:33` — only `DST_CONF` does), so a command
      that finishes destroys the remote pane, changes the layout and breaks the
      2-pane mirror mid-test. Bounded, not `sleep infinity`, which BSD `sleep`
      on the darwin leg does not reliably accept.
- [ ] `$SRC select-pane -t rem:1.1` **before** `bridge_up`. `split-window` leaves
      the new pane active, and `bridge_up` gates by `send-keys`-ing a marker to
      the active pane (`:740`); the content pane reads no input, so only the tty
      echo would satisfy the gate — painting the marker into the very alternate
      grid this test later byte-compares.
- [ ] `bridge_up 2 <tag>`, then gate on the fill having crossed the bridge before
      zooming: poll `$DST capture-pane -p -t host-sess:1.1` for `ZOOMFILL_40`
      on a **bounded** `for _ in $(seq 1 60); … sleep 0.15` loop like every other
      poll in this file, and fail the case explicitly with a diagnostic if the
      marker never lands. Unbounded, it would hang the whole suite instead of
      failing one test — and "the fill never crossed" is a completely different
      bug from "the zoom didn't repaint".
- [ ] While gated, assert the mirror already matched **before** the zoom
      (`[ "$dst_screen" = "$src_screen" ]` on the pre-zoom captures), so a red
      result is unambiguously about the zoom rather than a wrong initial seed.
- [ ] Zoom the **content** pane: `pane="$(remote_pane_of 1)"`, then
      `$CTL --sock "$sock" zoom "$pane"`. Zooming `remote_pane_of 0` would zoom
      the untouched interactive shell on the **main** screen, which pulls its
      scrolled lines back out of history as it grows — the test would pass
      against unfixed code.
- [ ] Poll on the house `60 × 0.15s` budget for all four together: remote zoom
      flag 1, local zoom flag 1, matching `sorted_dims`, and
      `dst_screen == src_screen`. Re-read **both** captures inside the loop
      (`rem:1.2` and `host-sess:1.1`) — the `:430` template reads them once,
      after polling on dims only.
- [ ] Capture the screens before killing the daemon; teardown takes the DST
      session down with it.
- [ ] Comment the two subtleties a later "simplification" would silently break:
      the `sh -c` wrapper is what makes the fill shell-independent, and bash
      `$(...)` strips trailing blank rows from **both** captures, so the equality
      is effectively over content rows — nobody should "fix" that with `-J` or a
      sentinel line.
- [ ] Keep it one test. This file is a known load-correlated flake source; a
      second case buys nothing the first does not.

Why this fails pre-fix, measured rather than assumed: the zoomed remote pane's
`capture-pane -e -p` returns one line per row **including trailing blanks**
(measured: 23 lines for a 23-row pane holding 10 content rows). Painted into the
still-unzoomed local pane those overflow rows scroll the content off a
historyless alternate grid, and the later `resize-pane -Z` grows it into blanks
with nothing to pull back.

## Step 6: prove every new test non-vacuous

- [ ] Revert steps 1-2 in the working tree only (keep the tests), run the new
      tests, and capture the real output showing each **failing**. Reverting
      those two hunks is sufficient and safe: they are the whole behavioural
      change, and the tests reference no symbol the revert removes. Use a
      temporary WIP commit or a tagged `git stash push -u -m "<tag>"` — never a
      bare `git stash`, the stack is shared with other worktrees.
- [ ] Run the bats case directly rather than through the derivation:
      `bats tests/remote-m2-integration.bats -f "<test name>"` with a
      **next-3.8** `tmux` first on PATH (the devShell ships no tmux at all,
      `flake.nix:157-168`, and `remote-m2-integration-tests` deliberately pins
      `mkTmux` next-3.8 rather than nixpkgs' 3.7b, `flake.nix:807-817`).
      Leaving `DAEMON`/`RENDERER`/`CTL` unset makes `setup()` `go build` them
      (`:37-48`).
- [ ] The red evidence for the bats case must be the specific
      `not ok <n> <test name>` line — **not** a non-zero exit code. This
      derivation is a known "all-ok TAP, non-zero exit" flake under load, so an
      exit status alone says nothing about this test.
- [ ] Restore the fix and confirm every new test passes.
- [ ] Paste both directions' output in the PR body. A test not shown red before
      the fix is not evidence.

Acceptance: a red-then-green pair for **every** new test (step 3 contributes
two cases, zoom and unzoom), with output.

## Step 7: gate, rebase, push

- [ ] `cd picker && go test -race -count=5 ./remotebridge/...` — whole packages,
      no `-run` filter (a filtered run hid a real data race on #510).
- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` — three
      separate commands, each redirected to a file and its **bare** exit status
      read. Never a pipe's status; treat `(cancelled)` as a failure tell.
- [ ] Commit the spec and this plan under `docs/superpowers/` alongside the code.
- [ ] `git fetch origin && git rebase origin/main` immediately before pushing.
      If #409 landed, re-run the **full** gate after the rebase — a clean rebase
      can still be semantically broken by a refactor underneath it (#420).
- [ ] If the rebase conflicts in the pass loop, resolve so **both** behaviours
      survive: floats reconciled separately from the tiled diff, **and** the
      zoom sequenced before the reseed; dims rule stays scoped to `newRemote`.
      If not confidently resolvable, stop and report.
- [ ] `/deslop`, then push. PR body carries `Closes #511`, the red-then-green
      evidence, the honest verification claim, the named follow-up gaps, and any
      unresolved review notes.

## Out of scope (named in the design, not fixed here)

- `setupWindow` discarding the remote zoom flag (`daemon.go:1004`) — a mirror
  opened on an already-zoomed window is seeded unzoomed. Recommend an issue; not
  filed by this worker (ticket writes need explicit approval).
- The `ops.Reset` / `errLocalPanesDesynced` early returns, which seed via
  `setupWindow` and return before the toggle at either position.
