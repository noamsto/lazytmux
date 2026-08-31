# Design spec: pipeline control-mode round-trips (#430)

## Problem

Every question the remote bridge asks its remote tmux is strictly
send → wait → send. At 100 ms RTT that serialization dominates startup and
every re-seed:

- `PaneSeed` (`daemon/seed.go`) sends `display-message` for the cursor, waits
  for its reply, then sends `capture-pane` and waits again — **2 RTTs per
  pane**, paid at startup, on every `%continue` re-seed, on every geometry
  re-seed, on every drop re-seed and on every session-excursion re-seed.
- `setupWindow` (`daemon/daemon.go`) seeds panes one after another, so a
  P-pane window costs `2P` RTTs of seeding on top of its converge + layout
  reads.
- `reconcileLayout` (`daemon/reconcile.go:77-87`) re-seeds every surviving pane
  of a window in a sequential loop — another `2P` RTTs per `%layout-change`,
  i.e. per resize, split and close.
- `sessionPin.reseed` (`daemon/sessionpin.go:76-91`) re-seeds **every pane of
  every mirrored window**, sequentially: `2·P_total` RTTs per excursion.
- `reseedDropped` (`daemon/daemon.go:788-801`) re-seeds every dirty pane,
  sequentially.
- M1's `seedFlow` (`remotebridge/main.go`) pays 4 serialized RTTs at startup on
  a tty, 3 without one.

The protocol already permits better. tmux guards each command a control client
sends with a `%begin…%end` block carrying `ClientCommandFlag`, **in the order
the commands were run**, so the Nth such block answers the Nth command. The
code already documents relying on exactly this (`daemon/seed.go:20`,
`daemon.go:196-203`). Nothing forces a reply to be read before the next command
is written.

## Goal

Let a caller issue N commands before reading any reply, without weakening the
issued-commands ↔ replies correspondence the reply readers depend on, and
without changing the order of frames any renderer sees.

## Non-goals

- No change to the wire frames renderers see. Seed-first FIFO per sink stays
  exactly as it is.
- No change to *which* reply reader runs where. The daemon's steady-state
  round-trips keep using the routing-aware reader; M1's one-shot startup keeps
  its plain skip reader (B3 invariant).
- No new concurrency. Round-trips still run only on the main loop.
- No cross-window batching in startup (see "Deliberately out of scope").

## The ordering hazard this design is built around

`readReplyRouting` routes `%output` to registered sinks *as it walks past reply
blocks* (`daemon.go:728-745`). So the moment a caller reads the stream, live
output for **any** registered pane is delivered into that pane's sink.

Today that is harmless, because of a property that is easy to lose: between
reading pane A's `capture-pane` reply and enqueueing A's `FrameSeed`, the
sequential code **does not read the stream at all**. Zero output can be routed
into A's sink in that gap, so a seed snapshotted at time `T_A` is never
overwritten by output that postdates it.

A naive batch breaks exactly this. If all `2P` replies are read and only then
are the seeds enqueued, then while replies for panes B…P are being read, A's
post-`T_A` `%output` is routed into A's sink — and the later
`enqueue(FrameSeed, seedA)` repaints the whole screen with content that
*predates* output the renderer has already painted. That is the same defect
class as #233 / #412 / #417, and `%layout-change` is the path where it is most
likely to fire: every pane repaints on the resize the notification announces,
so live output during the batch is the normal case.

**The load-bearing invariant of this whole change** is therefore:

> A pane's result is delivered into its sink before the next pane's reply is
> read off the stream.

Hold that, and every batched consumer is safe for one reason rather than
several. It is what keeps `reconcileLayout` / `sessionPin.reseed` /
`reseedDropped` (already-registered sinks) free of the inversion above, and it
is also what makes `setupWindow` safe: its sinks are registered *by* the
delivery, so output emitted after a pane's `capture-pane` executed is still
ahead of the reader on the stream when that pane's sink appears, and reaches it.

The seam must therefore yield replies **incrementally**, not return them all at
once.

## Design

### 1. The round-trip seam becomes a batch that yields replies incrementally

`daemon.roundTrip` is today:

```go
type roundTrip = func(cmd string) (controlmode.Line, bool)
```

It becomes:

```go
// replies yields a batch's reply blocks in issue order, one per call. ok is
// false once the batch is drained or the stream is gone.
type replies = func() (controlmode.Line, bool)

// roundTrip writes every command in cmds BEFORE any reply is read, then hands
// back a replies iterator over their reply blocks, in issue order.
type roundTrip = func(cmds ...string) replies
```

One seam, not two. The alternative — keeping the single-command `roundTrip` and
adding a sibling batch type — forces every function that both asks a question
*and* seeds a pane (`reconcileLayout`, `sessionPin.apply`, `setupWindow`) to
carry two closures that are two views of the same stream. One variadic seam
makes the batch the primitive and the single command the degenerate case, which
is what it is.

The **iterator**, rather than a returned slice, is what makes the load-bearing
invariant structural: a caller physically cannot read pane B's reply without an
explicit `next()` call, so "deliver A before reading B" is the shape of the
code, not a comment.

Contract, as the type documents it:

- Every command is written before any reply is read.
- `next()` returns reply *i* for command *i*; the caller must call it in issue
  order and may call it at most `len(cmds)` times.
- `ok == false` means the batch is drained or the control stream is gone. Once
  false it stays false.
- **No reentrancy.** Nothing done between two `next()` calls may issue another
  round-trip or otherwise read the control stream. A nested `rt` stamps a
  *later* ordinal, and `readReplyRouting` would consume and discard every
  remaining reply block of the in-flight batch while hunting for it
  (`daemon.go:728-745` matches on `seq == want` and drops the rest); the next
  `next()` would then wait forever for an ordinal already gone past, hanging the
  main loop until stream EOF. Between `next()` calls a caller may do local tmux
  work, `sink.enqueue` (non-blocking by construction) and stderr, nothing else.
- **Mid-stream loss is not atomic**: replies 1…k have already been taken off
  the reader and are discarded by the caller. Harmless — `ok == false` only
  happens at teardown/EOF, when the stream is dead anyway.
- **An undrained batch is safe.** Leaving replies unread desyncs nothing:
  `readReplyRouting` skips any block whose ordinal is not the one it wants, and
  `stream.seen` advances inside `nextLine` regardless of who reads. Abandoned
  replies are skipped by the next round-trip, or consumed and ignored by the
  main loop.
- `rt()` with no commands yields an iterator that is immediately `ok == false`.

Single-command call sites keep a one-line shape via a helper:

```go
func one(rt roundTrip, cmd string) (controlmode.Line, bool) { return rt(cmd)() }
```

so `l, ok := rt(cmd)` becomes `l, ok := one(rt, cmd)`. There are **nine**
single-command sites — `daemon.go` 277 / 557 / 632 / 828, `agentstatus.go`
98 / 216, `sessionpin.go` 39 / 62, `reconcilewindows.go` 25.

**`daemon.go:557` needs naming, because it is the one that changes silently.**
It is `rt(ConvergeCmd(mw.remoteID, w, h))` — a call whose result is *discarded*.
Under the variadic seam that line still compiles as an expression statement,
writes the command, and never calls the iterator: the converge stops being
awaited, with no compile error and no test to catch it. That would delete the
very guarantee "Deliberately out of scope" declines to touch (the converge's
reply is what establishes the resize landed before `readLayout` runs) and would
silently change the RTT `before` baseline. It becomes
`one(rt, ConvergeCmd(mw.remoteID, w, h))`, still discarding the result, so the
wait is preserved verbatim.

### 2. `stream.stampAll` writes the batch under one lock

`stream.stamp` writes one command under the mutex and returns its ordinal. The
batch form takes the lock **once**:

```go
func (s *stream) stampAll(cmds ...string) (seqs []uint64, ok bool)
```

The lock is held across the whole batch so no foreign command from `pumpInput`,
a ctl request or `watchResize` lands between ours. Correctness does not require
it — ordinals are assigned under the lock either way, and the reply readers skip
blocks nobody is waiting for — but a contiguous batch is what makes a
write-order assertion in a test meaningful and keeps a wire trace legible.

What it is *not*: this is not where the RTT saving comes from. The saving comes
from not waiting between sends; per-command `Flush` would collapse the RTTs
identically. `stream.w` is a default-4096-byte `bufio.Writer`, so a large batch
may flush mid-batch regardless — the design does not depend on the batch being
one write.

**The lock is never held across a read.** `stampAll` returns before any reply is
read, so no round-trip ever blocks on the stream while holding the writer mutex.
That is what keeps `pumpInput` / ctl / `watchResize` deadlock-free, and all
three keep going through the unchanged `stamp` / `send` wrappers.

`stamp` and `send` stay as thin wrappers over `stampAll` so every
fire-and-forget path is untouched. `stampAll()` with zero commands returns
`(nil, true)` and writes nothing.

### 3. `PaneSeed` issues both commands, then reads both replies

```go
func PaneSeed(rt roundTrip, paneID string) ([]byte, error)
```

Signature unchanged; body becomes one `rt(displayMessage, capturePane)` call
followed by two `next()` reads. Behavior preserved exactly:

- A `%error` reply to `capture-pane` (the pane closed between `list-panes` and
  the capture) is still the only rejection signal — keyed off `Kind`, not body
  length, so a genuinely blank pane still yields a valid empty seed.
- An error reply to `display-message` still degrades to cursor `(0,0)`,
  `alt=false`, `appck=false` rather than failing the seed.
- Stream loss (`ok == false`) still produces an error, as it does today via
  `readCapture`'s `isErr`.

### 4. `PaneSeeds` batches a pane set, delivering per pane

```go
// PaneSeeds issues display-message+capture-pane for every id in paneIDs — all
// 2N commands before any reply is read — and calls onSeed once per pane, in
// order, as soon as that pane's capture reply is parsed and BEFORE any later
// pane's reply is taken off the stream. onSeed must not issue a round-trip
// (see the no-reentrancy rule in §1).
func PaneSeeds(rt roundTrip, paneIDs []string, onSeed func(i int, seed []byte, err error))
```

Commands are **interleaved per pane** — `dm(A), cap(A), dm(B), cap(B), …`,
never all-`display-message`-then-all-`capture-pane`. The reason is per-pane
coherence, not ordering: a cursor position read `2N` commands before the capture
it decorates describes a screen the capture no longer shows, and the seed would
place the cursor where the pane used to have it. Interleaved, each pane's cursor
and capture are adjacent in the remote's command queue, exactly as today.

`onSeed` firing between panes is what upholds the load-bearing invariant, so
each pane's `FrameSeed` is enqueued into its sink before any further `%output`
can be routed there. `PaneSeed` is the N=1 case implemented on top of
`PaneSeeds`, so there is one implementation of the reply-pairing logic.

Per-pane errors stay per-pane: one closed pane must not cost its siblings their
seeds. On stream loss mid-batch, `onSeed` is still called for the remaining
panes, each with a stream-loss error, so a caller's bookkeeping is never left
half-done. `paneIDs == nil` calls `onSeed` zero times and issues nothing.

Four consumers — every multi-pane seed loop in the daemon, so there is one
batching shape rather than a batched path and a straggler:

| consumer | pane set | today |
|---|---|---|
| `reconcileLayout` post-reshape re-seed (`reconcile.go:77-87`) | surviving panes of one window | `2P` RTTs per `%layout-change` |
| `setupWindow` (`daemon.go:602-618`) | the window's panes, at creation | `2P` RTTs |
| `sessionPin.reseed` (`sessionpin.go:76-91`) | every pane of every window | `2·P_total` RTTs per excursion |
| `reseedDropped` (`daemon.go:788-801`) | `router.dirtyPanes()` | `2D` RTTs |

The three re-seed consumers are a straight substitution: their loop bodies
(enqueue on success, stderr on error, skip a nil sink) move into `onSeed`
unchanged, and panes with a nil sink are filtered out of `paneIDs` before the
batch so no command is issued for a pane nobody will seed.

`setupWindow` additionally needs `seedRenderer` split so the round-trip and the
sink wiring are separable:

```go
// unchanged signature; now PaneSeed + wireRenderer
func seedRenderer(rt roundTrip, router *Router, conn net.Conn, remotePane string, dims controlmode.PaneCell, gfx *graphics.Proxy) bool

// the wiring half: register-then-enqueue, FIFO unchanged
func wireRenderer(router *Router, conn net.Conn, remotePane string, seed []byte, err error, dims controlmode.PaneCell, gfx *graphics.Proxy) bool
```

`wireRenderer` keeps **every** behavior currently attached to a failed seed at
`daemon.go:941-953`: log to stderr, `conn.Close()`, return false. `setupWindow`
keeps the two behaviors that are its own and must not move: `delete(mw.conns,
remotePane)` on failure, and the **fatal** `return fmt.Errorf("daemon: seed
failed for sole pane %s")` when `len(mw.remotePanes) == 1` — that fatal return
is what makes `addWindow` / `mirrorNewWindow` tear down a half-created mirror
window (`daemon.go:657-665`, `reconcilewindows.go:85-94`); dropping it would
leave a live blank mirror window and a registry entry.

Index alignment is stated explicitly because the batch filters: `setupWindow`
builds `paneIDs` from the panes of `mw.remotePanes` that have a live conn
(today's `conn == nil` → `continue`), keeping a parallel slice of their indices
into `mw.remotePanes` so `onSeed(i, …)` maps back to the right `L.Panes[…]`
dims, conn and pane id. `pumpInput` still starts per successfully wired pane,
after the batch.

`reconcileLayout`'s *append* path (`ops.Append`, `reconcile.go:216-231`) keeps
per-pane `seedRenderer`: it is bounded by the panes added in one notification
(in practice 1) and interleaves `collectHellos`, so batching buys nothing and
costs clarity.

### 5. M1 `seedFlow` pipelines its dependent pair

`seedFlow` issues `list-panes`, then (only on a tty) `refresh-client`, then
`display-message`, then `capture-pane`. Only the last two are pipelined:
`list-panes` resolves the pane id the other two target, so it must be answered
first.

`refresh-client` is **deliberately left with its own wait.** Batching it with
the capture would make the capture's screen size depend on tmux having applied a
client resize by the time the next queued command runs — a timing assumption
this change cannot verify offline, on a path that runs once at startup.

So the tty path goes 4 RTTs → 3, and the non-tty path (which is what the offline
`remote-bridge-integration.bats` harness exercises) goes 3 → 2.

M1 keeps its own plain `readReply` skip reader. This change touches only the
order `seedFlow` writes and reads in.

### 6. What does *not* change

- `readReplyRouting` and M1's `readReply` are untouched. Which one runs where is
  untouched. Pipelining is entirely on the write side plus the order the caller
  reads in.
- `%pause` / `%continue` / async notifications interleaving between reply blocks:
  still tolerated, by the same readers, unchanged. `handleContinue` still
  enqueues the seed before `resume()`.
- Round-trips still run only on the main loop; nothing here starts a goroutine.

## Deliberately out of scope

- **Cross-window batching at startup.** `setupWindow` interleaves control-stream
  round-trips with local tmux work (`PlanWindow` splits, `respawn-pane`,
  `collectHellos`). Batching the converge + `readLayout` of *all* windows ahead
  of that loop is a real further win, but it means restructuring startup into
  gather-then-apply phases and changing when a window's failure is detected. A
  separate change with its own failure-mode analysis.
- **Batching converge + `readLayout` within one window.** `ConvergeCmd` resizes
  the remote window and `readLayout` reads the layout that resize produces; the
  reply to the converge is what today establishes the resize has landed. Same
  class of timing assumption as `refresh-client` above.
- **`reconcileLayout`'s append path**, per §4.
- **`agentstatus` / `sessionpin.id` / `list-windows` round-trips.** Genuinely
  single commands, so there is nothing to pipeline. (`sessionPin.reseed`, by
  contrast, *is* a multi-pane loop and is in scope — see §4.)
- **`agentstatus.poll`'s `list-panes`.** One command already covering every
  pane; batching would not reduce it.

## Acceptance criteria

1. `PaneSeed` writes both commands **before reading either reply** — asserted by
   a unit test wiring a real `stream` over a recording writer, read by a gating
   reader that yields the scripted bytes only once both command lines have been
   observed on the writer and **returns EOF otherwise**. EOF, not a block:
   writes and reads share one goroutine, so a reader that merely withheld bytes
   would hang the test instead of failing it. On a regression the round-trip
   fails, `PaneSeed` errors, and the test reports it. (The existing
   `strings.Reader`-backed `testRoundTrip` / `scriptedRT` helpers cannot express
   this — they hold the whole script up front.)
2. `PaneSeeds` delivers results index-aligned with `paneIDs`, and isolates a
   per-pane `%error` to that pane.
3. **Seed-before-output ordering survives batching**: a two-pane
   `reconcileLayout` test whose scripted stream emits `%output` for pane A
   *after* A's `capture-pane` reply and before pane B's replies asserts that
   A's sink receives its `FrameSeed` **before** that `FrameOutput`. This needs
   an `rt` helper that routes through the **caller's** router — `scriptedRT`
   (`sessionpin_test.go:15-28`) builds its own `NewRouter()` internally, so
   scripted `%output` would route into a router the test never observes.
4. Existing behavior preserved: blank-pane capture is a valid seed; `%error`
   capture is a rejection; a hook's `%begin…%end` (flag 0) between our blocks is
   skipped; sibling `%output` met while awaiting a batch's replies is still
   routed, not dropped; a failed sole-pane seed still fails `setupWindow`; the
   converge at `daemon.go:557` is still awaited.
5. `cd picker && go test ./...` green, and `go test -race ./remotebridge/...`
   as `nix flake check` runs it.
6. `nix flake check` green, including both offline bats bridge integration
   tests (`remote-bridge-integration.bats` for M1, `remote-m2-integration.bats`
   for the daemon).

## RTT accounting

Counting serialized control-stream round-trips (waits) for a remote session of
W windows with P panes each; `P_total = W·P`; `D` = dirty panes in one drop
re-seed.

`Run`'s startup makes four round-trips outside the per-window loop —
`list-windows`, `newSessionPin`'s `display-message`, the post-setup
`reconcileWindows` `list-windows`, and `remoteClockSkew` — and per window a
converge, a `readLayout`, and the seeding.

| Path | Before | After |
|---|---|---|
| `PaneSeed`, one pane | 2 | 1 |
| Seeding a P-pane window | 2P | 1 |
| `%layout-change` re-seed, P panes | 2P | 1 |
| `%continue` re-seed, one pane | 2 | 1 |
| Drop re-seed, D dirty panes | 2D | 1 |
| Session-excursion re-seed | 2·P_total | 1 |
| M1 `seedFlow`, tty / non-tty | 4 / 3 | 3 / 2 |
| **Startup, W windows × P panes** | **4 + W·(2 + 2P)** | **4 + 3W** |

The issue's worked example — 4 windows, 6 panes (1/2/2/1) — goes from
`4 + 4·2 + 2·6 = 24` serialized round-trips to `4 + 4·2 + 4 = 16`. At 100 ms RTT
that is ~2.4 s → ~1.6 s. Wider windows gain more: a single 4-pane window's
seeding drops from 8 round-trips to 1, and every subsequent `%layout-change` on
it from 8 to 1. A session-excursion re-seed of a 6-pane mirror drops from 12
round-trips to 1.
