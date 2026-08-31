# Drain the control stream during the renderer hello wait (#434)

`reconcileLayout` -> `applyPaneOps` waits up to `helloTimeout` (10s) for the
renderers it just spawned to dial back:

```go
added, err := collectHellos(connCh, len(ops.Append), helloTimeout)
```

`collectHellos` selects over `connCh` and a deadline only. It runs on the main
loop — the single goroutine that reads the control stream — so for the whole
wait nobody calls `reader.Next()`. The remote's output backs up; with
`pause-after` armed tmux `%pause`s every busy pane, and one slow renderer spawn
turns into a mass `%pause`/`%continue` re-seed churn once the loop comes back.

The same hazard applies to every hello wait that runs while the main loop owns
the stream: `addWindow` (dispatched from the main loop on `%window-add`) and
`reconcileWindows` -> `mirrorNewWindow` (called from `settle()`, not only at
startup) reach `setupWindow`'s `collectHellos` on exactly the same live stream.
Only `Run`'s own startup pass has no live stream behind it.

## Design

`reader.Next()` blocks, so a `select` cannot include it. Rather than a
per-wait goroutine handoff — which has to hand the stream back with a read
possibly still in flight — move the read off the main goroutine **permanently**:

- `ctlPump` owns `controlmode.Reader` for the daemon's life and is the only
  caller of `Next()`, feeding a buffered `chan controlmode.Line`.
- Every existing consumer (`nextLine`, `readReplyRouting`, the main loop) reads
  from the pump instead of the reader. There is exactly one reader goroutine,
  always, so "hand the stream back cleanly" is satisfied by construction — the
  stream is never handed anywhere.
- A hello wait can now `select` over `connCh`, the pump's channel and the
  deadline, handling each line exactly as `readReplyRouting` handles a line that
  answers nobody: `%output` -> router (B3), other notifications -> `asyncQueue`,
  a client-flagged `%end`/`%error` -> `stream.claim()` so reply ordinals stay in
  issue order.

Buffering lines is safe: every `controlmode.Line.Data` is freshly allocated
(`Unescape` builds a new slice, the block reader does `[]byte(strings.Join(...))`),
so nothing aliases the scanner's buffer. `controlmode` itself needs no change —
`Reader.Next() (Line, bool)` already satisfies the `lineReader` interface below.

Ordinal accounting is unaffected by the read-ahead: `stream.claim()` stays on
the *consumer* side, every consumer is the main goroutine, and a reply block can
never precede the `stamp()` that asked for it.

### Deviation from the task doc, called out

The task doc scopes the change to `reconcileLayout` and sanctions leaving
`setupWindow` on the blocking collector. This plan instead replaces
`collectHellos` with the draining waiter at **every** call site (a 1:1 parameter
swap, `connCh chan helloConn` -> `waitHellos helloWaiter`). Reasons:

1. The doc's premise ("startup's `setupWindow` — no live stream yet") does not
   hold for two of `setupWindow`'s four callers: `addWindow` and
   `mirrorNewWindow` both run from the main loop on a live stream. A fix scoped
   to `reconcileLayout` leaves the same bug in them.
2. The stream regime is not statically determinable at `setupWindow`, so two
   collectors would force every caller to know which one it is in.
3. Startup behaviour is unchanged: draining there routes to the sinks of
   already-mirrored windows (strictly better than leaving the bytes in the
   socket) and queues notifications the main loop takes on its first `settle()`
   — exactly what the `rt` round-trips interleaved with setup already do.

## Steps

- [ ] **Step 0: confirm the base (stacking rule)**
  - `WORKER_TASK.md` requires basing on
    `origin/feat/431-dedup-and-debounce-layout-change-re-seed` (PR #438).
  - The worktree is **already reset onto it** (`HEAD` = `c352100`, and
    `coalesceLayoutChanges` + `reconcilededup_test.go` are present). Verify with
    `git merge-base --is-ancestor origin/feat/431-... HEAD` and do **not**
    re-run `git reset --hard` — that would discard work committed here.
  - Only if that branch is gone upstream (#438 squash-merged): rebase on
    `origin/main` and base the PR there instead.

- [ ] **Step 1: introduce `ctlPump` and make the line source an interface**
  (`picker/remotebridge/daemon/daemon.go`) (implement: escalated)
  - Add `type lineReader interface { Next() (controlmode.Line, bool) }` and
    widen `nextLine` / `readReplyRouting` to take it. `*controlmode.Reader`
    satisfies it, so the existing tests that pass one keep compiling.
  - Add `ctlPump`: `startCtlPump(*controlmode.Reader) *ctlPump` spawns the one
    reader goroutine, which loops `Next()` into a buffered channel and closes it
    on stream EOF. `(*ctlPump).Next()` receives from that channel, so the pump
    is itself a `lineReader`.
  - Buffer depth: a named const whose comment says what it actually buys —
    slack across every stretch where the main loop is busy (`LocalTmux` execs,
    shaping, seeding, the hello wait), sized to cover a `pause-after` age
    window rather than merely "one line of lookahead". Once full the pump
    blocks on send, which is today's backpressure.
  - Document that the goroutine ends on stream EOF and otherwise dies with the
    process: `teardown`'s `cfg.Ctl.Close()` closes only the ssh stdin, so a pump
    parked on a send is not woken by it — harmless, since `Run` returning means
    the process is exiting.
  - `Run` builds the pump right after `controlmode.NewReader` and passes it
    everywhere `reader` went (`rt`'s `readReplyRouting`, the main loop's
    `nextLine`).

- [ ] **Step 2: factor the two per-line decisions out of `readReplyRouting`**
  (`daemon.go`)
  - `claimSeq(l, st) uint64` — the ordinal claim currently inline in `nextLine`.
  - `handleAsideLine(l, router, async)` — route `%output`, drop `Other` and
    already-claimed reply blocks, queue everything else. (`Begin` never reaches
    a consumer: `Reader.Next` folds it into `readBlock`.)
  - Rewrite `readReplyRouting` in terms of both, with no behaviour change.

- [ ] **Step 3: the draining hello waiter** (`daemon.go`) (implement: escalated)
  - `type helloWaiter func(n int) (map[string]net.Conn, error)`.
  - `waitHellos(p *ctlPump, router, async, st, connCh, n, timeout)`: same
    contract as `collectHellos` (read exactly `n` conns, close what it collected
    and error on timeout) with two added `select` arms — a pump line, handled via
    `claimSeq` + `handleAsideLine`, and pump-channel close, which closes the
    collected conns and errors.
  - Preserve `collectHellos`'s counting semantics exactly: count **connections
    received**, not `len(out)`, so two hellos naming the same pane still end the
    wait (a `len(out) < n` loop would hang).
  - Delete `collectHellos`.

- [ ] **Step 4: thread the waiter** (`daemon.go`, `reconcile.go`,
  `reconcilewindows.go`)
  - Replace the `connCh chan helloConn` parameter with `waitHellos helloWaiter`
    in `setupWindow`, `addWindow`, `reconcileWindows`, `mirrorNewWindow`,
    `reconcileLayout`, `applyPaneOps`, `resetWindow`. Call sites become
    `waitHellos(n)`.
  - `Run` builds the closure **after** `connCh := make(chan helloConn, 64)`
    (currently `daemon.go:305`, below where `rt` is defined) and before its
    first use in the startup `setupWindow` loop:
    `waitHellosFn := func(n int) (map[string]net.Conn, error) { return waitHellos(pump, router, async, st, connCh, n, helloTimeout) }`.
  - `connCh` then appears only in `Run` (creation, `acceptConns`, the closure).
  - Fix the two comments this falsifies: `helloTimeout`'s doc ("bounds how long
    collectHellos waits") and the `pauseAfterSet` rationale in the main loop
    ("setup does blocking collectHellos/seed round-trips without draining") —
    setup now drains during the hello wait, and the reason to arm `pause-after`
    late is that only `settle()` runs `handlePause`.

- [ ] **Step 5: tests** (`picker/remotebridge/daemon/`) (implement: escalated)
  - **Headline (proves the acceptance criterion):** drive `applyPaneOps` — not
    `waitHellos` alone — with a **real** draining waiter, so the reconcile path
    itself is pinned. Build on `reconcileshape_test.go`'s harness (`LocalTmuxOut`
    returning the post-split pane list, a `connCh` fed from a goroutine, a trace
    recorder): put the pump over an `io.Pipe`, register a signalling sink for a
    sibling pane, write `%output %<sibling> live` into the pipe, block until the
    sink fires, only **then** deliver the `helloConn`, and assert `applyPaneOps`
    returns nil and the sibling byte arrived. The ordering assertion is the
    proof: on a non-draining waiter the sink never fires and the test fails on
    its deadline.
  - **Unit companion:** `waitHellos` standalone — a client-flagged `%end` seen
    during the wait advances `stream.seen` by exactly one, so a later round-trip
    still recognises its own reply (issue order).
  - Rewrite `TestCollectHellosTimesOutWhenRenderersDontConnect` against
    `waitHellos`, keeping its deadline assertion. Feed the pump from an
    `io.Pipe` the test closes in a `defer` (so the pump goroutine exits) — not
    `strings.NewReader("")`, which would fire the pump-close arm instantly and
    make the deadline assertion vacuous.
  - Update the call sites in `reconcileshape_test.go`, `localpanes_test.go`,
    `reconcilededup_test.go`, `reconcilereseed_test.go` **and
    `reconcilewindows_test.go`** (line 33, `reconcileWindows(...)`) to the new
    parameter. Every case in `reconcilewindows_test.go`, and the reconcile cases
    that never append a pane, bail before any hello wait — a stub waiter
    (`func(int) (map[string]net.Conn, error) { return nil, nil }`) is enough
    there; no pump needed.

- [ ] **Step 6: gate and commit**
  - `cd picker && go test -race ./remotebridge/...` (matching `flake.nix`'s own
    `-race` invocation), then `cd picker && go test ./...`.
  - `nix build .#default`, `nix flake check`, `nix build .#lint`.
  - Stage the code **and** this plan document (`CLAUDE.md`: a substantial change
    commits its plan in the same PR). Commit from inside `nix develop`/direnv so
    the pre-commit hooks run.

- [ ] **Step 7: PR**
  - `gh pr create --assignee @me --base feat/431-dedup-and-debounce-layout-change-re-seed`
    (or `--base main` if Step 0 found #438 already merged).
  - Body carries `Closes #434`, a note that it stacks on #438, and the scope
    deviation above.

## Acceptance criteria

- [ ] A test drives the **reconcile** path (`applyPaneOps`) and proves live
      `%output` for a sibling pane is routed while the hello wait is in flight —
      the sink observes the byte *before* the hello is delivered.
- [ ] Reply ordinals still advance in issue order across a hello wait.
- [ ] Exactly one goroutine ever calls `controlmode.Reader.Next()`.
- [ ] Sink semantics untouched (no change to `outputSink`, drop-on-full stays).
- [ ] `go test -race ./remotebridge/...`, `go test ./...`, `nix flake check` and
      `nix build .#lint` green.
- [ ] PR based per the stacking rule (Step 0 / Step 7).
