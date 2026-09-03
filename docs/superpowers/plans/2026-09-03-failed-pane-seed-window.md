# Plan — a failed pane seed must not cost the mirror its window (#487 bug A)

Spec of record: `docs/superpowers/specs/2026-09-03-failed-pane-seed-window-design.md`
(R1–R6 and the acceptance list are normative; this plan is their transcription
into steps). Bug B is fixed on main by `fc6b615` (#489) — do not touch that
path's design.

## Shape of the fix

Two surgical changes, both in `picker/remotebridge/daemon`:

1. **A failed seed wires the pane unseeded instead of orphaning it** (R1, R2,
   R3, R5, R6) — `wireRenderer` stops closing the conn and registers the sink
   anyway; the trailing reseed in `reconcileLayout` (which already re-seeds
   every pane with a registered sink) repairs the pane in the same pass.
2. **`resetWindow` stops closing the conn of the pane it keeps** (R4) —
   mapping-free: close old conns only once their replacement is known.

Rejected alternatives (from the task doc, settled by the spec): retrying the
seed (the failing case is a *gone* remote pane — no retry fixes it, and the
trailing reseed already covers the pane moments later), and failing the whole
pane-add transaction (killing the local pane is the forbidden state — R2 —
since `refreshLocalPanes` regenerates `w.localPanes` from tmux wholesale).

## Steps

### 1. `wireRenderer` — wire unseeded on seed failure (`daemon/daemon.go`)

Current: on `err != nil` it logs `skipping renderer`, `conn.Close()`, returns
false. Change to:

- Register the sink with the router **first, unconditionally**.
- On seed error: log that the pane is wired unseeded and the trailing reseed
  will repair it, enqueue **only** `FrameResize` (dims from the layout cell),
  keep the conn open, return `false` (now meaning "unseeded", not "unwired").
- On success: unchanged (`FrameSeed` then `FrameResize`, return true).

Update the doc comment: state the R5 relaxation outright — for a pane whose
seed failed there is no seed, so its sink's first frame may legitimately be a
`FrameOutput` or `FrameResize`; the seed-before-output invariant binds a seed
and the output of the *same* pane, it does not require a seed to exist. The
pane starts blank, never stale, so #412 does not arise. The frozen-wire
invariant (no frame bypasses the sink) is unchanged.

### 2. `applyPaneOps` — a failed seed no longer unwires the pane (`daemon/reconcile.go`, append loop ~:305-318)

Current: `if seedRenderer(...) { go pumpInput(...) } else { delete(w.conns, id) }`.
Change to: start `go pumpInput(c, id, send)` for every appended pane
unconditionally, keep the `w.conns[id]` entry regardless of the seed verdict.
No `kill-pane` may be issued on this path (R2).

### 3. `setupWindow` — sole-pane fatality kept, self-disposing (`daemon/daemon.go`, seed loop ~:1046-1067)

Restructure the post-`PaneSeeds` loop:

- Non-sole pane with a failed seed: keep conn and sink, start `pumpInput` —
  same as step 2 (this branch is reachable from `resetWindow` on a live
  window, so it is co-primary, not a fallback — R6).
- Sole pane with a failed seed: still fatal, but dispose of itself now that
  `wireRenderer` registered the sink — `router.Unregister(remotePane)`,
  `mw.conns[remotePane].Close()`, `delete(mw.conns, remotePane)`, then return
  the existing `seed failed for sole pane` error. Do not start `pumpInput`
  for it.
- Seeded panes: unchanged.

### 4. `resetWindow` — never close a conn whose replacement doesn't exist yet (`daemon/reconcile.go:348-358`)

Current: unregisters every sink **and closes every conn** (including the kept
pane's), then `dropMirroredPanes`, then `setupWindow` — whose first act is a
`readLayout` round-trip, so the kept pane's renderer exits on EOF and tmux
destroys the window before the re-shape lands.

Change to:

- Unregister all sinks up front (unchanged — `Router.Register` overwrites
  without closing the old sink, so this stops the stale pumps; sink `Close`
  closes only the pump channel, never the conn).
- Save `w.conns` aside, hand `w` a fresh map.
- `dropMirroredPanes`, then `setupWindow` (unchanged).
- Afterward, per old conn: if `setupWindow` superseded it (same remote id now
  maps to a different conn) its renderer was already `respawn-pane -k`'d —
  close it. If setupWindow **succeeded**, close every remaining old conn (all
  renderers replaced). If it **failed**, merge un-superseded old conns back
  into `w.conns` and close nothing — the kept pane's old renderer is still
  that pane's live process, and closing its conn is the window death being
  fixed. Degraded (stale screen, live window), never silent death.

Mapping-free by construction (R4): no local→remote pane mapping is consulted;
the survivor's conn is kept because *every* unsuperseded conn is kept.

### 5. Tests — `picker/remotebridge/daemon/seedfailure_test.go`

Replace the scratch `repro487_test.go` (delete it; its two demonstrations
become assertions 1 and 4 below, inverted). Harness per the repro:
`scriptedRT` + `net.Pipe` renderer peers + fake `LocalTmux`/`LocalTmuxOut`
recording a command trace. One test per spec acceptance item:

1. **Seed failure on an incrementally added pane**: drive `applyPaneOps`
   (append `%2`, capture answers `%error`). On return: peer conn still
   writable, `router.sink("%2") != nil`, `w.conns["%2"] != nil`,
   `len(w.localPanes) == len(w.remotePanes)`, no `kill-pane` in the trace.
2. **Input still flows**: a `FrameInput` written by the fake renderer peer
   reaches `send` as `send-keys` for `%2`.
3. **The pane is in the trailing reseed set**: drive `reconcileLayout` with
   the post-reshape reseed scripted to succeed; a `FrameSeed` arrives at the
   peer after the reshape.
4. **`resetWindow` keeps the kept pane's renderer**: with `setupWindow`'s
   first round-trip scripted to fail, the kept pane's conn is still open
   after `resetWindow` returns; with it scripted to succeed, the old conn is
   closed only after the re-shape commands ran. No local→remote mapping
   consulted.
5. **`setupWindow` sole-pane failure**: error mentions `sole pane`; no sink
   stays registered; no conn stays open.

Not in `tests/remote-m2-integration.bats` — its DST server sets
`remain-on-exit on`, which makes it blind to this failure (spec §2).

### 6. Gate + docs

- `nix build .#default`, `nix flake check`, `nix build .#lint` — all green
  (redirect to files, read bare exit codes; a pipe hides a failed check).
- Spec + this plan committed under `docs/superpowers/{specs,plans}/` in the
  same PR.

## Verification traps (from the task doc — bind the execute agents)

- `tmux display-message -p -t @<dead>` exits 0 with empty output — compare the
  returned id, never the error.
- m2 integration tests are timing-sensitive and darwin-only flaky; a green
  local x86_64 gate can still fail on aarch64-darwin in CI.
- `remote-m2-integration-tests` has a pre-existing load-correlated
  non-zero-exit flake on main with green TAP — check against main before
  owning it.

## PR body requirements

- `Closes #487`.
- Separate what was established (spec §1–§3 + the measured tmux table) from
  what remains hypothesis (§4 — why `capture-pane` failed on `halo`).
- State which was achieved: the real race reproduced, or the failure injected
  (expected: injected — the race needs the `halo` remote and is not
  reproducible from this worktree).
