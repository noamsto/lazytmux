# Plan — #574 the mirror's graphics identity follows the viewing client

Design of record: `../specs/2026-09-08-mirror-graphics-follows-viewer-design.md` (accepted after two adversarial critic rounds; every
load-bearing claim carries a measurement in its Evidence section).
Structure of record: `2026-09-08-mirror-graphics-follows-viewer-decomposition.md` — every step maps to exactly one
`component` and stays inside its `boundaries`.

Two halves. **Half 1** (steps 1-5) makes the sixel relay capability follow the
viewer: purely local, no transport surgery, shippable on its own. **Half 2**
(steps 6-11) replaces the control client so the remote re-picks its kitty
backend, which is where the risk is.

**Declared ordering deviation.** `2026-09-08-mirror-graphics-follows-viewer-decomposition.md` orders `H1-A ∥ H2-A → H1-B ∥
H1-C → …`; this plan runs H2-A as step 6, after Half 1, so that Half 1 is a
coherent shippable unit and the transport is untouched until it is gated. The
reason the decomposition put H2-A first — H1-B and H2-A both edit
`newGraphics`'s parameter list — is handled by one implementer editing them
serially in this order instead: step 3 changes the `Relay` parameter, step 6
changes the `ctlSock` parameter. No step in Half 1 touches the transport, the
dial argv, or `sshControlArgs`.

### State model (fixed here once — every blocker in both rounds came from leaving it vague)

Three values, and the one sentence that matters: **the dial argv reads
`Desired()`; the raise guard compares against `Advertised()`.**

- **`graphics.RelaySource` is the *only* cell holding the capability.** Every
  `Proxy` reads it, and the `RelayEnvCmd` publish site reads the same one.
  `daemon.Viewing` holds a *pointer* to it, never a second copy — SPEC R4's
  "one single value" is this sentence.
- **`Viewing.Desired()` / `SetDesired(string)` — the termname we *want* a
  control client to carry.** Written by any resolve that returns non-empty:
  the watcher's due-branch (step 5) and the ctl handler (step 9), plus the
  startup seed (step 4). **Every dial's argv reads this** — `replaceConn`'s and
  `reattach`'s alike, so an involuntary reconnect after a terminal switch also
  picks up the fresh termname for free.
- **`Viewing.Advertised()` / `setAdvertised(string)` — what the currently
  *published* control client was actually dialled with.** Written only where a
  connection is published, on **both** paths (`replaceConn` and `reattach`), so
  the two can never disagree. Never written by a resolve, and never written on
  an aborted replacement.
  **It records a snapshot taken before the dial, never a fresh read at the
  publish site.** `cfg.Dial` is opaque to `daemon/`, so the daemon package
  cannot ask what `newCtlCmd` actually read: the only correct shape is
  `want := cfg.View.Desired()` **before** `cfg.Dial()`, then
  `setAdvertised(want)` at publish. The obvious
  `setAdvertised(view.Desired())` at the publish site widens the window to the
  whole dial + identity read + priming span — seconds, and up to
  `identityTimeout` (30s) on `reattach`'s path — and a viewer switch landing
  inside it makes `Advertised` a lie. That failure is **sticky and silent**: the
  guard then reads equal forever, so the next `prefix + I` paints tofu with no
  recovery until the viewer changes again, and no test catches it.
- A resolve landing *inside* the dial window is off by one and is corrected only
  by the next genuine change. Accepted: the window is one gesture wide.
- **A replacement is raised iff `resolve() != Advertised()`.** Trace it: press
  with `Advertised=xterm-kitty` and a foot viewer → resolve `foot` differs →
  `SetDesired(foot)`, raise, nack → dial reads `Desired=foot` → the new client
  carries `foot` → on publish `setAdvertised(foot)` → the next press resolves
  `foot == Advertised` and submits quietly (acceptance 2). An **aborted**
  replacement leaves `Advertised=xterm-kitty`, so the next press correctly
  raises again (acceptance 7), while `Desired` stays `foot` — which is right,
  since that is still what we want the next dial to carry.
- Suppressing a *second* press during a replacement is the in-flight flag's job,
  not the term comparison's.

---

## Half 1 — the relay capability follows the viewer

### Step 1: the viewing-identity resolver (H1-A)
- [ ] New `picker/remotebridge/daemon/viewident.go`: `ViewIdentity{Term string;
      Relay graphics.Relay}`, a pure `resolveViewIdentity(lines []string)
      (ViewIdentity, bool)` implementing SPEC R1/R2, and `viewClientFormat` —
      the one new tmux `-F`, **`|`-delimited** (R12): control-mode flag,
      termname, termfeatures.
- [ ] Rules exactly as R2: exclude control-mode clients; `kitty` = AND of an
      `xterm-kitty`/`xterm-ghostty` termname prefix; `sixel` = AND of a whole
      `sixel` token; advertised termname = lexicographically smallest termname
      among the clients witnessing the AND'd kitty capability. Second return
      `false` when no non-control client is attached (R3 — caller keeps its
      last value).
- [ ] `viewident_test.go`: empty set; one kitty; one foot; `{kitty, foot}`
      (mixed → non-kitty termname, both capabilities false); two kitty clients
      with different termnames (lexicographic pick); a control client alone
      (→ `false`, not a capability-killing entry); a control client mixed with a
      real one (excluded, real one decides); malformed/short lines.
- [ ] Determinism: the same client set in reversed input order yields an
      identical `ViewIdentity`.
- [ ] **Assert the `|` delimiter here, by parsing a real row** — `go test
      ./tmuxformat/...` does not cover it: `CheckLine` (`tmuxformat/scan.go`)
      rejects only control bytes on lines containing `#{`, so a
      space-delimited format would pass it silently.

### Step 2: the shared cells (H1-A)
- [ ] `graphics.RelaySource` in `graphics/relay.go`: an
      `atomic.Pointer[Relay]`-backed cell, `Load() Relay` / `Store(Relay)`,
      carrying the whole `Relay` including `raw` (`Proxy.Filter`'s drop
      diagnostic interpolates it — R11).
- [ ] Decide and document: a **nil** `*RelaySource` must be safe in `Filter`
      (`Load` on nil returns the zero `Relay`), which makes the nil path
      statically relay-off. Two of the six existing `NewRelay` callers pass a
      zero `Relay` and can pass nil; the other four pass a real
      `RelayFromTermFeatures(...)` and need `NewRelaySource(...)` or a test
      helper. Add the helper rather than repeating the constructor four times.
- [ ] `daemon.Viewing` in `viewident.go`, in the shape the State model fixes:
      `Desired() string` / `SetDesired(string)`, `Advertised() string` /
      `setAdvertised(string)`, and `Relay *graphics.RelaySource` (the pointer,
      not a copy).
- [ ] Tests: zero value is a no-relay `Relay`; `Store`/`Load` round-trips; nil
      source loads the zero value; both term cells round-trip and are
      independent (writing one never moves the other); concurrent access under
      `-race`.

### Step 3: `Proxy` reads the capability live, on its own pump (H1-B)
- [ ] `graphics.NewRelay(loc, logf, *RelaySource, hold)` replaces the by-value
      `Relay` parameter. `Proxy` keeps `rel` as a pump-local cache plus the
      source and the hold budget.
- [ ] At the top of `Filter`, before `sc.Feed`: load from the source; when
      `Sixel()` differs from the cache, apply or withdraw
      `sc.SetRasterHold(hold)` and update the cache. On the pump goroutine only,
      preserving `Proxy`'s documented lock-free confinement (R11).
- [ ] Update `Proxy`'s doc comment: the confinement invariant now covers "the
      capability is read from a shared atomic once per `Filter`; every field
      derived from it is written only here". Record that a mid-hold flip is safe
      in **both** directions — `scan.go`'s own comment says the budget "decides
      when a partial raster is given up on, never what happens to it", and
      `holdLimit` is consulted per `Feed`.
- [ ] `proxy_test.go` (six `NewRelay` callers change): on one `Proxy`, a sixel
      dropped while off → relayed after a mid-life `Store` flipping it on →
      dropped again after flipping off, asserting the raster hold follows. Note
      in the test that `loggedRelayOff` is a once-per-pane latch, so the second
      off-phase logs nothing — harmless, but newly reachable.
- [ ] Preserve `main_test.go`'s `TestNewGraphicsGatesOnRelayOnBothTransportBranches`
      **assertion**, not just its compilation: it is #565's guard that the relay
      value never tells the two transport branches apart.
- [ ] `go test -race ./remotebridge/graphics/...`.

### Step 4: the daemon carries the live identity (H1-C)
- [ ] `Config.View *Viewing` replaces `Config.Relay graphics.Relay` (one writer,
      one reader today; no test constructs it).
- [ ] `cmd/daemon/main.go`: build the `Viewing` + `RelaySource` pair; seed the
      capability cell and **both** term cells (`SetDesired` and
      `setAdvertised` — at startup the first dial really will carry the seed)
      from `resolveViewIdentity` against `LocalSess`, falling back to the
      `-term`/`-termfeatures` flags only when the resolution is empty (R3).
      `newGraphics` takes the `*RelaySource`. Construct the pair **above**
      `newCtlCmd` (built at `cmd/daemon/main.go:183-211`, while the relay value
      is computed at `:326`) — step 6b's dial argv reads `Viewing.Desired()`, so
      the pair has to move up. The compiler forces this one.
- [ ] `main_test.go`: the seeded identity wins over the flags when a client is
      resolvable; the flags are used when it is not.
- [ ] Note the honest limit of acceptance 2's "property of the code, not a
      coincidence": when the launcher has not yet switched a client onto the
      mirror session at daemon start, the resolve is empty and the seed is
      `-term` — the *invoking* client, sampled by `display-message -t
      $TMUX_PANE`. That is the common production path, and it re-opens a
      first-press nack whenever the two disagree. Step 10's bats attaches its
      client before `bridge_up`, so it exercises the resolved seed; say so in
      the PR rather than implying the fallback is equally strong.
- [ ] Explicitly **not** in this step (boundary conformance): the dial argv's
      `term` argument (step 6, H2-A) and the stale comment at
      `cmd/daemon/main.go:61-65` (step 11, H2-D). Half 1 leaves what a dial
      advertises exactly as it is today.

### Step 5: publish on change, and per attach (H1-C)
- [ ] Add `client-session-changed` and `client-detached` to `resizeHookEvents`
      (E3 measured both; `client-attached` is redundant with the former).
      Comment the stated side effect: each now also runs `watchResize`'s
      `area()` fork, deduped by `cv.need`.
- [ ] In the watcher's due-branch: re-resolve; on a **non-empty** resolution
      `Store` the capability into the `RelaySource`, `SetDesired` the resolved
      termname, and — only when the capability changed — send `RelayEnvCmd`
      immediately (no re-dial, no attach; R5). The watcher never writes
      `Advertised`, and a termname change alone sends nothing from this path.
      Feed the resolver in as a function parameter so the watcher stays
      unit-testable with no tmux.
- [ ] Rename `watchResize` → `watchLocalClient` (two consequences of one nudge)
      and update `size_test.go` / `daemon_test.go` references. Keep
      `resizeNudgeSuffix`/`registerResizeHook` — the nudge really is "the local
      client changed".
- [ ] Add `RelayEnvCmd` to `repair()` so a reconnect re-asserts the current
      capability (R5's second half).
- [ ] Watcher unit tests: a capability change emits exactly one `RelayEnvCmd`
      with the new value; an unchanged capability emits none; a termname-only
      change emits none from this path; **an empty resolution stores nothing,
      emits nothing, leaves `Desired` untouched, and the previously stored
      capability survives** (R3 — this path now fires every time the user closes
      the terminal viewing a mirror, which is why it is tested rather than
      assumed).
- [ ] Close R5's second half's instrument gap: append one `show-environment -t
      rem LZTMUX_RELAY_GRAPHICS` read to an existing reconnect bats case
      (`remote-m2-integration.bats` ~2208-2540 already drives a real reattach),
      so the `RelayEnvCmd` added to `repair()` is verified rather than shipped
      on inspection. `repair` is a closure over `Run`'s locals and has no unit
      seam, so this is the only available instrument.
- [ ] bats (`tests/remote-m2-integration.bats`, beside the existing sixel-relay
      pair): attach a DST client to the mirror session with a chosen `TERM` and
      `sixel` feature — verified feasible: `env TERM=foot tmux -T sixel attach`
      yields `client_termname=foot` with `sixel` present in
      `client_termfeatures` on the pinned 3.7c — then assert **both** halves of
      acceptance 3, not just the env var: `LZTMUX_RELAY_GRAPHICS` on the SRC
      session flips, **and** a re-run of `send_straddled_sixel` appears in /
      disappears from the pipe-pane bytes, which is the instrument the existing
      tests at `remote-m2-integration.bats:1007-1108` already use. Assert also
      that no replacement occurred (acceptance 4's other half).

**Half 1 is complete here.** Acceptance 3, 4, 5 hold; the transport, the dial
argv and `sshControlArgs` are untouched — so `SetDesired` is deliberately a
**dead write** until step 6b reads it, which a reviewer of a Half-1-only PR
should not mistake for an unwired feature. Run the full gate before Half 2.

---

## Half 2 — the control client is replaced so the remote re-picks

### Step 6a: transport lifetime — per-dial `ControlPath` and the child set (H2-A)

Split from 6b by *dependency*: this cluster needs nothing from Half 1, changes
`reattach`'s behaviour for every existing user, and is independently soakable on
hardware — so it gets its own gate run before 6b.

- [ ] Mint a fresh path per dial (`…/lztmux-bridge-<pid>-<n>.sock`); keep it
      short — a `ControlPath` at or over 108 bytes fails (R9).
- [ ] Carry the path as a field on `child`, and define the accessor as **the
      path of the most recently started child that is still open**. Stated that
      way because `cmd/daemon` cannot observe which connection the *daemon*
      package published (`hold.set` is inside `daemon/`, which
      `2026-09-08-mirror-graphics-follows-viewer-decomposition.md` forbids from learning what a `ControlPath` is) — and
      "most recently started still-open child" is locally evaluable from the
      child set this step introduces and is correct in all three states: during
      the overlap it returns the new, connected master; after an abort the new
      child is closed so it returns the surviving old path; after a successful
      swap the old child is closed so it returns the new one.
      Getting this wrong is not cosmetic: an aborted replacement that left the
      accessor on the dead dial would make every later image fetch and paste
      pass `-S <nonexistent>`, and ssh then *silently* stops multiplexing and
      re-authenticates per fetch with no tty — exactly the hazard R9 claims to
      retire, made permanent, on the one path acceptance 7 declares survivable.
- [ ] Unlink a child's path when that child closes (`child.Close` is where it
      reaches, which is why the path lives there); `cleanup` covers whatever is
      still open at exit. The old unconditional `os.Remove(ctlSock)` per dial
      was also the collector for a socket left by a SIGKILLed child, and
      `ControlPersist=no` unlinks only on a clean exit (R9).
- [ ] **`transport` must track every live child, not one.** It holds a single
      `ch *child` and `start` overwrites it, so during the dial-first overlap a
      SIGTERM's `tr.stop()` signals only the newest child. Confirmed
      consequence: `runConn` selects only on `c.pump.lines` and `loopTick.C` and
      never consults `cfg.Shutdown` (only `reattach` does), so if the identity
      check fails and the daemon closes the new child itself, `t.ch` points at a
      dead child, the surviving old one is untracked, a later SIGTERM signals
      nothing that EOFs and **the daemon hangs on detach**. Make it a set;
      `stop()` closes all; each child drops itself on close; `stopping` still
      bars a later `start` from publishing a survivor.
- [ ] `main_test.go`: two overlapping children, the accessor returns the newer;
      **closing the newer returns the accessor to the survivor's path** (the
      aborted-dial case, expressible with two `child`s and no daemon);
      `transport.stop()` closes both; a child closed individually is dropped
      from the set and a later `stop()` is a no-op for it; a closed child's path
      is unlinked.
- [ ] Gate here (all three commands) before 6b — this is the step that changes
      the involuntary reconnect path.

### Step 6b: the path's consumers, and the advertised term reaches the dial (H2-A)
- [ ] `graphics.NewSSHFetcher(host string, ctlSock func() string, …)` and
      `SSHFetcher.CtlSock func() string`, read inside `fetch`. Same accessor for
      the `pasteUpload` closure and `cleanup`. Preserve the existing
      empty-means-no-`-S` contract: `ctlSock == ""` is what selects the nil
      `Localizer` in `newGraphics` (the `--test-local`/`-ssh ""` relay-only
      branch), so a nil/empty accessor must behave as the empty string did.
      Update `fetch_test.go`'s `CtlSock: "/run/x.sock"` construction.
- [ ] `sshControlArgs` keeps its signature; its `term` argument comes from
      **`Viewing.Desired()`** at each dial rather than from `*term` (R11) —
      `Desired`, not `Advertised`, is the whole point of the State model: the
      argv is where the new termname has to land, and `Advertised` is only
      written once this dial is published.
- [ ] `--test-local`/no-ssh `newCtlCmd` sets `TERM` on `cmd.Env` from the same
      cell, as **`append(os.Environ(), "TERM=…")`** — assigning wholesale would
      drop `TMUX_TMPDIR`/`HOME`/`PATH` and break the offline harness. Without
      this seam the offline harness can prove a replacement happened but never
      that the remote *sees* the new termname (acceptance 1's core claim). A
      real behaviour fix — both branches should advertise the same identity —
      not test scaffolding.
- [ ] `main_test.go` with a **recording `newCtlCmd`**: the dialled argv carries
      `Desired()`, and carries the *new* value after a `SetDesired` (this is the
      test that would have caught the chicken-and-egg the State model fixes;
      `replaceconn_test.go` cannot see the argv). Also: `fetch`/paste argv carry
      the accessor's current path.

### Step 7: the replacement routine (H2-B) — `implement: opus`
- [ ] New `replaceConn` in `daemon/conn.go`, distinct from `reattach`,
      implementing R7's order exactly: dial → read identity on the **unbound**
      connection → check against the recorded identity → still unbound,
      `cv.reset()` and send `ClientSizeCmd` + each mirrored window's
      `ConvergeCmd` → close the old connection → bind and publish the new one,
      `setAdvertised(dialled term)` → `repair()`.
- [ ] **Three outcomes, not two** — `(next *ctlConn, outcome)` with
      `replaced` / `notReplaced` / `mirrorGone`:
      - `notReplaced` — a failed dial, a failed or timed-out identity read, or a
        mismatched identity. **The old connection was never closed and is still
        published**; `Advertised` is untouched (so the next press correctly
        raises again — acceptance 7); the path accessor returns to the survivor
        by construction (step 6a). Never reaches `teardown`, and never calls
        `hold.close()` before it holds a verified replacement.
      - `replaced` — the loop must assign `c = next`, exactly as it does for
        `reattach`; without that, `runConn` keeps reading the *old*, closed
        pump.
      - `mirrorGone` — the swap happened but `repair()` returned false, i.e. the
        registry is empty. `reattach` handles this by returning nil, which
        reaches `teardown()` and `kill-session`; this routine must say so
        distinctly rather than collapse it into either of the others. Collapsing
        it into `notReplaced` is actively dangerous: the loop would continue on
        a `c` this routine already closed, read a closed pump → `connDrop` →
        `reattach`, whose first act is `hold.close()` — killing the *working*
        new connection and re-dialling a third.
- [ ] **`reattach` gets `setAdvertised` too** — the State model declares both
      paths and this is the one no other bullet covers. It goes between
      `next.bind(router)` / `hold.set(next)` and `repair()` (`conn.go:277-279`),
      recording the same pre-dial snapshot. Missing it means a reconnect
      following a terminal switch dials with `Desired=foot`, publishes a client
      genuinely carrying `foot`, and leaves `Advertised=xterm-kitty` — so the
      next press nacks and performs a whole redundant dial + `repair()` to
      change nothing. Add one assertion to
      `TestReattachBindsTheRouterOnlyAfterIdentityMatches`
      (`reattach_test.go:104`), which already has the harness.
- [ ] One line for the future reader: the old connection's buffered
      notifications — including the `%client-session-changed` E6 measured, which
      lands on the old stream during the overlap while the main loop is inside
      this routine — are discarded wholesale with that connection, so no ordinal
      can desync and no parser case is needed.
- [ ] Reuse `armIdentityDeadline` and `readIdentity` as-is. Do **not** set
      `@bridge_state` (R7) — the mirror is never disconnected on this path.
- [ ] Avoid the double `cv.reset()` with a **pre-primed converger**, not a
      `repair` flag: `reattach(cfg, router, hold, want, repair func() bool)` has
      four test call sites passing `func() bool { return true }`, and adding a
      parameter ripples through all of them for no gain.
- [ ] `replaceconn_test.go`, driving `reattach_test.go`'s `scriptConn` pattern
      with a **recording** sibling — the existing `scriptConn.Write` is
      `return len(p), nil` and records nothing, so write-order assertions need
      one. Cases: a clean replacement returns `replaced` with the new
      connection, repairs, and sets `Advertised` to the dialled term; a dial
      error returns `notReplaced` leaving the original connection published,
      `Advertised` unchanged and the mirror standing; an identity mismatch does
      the same and does **not** tear down; **a swap whose `repair` returns false
      reports `mirrorGone` and hands back no connection**; the size sends are
      written before the old connection is closed; the old connection is closed
      before the new one is bound (no window in which both route).
- [ ] Do **not** assert an existing window resizing when the old client goes
      away — E7 measured that tmux does not do it.

### Step 8: the raise channel and the new verdict (H2-C) — `implement: opus`
- [ ] `connReplace` joins `connEnd`/`connDrop`. A session-lifetime buffered
      channel (session-lifetime for the same reason `loopTick` is) becomes a
      third arm of `runConn`'s select, so a raise wakes the loop instead of
      waiting out `mainLoopTickInterval` (5s) — the existing ctl gestures need
      no wake-up only because their command is sent, and this one deliberately
      is not (R6).
- [ ] The attach loop tells `connReplace` from `connDrop` and honours all three
      outcomes: `replaced` → `c = next` and re-enter `runConn`; `notReplaced` →
      keep the existing `c` (which was never closed) and re-enter; `mirrorGone`
      → break to `teardown()`.
- [ ] Guard with R8: raise only when the resolved termname differs from
      `Viewing.Advertised()`, the resolution is non-empty, `reconnect` is true
      (reuse `Run`'s existing variable, do not re-derive it), and no replacement
      is in flight. The in-flight flag is **atomic** — the ctl handler goroutine
      sets it, the main loop clears it (R11) — and is cleared **by the attach
      loop after `replaceConn` returns**, which is after the `Advertised` write
      by construction. Clearing it earlier would let a press in that window read
      the stale `Advertised` and raise a redundant second dial and `repair()`.
- [ ] **Verification split, because `runConn` and the attach loop are closures
      over `Run`'s locals and `daemon.Run` has exactly one caller
      (`cmd/daemon/main.go:349`) — no Go test drives it.** Put the *guard* on a
      package-level seam: a type owning
      `{view *Viewing, resolve func() (ViewIdentity, bool), reconnect bool,
      inFlight atomic.Bool, ch chan struct{}}` with `raise() bool`,
      **`C() <-chan struct{}`** (exposed, because the channel must be created at
      session lifetime outside `runConn` — `loopTick`'s reason) and `done()`.
      That is constructible verbatim in a test, the way `ctl_test.go` already
      hand-builds `ctlState`s. Unit-test from `ctl_test.go`: `reconnect` false →
      ignored; a second raise while one is in flight → dropped; a matching
      termname → no raise; `done()` re-arms. Move the wake-up and "notReplaced
      keeps running" assertions to step 10's bats case, where the discriminator
      is real: time the observable change after the first press against a
      **< 2s** budget, which fails if the raise is waiting out the 5s
      `loopTick`. No extraction of `runConn`, and no new `Run`-driving harness.

### Step 9: the carousel verb resolves and nacks (H2-C) — `implement: opus`
- [ ] In `acceptConns`' handler, **before `cst.submit` and outside
      `ctlState.mu`**, for the carousel verb only: resolve, and when the
      seam's `raise()` reports it acted (it owns resolve/compare/`SetDesired`
      per step 8 — the handler must not re-implement the comparison),
      return a distinct press-again error instead of submitting. Verified
      deadlock this avoids: `repair()` → `reconcileWindows` (`daemon.go:1038`)
      → `cst.forgetWindow` (`ctl.go:76`) takes `ctlState.mu`, so a handler
      blocking on main-loop progress while holding it deadlocks (R6).
- [ ] Identify the verb through a **`ctl.go` seam** (a field on `verb`, or a
      predicate beside the table) rather than a bare `argv[1] == "carousel"`
      compare in `daemon.go` — verbs are otherwise entirely table-driven.
- [ ] Extend `ctlState`'s lock-order comment: no ctl handler may block while
      holding `mu`, and the resolve/compare/raise happens before `submit`.
- [ ] The nack text is user-facing — it reaches the user through
      `display-message -d 5000`, so it is a short status-line string, not a log
      line. R8's in-flight guard returns the **same** text, never the generic
      `bridge has no live connection to the remote`.
- [ ] Return the nack when the replacement is **raised**, not when it completes.
- [ ] `ctl_test.go`: a matching termname submits normally and is never nacked
      (acceptance 2); a differing termname nacks, raises exactly once and
      submits nothing; **the press after a completed replacement submits
      normally**; **a failed replacement leaves `Advertised()` unchanged so the next
      press raises again**; a non-carousel verb is unaffected by a stale
      termname; an in-flight second press gets the same text.

### Step 10: the end-to-end bats case (H2-C)
- [ ] Bring a bridge up with a DST client whose `TERM` is `xterm-kitty`; assert
      `$SRC list-clients -t rem -F '#{client_termname}'` is `xterm-kitty`.
      Switch the mirror to a client whose `TERM` is `foot`. Press `$CTL …
      carousel` once (expect the nack), press again. Assert the SRC control
      client's `client_termname` is now `foot` and the mirror still shows live
      content. (`$CTL` is the existing bats-built ctl binary; the existing
      `ctl carousel` test is the precedent.)
- [ ] Acceptance 6: a session switch on the *same* terminal leaves
      `client_termname` and the child set unchanged — no replacement.
- [ ] Acceptance 1's timing discriminator from step 8: the first press's
      observable change lands in < 2s.
- [ ] Acceptance 7 offline: name the mechanism or defer it. Under
      `--test-local` `newCtlCmd` is a fixed `tmux -L m2src -C attach-session`,
      so the way to fail a dial is to unlink the `m2src` socket file — the live
      client keeps its fd while a new `attach` cannot find the session. If that
      proves unstable in practice, leave acceptance 7 to the hardware pass and
      say so in the PR rather than weakening the assertion.

### Step 11: docs and comments (H2-D)
- [ ] Rewrite `CLAUDE.md`'s Bridge Graphics section: the capability follows the
      viewing client continuously; the advertised termname is replaced on the
      carousel gesture by a dial-verify-swap; the multi-client rule is
      capability intersection; the `ControlPath` is per-dial and owned by the
      connection. Add the already-open-viewer and direct-remote-client
      limitations.
- [ ] Bridge Reconnect gains a line distinguishing the involuntary `reattach`
      (close-then-dial, `@bridge_state`) from the voluntary `replaceConn`
      (dial-verify-swap, no badge).
- [ ] Rewrite the stale comment at `cmd/daemon/main.go:61-65` — "a non-kitty
      local terminal therefore degrades the remote carousel to block art on its
      own" is exactly the assumption this change removes (R13). Moved here from
      step 4 for boundary conformance.
- [ ] Relocate the three worker docs to the tracked convention (#565's shape):
      `docs/superpowers/specs/2026-09-08-mirror-graphics-follows-viewer-design.md`,
      `docs/superpowers/plans/2026-09-08-mirror-graphics-follows-viewer.md`,
      `docs/superpowers/plans/2026-09-08-mirror-graphics-follows-viewer-decomposition.md`.
      **Add a one-line amendment to the relocated decomposition**: it pins
      `Viewing` as `Term()`/`SetTerm(string)`, which the plan replaced with the
      three-cell shape because the two-cell one is unimplementable (the dial
      would read a cell written only after the dial). Without the amendment a
      committed structure-of-record document describes an API that does not
      exist. Remove `../specs/2026-09-08-mirror-graphics-follows-viewer-design.md`/`2026-09-08-mirror-graphics-follows-viewer.md`/`2026-09-08-mirror-graphics-follows-viewer-decomposition.md` from the root;
      `WORKER_TASK.md` is never committed.

---

## Gate and verification

- [ ] `nix build .#default`
- [ ] `nix flake check`
- [ ] `nix build .#lint`
- [ ] `go test -race ./remotebridge/...` from `picker/` — explicitly, not only
      via `nix flake check` (flake.nix:130 runs it, but the local run is the
      fast loop for R11's atomics).
- [ ] Paste all three gate outputs in the PR; per CLAUDE.md none subsumes
      another.
- [ ] State in the PR which acceptance criteria are machine-verified and which
      are not: the *pixels* (a real kitty vs foot viewer actually painting) are
      hardware-only, and the verification host must have no direct client
      attached to the mirrored session, or aeye's `relayed` gate is false and
      Half 2 changes nothing (SPEC non-requirement).
- [ ] Note in the PR that an image fetch in flight across a swap fails by design
      (2s timeout, store dropped, self-heals on the next repaint) so it is not
      read as a regression.

## Known-unresolved (for the PR's `## Escalated`)

- [ ] The **spec** critic loop hit its 2-revision cap with one blocking finding
      outstanding (the size-send ordering in R7). It was verified, partly
      **disproved** by measurement (E7: an existing window does not shrink), and
      its remedy adopted for the narrower #449 window it genuinely closes — but
      without a further critic pass.
