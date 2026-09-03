# Design spec: bridge daemon survives a control-connection drop (#482)

## Problem

`daemon.Run` (`picker/remotebridge/daemon/daemon.go`) is a single lifetime:
attach → enumerate → mirror → main loop → control-stream EOF → `teardown()`.
And `teardown` ends with

```go
cfg.LocalTmux("kill-session", "-t", cfg.LocalSess)
```

so the instant the ssh control stream drops, **every window of the mirror is
gone** — the panes, their scrollback, and the window set the user arranged.

The keepalives in `cmd/daemon/main.go` (`ServerAliveInterval=15` ×
`ServerAliveCountMax=4`) buy 60 s of tolerance. That is the right *detection*
window for a packet blip, and it is far too short for the ordinary laptop
cases: a closed lid, a wifi→LTE hop, a VPN reconnect, a remote `sshd` restart.
Every one of those exceeds 60 s and costs the user their whole mirror.

The comment at `main.go:29` already names the tension: *"a mirror torn down is
every window of it gone."*

The value here is **not** seamless roaming. It is narrower and firmer:

> A transport drop must not destroy local state that is trivially
> re-derivable.

Everything the mirror holds — window set, pane split shape, renderer
processes, the registry mapping remote pane ids to local panes — is derivable
from the remote in one reconcile pass. Discarding it because a TCP connection
died is throwing away work that costs one round-trip to rebuild.

## Goals

1. A control-connection drop leaves the mirror **standing**: same local
   session, same windows, same renderer panes, last-painted screens intact.
2. The daemon re-dials, and on a successful re-attach the mirror converges
   back to remote ground truth (windows added/closed while away, layouts
   changed, every pane repainted).
3. While disconnected, the mirror **says so** — a user must never be able to
   mistake a frozen mirror for a live one.
4. A remote that is no longer the same server still gets today's behaviour:
   teardown.
5. Retries are bounded; past the bound, today's `teardown()` runs unchanged.
6. Detach (`lztmux-remote-detach`, SIGTERM/SIGINT) stays prompt **while a
   reconnect is pending**.

## Non-goals

- **No transport other than ssh.** mosh was evaluated and rejected: no
  multiplexing, no binary stdio pipe, lossy by design — the control-mode
  bridge depends on all three.
- **No mirror rebuild for a *different* remote session in place.** The
  one-local-session ↔ one-remote-session invariant that `@bridge_host`,
  `@bridge_win` and `lztmux-remote-detach` all assume stays intact. The
  existing `HandOff` path (`sessionpin.go`) already covers a switch to another
  session.
- **No keepalive change.** 60 s stays the detection window. This work changes
  what happens *after* detection.
- **No input buffering while disconnected.** Keystrokes typed into a frozen
  mirror are dropped, as they are today once the stream is closed. Replaying
  them into a shell that has moved on is worse than dropping them.
- **No second seed path.** The reconnect repaint reuses the existing
  `PaneSeeds` batch machinery.

## Design

### The split: session lifetime vs connection lifetime

`Run` today interleaves two lifetimes that happen to coincide. The change is
to separate them.

**Session lifetime** — created once, survives a drop, destroyed only by
`teardown`:

| thing | why it survives |
| --- | --- |
| the unix listener on `cfg.SockPath`, the pidfile | renderers keep their connection; a new listener would orphan them |
| the `registry` (remote window id → local window, remote pane → conn) | the remote pane ids are still valid on the same server |
| the renderer panes and their `net.Conn`s | a renderer holding its socket keeps painting its last screen — the honest state of a disconnected mirror |
| the `Router` and its per-pane `outputSink`s | a sink is a pipe to a renderer, not to ssh; unregistering would close it and kill the pane |
| `ctlState`, `agentShipper`, the resize watcher and its hook | all describe the mirror, not the connection |

A sink survives, but **its `paused` flag must not**. `%pause` is per-control-client
state: `handlePause` sets `s.pause()` and asks tmux for a paired `%continue`
(`daemon.go:1016`), and a fresh control client will never emit that `%continue`,
so `handleContinue` never runs for it. A sink left `paused == true` drops every
routed frame forever (`writeOwned`, `daemon.go:1414`) and `takeDirty` explicitly
skips paused sinks (`daemon.go:1458`), so `reseedDropped` cannot rescue it
either — the reattach reseed would land (`enqueue` gates only on `closed`) and
then the pane would freeze for the life of the daemon. That is *worse* than
today's teardown. This is not a corner case: `PauseAfterSecs` defaults to 1, and
a connection stalling hard enough to back ssh up is exactly what pauses panes
just before it drops. **Reattach `resume()`s every registered sink**, as part of
the reseed pass that immediately repaints them.

**Connection lifetime** — torn down and rebuilt per attach:

| thing | why it is rebuilt |
| --- | --- |
| the ssh process and its `ControlMaster` | it is the thing that died |
| `cfg.Ctl`, the `ctlPump` goroutine | reader over the dead pipe |
| the `stream` (`st`) | its `sent`/`seen` ordinals count *this* connection's commands; tmux restarts the correspondence at 1 on a fresh attach |
| `roundTrip` (`rt`), the `asyncQueue` | built over the pump + stream |
| the `converger` (`cv`) | it caches *what this control client has told the remote* — client size and every per-window cap. A fresh client has told it nothing |
| the remote-side client state: client size, per-window caps, `pause-after`, session pin | a fresh control client has none of it |

Note what is deliberately **not** in the connection column: the `Router`'s
per-pane registrations. The task doc lists them as connection-scoped; this
design keeps them session-scoped, and the reason is mechanical. A registration
binds a *remote pane id* to a *local sink*; neither end is the ssh connection.
Re-registering means first unregistering, and `Router.Unregister` **closes the
sink** (`router.go:24-32` → `outputSink.Close`, `daemon.go:1476`), which closes
the sink's frame channel and stops the goroutine that drains it to the renderer.
The renderer's own `net.Conn` is *not* closed by that — it is closed separately,
by `closeWindow` and `teardown` — so the pane would keep showing its last screen
rather than blanking; what it would lose is the ability ever to be painted
again. "Rebuild per attach" would mean re-creating sinks over still-live
renderer conns for no gain.
What the registrations *do* need per attach is a `resume()` and a reseed, both
below. The identity check is what makes keeping them sound: it guarantees the
remote pane ids still name the same panes.

### The indirection this forces

Three long-lived things capture connection-scoped values in closures today,
and each must be re-pointed rather than re-created:

- **`send`** — captured by every `pumpInput` goroutine (one per live renderer
  conn, started in **two** places: `setupWindow` at `daemon.go:782` and
  `applyPaneOps` at `reconcile.go:293`), by `watchResize`, and by the mirror
  paths.
- **`st.send`** — captured by the `acceptConns` ctl handler goroutine.
- **`waitHellosFn`** — captures `pump`, `router`, `async`, `st`.

`teardown` is a fourth capture and the easiest to get wrong: it calls
`st.close()` and `cfg.Ctl.Close()` (`daemon.go:492-493`). Both must resolve to
the **current** connection — after a reconnect, `cfg.Ctl` is the original dead
pipe, and closing it tears down nothing.

The fix is one indirection: a mutex-guarded holder for the current connection
(`pump`, `st`, `rt`, `async`), with `send` / `waitHellosFn` as stable closures
that dereference it. `pumpInput` and the ctl accept loop then keep working
across a re-dial without being restarted.

A command written while no connection is live must **fail closed**, not block:
`stream.stampAll` already returns `ok=false` on a closed stream, and every
caller already handles that (a ctl request is nacked, a keystroke is dropped).
The holder's "no current connection" state reuses exactly that path.

### The dial seam

`Config.Ctl` is an already-opened `io.ReadWriteCloser` supplied by
`cmd/daemon/main.go`. Reconnect needs the daemon to be able to open another
one, so `Config` gains a **dialer**:

```go
// Dial opens a fresh control-mode connection. Called once at startup and
// again after each drop. nil = single-shot (today's behaviour: a drop is
// terminal).
Dial func() (io.ReadWriteCloser, error)
```

`Ctl` stays for callers that hold one connection and cannot make another —
the M1 path and the Go unit tests, which drive `Run` over a scripted
`io.ReadWriteCloser`. When `Dial` is set it is the only source of connections.

**`Dial` is built for all three of `cmd/daemon/main.go`'s transport branches,
not just ssh.** Today that file picks one of `--test-local`
(`tmux -L <src> -C attach-session`, `ctlSock == ""`), bare local-tmux
(`--ssh=""`), or ssh, and builds a single `exec.Cmd` from it. The reconnect work
turns that choice into a *recipe* the daemon can re-run, because the offline
`--test-local` seam is the vehicle for both required integration tests — an
ssh-only `Dial` would make them inexpressible. It is also what makes
"drop into a different server" testable at all: killing and re-creating
`tmux -L m2src` is the offline twin of a rebooted remote.

A `nil` `Dial` keeps `Run` behaviourally identical to today, which is what
keeps the existing Go tests honest.

Two process-hygiene obligations come with a re-dialable transport, neither of
which single-shot code needed:

- **Reap the dead transport.** `cmd/daemon/main.go` never `Wait()`s on `ctl` —
  harmless when there is exactly one and the process then exits, a zombie per
  reconnect otherwise.
- **The signal handler must not race the swap.** It is a single `<-sigCh`
  closing over the `ctl` variable (`main.go:164-172`); re-assigning `ctl` from
  the dialer races it. The handler needs the same guarded indirection the
  daemon's `send` does, and — per the endings table below — it must raise the
  stop signal before it touches whatever transport is current.

**The `ControlMaster` socket must be unlinked before every dial.**
`ControlPersist=no` ties the master to the ssh process, but a process that dies
without catching a signal (the SIGKILL fallback, a crash) leaves its
`ControlPath` socket file behind. OpenSSH's `ControlMaster=auto` meeting a
socket it cannot connect to **disables multiplexing for that ssh instance**
rather than replacing it — it does not error, so the reconnect looks healthy
while the graphics fetcher's `ssh -o ControlPath=<ctlSock>` execs stop being
multiplexed and start failing. The path itself still must not change (see
below), so the dialer removes the stale socket and then dials.

### Server identity — the correctness cliff

If the remote rebooted, every pane id in the registry is dead, and reconnecting
into it would mirror garbage into panes the user believes are their shells.
That is the #471 scenario, whose correct answer is still teardown.

At **every** attach — the first included — the daemon reads an identity pair
from the remote:

```
display-message -p -t <session> -F '#{pid}|#{start_time}|#{session_id}'
```

- `#{pid}` is the remote **tmux server** pid. A rebooted host, or a
  `tmux kill-server`, gives a different one. Session ids alone are not enough:
  a fresh tmux server restarts them at `$0`, so a new server hosting a
  same-named session can present the *same* `$N`.
- `#{start_time}` is the server's start epoch. Cheap belt-and-braces against a
  recycled pid on a long-lived host.
- `#{session_id}` is the mirrored session's id, which `sessionpin.go` already
  fetches at startup for its own purposes — this read subsumes it, so there is
  one identity round-trip, not two, and one authority for "which session are we
  on".

All three resolve on the pinned tmux (verified: `2151|1788283304|$1`).
Pipe-delimited, per the repo's `-F` convention (a tab collapses under a non-UTF-8
client locale — #373).

The first attach records the tuple. Every re-attach compares.

**The read has two failure shapes and they are not the same failure.** `one(rt,
…)` returns `ok == false` when `readReplyRouting` hit EOF — the *new*
connection died before answering, which is another drop and must **retry**,
consuming a retry-budget attempt. Only a reply that actually arrived and is
wrong — `l.Kind == Error`, or a body that does not parse as
`(pid, start_time, $N)` — is a genuine identity failure and means **teardown**.
Collapsing the two makes a second flaky dial permanently kill a mirror that a
third dial would have recovered.

On a genuine identity failure: never interpolate, never guess. This follows `sessionpin.go`'s existing precedent
verbatim: a reply that is not a `$N` id leaves the feature off rather than
being coerced (`sessionIDRe`), and `parseWindowID` does the same for `@N`.

Teardown on mismatch is the *correct* outcome, not a failure: the mirror's
contents are provably stale, and the user's own gesture (`prefix + s`) rebuilds
it against the new server.

**"Before anything else touches the mirror" has to include the identity
round-trip itself.** `readReplyRouting` routes `%output` into registered sinks
*as it walks past reply blocks*, so an unverified far end can paint into the
user's renderer panes while its own identity reply is still in flight — and
remote pane ids (`%0`, `%1`, …) are small, sequential and entirely predictable,
so "it would have to guess one" is not a boundary. The connection is therefore
built over a **throwaway `Router`** (`newCtlConn`): nothing is registered, so
`Route` finds no sink and drops. Only once the identity matches is the
round-tripper rebound to the real router (`ctlConn.bind`) — over the *same*
pump, stream and async queue, because a second `ctlConn` would restart the
ordinals tmux's command/reply correspondence is counted by. Output dropped
during verification needs no handling of its own: step 5's reseed repaints every
pane immediately afterwards. Notifications the reply reader queued meanwhile sit
on that connection's own `asyncQueue`, so they reach the main loop on the match
path and are discarded with the connection on the mismatch path. The first
attach takes the same route, where it is currently harmless — the registry is
empty — precisely so that "harmless today" is not load-bearing.

**The identity read carries its own deadline.** `one(rt, …)` has none, and the
case that needs one is a **wedged remote tmux behind a healthy `sshd`**: the
`ServerAlive` probes are answered by `sshd`, not by tmux, so the keepalives
above never fire, the connection stays up, and the read blocks on a reply that
is never coming. `Backoff`'s attempt and elapsed caps are consulted only
*between* attempts, so in that state the bounded-retry guarantee of this section
is not enforced at all and only a manual SIGTERM recovers.
`Config.IdentityTimeout` (`defaultIdentityTimeout`, 30s) arms a watchdog that
*closes the connection* — the pump then hits EOF and `readIdentity` returns the
retry shape above, so it adds no second reply reader to desync the stream. 30s
because `Dial` returns as soon as the transport process starts, so the deadline
covers the ssh handshake, auth and remote attach as well as the round-trip.

### The frozen mirror must announce itself

Without this the change reintroduces the actual complaint in #471 —
*"presenting stale screens while discarding every keystroke"* — merely
time-bounded. A stale screen the user knows is stale is a paused mirror; one
they don't is a lie.

- The daemon stamps a session option `@bridge_state` on `cfg.LocalSess`:
  `disconnected` while a re-dial is pending, **unset** while connected. The
  value names what the *user's mirror* is, which is what the badge reports; the
  daemon's own retry activity is not the fact being communicated.
- `picker/statusline` reads `#{@bridge_state}` alongside the existing
  `#{@bridge_host}` (`volatileFields`) and, in the `@bridge_win == "1"` branch
  that already renders the host badge, appends a red disconnected marker when
  it is set. Session-scoped options resolve through tmux's pane→window→session
  →global lookup, so the existing `display-message -t <session>` fetch reads it
  with no extra round-trip.
- Stamped **before** the first re-dial attempt, so the badge appears within one
  status tick of the drop rather than after the backoff.
- Cleared **after** the reattach repair completes (step 5's reseed), not on the
  bare re-attach. By this section's own principle — a stale screen the user
  knows is stale is a paused mirror; one they don't is a lie — the honest clear
  point is when the panes actually show live content again. The window between
  attach and repaint is sub-second, so the accuracy is free.
- `teardown` does not need to clear it — it kills the session.

### Which endings retry, and which do not

Today every way out of the main loop lands on the same `teardown()`. Reconnect
has to tell them apart, and only one of them is a drop:

| ending | meaning | retry? |
| --- | --- | --- |
| `%exit` | the remote deliberately ended this control client — the bridged session exited, or the server told us to detach | **no**, terminal |
| `reg.empty()` | the mirror has no windows left | **no**, terminal (as today) |
| reader EOF with no preceding `%exit` | the transport died under us | **yes** |
| stop raised (SIGTERM/SIGINT) | the user asked to detach | **no**, terminal |
| identity reply arrives and is wrong or malformed | a different server | **no**, teardown |
| identity read EOFs before answering | the new connection died too | **yes** |
| retry budget exhausted | gave up | **no**, teardown |

The stop case is a new trap and the ordering is the fix. SIGTERM today works by
signalling the ssh process so the stream EOFs — which, with reconnect, is
*indistinguishable from a drop*. So the signal handler must raise the stop
signal **before** it touches the transport, and the daemon must consult it
before scheduling any retry. Get this backwards and `lztmux-remote-detach` waits
out its 2 s, falls through to `tmux kill-session` itself, and strands a daemon
that is still sitting in backoff.

Two windows in that sequence are wide enough to lose a detach in, and both are
closed:

- **In the dialer, between `cmd.Start()` and publishing the child.** A signal
  landing there reaches the transport this one *replaces* — already dead and
  reaped, so signalling it is a no-op — and the ssh child just started outlives
  the daemon holding the `ControlMaster` open. Start and publish are therefore
  one critical section (`transport.start`), and a `start` that wins the lock
  after `transport.stop` has run ends its own child rather than publishing a
  survivor.
- **In `reattach`, between a successful dial and the main loop.** The
  top-of-iteration `stopped()` predates the dial, so without a second check
  right after it `Run` re-enters `runConn` on a connection the user has already
  asked to go away.

### Bounded retry

Backoff between attempts, capped, then fall through to today's `teardown()`:

- Exponential with a small base and a ceiling, plus jitter, bounded by a total
  elapsed budget rather than only an attempt count (a fixed count times a
  growing delay is an unintuitive wall-clock bound).
- The bound is a `Config` field with a default, so tests can shrink it.
- Exhaustion → `teardown()` → `Run` returns nil, exactly as an ordinary `%exit`
  does today.
- A dial that *fails* and a dial that *succeeds but fails the identity check*
  are different: the former retries, the latter tears down immediately.

**The `ControlMaster` path must not move.** `cmd/daemon/main.go` captures
`ctlSock` in the `NewGraphics` closure, and `ctlSock` is derived from
`os.Getpid()` — stable across re-dials. So the path is reused: a per-dial path
would leave the graphics fetcher pointing at a dead socket after the first
reconnect, and image fetches would silently go stale. Reusing it is not *free*,
though — see the unlink-before-dial rule above.

### Reconnect = work the daemon already does

After a successful re-attach and identity match, in this order:

1. **Reset the converger wholesale**, then re-assert every client-scoped value
   the remote no longer holds: `ClientSizeCmd`, **and `ConvergeCmd` for every
   registered window**.

   Resetting rather than invalidating one key is load-bearing twice over.
   First, only two sites write the converger — `setupWindow` and `watchResize`
   — and steps 2–3 call neither for a *surviving* window, so nothing else would
   ever re-assert those per-window caps. Second, `watchResize` records before it
   sends (`cv.need` mutates at `daemon.go:229`/`:233`, then `send` fails closed
   on the dead stream), so a local resize *during* the outage leaves the
   converger believing a size the remote was never told. A converger carried
   across the drop is therefore not merely stale, it is actively wrong, and the
   symptom is a silently 80-column mirror.
2. **`resume()` every registered sink** (see the pause discussion above), so the
   repaint below is not enqueued into a sink that will drop everything after it.
3. `reconcileWindows(...)` — adds windows that appeared, closes windows that
   went, re-asserts names, reflows. Existing function, unchanged.
4. `reconcileLayout(...)` per surviving registered window — pane adds/removals
   and geometry. Existing function, unchanged — but its **retire** verdict
   (#487: the mirror's local window is gone, so nothing this pass aims at it can
   land) is honoured here exactly as at the two live call sites, and this pass
   additionally asks `localWindowGone` outright before running it at all.
   Rationale, and the answer to "is a retire correct mid-reconnect?": no, it
   must wait for the transport. `retireMirror` rebuilds through
   `reconcileWindows`, which needs round-trips, so a retire raised while
   disconnected would `closeWindow` — killing the local window, unregistering
   its sinks, closing its renderer conns, all of which succeed — and then fail
   to build the replacement, losing a window the remote still has. That is
   strictly worse than the stranded entry it repairs. Nothing raises it while
   disconnected in any case: `reconcileLayout` runs only from `dispatch`/
   `settle`, both driven by the stream. The proactive ask is what the live
   path's "only of a pass that already failed" posture cannot cover here — an
   outage is the one stretch in which a local window can die with **no**
   `%layout-change` to discover it on, so a remote that never touches that
   window again would strand the entry for the life of the daemon.
5. **Repaint every pane.** `reconcileLayout` early-returns when the layout is
   unchanged, so it cannot be relied on for this; and output produced while
   disconnected was dropped by the remote server, not buffered — exactly the
   `sessionPin.reseed` situation. That function is already "repaint every
   mirrored pane" over the batched `PaneSeeds`; it is lifted to a shared
   package-level helper used by both callers. **No second reseed path.**
6. Re-measure `remoteClockSkew` for the agent shipper. It is measured once per
   connection today because NTP drift over a session is below the fade's
   resolution; an outage of unknown length is not that.

**`pause-after` is deliberately *not* in that list, and must not be moved into
it.** `daemon.go:658-670` arms it late on purpose, and says why: setup drains
the stream (its round-trips route, and so does the hello wait) but only
`dispatch` runs `handlePause`, so a `%pause` arriving mid-setup is merely
queued, and its pane would sit paused with no `%continue` re-seed until setup
finished. **A reattach is a setup pass** — steps 3–5 run `reconcileWindows` →
`setupWindow` → hello waits → `PaneSeeds`, all of which drain the stream without
dispatching. Arming `pause-after` before them re-opens exactly that window, and
it lands on the failure this revision exists to close: a `%pause` queued after
step 2's `resume()` re-pauses the sink, step 5's seed is enqueued into it, and
everything after is dropped forever.

So `pause-after` does not move at all. The per-connection state to reset is the
`pauseAfterSet` flag, and the existing main-loop site re-sends it after the
first `settle()` — preserving the documented invariant verbatim rather than
restating it somewhere new.

The seed-ordering invariant (#233 / #412 / #417 / #430) applies unchanged: a
pane's `FrameSeed` must be delivered before the `FrameOutput` that follows it,
and a seed must be painted after any reshape, never before. Reusing
`reconcileLayout` and `PaneSeeds` is what preserves it — writing a bespoke
reconnect repaint is what would break it.

### Prompt shutdown during a pending reconnect

Today SIGTERM is answered by signalling the ssh process, which drops the stream
and lets EOF carry `Run` to `teardown`. During a backoff sleep there is no ssh
process, so that signal reaches nothing and the user waits out the backoff —
and `lztmux-remote-detach` only waits 2 s before falling back to
`tmux kill-session`, which strands the daemon.

So the daemon needs a shutdown channel it can select on:

- `Config.Shutdown <-chan struct{}` (nil = never), closed by
  `cmd/daemon/main.go`'s existing signal handler alongside the ssh signal.
- The backoff sleep is a `select` on the timer and that channel; a close aborts
  the wait, skips further dials, and goes straight to `teardown`.
The ctl listener stays alive across a drop, so a keybind's ctl request during
the outage gets a nack from the closed stream rather than hanging — `stampAll`
returns `ok=false` and `submit` reports it. Two consequences to hold:

- the nack must carry a non-empty error, so the keybind reports failure rather
  than claiming a gesture landed;
- the `ping` verb must still ack **empty**, because `lztmux-remote-open`'s dedup
  keys off it: a ping that fails during an outage would read as "no live
  daemon" and stack a second daemon on the same socket.

### Teardown stays exactly-once

`teardown` closes `stopWatch`, so a second call panics. Today's guarantee is
structural: exactly one `teardown()` on every `Run` return path. The reconnect
loop must not weaken it — the natural shape is an inner "run one connection"
function that returns a verdict, with `teardown` called once by the outer loop
after it decides to stop. A `sync.Once` would be a defensive plaster over a
structure that should just be right.

The verdict enum must cover the endings table above in full. In particular
`reg.empty()` (`daemon.go:597-600`, `:631-633`, and the `WindowClose` branch) is
an **exit**, not a drop: routing it to the retry path would reconnect into a
session that has no windows left.

Two things that look like they need per-connection handling and do not:

- **`ctlState.focus`'s `commanded` FIFO** will hold entries for `select-pane`s
  whose reports died with the connection. `focus.go:28-34` documents that FIFO
  as deliberately self-healing, so it needs nothing — stated here so its absence
  from the connection column is a decision rather than an oversight.
- **`agentShipper.written`** and the `/tmp/claude-status/**` files it owns are
  session-lifetime; they fade by age on their own, and `clear()` still runs once
  in `teardown`. Only the clock skew is re-measured.

One comment goes stale and must be updated with the code: `ctlPump`'s
(`daemon.go:924-931`) claim to be the daemon's one caller of
`controlmode.Reader.Next()` *"for the life of the process"*. It becomes one per
connection.

## Acceptance criteria

- [ ] A drop with a re-dial that reattaches to the **same** remote server
      leaves the mirror's local windows and renderer panes alive, and repaints
      them from the remote.
- [ ] Windows created on the remote while disconnected appear after reconnect;
      windows closed while disconnected are removed.
- [ ] A drop whose re-dial lands on a **different** tmux server (different
      server pid, even with an identically-named session) tears the mirror
      down.
- [ ] An identity reply that arrives malformed tears down; an identity read
      that EOFs mid-flight retries instead.
- [ ] A pane that was `%pause`d when the connection dropped is live again after
      reconnect — it repaints *and* keeps repainting.
- [ ] A mirror whose local client resized during the outage comes back at the
      right size, not at 80 columns.
- [ ] `%exit` and an emptied registry stay terminal; only EOF retries.
- [ ] `ping` on the ctl socket still acks empty while disconnected, so
      `lztmux-remote-open` reuses the bridge instead of stacking a second
      daemon.
- [ ] Image fetches still work after a reconnect (the `ControlMaster` is
      genuinely re-established, not silently unmultiplexed).
- [ ] `@bridge_state` is set while reconnecting and cleared on reattach;
      `tmux-statusline` renders a disconnected marker for a mirror window
      whose session carries it.
- [ ] Retries are bounded; exhaustion runs today's `teardown()` and returns
      cleanly.
- [ ] SIGTERM during a pending reconnect tears down in well under
      `lztmux-remote-detach`'s 2 s fallback window.
- [ ] `teardown` still runs exactly once per `Run` return path.
- [ ] `Dial == nil` reproduces today's single-shot behaviour.
- [ ] This spec and the plan document are committed under
      `docs/superpowers/specs/` and `docs/superpowers/plans/` in the same PR as
      the code (CLAUDE.md, "Plans and Specs").
- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` all green.

## Test strategy

The offline `--test-local` harness is the vehicle: the "remote" is a second
local `tmux -L` server, and the control transport is
`tmux -L <src> -C attach-session`.

**How the drop is produced is not a detail — get it wrong and the tests prove
the opposite of what they claim.** Measured on the pinned tmux (`next-3.8`),
with the transport's stdin held open as the daemon holds it:

| action | last thing the control client sees |
| --- | --- |
| `tmux -L <src> detach-client -s <sess>` | `%session-changed`, then **`%exit`** |
| `tmux -L <src> kill-server` | `%session-changed`, then **`%exit`** |
| `kill -9` the `tmux -C attach-session` child | `%session-changed`, then **bare EOF** |

Both of the obvious mechanisms are the *terminal* `%exit` row of the endings
table, not the retry row. A drop-and-reconnect test built on `detach-client`
would assert teardown — failing a correct implementation — and, far worse, a
different-server test built on `kill-server` would **pass while proving
nothing**: the mirror would end via `%exit` and never reach the identity check
the test is named for. A silent false-green on the correctness cliff.

So the offline drop is **SIGKILL of the transport child**, which is also the
honest analogue of a dead ssh. That imposes one harness requirement: with `Dial`
building the transport inside the daemon process, bats no longer has that pid
and must find it (`pgrep -P "$daemon_pid"`).

- **bats** (`tests/remote-m2-integration.bats`):
  - *drop-and-reconnect* — SIGKILL the transport child; assert the mirror
    session and its windows survive and `@bridge_state` is set; then assert the
    daemon re-attaches (a new control client on the src server),
    `@bridge_state` clears, and content written on the src **during** the outage
    reaches the mirror.
  - *drop into a different server* — SIGKILL the transport child, then
    `kill-server` the src and re-create a same-named session on a fresh one; the
    daemon re-dials, the identity check fails, and the mirror is torn down with
    socket and pidfile removed. A fresh server hosting a same-named session is
    exactly the case a session-id-only identity check would wave through, so
    this is the test that pins the server pid (and start time) into the
    identity.
- **Go** (`picker/remotebridge/daemon/`): the identity parse/compare in
  isolation (well-formed match, well-formed mismatch, malformed, empty); the
  backoff bound; the shutdown-during-backoff abort; `Dial == nil` staying
  single-shot.

## Risks

- **Renderer conns outliving a long disconnect.** A renderer whose socket is
  still open but whose remote pane vanished while disconnected is cleaned up by
  `reconcileWindows`/`reconcileLayout` on reattach, via the same
  `closeWindow`/`applyPaneOps` paths a live close uses. The risk is a pane that
  the reconcile decides to *keep* but whose remote id has been reused — which
  the server-identity check is what rules out.
- **`converger` staleness.** Handled above by resetting it wholesale, but it
  stays the highest-value silent failure in this change: every symptom is a
  *wrong size*, never an error.
- **Ordinal desync.** A new `stream` starting at 0 against a reply reader still
  holding the old one's counters would desync every round-trip. The connection
  holder must replace pump, stream, rt and queue as one unit.
