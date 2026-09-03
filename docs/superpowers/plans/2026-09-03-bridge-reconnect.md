# Plan: bridge daemon survives a control-connection drop (#482)

Design spec: `docs/superpowers/specs/2026-09-03-bridge-reconnect-design.md`.
Read it first — this plan does not restate its reasoning, only the work.

Structure follows the task's component decomposition. Each step below names its
component; step order respects the dependency ordering
(`transport-seam` ∥ `identity-check` ∥ `backoff` ∥ statusline half → `run-split`
→ `reattach-repair` → daemon stamp half → `offline-tests` → docs).

## Step 1: a bounded, cancellable retry schedule (component: `backoff`)

- [ ] New `picker/remotebridge/daemon/backoff.go`: a pure schedule — exponential
      delay from a small base to a ceiling, with jitter, bounded by **both** a
      max attempt count and a max total elapsed time. Clock and jitter source
      injected so the test is deterministic.
- [ ] A cancellable wait: `select` on the delay timer and a stop channel;
      returns whether it was cancelled. A nil stop channel never cancels.
- [ ] `backoff_test.go`: delays grow and clamp at the ceiling; the elapsed
      bound ends the schedule even when attempts remain, and vice versa; a
      closed stop channel returns immediately rather than sleeping.
- [ ] Do not touch `serverAliveInterval` / `serverAliveCountMax`. The keepalives
      are the detection window and stay as they are.

## Step 2: remote server identity (component: `identity-check`)

- [ ] In `sessionpin.go`, read the identity tuple in **one** round-trip:
      `display-message -p -t <session> -F '#{pid}|#{start_time}|#{session_id}'`.
      Pipe-delimited (never tab — #373). Fold it into the existing
      `newSessionPin` read so `session_id` is fetched once, not twice, and one
      place is the authority for "which session are we on".
- [ ] Parse strictly: `pid` a decimal integer and `session_id` matching the
      existing `sessionIDRe` are **required**; anything else is *malformed*, not
      something to coerce — the same posture `sessionIDRe` and `parseWindowID`
      already take.
- [ ] `start_time` is **optional**: recorded when non-empty and a decimal
      integer, and compared only when both the recorded and the current value
      are present. tmux renders an unknown format as an *empty field* (verified:
      `#{bogus}` → `""`), so a remote tmux predating `#{start_time}` would
      otherwise fail a strict parse — and the remote is any ssh host, not
      necessarily one rebuilt from this revision. `pid` + `session_id` alone
      already carry the correctness cliff; `start_time` is belt-and-braces
      against a recycled pid and must not cost compatibility.
- [ ] **The first attach records; it never tears down.** `newSessionPin`
      (`sessionpin.go:40-48`) prints "session pinning is off" and continues, and
      this read folds into it — so an unusable identity at attach 1 must keep
      today's startup behaviour, not turn a working bridge into a startup
      failure. If no usable identity can be recorded, **disable reconnect** for
      this daemon (single-shot, exactly today's behaviour) rather than failing:
      feature off, not fatal. Only a *later* attach can tear down on identity.
- [ ] Distinguish the two failure shapes the spec calls out: `one(rt, …)`
      returning `ok == false` (EOF mid-read — the caller must retry) from a
      reply that arrived and is `Kind == Error` or unparsable (the caller must
      tear down). The function's signature has to let the caller tell them
      apart; do not collapse both into one error.
- [ ] Comparison is equality on `pid` and `session_id` **always**, and on
      `start_time` **only when both sides carry one** — per the optional rule
      above.
- [ ] `sessionpin_test.go` additions: well-formed match; mismatch in each field
      independently; malformed body; empty body; `Kind == Error`; EOF.
- [ ] Leave `sessionPin.apply` / `HandOff` semantics untouched.

## Step 3: the disconnected badge, renderer half (component: `bridge-state-badge`)

- [ ] `picker/statusline/main.go`: append `#{@bridge_state}` to
      `volatileFields` and parse it into `args` at its new index. The list is
      index-parsed and fails closed on a field-count mismatch, so the append and
      the parse are one edit.
- [ ] In the `bridgeWin == "1"` branch that already renders the host badge,
      render a disconnected marker in `thmRed` when `@bridge_state` is set.
      Additive, like `@pr_draft` on a PR badge — it does not replace the host
      badge or change its colour.
- [ ] `main_test.go`: a mirror window with the option set renders the marker;
      without it, the existing host-badge output is byte-identical to today.
- [ ] No `config/tmux.conf.nix` change and no new `#()` argv — the option is
      fetched by the existing `display-message`, and adding it to the argv would
      re-introduce the line-0 blink `volatileFields` exists to prevent.

## Step 4: a re-dialable transport (component: `transport-seam`)

- [ ] `daemon.Config` gains **three** fields, not two:
      `Dial func() (io.ReadWriteCloser, error)`, a stop channel, and the retry
      bound (attempts + total elapsed) with a sane zero-value default. The bound
      must be injectable or the Go tests cannot shrink it and every reconnect
      test pays the real backoff. `Ctl` stays for callers that hold one
      connection and cannot make another (M1, the scripted Go tests).
      `Dial == nil` must reproduce today's single-shot behaviour exactly.
- [ ] `cmd/daemon/main.go`: turn the one-shot transport choice into a recipe
      `Dial` can re-run, for **all three** branches — `--test-local`, bare
      local-tmux (`--ssh=""`), and ssh. An ssh-only dialer makes the offline
      integration tests inexpressible.
- [ ] The dialer's own body is where both pieces of per-dial hygiene live —
      `main.go` regains control nowhere else:
      - `Wait()` the **previous** transport first, then dial the new one. One
        unreaped child is harmless; one per reconnect is a zombie per
        reconnect.
      - `os.Remove(ctlSock)` before starting ssh, **guarded on
        `ctlSock != ""`** (it is empty on the `--test-local` and `--ssh=""`
        branches). The path itself must not change — the `NewGraphics` closure
        captures it — but a transport killed without catching a signal leaves
        the socket behind, and `ControlMaster=auto` meeting a stale socket
        *disables multiplexing* rather than replacing it, silently, which is how
        image fetches go stale after the first reconnect.
- [ ] Signal handler: raise the stop signal **before** touching the transport,
      and reach the current transport through the same guarded indirection the
      daemon uses — the handler is a single `<-sigCh` closing over the `ctl`
      variable today, which the dialer's re-assignment would race. Keep the
      existing SIGTERM→2s→SIGKILL fallback, but do not let a wedged transport
      hold teardown for those 2 s: that is `lztmux-remote-detach`'s entire
      budget, and blowing it strands the daemon behind its fallback
      `kill-session`. Note the existing SIGTERM→sleep→SIGKILL already runs on
      its own goroutine (`main.go:164-172`) and has never held teardown — this
      is a property to preserve, not code to rewrite.
- [ ] Do not change `sshControlArgs`' option set (asserted by existing tests).

## Step 5: split `Run` into session and connection lifetimes (component: `run-split`) (implement: opus)

Subtle concurrency across long-lived goroutines plus the widest blast radius in
the change — this is the step where a mistake is silent.

- [ ] Extract a connection holder: the per-attach set (`pump`, `stream`, `rt`,
      `asyncQueue`, and the `io.ReadWriteCloser` itself) swapped as **one unit**
      under a mutex. A new `stream` starts its ordinals at 0, so a half-swapped
      holder desyncs every round-trip.
- [ ] `send` and `waitHellosFn` become stable closures that dereference the
      holder. This is what lets the long-lived capturers keep working across a
      re-dial without being restarted: `pumpInput` (started in **two** places —
      `setupWindow` and `applyPaneOps` in `reconcile.go`), `watchResize`, and
      the `acceptConns` ctl handler.
- [ ] With no live connection, a send must **fail closed, never block**. Reuse
      the existing path: `stampAll` returns `ok == false` on a closed stream and
      every caller already handles it.
- [ ] `st.close()` the outgoing stream **at the drop, before the swap**. Nothing
      closes it on a bare EOF today — only `teardown` does — so "fail closed"
      would otherwise rely on the next write hitting EPIPE and latching `closed`
      inside `stampAll`'s flush path. Deterministic beats incidental.
- [ ] The holder must tolerate an **empty slot**: after an exhausted retry
      budget there is no current connection, and `teardown`'s `st.close()` /
      `Ctl.Close()` against a nil slot is the first panic a real user would hit.
- [ ] `teardown` must close the **current** connection. It calls `st.close()`
      and `cfg.Ctl.Close()` today; after a reconnect `cfg.Ctl` is the original
      dead pipe.
- [ ] Restructure `Run` as: session setup once (listener, pidfile, `@bridge_*`
      stamps, registry, ctl state, resize watcher, `teardown`) → an attach loop
      → exactly one `teardown()` after the loop decides to stop. Keep the
      exactly-once guarantee **structural**; a `sync.Once` is a plaster over a
      shape that should be right (`stopWatch`'s close panics on a second call).
- [ ] The inner "run one connection" function returns a verdict covering the
      spec's endings table in full. `reg.empty()` is an **exit**, not a drop —
      routing it to the retry path reconnects into a session with no windows.
- [ ] `pauseAfterSet` resets per connection so the existing main-loop site
      re-arms `pause-after` after the first `settle()`. Do **not** move that
      send into the reattach sequence — see step 6.
- [ ] **The attach sequence, explicitly.** Every attach runs, in this order:
      1. dial (step 4) — on failure, retry per the schedule;
      2. **the identity read (step 2), before anything else touches the
         mirror.** On the *first* attach it records (and never tears down —
         see step 2). On a *later* attach it gates everything after it: a reply
         that arrives and mismatches or is malformed → teardown; an `ok ==
         false` EOF → this attempt is another drop, retry;
      3. first attach only: today's mirror-creation path
         (`firstMirrorWindow`/`createMirrorWindow` + `setupWindow`), then the
         initial `reconcileWindows`;
      4. later attaches only: the repair sequence (step 6). Later attaches
         create windows **only** through `reconcileWindows`, never directly;
      5. the main loop.

      Placement is the whole point of the check: repairing before an identity
      match is what writes another server's output into panes the user believes
      are their shells.
- [ ] Update `ctlPump`'s doc comment: it is no longer the one caller of
      `controlmode.Reader.Next()` "for the life of the process", but one per
      connection.
- [ ] Do not touch `setupWindow`, `addWindow`, `closeWindow`, the reconcile
      files, `seed.go`, `router.go`, `outputSink` internals, the `ctl.go` verb
      table, `focus.go` or `windows.go`.

## Step 6: reattach repair (component: `reattach-repair`) (implement: opus)

Ordering here is load-bearing and every error is silent. All of it runs on the
main-loop goroutine — round-trips may run nowhere else.

- [ ] **Decomposition deviation, justified:** this step adds
      `converger.reset()` to `size.go`, which no component lists as MAY-touch,
      and the declared interface says a new attach uses `newConverger()`.
      `reset()` is nonetheless the correct call and `newConverger()` is wrong:
      `watchResize` is started once, off the main-loop goroutine, and takes the
      `*converger` (`daemon.go:565-568`) — a fresh one would leave the watcher
      writing to an object nothing reads, silently. Mutating the existing one in
      place is the only option that keeps the single-writer story true. The
      boundary is extended by this one method; nothing else in `size.go` moves.
- [ ] Add `converger.reset()` and call it first, then re-send `ClientSizeCmd`
      **and** `ConvergeCmd` for every registered window. Only `setupWindow` and
      `watchResize` write the converger, and neither runs for a *surviving*
      window in the steps below — so nothing else re-asserts those caps. And
      `watchResize` records before it sends, so a resize during the outage left
      the converger believing a size the remote was never told.
- [ ] `resume()` every registered sink. `%pause` is per-control-client state and
      the new client will never send the paired `%continue`, so a sink left
      paused drops every frame forever and `takeDirty` skips it, putting it
      beyond `reseedDropped`'s reach too.
- [ ] `reconcileWindows(...)` — existing function, unchanged — **followed by a
      `reg.empty()` check that takes the exit verdict.** Both existing call
      sites guard it (`daemon.go:546`, `:651`); a reattach that finds every
      mirrored window gone would otherwise leave the main loop running against a
      windowless session.
- [ ] `reconcileLayout(...)` per surviving registered window — existing
      function, unchanged — **honouring its #487 retire verdict as the two live
      call sites do**, and preceded by an outright `localWindowGone` ask.
      Iterate `reg.remoteIDs()` and re-look-up each id, not `reg.all()`:
      `retireMirror` reconciles the whole registry, so a `*mirrorWindow` taken
      before it ran may no longer be that remote window's entry.
- [ ] Hoist `sessionPin.reseed`'s body to a package-level helper and call it for
      the full repaint; `sessionPin.reseed` becomes a caller of it. **No second
      reseed path** — the seed-before-output invariant (#233/#412/#417/#430)
      survives precisely because this reuses `PaneSeeds`.
- [ ] Re-measure `remoteClockSkew` and hand it to the **existing**
      `agentShipper` — do not build a replacement, whose `written` map would
      strand the status files `teardown` owes.
- [ ] The `ConvergeCmd`s in the first bullet will draw `%error` blocks for
      windows that died during the outage, since `reconcileWindows` has not
      pruned them yet. That is inert — they are fire-and-forget `send()`s, and
      `claimSeq` claims `End` *or* `Error` carrying `ClientCommandFlag`, so
      ordinals stay exact. Leave a comment saying so, or someone later routes
      them through a round-trip that treats `Kind == Error` as fatal.

## Step 7: the disconnected badge, daemon half (component: `bridge-state-badge`)

- [ ] **The value is the string `disconnected`**, and absent means connected.
      (The spec drafted `reconnecting` and the decomposition `disconnected`;
      this pins it. `disconnected` describes what the *user's mirror* is, which
      is what the badge reports — the daemon's own retry activity is not the
      fact being communicated.) Steps 3 and 7 must agree on this literal.
- [ ] Stamp `@bridge_state` on `cfg.LocalSess` **before** the first re-dial
      attempt, so the badge appears within one status tick of the drop rather
      than after the backoff.
- [ ] Clear it **after** step 6's reseed completes, not on the bare re-attach —
      the honest clear point is when the panes show live content again.
- [ ] `teardown` need not clear it; it kills the session.

## Step 8: offline tests (component: `offline-tests`)

- [ ] `tests/remote-m2-integration.bats`, drop-and-reconnect: SIGKILL the
      transport child, found with `pgrep -P "$daemon_pid"` — bats has never held
      that pid (the daemon has always spawned the transport itself), so this is
      how the test reaches it, not a regression `Dial` introduces. Assert the
      mirror
      session and its windows survive and `@bridge_state` is set; then assert
      re-attach, `@bridge_state` cleared, and that content written on the src
      **during** the outage reaches the mirror — that last assertion is what
      exercises step 6's reseed rather than mere window survival.
- [ ] `tests/remote-m2-integration.bats`, drop into a different server: SIGKILL
      the transport child, `kill-server` the src, recreate a same-named session
      on a fresh one; assert teardown, socket and pidfile removed.
      **Assert on evidence of the mismatch** — the daemon's own stderr identity
      line — not on "the mirror is gone" alone. If the daemon's first retry
      fires before the harness has killed the old server, it reattaches, the
      identity legitimately matches, and the harness's own `kill-server` then
      ends the mirror via `%exit`: the test passes green having never reached
      the identity check it is named for.
- [ ] Never use `detach-client` or `kill-server` as the *drop*: measured on the
      pinned tmux, both end in `%exit` (terminal), and only SIGKILL of the
      transport child gives the bare EOF that is a drop.
- [ ] Go tests: the backoff schedule and its cancellation (step 1); identity
      parse/compare/EOF, including the optional-`start_time` and
      record-don't-teardown-on-attach-1 cases (step 2); `Dial == nil` staying
      single-shot; and the reattach order — identity before repair, resume
      before reseed, converger reset before the converges — over the existing
      scripted round-trip fakes.
- [ ] Pin `ping`: it already acks empty while disconnected — `parseCtl` returns
      an empty request for it (`ctl.go:337-341`) and `submit` therefore sends
      nothing — so this is a regression test, not new work. Without it,
      `lztmux-remote-open`'s dedup would read a mid-outage bridge as dead and
      stack a second daemon on the same socket.
- [ ] Two spec acceptance criteria need **outcome** assertions, not just the
      ordering ones above — the ordering test proves the calls happen in the
      right sequence, not that the user gets a working pane:
      - a pane `%pause`d when the connection dropped repaints *and keeps
        repainting* after reconnect (write to it again post-reattach and assert
        the new content lands, not just the seed);
      - a mirror whose local client resized during the outage comes back at the
        remote's converged size, not 80 columns.
- [ ] **Known offline coverage hole, stated rather than hidden:** "image fetches
      still work after a reconnect" is not expressible in `--test-local`, where
      `ctlSock` is empty and `NewGraphics` therefore returns nil. The
      unlink-before-dial rule (step 4) is covered by reasoning and review only.
- [ ] Every existing test keeps passing unchanged. They SIGTERM the daemon and
      expect teardown, which the stop-before-transport ordering preserves.

## Step 9: docs (component: `docs`)

- [ ] Commit the spec and this plan under `docs/superpowers/specs/` and
      `docs/superpowers/plans/`, in the same PR as the code (CLAUDE.md, "Plans
      and Specs").
- [ ] CLAUDE.md: one Key Conventions entry covering the two lifetimes, the
      identity tuple and what a mismatch means, `@bridge_state`, and the
      resume-then-reconcile-then-reseed order. Match the surrounding entries'
      density — these are dense one-paragraph rules, not a section.

## Gate

- [ ] `nix build .#default`
- [ ] `nix flake check`
- [ ] `nix build .#lint`

All three; none subsumes another.
