# Plan: pipeline control-mode round-trips (#430)

Design spec: `docs/superpowers/specs/2026-08-31-pipeline-control-mode-round-trips-design.md`.
Read it first — it owns the *why*, especially "The ordering hazard this design is
built around". This plan is the *how*, in order.

All paths are relative to `picker/remotebridge/`. Build/test after every step:

```
cd picker && go build ./... && go test ./remotebridge/...
```

---

- [ ] **Step 1: widen the round-trip seam to a batch that yields replies incrementally** (implement: opus)

  - Replace the `roundTrip` type in `daemon/seed.go` with the two aliases from
    spec §1:
    ```go
    type replies = func() (controlmode.Line, bool)
    type roundTrip = func(cmds ...string) replies
    ```
    Carry the spec's contract into the doc comment — the six bullets, and
    especially the **no-reentrancy** rule (a nested round-trip eats the
    in-flight batch's remaining replies and hangs the main loop).
    Add one more contract line the spec leaves implicit: **`rt` always returns a
    callable iterator.** A failed `stampAll` must yield one that is immediately
    `(controlmode.Line{}, false)`, never `nil` — a `nil` return compiles and
    then nil-derefs in `one()` on the teardown race (`st.close()` against an
    in-flight ctl round-trip), which no test covers.
  - Add `stream.stampAll(cmds ...string) (seqs []uint64, ok bool)` in
    `daemon/daemon.go`: one `s.mu.Lock()` for the whole batch, `fmt.Fprintf` +
    `s.sent++` per command, **`s.w.Flush()` before returning** (load-bearing:
    Step 2's write-order test observes the command lines on the writer before
    the first read). Zero commands ⇒ `(nil, true)`, nothing written. Document
    that the lock is never held across a read, which is what keeps
    `pumpInput`/ctl/`watchResize` deadlock-free.
  - Reduce `stamp` to a wrapper over `stampAll`; leave `send`, `claim`, `close`
    alone.
  - Rewrite `Run`'s `rt` closure (`daemon.go:264-271`) to `stampAll` + an
    index-advancing `next()` over
    `readReplyRouting(reader, router, async, st, seqs[i])`. `next()` bound-checks
    `i >= len(seqs)` and returns `(controlmode.Line{}, false)` — "at most
    `len(cmds)` calls" is a caller rule, and an over-call would otherwise index
    out of range on teardown paths nothing tests.
  - Add `func one(rt roundTrip, cmd string) (controlmode.Line, bool) { return rt(cmd)() }`.

  **Eleven call sites take `one(rt, …)` in this step, in two groups.**

  *Nine that keep it permanently:* `daemon.go` 277 / 557 / 632 / 828,
  `agentstatus.go` 98 / 216, `sessionpin.go` 39 / 62, `reconcilewindows.go` 25.

  *Two temporary shims:* `seed.go:44` (`readCursor`) and `seed.go:63`
  (`readCapture`). These are single-command `rt(cmd)` calls too, and without the
  shim this step does not compile (`assignment mismatch: 2 variables but rt(...)
  returns 1 value`) — `PaneSeed` is not rewritten until Step 2. Wrap both with
  `one(rt, …)` here so Step 1 builds and the four existing `TestPaneSeed*` cases
  stay green; **Step 2 deletes both** when `PaneSeed` is reimplemented on
  `PaneSeeds`.

  **`daemon.go:557` is the trap.** It is `rt(ConvergeCmd(...))` with the result
  discarded; under the new seam that still compiles and silently stops awaiting
  the converge. It becomes `one(rt, ConvergeCmd(mw.remoteID, w, h))`, result
  still discarded.

  **Update all five test `rt` builders.** `roundTrip` is a type *alias*, so every
  function literal of the old signature stops being assignable the moment the
  alias changes — and `go build ./...` does not compile test files, so these
  surface only at `go test`. The exhaustive check is
  `grep -rn 'controlmode.Line, bool' picker/remotebridge/daemon/*_test.go`; trust
  that grep, not this list:

  - `daemon/seed_test.go:19` — `testRoundTrip`
  - `daemon/sessionpin_test.go:21` — `scriptedRT`
  - `daemon/daemon_test.go:231` — inline, in `TestPauseContinueReseedsBeforeResumingOutput`
  - `daemon/reconcilewindows_test.go:31` — `rt := func(string) (controlmode.Line, bool) { return c.reply, c.ok }`,
    passed to `reconcileWindows` at line 34. Becomes
    `func(...string) replies { return func() (controlmode.Line, bool) { return c.reply, c.ok } }`.
  - `daemon/reconcileshape_test.go:68` — `rt := func(string) (controlmode.Line, bool) { return controlmode.Line{}, false }`,
    passed to `applyPaneOps` at line 71. Same shape, returning
    `controlmode.Line{}, false`.

  Both replacement iterators return the same reply on every call and never latch
  `ok=false`. Fine for their single `one(rt, …)` use; do not copy that shape into
  any multi-command helper.

  `daemon_test.go`'s script places `%output %2 sibling` **before** the first
  `%begin` block. It still exercises B3 under the new seam — the output is routed
  during the first `next()` — so confirm rather than edit the script.

  *Done when:* `go build ./...` clean, `go test ./remotebridge/...` green with
  no test-expectation edits beyond the rt-builder shape. Behavior-preserving.

- [ ] **Step 2: `PaneSeeds`, with `PaneSeed` on top of it**

  `daemon/seed.go`:

  - ```go
    func PaneSeeds(rt roundTrip, paneIDs []string, onSeed func(i int, seed []byte, err error))
    ```
    Build `2N` commands **interleaved per pane** (`dm(A), cap(A), dm(B), cap(B), …`
    — spec §4: per-pane cursor/capture coherence), one `rt(cmds...)`, then per
    pane read its two replies and call `onSeed` **before** reading the next
    pane's. On stream loss mid-batch, still call `onSeed` for every remaining
    pane with a stream-loss error. `paneIDs == nil` ⇒ no commands, no calls.
  - Reimplement `PaneSeed` as the N=1 case (signature unchanged) so the
    reply-pairing logic exists once. **Delete the two Step 1 shims**: rework
    `readCursor`/`readCapture` into parsers over an already-read
    `controlmode.Line` rather than round-trippers.
  - Keep the parsing rules exactly: a `%error` capture is the only rejection
    signal (keyed on `Kind`, not body length — a blank pane is a valid seed); an
    errored `display-message` degrades to `(0,0,false,false)`.

  Tests in `daemon/seed_test.go`:

  - **Write-order (AC1).** A real `stream` over a recording `io.Writer`, read by
    a gating reader that yields the scripted bytes only once **both** command
    lines have been observed on the writer and **returns EOF otherwise** — EOF,
    not a block: writes and reads share one goroutine, so withholding bytes
    would hang the test instead of failing it. On a regression the premature
    `Read` returns EOF, the scanner latches done, and `PaneSeed` errors.
  - `PaneSeeds` index alignment, and a per-pane `%error` isolated to that pane
    while its siblings still get seeds.
  - Keep the four existing `TestPaneSeed*` cases passing.

- [ ] **Step 3: split `seedRenderer`, batch `setupWindow`** (implement: opus)

  Tagged because it is the only step with **no test that reaches it** —
  `setupWindow` needs `LocalTmux`, `LocalTmuxOut`, `connCh` hellos and
  `spawnRenderer`, so it has no unit test — while it has to carry a *fatal
  return* across a newly introduced callback boundary and a filtered index
  mapping.

  `daemon/daemon.go`:

  - Extract `wireRenderer(router, conn, remotePane, seed []byte, err error, dims, gfx) bool`
    holding the current failure half of `seedRenderer` (`daemon.go:941-953`)
    verbatim — stderr log, `conn.Close()`, return false — and the success half:
    `newOutputSink` → `router.Register` → `enqueue(FrameSeed)` →
    `enqueue(FrameResize)`. **The FIFO order is the invariant; do not reorder.**
  - `seedRenderer` keeps its signature and becomes `PaneSeed` + `wireRenderer`.
  - `setupWindow` (`daemon.go:602-618`): build `paneIDs` from the panes of
    `mw.remotePanes` that have a live conn (today's `conn == nil` → `continue`),
    plus a parallel slice of their indices into `mw.remotePanes`. One
    `PaneSeeds`. In `onSeed`, call `wireRenderer` with `L.Panes[idx]` and
    **record** the outcome — do not start pumps and do not try to abort there.
  - After `PaneSeeds` returns: start `pumpInput` for each pane that wired, and
    apply the two behaviors that belong to `setupWindow` and cannot live in the
    callback — `delete(mw.conns, remotePane)` for each failure, and the **fatal**
    `return fmt.Errorf("daemon: seed failed for sole pane %s")` when
    `len(mw.remotePanes) == 1` and that pane failed. A callback cannot return
    from its caller. That fatal return is what makes
    `addWindow`/`mirrorNewWindow` tear down a half-created mirror window
    (`daemon.go:657-665`, `reconcilewindows.go:85-94`); losing it leaves a live
    blank mirror window and a stale registry entry.
  - Starting the pumps after the batch rather than between seeds is a
    deliberate, benign change — no renderer keystroke can stamp a command
    mid-batch during setup. State that in the code comment, and make sure the
    code actually holds it (pumps start after `PaneSeeds` returns, nowhere else).

- [ ] **Step 4: batch the three re-seed consumers**

  Straight substitutions — each loop body moves into `onSeed` unchanged
  (enqueue on success, stderr on error), with nil-sink panes filtered out of
  `paneIDs` **before** the batch so no command is issued for a pane nobody will
  seed. Each already has a single-pane regression test that must stay green:

  - `reconcile.go:77-87` — `reconcileLayout`'s post-reshape re-seed
    (`reconcilereseed_test.go`). The hottest path, and the one the ordering
    hazard was found on.
  - `sessionpin.go:76-91` — `sessionPin.reseed`, across every pane of every
    registered window (`sessionpin_test.go`).
  - `daemon.go:788-801` — `reseedDropped` over `router.dirtyPanes()`
    (`dropreseed_test.go`).

  Unlike Step 3, these three need **no parallel index slice**: the filtered
  `paneIDs` slice *is* the index space, so `onSeed`'s `i` indexes it directly.

  Filter only the **re-seed** loop. In `reconcileLayout` the preceding
  `FrameResize` loop (`reconcile.go:61-65`) must stay exactly as it is — it walks
  `newRemote` with the layout index `L.Panes[i]`, and a filtered slice would
  misalign those dims.

  Leave `reconcileLayout`'s **append** path (`reconcile.go:216-231`) on
  per-pane `seedRenderer`: bounded by one notification's added panes and
  interleaved with `collectHellos`. Leave `handleContinue`
  (`daemon.go:762-772`) alone too — it is a single-pane `PaneSeed` caller and
  already gets 2 RTT → 1 from Step 2; converting it to `PaneSeeds` is not part
  of this step.

  The `sink == nil` guard inside each moved-over loop body becomes dead once the
  panes are pre-filtered out of `paneIDs`. Drop it — the filter is the guard.

  The invariant to preserve: every one of these panes **already has a registered
  sink**, so a seed enqueued after a later pane's reply was read would repaint
  over output the renderer already painted. `onSeed` must fire between panes;
  nothing in it may read the control stream.

- [ ] **Step 5: the ordering regression test (AC3)**

  New test in `daemon/` (alongside `reconcilereseed_test.go`): a **two-pane**
  `reconcileLayout` whose scripted stream emits `%output` for pane A *after*
  A's `capture-pane` reply and before pane B's replies. Assert pane A's sink
  receives its `FrameSeed` **before** that `FrameOutput`. Observable because
  `sink.Write` and `sink.enqueue` share one channel, so the peer's frame order
  is enqueue order.

  Scan the pane's frames for the ordering rather than reading frame #1:
  `reconcileLayout` enqueues a `FrameResize` for every pane
  (`reconcile.go:61-65`) *before* the re-seed loop, so A's peer sees
  `FrameResize` first (as `reconcilereseed_test.go:52-65` already accounts for).

  This needs an `rt` helper that routes through the **caller's** router —
  `scriptedRT` (`sessionpin_test.go:21`) builds its own `NewRouter()`
  internally, so scripted `%output` would route into a router the test never
  observes. Add a router-sharing variant rather than mutating `scriptedRT`'s
  callers.

  Scaffolding: the same shape `reconcilereseed_test.go` uses — `cfg.LocalTmux`
  **and** `cfg.LocalTmuxOut` (the non-structural path calls `localZoomed`), plus
  a script covering `readLayout`, four seed replies, and the trailing re-read.

  Verify the test is a real discriminator: it must fail against a
  read-all-replies-then-wire implementation.

- [ ] **Step 6: pipeline M1 `seedFlow`**

  `main.go:182-199`: issue `display-message` and `capture-pane` together, then
  read both replies in issue order via the existing `readReply` skip reader —
  **which reader runs here does not change** (B3). `list-panes` keeps its own
  wait (it resolves the pane id the other two target) and `refresh-client` keeps
  its own wait (batching it would make the capture's size depend on tmux having
  applied a client resize by the time the next queued command runs).

  Refactor `main.go`'s `readCursor`/`readCapture` into parsers over an
  already-read `controlmode.Line`, so both reads happen at the call site in
  order. Preserve each one's current handling of the reply verbatim:
  `readCursor` today checks `ok` **and** `Kind == Error` and degrades to
  `(0,0,false,false)`; `readCapture` today ignores `ok` and returns `l.Data`
  whatever it is. Keep that asymmetry — narrowing or widening it is a behavior
  change this step is not making.

  Test in `remotebridge/seed_test.go`: the two existing cases
  (`TestSeedFlowTTYAlignsRepliesWithCommands`,
  `TestSeedFlowNonTTYSkipsRefresh`) hold the whole script in a `strings.Reader`
  and would pass **before and after** the change, so extending them proves
  nothing. Add a write-order case gated on the test's own `sent` slice (M1
  supplies both `send` and `reader`).

  **The gate must be staged, not blanket.** `seedFlow` reads the `list-panes`
  reply — and, on the tty path, the `refresh-client` reply — *before* either
  pipelined command is sent, and `controlmode.NewReader` wraps a `bufio.Scanner`
  whose `Scan()` **latches done permanently on EOF**
  (the latch is `bufio.Scanner`'s own `done` flag; `NewReader` is
  `controlmode/parse.go:148-151`). A reader that returns EOF until both
  `display-message` and `capture-pane` are in `sent` therefore kills the scanner
  on `readActivePane`'s first read: `pane` comes back empty and `seedFlow` fails
  with `no active pane for …` against a *correct* implementation too. So: serve
  the script freely up to and including the `list-panes` reply (plus the
  `refresh-client` reply on the tty case), and only then return EOF until both
  pipelined command lines have been recorded. Never return EOF before the
  pre-pipeline replies have been consumed.

  (Step 2's version of the trick needs no staging — `PaneSeed`'s script contains
  nothing ahead of the gated pair.)

- [ ] **Step 7: docs + commit**

  - One bullet in `CLAUDE.md`'s "Key Conventions" for the new invariant: a
    batched round-trip must deliver each pane's result before reading the next
    pane's reply, because `readReplyRouting` routes `%output` into registered
    sinks as it walks reply blocks. Add one clause noting the batch is bounded
    by one session's panes — orders below the transport buffer — so writing it
    without interleaved reads cannot wedge on backpressure. Match the
    surrounding bullets' density: one tight paragraph, not a section.
  - Commit the spec and this plan alongside the code (repo convention:
    `docs/superpowers/{specs,plans}` ship in the same PR).

---

## Verification

1. `cd picker && go build ./... && go test ./...`
2. `nix build .#default`
3. `nix flake check` — includes `go test -race ./remotebridge/...` and both
   offline bats bridge integration tests (`remote-bridge-integration.bats` for
   M1, `remote-m2-integration.bats` for the daemon).
4. `nix build .#lint`

## Review-only gates (no test reaches them)

`setupWindow` has no unit test, so two acceptance items are verified by reading
the diff, not by a run, and must be checked explicitly at review:

- a failed **sole-pane** seed still returns an error from `setupWindow`;
- the converge at `daemon.go:557` is still **awaited** (`one(rt, …)`, not a
  bare `rt(…)`).

## Out of scope (from the spec, restated so no step drifts into it)

Cross-window batching at startup; batching converge + `readLayout` within a
window; `reconcileLayout`'s append path; the genuinely single-command
round-trips in `agentstatus.go`, `sessionpin.go`'s id read, and `list-windows`.
