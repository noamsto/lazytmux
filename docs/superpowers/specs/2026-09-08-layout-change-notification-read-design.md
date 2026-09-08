# Take the layout and zoom flag from `%layout-change`

Closes #570.

## Problem

`dispatch`'s `LayoutChange` branch reads only `l.Args[0]` (the window id) and
hands the window to `reconcileLayout`, whose first act is an ssh round-trip:
`readLayout` runs `display-message -p -t <win> -F '#{window_layout} #{pane_id}
#{window_zoomed_flag}'`. Two of those three values were on the wire in the very
notification that triggered the read.

Verified against the pinned tmux (`578e07fc`, next-3.8):

- `control-notify.c:79` builds the notification from a format template,
  `%layout-change #{window_id} #{window_layout} #{window_visible_layout}
  #{window_raw_flags}`, expanded with `format_single` against the window's
  first winlink. Field 2 is therefore **byte-identical** to what `readLayout`
  reads: the same `format_cb_window_layout`, which returns the saved
  (unzoomed) tree while zoomed (`format.c:863`). Field 4 carries `Z` iff
  `w->flags & WINDOW_ZOOMED` (`window.c:1313`), the same bit
  `#{window_zoomed_flag}` reports (`format.c:3414`). The pinned tree's
  `CHANGES:1236` records only the 3.2 switch from `window_flags` (`#` doubled
  to `##`) to `window_raw_flags`; that field 4 predates 3.2 as `window_flags`
  is remembered, not read from this tree, and the parse below does not depend
  on it — every other historical shape falls to the read path.
- `layout_dump` (`layout-custom.c:61`) emits no spaces, floats included
  (`<…>` section, comma-joined), so `bytes.Fields` in `controlmode.parseLine`
  splits the line correctly and `Args` already holds every field. It returns
  `NULL` when the dump exceeds its 8192-byte buffer, and a `NULL` callback
  expands to the empty string, so a field can in principle vanish and shift
  the ones after it left.
- `window_printable_flags` can return the empty string (`window.c:1290`), so
  a window that is not current/last and carries no activity/bell/silence/
  mark/modal/zoom arrives as **three** fields.
- `%window-pane-changed` (`control-notify.c:116`) is emitted from
  `window_set_active_pane(..., notify=1)` and `window_lost_pane`. It is **not**
  emitted by `window_copy_scroll` (`window-copy.c:771`, a mouse scroll in copy
  mode selects the pane with `notify=0`), by `spawn_window`'s respawn path
  (`spawn.c:154`), or by any `SPAWN_NONOTIFY` spawn (`spawn.c:600-607`).
- **Zoom is pushed and popped around most structural commands.**
  `window_push_zoom` (`window.c:1054`) unzooms with `notify=1`, which fires a
  `window-layout-changed` carrying the tiled layout and **no `Z`**; the
  command then mutates the layout and fires again (still no `Z`); then
  `window_pop_zoom` re-zooms and fires a third time (with `Z`). Verified
  bracket sites: `server_kill_pane`/`server_destroy_pane` (`server-fn.c:229,
  425`), `cmd-split-window.c:290/345`, `cmd-rotate-window.c:55/111`,
  `cmd-swap-pane.c:85/114`, `cmd-select-pane.c:225-239`,
  `layout_get_tiled_cell`/`layout_get_floating_cell` (`layout.c:1661, 1690` —
  so opening a float on a zoomed window brackets too), `spawn.c:750-781`,
  `cmd-switch-client.c:146/150`; `resize_window` (`resize.c:61`) does the same
  by hand. The list is what a grep for `window_push_zoom` finds today, not a
  promise. So on a **zoomed** window every one of these emits one or two lines
  that describe a state the remote leaves before the command returns — a
  changed layout with the zoom flag off is the middle line of `kill-pane` on a
  zoomed window, not a user unzooming.
- `events_fire` is synchronous (`events.c:94`), so those lines are written to
  the control client back-to-back inside one command; they are genuinely
  observable, and how many of them the daemon dispatches individually rather
  than coalesced depends only on scheduling.
- Duplicates are the norm: `resize-pane` fires from `layout_resize_layout`
  (`layout.c:976`) and again from `cmd-resize-pane.c:186`; `select-layout`
  from `layout_parse` (`layout-custom.c:289`) and again from
  `cmd-select-layout.c:142`; every ctl verb this daemon sends is echoed back as
  a notification after the intent reconcile already applied it. Today each
  duplicate costs one `readLayout` round-trip to discover it is a no-op.
- `runConn` dispatches lines off the pump **one at a time**, uncoalesced
  (`daemon.go:970`); only the `asyncQueue` batch that reply readers fill
  during a round-trip is coalesced (`settle`, `daemon.go:906`). A layout
  string carries no sequence number: a notification whose layout differs from
  `w.layout` cannot be told from a fresh one.

## Decision

**Land option (1), and be precise about what it is: the notification becomes
the authority for "is there anything to do", and the input for a
geometry-only reshape. The leading round-trip is dropped on those two paths.
Everything that involves the pane set, the zoom state, a float, or the active
pane id keeps today's ground-truth read. Option (2) is declined.**

The "read" here means the leading `readLayout` — the tuple `(layout, active
pane, zoom)` fetched in one round-trip at the top of `reconcileLayout`. What
the notification path substitutes for it is `(layout string, zoom flag)` from
the line, and on the one path that applies anything, the layout string is a
*belief* about the remote's geometry. That belief has a smaller blast radius
than the active pane id only because of how the gate below is cut.

### The gate

A `%layout-change` line is parsed positionally, not by count: with 3 fields,
fields 2 and 3 must both be layout-shaped (four hex digits then a comma) and
the flag is off; with 4 fields, fields 2 and 3 must be layout-shaped and field
4 must match the flag alphabet `[#!~*\-MOZ]+` (`window.c:1296-1315`), and the
flag is `Z ∈ field 4`. Anything else — 2 fields, a shifted line from an
overlong dump, a field 2 that does not `ParseLayout` — takes today's read
path. The shift that would matter (field 2 emptied, everything moved left) is
only reachable while zoomed — unzoomed, `saved_layout_root` is `NULL` and both
layout fields dump the same tree, so they overflow together — and while zoomed
the shifted field 3 is the `Z`-bearing flags string, which is not
layout-shaped, so the 3-field rule rejects it.

The checks run **pure ones first**, so the one local fork the gate can cost is
paid only where today's dedup already pays it (after its read, on a layout-
equal line) and never where no round-trip is saved. With `L` the parsed
layout, `order` the remote pane order `L` implies, and `local` the mirror
window's own zoom state:

1. **Pane set or order differs** — `order != w.remotePanes` → read (pure). A
   structural change needs the active pane for focus-follow anyway, and a
   *stale* structural notification is the one that hurts: applied, it would
   `split-window` a local pane, spawn a renderer, and `capture-pane` a remote
   pane that no longer exists, then kill it on the next pass (the #487 seed-
   failure territory). `kill-pane -a`'s per-pane loop (`cmd-kill-pane.c:82`)
   is the same shape: N intermediate pane sets, of which the read sees only
   the last.
2. **Floats differ** — `!noFloatWork(w, L)` → read (pure). Float reconcile
   focus-follows an added float, which needs the active pane.
3. **Layout unchanged** — `L.Raw == w.layout` → fork `localZoomed` once (the
   fork today's dedup pays at `reconcile.go:57` after its read):
   - `ok=false` (a nil or erroring `LocalTmuxOut`) → read, as today. Parity,
     not a widening: a dead local window reads `ok=true, zoomed=false` on both
     paths (`localpanes.go:81`, #152/#169) and the retire path owns it.
   - flag `== local` → **return. Zero remote round-trips; the fork is the
     only local command.** Duplicates, echoes of the daemon's own verbs, and
     the trailing (re-zoom) line of a push/pop zoom bracket once the read has
     applied its predecessor all land here.
   - flag `!= local` → read: a real zoom or unzoom by any remote client
     (#413), or the unzoom transient that opens a push/pop bracket. Today's
     read runs after the command completed and returns the post-`pop_zoom`
     truth, so the mirror sees one consistent state and takes one pass; that
     is preserved, and the read buys the active pane `resize-pane -Z` needs.
4. **Layout changed, flag on** → read (pure). A zoom with a reshape needs the
   active pane for the toggle and the zoomed pane's dims.
5. **Layout changed, mirror holds a mirrored float** — `len(w.localFloats) >
   0` → read (pure). `applyLayout` drops the mirrored floats when
   `localCellsMatch` misses (`reconcile.go:395`), the post-loop
   `reconcileFloats` re-adds them, and its focus-follow of a re-added float
   that is the remote's active pane (`reconcile.go:272`) needs that pane's id —
   which only a read supplies.
6. **Geometry-only reshape** — same pane set and order, same floats, no local
   floats, flag off, tiled geometry differs → enter the existing pass loop with
   `L` from the notification, `zoomed = false`, and **no active pane id**. No
   fork, no round-trip before the loop. No branch of the loop consumes the id
   on this path: focus-follow is gated on `structural` (false by
   construction), the `-Z` toggle and the zoomed-pane dims are gated on a zoom
   the notification does not report, and the post-loop float focus has nothing
   to add (2 and 5). The mirror may itself still be zoomed here — the line
   after `cmd-resize-pane.c:88`'s `window_unzoom` is a real unzoom-and-resize,
   not a transient — and the loop already handles that: `select-layout`
   unzooms the mirror (measured, per the comment at `reconcile.go:139`) and
   the in-loop `localZoomed` then agrees with the flag. If the trailing re-read
   finds the layout moved, the next pass runs on the re-read's own `(layout,
   active, zoom)` triple — ground truth, atomic, exactly as today's later
   passes do.

No push/pop-zoom bracket enumerated above has a geometry-only middle line
(they wrap pane-set, order, float and focus changes), so gate 6 never sees a
transient; were a future tmux to add one, the cost is one zoom flap corrected
by the trailing re-read, not a stuck state.

Callers that reconcile without a notification — `settle`'s ctl-verb layout
intents, `reconcileWindows`, the reattach repair pass, `retryFailedShapes` —
keep the read-first entry untouched, as does `setupWindow`. The pass loop, the
trailing re-read, `applyLayout`, `reconcileFloats` and everything after the
read site are untouched: the change is an entry gate that either returns, seeds
the loop, or calls today's entry.

### Coalescing

`coalesceLayoutChanges` keeps the **last** `Line` per window whole, `Args`
included, so a queued batch hands the gate the newest layout it has. The live
path is not coalesced and stays that way: the trailing re-read (unchanged) is
what corrects a notification the remote had already moved past, and on the one
path that applies from the notification (6) the correction is one extra
geometry pass — never a renderer spawn, kill or dead-pane capture, because (4)
sends every pane-set change to the read. The bound is **per burst**, not per
line, and the two halves connect: the stale line's own round-trips (seeds,
re-read) drain the rest of the burst off the pump into the `asyncQueue`, which
`settle` coalesces to the newest line — by then equal to what the re-read's
pass applied, so the tail is one gate-2 no-op. Draining the pump non-blockingly
before dispatch was considered and rejected: it touches the main loop's
per-line `claimSeq` contract for a saving the re-read already bounds.
`coalesceLayoutChanges`' doc comment, which justifies itself with "reconcile
always re-reads fresh", is rewritten to say what is now true: the last line is
the one the gate reads.

`retryFailedShapes` is unaffected in the direction that matters: `applyLayout`
leaves `w.layout` stale when `select-layout` fails behind a float
(`reconcile.go:411`), so gate 2 can never swallow a line for a window in that
state — the layout differs, gate 6 re-applies, re-fails and re-arms
`shapeFailedFor`, and the retry pass still owns the recovery through the
read-first entry.

### Round-trip accounting (remote round-trips per dispatched notification)

| case | today | after |
| --- | --- | --- |
| duplicate / echo / trailing pop-zoom line (3, flag == local) | 1 (read) + 1 fork | **0** + 1 fork |
| geometry-only reshape, e.g. `watchResize` converge (6) | 3 (read, seeds, re-read) | **2** (seeds, re-read) |
| stale geometry-only line in a live burst (6) | 3 + 1 (coalesced tail no-ops via read) | 4 (seeds, re-read, seeds, re-read; tail costs 0) and **one extra pass** |
| zoom toggle either direction (3, flag != local; 4) | 3 | 3 |
| structural (1) | 3 | 3 |
| float open/close (2), or a reshape with a mirrored float (5) | 1 + float work / 3 | same |

Local cost: none added. The one fork the gate can pay (`localZoomed`, ~30ms
measured — `reconcilewindows.go:283`) is paid only on a layout-equal line,
where today's dedup pays the same fork after its read; the pure checks are
ordered first so no other path forks. A geometry reshape still forks
`resize-window`, `localCellsMatch`, `select-layout` and the pass loop's own
`localZoomed`, exactly as today. So every row is a strict saving or equal, and
the decision does not depend on how a round-trip compares to a fork.

### Why not option (2)

Tracking the active pane from `%window-pane-changed` trades a read for a
belief, and the belief has holes in this tmux: `window_copy_scroll` moves the
active pane with `notify=0`, so a mouse scroll in copy mode on the remote
silently desynchronises it; `SPAWN_NONOTIFY` and the respawn path do the same.
The bridge also needs a read-first entry regardless (intents, repair,
`retryFailedShapes`), so the round-trip cannot be deleted. What (2) would add
over this design is skipping the read on structural ops and zoom toggles —
precisely where a wrong pane id focuses the wrong renderer or zooms the wrong
cell, on a repo with #233/#412/#417 in its history — for one round-trip per
user-paced gesture.

### Why the zoom and structural paths still read

Applying the notification on those paths is where a stale or transient line
does damage: the push/pop-zoom middle line would unzoom the mirror, reseed
every pane, then re-zoom and reseed again on the trailing pass — a zoom flap
and two repaints where today there is one; and a stale structural line does
renderer surgery on a pane set the remote has already left. Routing both to
the read keeps today's single consistent pass. The paths that do skip the read
are the ones where being wrong costs one geometry pass (6) or nothing (3).

### Cost of being wrong, bounded

On (6) a stale layout is applied: `applyLayout` pins the mirror window to the
stale `L.W×L.H` and shapes its panes to the stale cells, and `PaneSeeds` then
captures the remote at its **current** geometry and paints that into them — for
the length of one pass the mirror shows the remote's present screen in panes
of the previous size. The trailing re-read (unchanged) sees the newer layout,
the next pass reshapes and reseeds, and `markReshaped` already owes every
reshaped pane a second capture a main-loop pass later, which is what repairs
the content. So the bound is one pass of mismatched dims inside the same
`reconcileLayout` call — the same shape as a remote app that has not yet
repainted for a resize — never a stuck mismatch (#233/#417 were seeds that
*stayed* wrong), and never renderer surgery, because no pane is created or
killed from the notification (gate 1). On the no-op return a stale line that says "unchanged" is
safe because every layout mutation fires its own line after the mutation
completes, and that line is behind it in the stream. Reattach and
`registry.generation` paths are read-first and unchanged.

### The mirror's zoom comparand and #568

Gate 3 needs "the mirror window's own zoom state", and that is a **hard
requirement of any no-op gate, not a term a merge may drop**: a flag-off line
with an unchanged layout is either a duplicate (mirror unzoomed) or a real
remote unzoom (mirror zoomed — `$SRC resize-pane -Z` on a zoomed window, the
existing m2 case at `remote-m2-integration.bats:1194`), and only the mirror's
state tells them apart. Without it gate 2 would swallow every remote unzoom and
that test goes red; the alternative, reading on every flag-off unchanged line,
is no gate at all. The dedup on `main` has the identical requirement at
`reconcile.go:57`.

This PR lands with the comparand `localZoomed` (`localpanes.go:62`) — the
mirror window's actual state, the same source and the same call the dedup uses
today. #568 (sibling branch, in flight) changes how the mirror's zoom is
*applied* and drops `localZoomed` from that side. #568 owns what the dedup
site becomes — a kept `localZoomed` read, a recorded field, or an absolute
idempotent assert of the flag that needs no comparand at all — and this gate
takes the same shape, whichever it is. That is the expected merge conflict the
task doc names: one predicate at one site. What the merge is not free to do is
resolve either site to "layout equal ⇒ no-op" with the flag ignored; the m2
unzoom case is the guard. This change introduces no recorded zoom belief of
its own: that would compare against what the daemon last asserted rather than
what the mirror window is, a behaviour change on the apply side and #568's to
make.

## Non-goals

- Not touching how the mirror's zoom is *applied* (`resize-pane -Z`, the
  toggle-on-mismatch) — #568.
- Not removing the trailing re-read or the pass loop; not coalescing the live
  path.
- Not changing `setupWindow`, `readLayout`'s format, or the intents/repair
  entries.
- Not fixing the pre-existing `shapeFailedFor` leak: after a `select-layout`
  failure behind a user float, a remote that returns to the last good layout
  hits the dedup (today) or gate 3 (here) without clearing `shapeFailedFor`,
  so `retryFailedShapes` re-drives that window once per sweep. Same behaviour
  on both paths; filed separately if it matters.

## Landing shape

Two commits, so the one belief is revertable alone: (a) the parse plus gates
1-5 — the zero-round-trip no-op and every read-routing predicate, which apply
nothing from the notification; (b) gate 6, the geometry-only apply from the
notification.

## Acceptance

End-to-end (m2 bats, real tmux, `--test-local`):

- **Benefit, fails on `main`:** on a settled, unzoomed 2-pane mirror, with an
  `after-display-message` hook on the SRC server counting into a global
  option, quiesce until the counter holds still (nothing in the daemon issues
  a periodic `display-message` at the remote: the shippers' backstops are
  `list-windows`/`list-panes`, and the clock-skew, session-pin and theme
  probes are per attach), then `select-layout` the window's own
  `#{window_layout}` on SRC. tmux emits two `%layout-change` lines for it
  (`layout-custom.c:289`, `cmd-select-layout.c:142`) and the daemon issues
  **no `display-message` at SRC** for either: the counter is unchanged after a
  settle. On `main` each dispatched line costs one `readLayout`
  `display-message`, so the counter moves by at least one; the negative
  control is this test run against `main`, output in the PR.
- **Z-parse discriminator, both directions:** (a) from a settled **unzoomed**
  2-pane mirror, `$SRC resize-pane -Z` with no ctl verb — the flag arrives
  **on**, the layout is unchanged, and gate 3 must read and zoom the mirror
  (`dst_z` reaches 1); with the flag misread as off, gate 3 sees flag `==
  local` and swallows the line, and the mirror never zooms. (b) The existing
  leg at `:1194` — `$SRC resize-pane -Z` on a window the ctl verb had zoomed —
  is the **unzoom** direction: the flag arrives off against a zoomed mirror,
  gate 3 reads; with the flag misread as on, it swallows the line and the
  mirror stays zoomed. Both are real-tmux parses of field 4, which the scripted
  unit tests cannot substitute for.
- **Geometry-only burst (gate 6) — regression net:** on a settled unzoomed
  2-pane mirror, several `resize-pane -L` in immediate succession on SRC (no
  pane-count change, no zoom); DST's per-pane dims converge to SRC's and hold,
  and pane content repaints at the final geometry (the #231 test's content
  check). It does not prove gate 6 was taken and cannot be made to: the
  burst's own round-trips drain the tail into the coalesced queue, so which
  line was stale is not observable from outside. Gate 6 is discriminated in
  the unit layer (below), where the read count on the stream is exact.
- **Structural burst regression net:** `split-window` immediately followed by
  `kill-pane` on SRC, once on an unzoomed and once on a zoomed window; DST's
  pane dims and zoom flag converge to SRC's. Convergence-only, so a disabled
  gate 1 still passes it (the trailing re-read or `resetWindow` converges the
  end state); kept as a regression net, with gate 1 netted in the unit layer.

Unit (`picker/remotebridge/daemon`, scripted round-trips):

- Parse: 4 fields with `*Z` → zoomed; 4 fields with `*` → not zoomed; 4
  fields with `##Z` → zoomed; 3 fields → not zoomed; 2 fields → rejected; 3
  fields whose third is flag-shaped (a shifted line) → rejected; a 4th field
  that is not flag-shaped → rejected.
- Gate 3 no-op: notification layout equal to `w.layout`, flag off, mirror
  unzoomed → the control stream receives **no bytes** and `LocalTmux` is never
  called; same with flag on and mirror zoomed. Negative control: the same
  script fed to the read-first entry writes a `display-message` — the
  assertion is on `sent.Len()`, which today's code cannot keep at zero.
- Gate 3 transition: flag on, layout unchanged, mirror unzoomed → the read is
  issued and the zoom is applied from the read (the #413 case via the
  notification). Flag off, layout unchanged, mirror zoomed → the read is issued
  and, with the read reporting zoomed, **no** `resize-pane -Z` is sent (the
  push/pop transient does not flap).
- Gate 3 unknown: `LocalTmuxOut` failing → the read is issued.
- Gate 1: a notification whose pane set differs from `w.remotePanes` → the read
  is issued and no `split-window`/`kill-pane` is derived from the
  notification's set; the gate is not reached by a fork (`LocalTmuxOut`
  fails the test if called before the read).
- Gates 2, 4, 5: floats differing, flag on with a changed layout, and a
  mirrored local float with a changed layout each → the read is issued, and
  no fork precedes it.
- Gate 6: a same-pane-set geometry change, flag off, mirror unzoomed, no
  local floats → `select-layout` is sent with the notification's `Raw`,
  exactly one `#{window_layout}` read appears on the stream (the trailing
  one), no `display-message … #{pane_id}` read precedes the seeds, and no
  `#{window_zoomed_flag}` read of any kind precedes `select-layout`
  (`localCellsMatch`'s local `#{window_layout}` read does and is expected;
  the pass loop's own `localZoomed` follows `select-layout`). Negative
  control: routing gate 6 to the read makes the stream count two.
- Staleness heal on gate 6: the trailing re-read reports a different layout →
  that layout is applied in the next pass and recorded in `w.layout`.
  Negative control: with the trailing re-read's loop suppressed, the stale
  `Raw` stays in `w.layout` — run once, output in the PR, per the task doc's
  requirement for a read moved onto the stream.
- `coalesceLayoutChanges` keeps the last line's `Args`.

Gate and docs:

- `nix build .#default`, `nix flake check`, `nix build .#lint` green; output
  in the PR.
- `CLAUDE.md`'s zoom bullet describes the gate: which notifications are
  answered from the line, why zoom and pane-set changes still read
  (push/pop-zoom transients; stale structural lines), and that the live path
  is uncoalesced with the trailing re-read as the corrective.
  `coalesceLayoutChanges`' doc comment is rewritten. Plan committed under
  `docs/superpowers/plans/`.
