# Spec — a failed pane seed must not cost the mirror its window (#487 bug A)

## The observed failure

`prefix + p` on a **one-pane** mirror window killed the mirrored session. From
the `halo` daemon log, in order:

```
daemon: seed %2: capture-pane failed for %2 (skipping renderer)
can't find pane: %328
daemon: reconcile select-pane: exit status 1
daemon: layout-change @0: local panes desynced from the remote order: 1 local panes for 2 remote; rebuilding
can't find window: @143
daemon: layout-change reset @0: daemon: apply mirror for @0: exit status 1
```

Bug B (the unbounded retry that followed) is fixed on main by `fc6b615` (#489).
What is left is everything up to it: how a single failed `capture-pane` reply
walks the mirror all the way to a dead local window.

## What is established, and how

Every link below is read off the code in this tree. The three tmux behaviours it
rests on were measured against a real server on this machine (`tmux -f /dev/null
-L …`, a two-window session whose victim window ran a single `cat` on a fifo,
EOF'd to make its process exit):

| measured | result |
| --- | --- |
| sole pane's process exits | pane gone, **window gone**; when it was the last window, the session and server went too |
| `select-pane -t %<dead>` | `can't find pane: %N`, exit **1** |
| `capture-pane -e -p -t %<dead>` | `can't find pane: %N`, exit **1** |
| `display-message -p -t @<dead> -F '#{window_id}'` | exit **0**, output **empty** (re-confirms #152/#169, #489's basis) |

### 1. `can't find pane: %328` is about the LOCAL pane, not the remote one

The issue reads that line as adjacent evidence that *the remote server did not
know the pane the seed asked about*. It is not. `cmd.Stderr = os.Stderr` on both
local-tmux seams (`picker/remotebridge/cmd/daemon/main.go:200,205`), so **local**
tmux's stderr lands in the same `${sock}.log` as the daemon's own prints,
unprefixed. The very next line, `daemon: reconcile select-pane: exit status 1`,
is `focusLocalPane`'s wrapper around `cfg.LocalTmux("select-pane", "-t", local)`
(`daemon/reconcile.go:390-392`) — the only `select-pane` seam in the daemon, and
a **local** pane target. The measured error text and exit status match exactly.
`%328` is the local pane `applyPaneOps` had just split, already gone by the time
focus was applied.

So the line is a *consequence* of the seed failure, not evidence about the
remote. Reading it the other way sends the diagnosis after a phantom.

### 2. A failed seed closes the renderer's conn, and the renderer exits on EOF

`wireRenderer` (`daemon/daemon.go:1557-1562`) on `err != nil` logs
`skipping renderer` and calls `conn.Close()`. The renderer's read loop returns
`nil` on `io.EOF` (`render/renderer.go:52-56`), `main` falls off the end
(`cmd/renderer/main.go:20-25`), the process exits. **No production mirror pane
carries `remain-on-exit`** — nothing under `config/`, `modules/` or `scripts/`
sets it for the mirror session — so the pane exits with its renderer, and by the
measurement above a sole pane takes its window.

(`tests/remote-m2-integration.bats:31` *does* set `remain-on-exit on` on the DST
server, deliberately and for exactly this reason — see its own comment at
`:13-17`. That is why the m2 harness cannot observe this bug: with the option
on, a renderer whose conn is closed leaves a dead-but-present pane, the window
never dies, and "the mirror still has its window" passes against the **unfixed**
daemon. Coverage must not go there.)

`applyPaneOps` does not learn any of this. It `delete`s the conn from `w.conns`
(`daemon/reconcile.go:317`) and carries on with `w.localPanes` still listing the
dead id — which is why the swaps and `focusLocalPane` below it then aim at
`%328`. Note that `router.Unregister` closes the *sink*, not the conn
(`daemon/router.go:24-34`): the pane dies from `conn.Close()` alone.

### 3. The local window dies inside `resetWindow`, not at the seed

The next `%layout-change` finds 1 local pane against 2 remote →
`errLocalPanesDesynced` (`daemon/reconcile.go:217-222`) → `resetWindow`
(`daemon/reconcile.go:348-358`). Its first act is:

```go
for _, id := range w.remotePanes {
    router.Unregister(id)
    if c := w.conns[id]; c != nil { c.Close(); delete(w.conns, id) }
}
dropMirroredPanes(cfg, w)   // kills localPanes[1:], keeps [0] on purpose
return setupWindow(...)
```

`dropMirroredPanes` keeps `w.localPanes[0]` precisely so the window has a pane
to be re-shaped in (`daemon/reconcile.go:364-373`). But the loop above closes
the conn of **every** pane it knows, that surviving one included. By §2 its
renderer exits, and it is now the window's last pane, so tmux destroys the
window. `setupWindow` then reaches `PlanWindow(@143, L)` and gets
`can't find window: @143` → `apply mirror for @0: exit status 1`. Exactly the
log.

The race is not close. Between the `conn.Close()` and `PlanWindow`'s first
command, `setupWindow` runs `readLayout` — a full round-trip over ssh to the
remote (`daemon/daemon.go:991-1001`). The renderer needs one scheduler slice.

This is not specific to the desync route, and not specific to the seed bug.
`resetWindow` is also entered on `ops.Reset` — the ordinary "no surviving pane"
rebuild (`daemon/panediff.go:56-59`) — and reachable from a `waitHellos` timeout
in `applyPaneOps` that leaves the split already landed
(`daemon/reconcile.go:301-304`). Every one of those paths destroys the window it
means to rebuild. `resetWindow`'s own doc comment names the hazard it is falling
into ("Killing every pane instead would make tmux destroy the mirror window and
leave a registry entry pointing at nothing") — what it missed is that closing a
pane's conn kills that pane just as surely as `kill-pane` does.

### 4. Why the `capture-pane` itself failed — hypothesis, not finding

`PaneSeeds` reports `capture-pane failed for %2` when the capture reply is an
`%error` **or** the stream died before the reply arrived
(`daemon/seed.go:103-105`, `:138-143`). `parseCapture`'s own doc already names
the expected cause of the former: "a pane that closed between list-panes and
this capture-pane". The window between those two moments is wide —
`applyPaneOps` runs a local `split-window`, a `refreshLocalPanes`, an
`applyLayout`, and then **`waitHellos`** (a renderer process spawn plus a
unix-socket dial) before the seed is issued.

And the remote pane in question is short-lived by construction. The `tool`
verb's remote leg is `split-window … 'exec /bin/sh -c <toolResolveScript>'`
(`daemon/ctl.go:242-250`), whose body ends (`:300-307`):

```sh
command -v prdash >/dev/null 2>&1 && exec prdash; \
echo lazytmux: prdash is not on PATH on this host; sleep 5
```

If `prdash` resolves but exits immediately (missing repo, bad state, a crash),
or if the fallback's `sleep 5` elapses, the remote pane closes on its own — with
no `remain-on-exit` there either. A remote pane that closed between the layout
read and the capture is therefore an ordinary, expected event.

**This remains a hypothesis and this spec does not claim it.** It is not
reproducible from this worktree: it needs the `halo` remote, `prdash` behaving
as it did, and the timing that made it. What the spec claims instead is the
stronger and sufficient statement: **a `%error` (or a lost stream) on a seed's
`capture-pane` is a legitimate outcome the daemon must survive**, whatever
produced it — `parseCapture` was written on exactly that premise.

## Requirements

**R1 — a failed pane seed must leave its pane fully wired.** After
`applyPaneOps` returns, a pane whose seed failed must still be there, with its
conn open, its sink registered on the router, and `pumpInput` running for it.
That is the *only* admissible outcome: `w.localPanes` is regenerated wholesale
from tmux by `refreshLocalPanes` (`daemon/localpanes.go:39-46`), so the only way
to take an id out of it is to kill the local pane — which is R2's forbidden
state, not an alternative to it.

**R2 — a failed pane seed must not desync the mirror.** `len(w.localPanes)`
must still equal `len(w.remotePanes)` on the pass that follows, so a seed
failure does not route the mirror into `resetWindow` at all.

**R3 — the pane must be repaired by the reseed that already covers it.**
`reconcileLayout` re-seeds every pane in `newRemote` that has a **registered
sink**, after the reshape, in the same pass — and its comment already says a
newly appended pane is included, because `applyPaneOps`' own seed was painted
into a pane that was still the old size (`daemon/reconcile.go:102-126`,
#233/#417). So registering the sink is not merely "keeping the pane alive": it
is what puts the pane inside that existing repair set. No new `outputSink`
affordance is required, and `reseedDropped` is **not** the path — it is reached
only through `takeDirty`, which needs `dropped != 0`, and a freshly registered
sink has dropped nothing (`daemon/daemon.go:1749-1758`, `daemon/router.go:65-81`).

Registering also makes the pane visible to the reconnect repair pass, which
resumes and re-seeds only sinks that are registered (`daemon/daemon.go:904-910`,
`:941` → `reseedPanes`, `daemon/sessionpin.go:187-194`). That matters for the
lost-stream half of §4's failure signal, where "the pass that follows" arrives
only after a reattach.

If the trailing reseed also fails — the remote pane really is gone — the pane
stays blank and the remote's own `%layout-change` removes it on the next pass.
Bounded, and no window is lost.

**R4 — `resetWindow` must not destroy the window it is rebuilding** (co-primary
with R1, not a fallback: §3 establishes it as the *proximate* cause of the lost
window, and it is reachable without the seed bug at all). The pane
`dropMirroredPanes` keeps must still have a running renderer when
`setupWindow`'s re-shape lands.

The fix must be **mapping-free**. There is no reliable local→remote pane
mapping available here: it is positional only (`daemon/localpanes.go:11`,
`:39-46`; `@bridge_pane` is written at `daemon/daemon.go:1455-1458` and never
read back), and in the desync case that reaches `resetWindow` it is broken *by
definition* — that is what `errLocalPanesDesynced` reports. So "skip
`w.conns[w.remotePanes[0]]`" is not acceptable: it fixes the one trace in the
log and still kills the window whenever the survivor renders some other index.

**R5 — the seed contract must not be weakened, and its one relaxation must be
explicit.** The frozen-wire invariant holds: no daemon→renderer frame may bypass
the sink. `PaneSeeds`' per-pane `onSeed` delivery ordering and the "no
round-trip inside `onSeed`" rule (#430) hold unchanged. The relaxation, stated
outright so nobody re-derives an orphan or an inline retry from a strict
reading: **for a pane whose seed failed there is no seed, so its sink's first
frame may legitimately be a `FrameOutput` or a `FrameResize`.** The
seed-before-output guarantee binds a seed and the output for the same pane; it
does not require a seed to exist. The pane starts blank rather than stale, so
#412's "output over a screen the remote has moved past" does not arise.

**R6 — `setupWindow`'s non-sole seed failure is in scope; its sole-pane
fatality is not.** `setupWindow` is not a creation-only path: `resetWindow`
calls it (`daemon/reconcile.go:357`) and `retireMirror` reaches it through
`reconcileWindows` (`daemon/daemon.go:1148-1151`). Its non-sole branch
(`daemon/daemon.go:1054-1066`) has the identical orphan defect R1 removes, on a
window already on the user's screen, so it must be fixed with it — and it must
be, because the natural locus of the fix is `wireRenderer`'s `conn.Close()`,
which both callers share.

The sole-pane *fatality* stays: it is what makes `addWindow` /
`mirrorNewWindow` tear a half-built mirror down instead of publishing a blank
one behind a live registry entry. But if `wireRenderer` stops closing the conn
and starts registering the sink, that path must now dispose of both itself —
unregister the sink and close the conn before returning the error — rather than
relying on `wireRenderer`'s side effect as it does today.

## Non-requirements

- Establishing why `capture-pane` failed on `halo`. Stated as a hypothesis in
  §4; the fix does not depend on it.
- Making the remote pane's own lifetime more robust (`remain-on-exit` on remote
  tool panes, retrying `prdash`). Different bug, different surface.
- Re-litigating #489.
- A retry of the seed inside `applyPaneOps`. The failure this must survive is a
  pane that is *gone*, which no retry can fix; and R3 shows the pass already
  re-seeds this pane moments later, so a retry buys latency in the common case
  for nothing in the failing one.
- `remain-on-exit` on local mirror panes. It would stop a renderer's exit from
  taking the window, but it would also leave dead panes on screen for every
  legitimate teardown and change the shape of every reconcile; a far larger
  change than the two this needs.

## Acceptance

Stated in terms a Go test can actually observe — nothing here asserts "the pane
is alive", which the fake `LocalTmux`/`LocalTmuxOut` harness cannot model.

1. **Seed failure on an incrementally added pane.** Driving `applyPaneOps` with
   a scripted `%error` capture reply (`setupWindowRT` / `scriptedRT` +
   `net.Pipe`), on return: the renderer's conn is **not** closed (its `net.Pipe`
   peer is still writable), `router.sink(id) != nil`, `w.conns[id] != nil`,
   `len(w.localPanes) == len(w.remotePanes)`, and no `kill-pane` appears in the
   `LocalTmux` trace.
2. **Input still flows.** A `FrameInput` written by the fake renderer peer
   reaches `send` as a `send-keys` for that remote pane — the B3 regression: a
   pane kept alive that swallows keystrokes is a worse silent failure than the
   one being fixed.
3. **The pane is in the trailing reseed set.** With the post-reshape reseed
   scripted to succeed, a `FrameSeed` arrives at the peer after the reshape.
4. **`resetWindow` keeps its kept pane's renderer.** The conn belonging to the
   pane `dropMirroredPanes` retains is still open when `setupWindow`'s re-shape
   commands run, with no dependence on a local→remote mapping.
5. **`setupWindow`'s sole-pane failure still errors and still cleans up** — the
   error mentions `sole pane`, and no sink stays registered and no conn stays
   open for it.
6. `nix build .#default`, `nix flake check`, `nix build .#lint` all pass.
7. Plan and spec committed under `docs/superpowers/{plans,specs}/`.
8. The PR body separates what was established (§1–§3, plus the measured tmux
   table) from what is hypothesis (§4).

Tests live in `picker/remotebridge/daemon` (run by `picker-go-tests` inside
`nix flake check`). Explicitly **not** in `tests/remote-m2-integration.bats`:
its DST server sets `remain-on-exit on` (§2), so it is blind to this failure —
and it carries a known load-correlated non-zero-exit flake on `main`.
