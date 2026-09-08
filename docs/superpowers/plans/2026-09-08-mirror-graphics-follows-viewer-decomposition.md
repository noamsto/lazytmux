# Decomposition — #574 the mirror's graphics identity follows the viewing client

> **Amendment (applied during execution).** This document pins `daemon.Viewing`
> as `Term()` / `SetTerm(string)`. The plan replaced that with a three-cell
> shape — `Desired()`/`SetDesired()` (what every dial's argv reads) and
> `Advertised()`/`setAdvertised()` (what the currently published control client
> was actually dialled with), alongside the `*graphics.RelaySource` pointer.
> The two-cell shape is unimplementable: the dial would have to read a cell
> that is only written *after* the dial, so the replacement client would carry
> the old termname and the raise guard would compare equal forever. Everything
> else below shipped as written.


Governing document: `../specs/2026-09-08-mirror-graphics-follows-viewer-design.md` (R1–R13, evidence E1–E7, non-requirements,
acceptance 1–8). Everything below splits that spec into independently
verifiable components; nothing here re-opens a requirement or the seam choice.
Where a boundary is drawn to *enforce* a requirement rather than merely
implement it, the note says so.

Facts found in the code that the split is built around — none of them is in
the spec, all of them move a boundary:

- **`transport` holds one child.** `cmd/daemon/main.go`'s `transport.start`
  publishes the most recently started ssh child as `t.ch`, and the SIGTERM
  handler's `tr.stop()` ends only that one. R7's dial-first order means two
  live ssh children exist between the new dial and the old close; a detach
  landing in that window ends the *newer* child and leaves the older one
  streaming, so the daemon never reads the EOF a detach relies on and
  `lztmux-remote-detach` falls through to its 2s `kill-session`. The
  ControlPath owner (H2-A) therefore also owns "which children does stop
  reach" — it is the same lifetime.
- **`--test-local` advertises no termname.** Its `newCtlCmd` is a bare
  `tmux -L m2src -C attach-session`, so the "remote" server sees the daemon's
  own inherited `TERM`, not anything the daemon chose. Unless the test-local
  branch sets `TERM` from the same accessor the ssh branch reads, the offline
  harness can prove a *replacement happened* (a new control client, the nack,
  the repaint) but never that the remote *sees the new termname* — which is
  acceptance 1's core claim. That seam is named under H2-A's boundary and the
  verification tables assume it exists.
- **`Config.Relay` has one reader and one writer.** Read at `daemon.go:602`,
  set only by `cmd/daemon/main.go:345`; no test constructs it. Turning the
  value into a live source is a two-site change. `graphics.NewRelay`, by
  contrast, has six test callers in `proxy_test.go` passing a `Relay` value.
- **`readClients` (cmd/daemon) already reads `list-clients -t LocalSess`**,
  space-delimited, sizes only. The R1 resolver is its daemon-package sibling
  and must not be folded into it: different package, different consumer, and
  its format carries free text (`client_termfeatures`), which is exactly the
  case the `|`-delimiter rule exists for (R12).
- **The offline harness can drive both halves of a local client's identity.**
  `tests/remote-m2-integration.bats` already hosts a real attached client in a
  pty via a third server (`tmux -L m2obs`, the "bigger human client" test at
  ~line 544), and the pinned tmux 3.7c accepts `-T features` on the client
  (`tmux -T sixel -V` exits 0). So a client attached to DST with a chosen
  `TERM=` and `-T sixel` is expressible in bats — capability *and* termname,
  with no hardware.
- **The nack is what the user reads on status line 0.** `cmd/ctl` shows a
  request failure with `display-message -d 5000 -t <client>` prefixed
  `lztmux-remote-bridge-ctl:`. The press-again text is a user-facing string
  with a 5s lifetime, not a log line.
- **`resizeHookEvents` is iterated by both `registerResizeHook` and
  `unregisterResizeHook`**, so R10's two new events are one edit — and the
  session-scoped `client-session-changed` hook coexists with the global
  indexed `client-session-changed[60]` carousel-reconcile hook in
  `config/tmux.conf.nix` (session and global hook tables both run).

## components

Two halves, six code components, one docs component. H1-* is the spec's
Half 1 (local relay capability); H2-* is Half 2 (control-client replacement).

### H1-A `view-identity`

Purpose: the one resolver (R1/R2/R3) — a pure function from the mirror
session's client rows to an advertised termname plus a `graphics.Relay`, the
local reader that feeds it, and the shared atomically-read state both halves
write and read (R11).

- `boundaries`
  - may-touch: new `picker/remotebridge/daemon/viewident.go` (+ sibling
    `viewident_test.go`, the package convention), `graphics/relay.go`
    (the `RelaySource` cell only — `Relay`, `RelayFromTermFeatures`,
    `String`, `Sixel` stay byte-identical), `graphics/relay_test.go`
  - must-not-touch: `proxy.go`, `fetch.go`, `daemon.go`, `conn.go`, `ctl.go`,
    `cmd/daemon/main.go`, any `.sh`/`.nix`
- `risk`: **low** — pure logic plus one fork. The only trap is R2's
  lexicographic rule being a pure function of the client *set* (never
  `list-clients` order), and the kitty predicate matching aeye's
  `chooseRelayBackend` prefixes exactly (`xterm-kitty`, `xterm-ghostty`).
- Enforces: control-mode rows excluded before any AND (R1); "no rows" is a
  distinct *empty* result the caller must not store (R3); the `Relay` carries
  its `raw` for `Proxy.Filter`'s diagnostic (R11).
- Verification: **Go unit tests alone.** Acceptance 5 lives entirely here —
  the mixed-capability set degrading, control-mode exclusion, the smallest
  witness rule, the empty set. `go test ./tmuxformat/...` (in
  `picker-go-tests`) gates the new `-F` string (R12).

### H1-B `proxy-live-relay`

Purpose: `Proxy.Filter` reads the *current* capability once per call and
derives every dependent field from it inside the pump goroutine — the drop
gate, the diagnostic's `raw`, and the scanner's raster hold, which `NewRelay`
sets once today (R4/R11).

- `boundaries`
  - may-touch: `picker/remotebridge/graphics/proxy.go`, `proxy_test.go`,
    `cmd/daemon/main.go` (the `newGraphics` constructor only — its
    `graphics.NewRelay` call), `cmd/daemon/main_test.go`
    (`TestNewGraphicsGatesOnRelayOnBothTransportBranches`),
    `daemon/daemon_test.go` (`TestOutputSinkFiltersAndCoalescesThroughTheProxy`
    if its construction changes), `daemon/graphics_integration_test.go`
  - must-not-touch: `scan.go`, `seq.go`, `coalesce.go`, `rewrite.go`,
    `fetch.go`, `daemon.go`, `conn.go`, `ctl.go`
- `risk`: **low-medium** — `Proxy` is documented lock-free and pump-confined;
  the one way to break `go test -race` here is reading the source anywhere
  but the top of `Filter`. `SetRasterHold` mid-life is new: whether a hold
  already in progress survives a flip is the implementer's to settle, and the
  `loggedRelayOff` latch's behaviour across a flip is unspecified by the spec
  (a reasonable reading: re-arm on a change, since the diagnostic names the
  `raw` it was derived from).
- Verification: **Go unit tests alone** (`proxy_test.go`: the existing
  `TestProxyRelayGateForwardsOrDropsSixel` and
  `TestProxyRelayOffLogsSixelDropOncePerPane` gain a flip-mid-stream case;
  `-race` is on in `flake.nix:130`). `main_test.go`'s both-branches test keeps
  the R7 invariant from #565 (relay never tells the transport branches apart).

### H1-C `relay-follow`

Purpose: Half 1's wiring — `Config` carries the live identity instead of a
frozen `Relay`; the startup seed comes from the resolver with the launcher
flags as R3 fallback; the resize watcher's due-pass re-resolves and, on a
capability change, re-publishes `RelayEnvCmd` at once; `repair()` re-sends it
on every attach; `client-session-changed` and `client-detached` join the
session-scoped nudge hooks (R5/R10).

- `boundaries`
  - may-touch: `picker/remotebridge/daemon/daemon.go` — `Config` (the `Relay`
    field), `Run`'s startup seed and its `RelayEnvCmd` send, `watchResize`'s
    body and signature, `resizeHookEvents`, the `RelayEnvCmd` line inside
    `repair` — and `daemon_test.go` (`TestWatchResize*`);
    `cmd/daemon/main.go` (the `Config` literal, the `-termfeatures` flag's
    role as seed); `tests/remote-m2-integration.bats` (the sixel-relay block
    ~955-1110 and `bridge_up`'s pass-through args)
  - must-not-touch: `conn.go`, `ctl.go`, `Run`'s main loop / `runConn` /
    the attach loop, `relayenv.go` (`RelayEnvCmd`/`RelayEnvUnsetCmd` are
    stable), `graphics/*`
- `risk`: **medium** — it edits `Run`'s startup and `repair`, which H2-B and
  H2-C also edit; the risk is merge contention, not mechanism. Side effect
  the spec states and this component owns: every session switch and detach
  now also runs `area()` and a converge pass (deduped by `cv.need`).
- Enforces: the relay gate and the published value are the *same* `Relay`
  read from the *same* cell (R4) — `RelayEnvCmd` is fed from the cell H1-B's
  proxies read, never from a second copy. The existing `gxoff`/`gxon` bats
  tests stay meaningful because DST has no attached client there, so the
  resolver returns empty and R3 keeps the `--termfeatures` seed.
- Verification: **Go unit** for the watcher (extend `TestWatchResize*`:
  a capability flip publishes once, an unchanged capability publishes
  nothing, an empty resolution keeps the last value) — then **offline bats**
  for acceptance 3 and 4: the existing sixel pair keeps passing unchanged,
  and a new case attaches a pty-hosted client to DST with `-T sixel`, then
  another without, and reads `$SRC show-environment -t rem
  LZTMUX_RELAY_GRAPHICS` flip with no new control client appearing
  (`transport_child` unchanged — acceptance 4's "no replacement").
  `tests/remote-m2-integration.bats` is the file; the sixel block is the
  precedent. Hardware adds nothing Half 1 needs.

### H2-A `ctlpath-per-dial`

Purpose: the ssh `ControlPath` becomes per-dial and reachable only through an
accessor, read *inside* the graphics fetcher and the paste closure at call
time; the old path is unlinked when its connection closes and `cleanup`
covers the current one; the `transport` reaches every child alive during an
overlap; `sshControlArgs`' argv is built from the accessor values (R9/R11).

- `boundaries`
  - may-touch: `picker/remotebridge/cmd/daemon/main.go` (the `ctlSock`
    variable and every site that reads it: `newCtlCmd`, `dial`, `cleanup`,
    `pasteUpload`, `newGraphics`; `transport`/`child`; the `--test-local`
    `newCtlCmd` branch gaining the same `TERM` the ssh branch carries),
    `cmd/daemon/main_test.go`, `graphics/fetch.go` (`SSHFetcher.CtlSock`
    becomes an accessor; `fetch` reads it per call), `graphics/fetch_test.go`
  - must-not-touch: anything in `daemon/` (the daemon package sees an
    `io.ReadWriteCloser` from `Dial` and must not learn what a ControlPath
    is), `proxy.go`, `scripts/*`
- `risk`: **medium** — mechanically small, but every ssh consumer is on the
  path (control stream, image fetch, paste upload, `cleanup`), the
  108-byte `ControlPath` ceiling is real, and the `transport`'s single-child
  assumption (fact 1 above) is a detach-correctness bug waiting in the R7
  overlap. It is also the component that *retires* the documented
  "stale socket silently disables multiplexing" hazard — the unconditional
  `os.Remove` at dial goes away, so the unlink-on-close must be reliable or
  `/tmp` grows a socket per replacement.
- Independent of everything else: it changes reconnect (`reattach`) behaviour
  too — a fresh path per re-dial — and can land alone.
- Verification: **Go unit** for the pure parts (`TestSSHControlArgs*` fed
  accessor values; `TestPasteUploadArgs`; a path-builder test asserting the
  length bound against a long `TempDir`; `fetch_test.go`'s `Run`-injected
  tests asserting the `-S` value is read at call time, not construction).
  **Offline bats** sees nothing — `--test-local` has no ControlPath — beyond
  the reconnect tests (`drc` and siblings ~2208-2540) staying green, which
  only proves the no-ssh branch is intact. **Hardware only**: after a real
  reconnect and after a real swap, an image fetch is still multiplexed (one
  ssh master per live connection in `ss -xl`/`ls /tmp/lztmux-bridge-*`, no
  orphan after the old connection closes, no `Connection to ... closed`
  handshake in the fetcher's timing), and a SIGTERM during the overlap ends
  *both* children. The repo's history is exactly here: checking that the
  option holds a path is not checking that the fetch rode the master.

### H2-B `replace-conn`

Purpose: the dial-verify-swap routine — sibling to `reattach`, in `conn.go` —
that dials, reads identity unbound, sizes the new client while still unbound,
closes the old, binds and publishes the new, then runs the shared `repair()`
without a second `cv.reset()`; and never closes a working connection it cannot
replace (R7). Plus the `connVerdict` value the main loop returns to reach it.

- `boundaries`
  - may-touch: `picker/remotebridge/daemon/conn.go` (new routine,
    `connVerdict`), `reattach_test.go` (its `scriptConn` harness is the
    instrument), `daemon.go` **only** for `repair`'s parameterisation (skip
    the reset on this path) and the priming closure it hands the routine
    (client size + per-window `ConvergeCmd`, built from `cv`, `reg`,
    `cfg.LocalArea`, `activeFirst`)
  - must-not-touch: `runConn`'s select, the attach loop, `acceptConns`,
    `ctl.go`, `sessionpin.go` (`readIdentity`/`remoteIdentity.matches` are
    reused as-is), `size.go` (`converger` is reused as-is), `graphics/*`,
    `cmd/daemon/*`
- `risk`: **high** — this is the most order-sensitive code in the daemon,
  and every step's order is load-bearing: identity read before anything
  touches the mirror (the #482 trust boundary); size sends before the close
  (E7's #449 window); close before bind (two bound connections route
  duplicate `%output` into one sink); `repair()` reused whole (subscriptions,
  `pause-after`, converger state are all per-client). Failure branches must
  leave `hold` untouched and the old stream live — the opposite of
  `reattach`, whose first act is `hold.close()`.
- Enforces: the boundary keeps this a *separate* routine rather than a flag
  on `reattach`, because `reattach`'s exhausted-budget/failed-identity
  `return nil` path leads `Run` to `teardown()` and `kill-session` — the
  exact outcome R7 exists to avoid. Sharing the loop would share the exit.
- Verification: **Go unit tests alone**, and thoroughly — `reattach_test.go`'s
  `scriptConn`/`reattachCfg`/`identityMatch` harness covers every branch with
  no tmux: a failed dial leaves `hold` holding the old conn and open; an
  identity mismatch closes `next` and leaves the old conn; a deadline expiry
  likewise; the happy path binds only after the identity matched (the
  existing `TestReattachBindsTheRouterOnlyAfterIdentityMatches` shape); write
  order on a recording conn shows `refresh-client -C` sends *before* the old
  conn's close. Per the spec: **no test may assert a window resize** — E7
  measured that tmux never performs one. **Offline bats** (in H2-C's case)
  witnesses the happy path end to end: mirror survives, repaints, a new
  `transport_child` replaces the old. **Hardware only**: acceptance 7's
  realistic dial failure (a lapsed agent key, a re-armed Tailscale `check`)
  leaving the mirror live and the old identity advertised — no
  `@bridge_state disconnected`, no killed session.

### H2-C `carousel-raise-and-nack`

Purpose: the `carousel` ctl handler resolves the identity synchronously,
outside `ctlState.mu` and before `submit`; when it differs from what is
advertised, and reconnect is available, and nothing is in flight, it writes
the new termname, raises the replacement on a session-lifetime buffered
channel, and returns the press-again nack — the same nack for a press that
lands mid-flight. `runConn` gains the third select arm; the attach loop gains
the branch that calls H2-B and, on failure, restores the advertised termname
(R6/R8).

- `boundaries`
  - may-touch: `picker/remotebridge/daemon/daemon.go` — the `acceptConns`
    handler closure, `runConn`'s select, the attach loop, the in-flight
    flag and channel declarations beside `loopTick`; `ctl.go` (the nack
    message constant, and a handler-level seam the closure calls so it is
    testable without `Run`), `ctl_test.go`; `tests/remote-m2-integration.bats`
    (a sibling of the carousel test at ~1448)
  - must-not-touch: `parseCtl`/`submit`/`verbs` semantics (no new verb, no
    argv change, `wire.CtlProtocolVersion` stays `"4"`), `conn.go`,
    `sessionpin.go`, `controlmode/parse.go` (E6: `%client-session-changed`
    gets **no** case), `config/tmux.conf.nix` (the bind already routes
    through `bridgeCtl` and displays the error), `graphics/*`,
    `cmd/daemon/*`
- `risk`: **high** — the lock-order constraint is the whole design: the
  resolve/compare/raise happens before `submit` and off `ctlState.mu`, and no
  ctl handler blocks on main-loop progress (the main loop takes `mu` inside
  `repair()` → `reconcileWindows` → `forgetWindow`). The channel is
  session-lifetime for `loopTick`'s reason (a per-attach one leaks per
  reconnect and teardown stops only what it sees). The in-flight flag crosses
  goroutines (ctl handler writes, main loop clears), so it is atomic (R11).
  Second-order: `reconnect` is `Run`'s existing local (`daemon.go:565`) and
  must be read, not re-derived.
- Enforces: the nack is a *distinct* string from `bridge has no live
  connection to the remote` — both travel `submit`'s bool/err path, so only
  the text distinguishes "your viewer identity is updating" from "your bridge
  is broken". Acceptance 2 falls out of R1's shared seeding: same terminal →
  same resolver → equal termname → no raise.
- Verification: **Go unit** (`ctl_test.go`) for the gate with a fake
  resolver and fake in-flight/reconnect state: equal → passes through to
  `submit`; differs → nack text, flag set, one channel send; mid-flight →
  same nack, no second send; empty resolution → passes through (R3);
  reconnect unavailable → passes through. **Offline bats** for acceptance 1's
  observable half, 2 and 6 (`tests/remote-m2-integration.bats`, beside the
  carousel test whose `tmux-claude-images` stub is reusable): a pty-hosted
  client attached to DST with `TERM=foo` → first `ctl carousel` exits
  non-zero with the press-again text, `transport_child` changes, the mirror
  still holds its pane and repaints, `$SRC list-clients -F
  '#{client_termname}'` shows `foo` (needs H2-A's test-local `TERM` seam),
  second press exits 0 and the stub's viewer split appears; the same client
  switching sessions on DST and back produces no new `transport_child`;
  a client with the daemon's own `TERM` never nacks. **Hardware only**: the
  pixels — from kitty, open a bridge; view the mirror from foot; press
  `prefix + I` twice; the carousel paints an image, not a `U+10EEEE` grid,
  and the remote's `list-clients -F '#{client_control_mode}
  #{client_termname}'` shows the control client carrying `foot`. Verifying
  the termname alone repeats the repo's known mistake — the acceptance is what
  the viewer paints.

### H2-D `docs-and-comments`

Purpose: R13 and acceptance 8 — every comment and doc that states the frozen
design says what shipped instead; the plan and spec are committed where the
repo keeps them.

- `boundaries`
  - may-touch: `cmd/daemon/main.go:61-65` (`sshControlArgs`' TERM comment)
    and the `ctlSock` "stable across re-dials" comments it now contradicts;
    `scripts/lztmux-remote-open.sh:461-478` (the launcher's read is now the
    R3 *fallback*, its comment says so; no shell logic changes — run
    `shellcheck` regardless); `CLAUDE.md` — Bridge Graphics (the capability
    now follows the viewer; Half 2's replace-on-carousel and its documented
    limitations), Bridge Reconnect (the `ControlMaster` paragraph "path is
    reused and must be unlinked first" becomes false; `connVerdict` gains a
    value; `reattach` is no longer the only re-dial), "What the Remote Host
    Needs on PATH" (nothing new — worth one line saying so); the `#565`
    plan/spec's "computed once" phrasing is history and stays;
    `docs/superpowers/plans/2026-09-08-<slug>.md` (this decomposition, as
    the three tracked decompositions before it) and
    `docs/superpowers/specs/2026-09-08-<slug>-design.md` (`../specs/2026-09-08-mirror-graphics-follows-viewer-design.md`
    relocated; root-level `../specs/2026-09-08-mirror-graphics-follows-viewer-design.md`/`WORKER_TASK.md` have never been
    committed and stay out of the PR)
  - must-not-touch: any `.go` logic, any `.nix`, the bats files
- `risk`: **low**. Depends on everything; lands last. `nix build .#lint`
  (`typos`, `shfmt`, `shellcheck`) is its gate.
- Verification: the lint build, plus a read of `CLAUDE.md`'s Bridge Graphics
  against the shipped mechanism by someone who did not write it.

## ordering

```
H1-A ∥ H2-A
   → H1-B ∥ H1-C
   → H2-B
   → H2-C
   → H2-D
```

- **H1-A ∥ H2-A** first: disjoint files (`daemon/viewident.go` +
  `graphics/relay.go` versus `cmd/daemon/main.go` + `graphics/fetch.go`), no
  shared symbols, both testable with no tmux. H2-A is the one Half 2
  component with no dependency on Half 1 and the one most worth landing
  early: it changes `reattach`'s ControlPath behaviour too, so a reconnect
  soak on hardware can start before any swap code exists.
- **H1-B ∥ H1-C** next: both consume H1-A's `RelaySource`; H1-B owns
  `graphics/proxy.go` and `newGraphics` (after H2-A has already rewritten
  `newGraphics`'s `ctlSock` parameter, so they do not collide on that
  function), H1-C owns `daemon.go`'s startup/watcher/`repair` sites. **Half 1
  is complete and shippable at this line** — acceptance 3, 4, 5 hold, the
  sixel relay follows the viewer, and nothing has touched the transport
  lifetime.
- **H2-B** after H1-C, serialised: both edit `repair` in `daemon.go`. Its
  own tests need neither H1 nor H2-A, but the priming closure it takes is
  built from `Run`'s locals, which H1-C has just moved.
- **H2-C** after H2-B (it calls the routine and returns the verdict it added)
  and after H1-A (it calls the resolver). It is the only component that
  touches `runConn` and the attach loop, and the only one whose bats case
  needs H2-A's test-local `TERM` seam.
- **H2-D** last.

`daemon.go` is edited by H1-C, H2-B and H2-C — the contention point. Each
touches a named, disjoint region (startup+watcher+`repair`'s send /
`repair`'s parameterisation+priming / `acceptConns`+`runConn`+attach loop),
but they are serialised above rather than marked `∥` because a rebase across
`Run` is where an ordering invariant gets silently reordered (#420's lesson:
a clean-applying patch can still be semantically broken by a refactor that
landed under it).

Half 1 alone is a coherent PR if Half 2 slips; Half 2 without Half 1 is not,
since H2-C's resolver and startup seed are H1-A/H1-C.

## interfaces

Contracts that cross a component line. Names are pinned so parallel
components agree; a component may add to these, never change them without
touching every consumer listed.

### Graphics (H1-A → H1-B, H1-C; H2-A → H1-B)

- `graphics.Relay`, `RelayFromTermFeatures`, `Relay.String`, `Relay.Sixel`,
  `DefaultRasterHold` — **unchanged**. `Relay.raw` keeps carrying the raw
  `client_termfeatures` for `Proxy.Filter`'s diagnostic (R11).
- `graphics.RelaySource` — the shared capability cell: `NewRelaySource(Relay)
  *RelaySource`, `(*RelaySource) Load() Relay`, `(*RelaySource) Store(Relay)`.
  Atomic; one logical writer (the resolver path in `daemon`), many readers
  (every `Proxy`, `Run`'s publish sites). Lives in `graphics` so `daemon` and
  `cmd/daemon` import it and never the reverse.
- `graphics.NewRelay(loc Localizer, logf, src *RelaySource, hold int64)
  *Proxy` — replaces the `rel Relay` parameter. `Proxy.Filter` reads
  `src.Load()` **once per call**, at the top, and derives the drop gate, the
  diagnostic and the scanner's raster hold from that one read, on the pump
  goroutine only. `Proxy.Replay`, `Close`, `New` — unchanged.
- `graphics.NewSSHFetcher(host string, ctlSock func() string, cacheDir
  string, maxBytes int64) *SSHFetcher`; `SSHFetcher.CtlSock func() string`,
  read inside `fetch` per call (R9). Returning `""` means "no `-S`", as the
  empty string does today.

### Daemon package (H1-A → H1-C, H2-C; H1-C → H2-B; H2-B → H2-C)

- `daemon.ViewIdentity{ Term string; Relay graphics.Relay }` plus an
  emptiness predicate distinguishing "no attached client" (R3) from a
  resolved identity whose `Term` is `""`.
- `daemon.ResolveViewIdentity(rows []ClientRow) ViewIdentity` — pure; the R2
  rule. `ClientRow{ Control bool; Term string; Features string }`.
- `viewClientFormat = "#{client_control_mode}|#{client_termname}|#{client_termfeatures}"`
  — the only new tmux `-F` string; `|`-delimited (R12), read via
  `cfg.LocalTmuxOut("list-clients", "-t", cfg.LocalSess, "-F", …)`. A reader
  `readViewClients(cfg Config) []ClientRow` wraps it.
- `daemon.Viewing` — the shared identity state handed through `Config`:
  `Term() string`, `SetTerm(string)` (atomic), and `Relay *graphics.RelaySource`.
  `Config.Relay graphics.Relay` is **replaced** by `Config.View *Viewing`
  (one writer site in `cmd/daemon/main.go`, one reader site in `Run`, no test
  constructs it). Seeded by `cmd/daemon` from `-term`/`-termfeatures`;
  overwritten by `Run`'s first resolve when non-empty (R1).
- `resizeHookEvents` = `client-resized`, `window-resized`,
  `client-session-changed`, `client-detached` — session-scoped via
  `set-hook -t LocalSess`, removed in `unregisterResizeHook` (R10).
- `RelayEnvCmd(session, value)` / `RelayEnvUnsetCmd(session)` — unchanged;
  sent at startup, on every capability change (fire-and-forget, one command),
  at the end of every `repair()`, and unset on teardown (R5).
- `connVerdict` gains `connReplace` beside `connEnd`/`connDrop`. `runConn`
  returns it when the raise channel fires; the attach loop then calls the
  replacement and re-enters `runConn` on whichever connection is current
  afterwards. Only `connDrop` still leads to `reattach`.
- The replacement routine (H2-B, called by H2-C): takes `cfg`, the `Router`,
  the `connHolder`, the recorded `remoteIdentity`, a priming closure
  `func(send func(string) bool)` that issues `ClientSizeCmd` and every
  mirrored window's `ConvergeCmd` on the *unbound* connection, and `repair`;
  reports whether the swap happened. On `false` the holder is untouched and
  the old stream is live. `repair` is parameterised so this path skips
  `cv.reset()` (the priming closure already reset and re-recorded).
- The raise channel: `chan struct{}` with capacity 1, created beside
  `loopTick` (session-lifetime), never closed by a ctl handler. The in-flight
  flag: atomic, set by the handler that raised, cleared by the attach loop
  after the swap succeeded or failed.
- The nack text: one constant in `ctl.go`, user-facing (shown by `ctl` via
  `display-message -d 5000`), distinct from `bridge has no live connection to
  the remote`, telling the user to press again. Returned for both the
  discovering press and any press that lands in flight (R8).

### Ctl wire protocol (H2-C, binding)

- `wire.CtlProtocolVersion` stays `"4"`. No new verb, no new argument; the
  `carousel` request's argv is unchanged (`[version, "carousel", pane]`). The
  nack rides the existing error-payload frame. An older `ctl` binary against a
  new daemon, or the reverse, behaves exactly as today.

### cmd/daemon (H2-A → H1-B, H2-C)

- `sshControlArgs(ctlSock, host, tmpdir, term, colorterm, termProgram,
  session string, tmuxArgv []string) []string` — **signature unchanged**
  (it is the assertable pure core). Its *callers* pass the accessor's current
  ControlPath and `cfg.View.Term()` at dial time, never `*term` (R11).
- The ControlPath accessor: `func() string`, returning the path of the
  connection most recently dialled; minted per dial from a pid-derived,
  `TempDir`-rooted prefix plus a short per-dial suffix, total length below the
  108-byte `sun_path` ceiling. Read by `newGraphics`'s fetcher, by
  `pasteUpload`, and by `cleanup`. The path of a connection is unlinked when
  that connection's child ends; `cleanup` unlinks whatever the accessor
  currently returns.
- `transport.stop()` ends every child started and not yet ended, not only
  the most recent (fact 1).
- The `--test-local` `newCtlCmd` sets `TERM` in the child's environment from
  the same accessor the ssh branch reads, so the offline harness can observe
  the advertised termname on the SRC server.
- Flags `-term` / `-termfeatures` (env `LZTMUX_BRIDGE_TERM` /
  `LZTMUX_BRIDGE_TERMFEATURES`) keep their names and meaning as the R3
  startup fallback. The launcher's read at `lztmux-remote-open.sh:475`
  is unchanged in code.

### Cross-repo contract (binding; restated so it is not lost)

- `LZTMUX_RELAY_GRAPHICS` — name, grammar and meaning **unchanged**: a
  comma-separated set of protocol tokens the local terminal can paint; exactly
  one token defined, `sixel`; absent/empty/unrecognised means no relayable
  capability. Published into the bridged remote **session**'s environment via
  control-mode `set-environment`; only its *freshness* changes (R5). aeye has
  no consumer yet (E4); this change adds none.
- The advertised termname reaches the remote only as the control client's
  `TERM` (E1), read by aeye per viewer launch (E2). Nothing new is exported.

### tmux surface (binding)

- New `-F`: `viewClientFormat` above, `|`-delimited.
- New session-scoped hooks: `client-session-changed`, `client-detached` on
  `LocalSess`, each running the same `touch` of the `.resize` nudge file.
- `@bridge_state` is **never** set during a replacement (R7: no disconnected
  badge). `@bridge_sock`, `@bridge_host`, `@bridge_session`, `@bridge_pane`,
  `@bridge_win` — unchanged.
- `%client-session-changed` on the control stream: parsed to the unknown
  default and dropped, ordinal claimed by `claimSeq` — **no parser case**
  (E6).

### Test instruments per component

| component | Go unit alone | offline `--test-local` bats | hardware only |
|---|---|---|---|
| H1-A `view-identity` | `viewident_test.go` (new sibling), `relay_test.go`; acceptance 5 | — | — |
| H1-B `proxy-live-relay` | `proxy_test.go`, `daemon_test.go` (sink+proxy), `main_test.go` (both branches), `-race` | — | — |
| H1-C `relay-follow` | `daemon_test.go` `TestWatchResize*` | `remote-m2-integration.bats` sixel block (existing pair unchanged + pty client `-T sixel` flip); acceptance 3, 4 | — |
| H2-A `ctlpath-per-dial` | `main_test.go` (`TestSSHControlArgs*`, `TestPasteUploadArgs`, path length), `fetch_test.go` | reconnect tests stay green (no-ssh branch only) | two masters during overlap, unlink on close, fetch multiplexed after reconnect and after swap, SIGTERM ends both children |
| H2-B `replace-conn` | `reattach_test.go` harness: every failure branch leaves the old conn; write order; bind-after-identity | happy path witnessed through H2-C's case | acceptance 7 on a real auth failure |
| H2-C `carousel-raise-and-nack` | `ctl_test.go` gate with fake resolver/flags | `remote-m2-integration.bats` sibling of the carousel test: nack text, new `transport_child`, mirror survives, remote sees `TERM`, second press opens the viewer, session switch → no swap; acceptance 1 (observable half), 2, 6 | acceptance 1's pixels: kitty-launched bridge viewed from foot paints an image, not tofu |
| H2-D `docs-and-comments` | — | — | `nix build .#lint`; a read of CLAUDE.md against the shipped mechanism |

Gate for every component: `nix build .#default`, `nix flake check`,
`nix build .#lint` — three commands, none subsumes another; output pasted in
the PR (acceptance 8).
