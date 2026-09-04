# Floating panes in mirror windows (#419)

`controlmode.Layout.Floats` is parsed and dropped, so every float-shaped gesture
degrades inside a mirror window: the tool binds and the carousel become remote
*splits*, and `prefix + s` `^o` is refused outright (spec D7).

## What tmux actually does (probed, `tmux next-3.8`)

These are the facts the design rests on. None is reachable from a unit test.

- **`select-layout` fails on any window holding a float.** With the tiled-only
  tree it reports `have 4 panes but need 3` — floats are counted against the
  cell count. With tmux's own `#{window_layout}` string, trailing
  `<WxH,X,Y,id>` float section included, it reports `invalid layout`: the
  format tmux *emits* does not parse back in. This is the core blocker, and it
  sits on the mirror's hot path (`daemon/reconcile.go`, `daemon/mirror.go`).
- **Floats occupy `pane_index` slots.** One made with `new-pane` sorts last,
  but one lifted out with `break-pane -W` keeps its donor's position, so
  ordinals interleave. Addressing local panes by `<window>.<index>` is only
  correct for a window that has never held a float.
- **`break-pane -d -W -s <pane> -x <w> -y <h> -X <x> -Y <y>`** is the only way
  to make an existing pane float. Geometry is *not* reliably restored across an
  un-float/re-float, so it must be passed explicitly every time.
- **`move-pane -X/-Y` repositions a float within its own window only.** It
  validates the *target*, not the source, so it cannot carry floatness across
  windows: a parked float re-enters a window tiled.
- ~~park → `select-layout` → `join-pane` back → `break-pane -W` restores the
  tiled geometry exactly~~ — **wrong; measured false while speccing Stage 2.**
  Against a target tiled `[60x20 , 60x19]` the cycle returned
  `[60x10 , 60x29]`: `join-pane` re-tiles the pane into the tree, and
  `break-pane -W` does not restore the pre-join geometry. Worse, a parked float
  cannot be returned at all — `break-pane -d -W -s <parked> -t <win> -x .. -y ..`
  reports success and leaves the pane floating in *its own* window, never
  entering the target. There is no way to inject a float into an existing
  window from outside it. Stage 2 therefore drops and re-creates its mirrored
  floats around a `select-layout` instead of parking them; see
  `../specs/2026-08-31-mirror-remote-floats-design.md`.

## Stage 1 — float-aware pane addressing (landed in #420)

No behaviour change; it removes the ordinal hazard and the precondition that
blocks the next two stages.

- `Config.LocalTmuxOut` captures tmux stdout. The daemon previously had no
  source of pane identity at all — `setupWindow` said so in a comment and
  addressed panes positionally because of it.
- `mirrorWindow.localPanes` holds the window's *tiled* pane ids,
  index-parallel to `remotePanes`; `refreshLocalPanes` re-reads it from tmux
  after each structural change rather than tracking it incrementally, since a
  mirror window can acquire a pane this daemon never created.
- Every pane-addressing command (`kill-pane`, `split-window`, `swap-pane`,
  `select-pane`, `respawn-pane`) targets a `%N` id.
- `select-layout` runs only when the tiled tree actually changed
  (`mirrorWindow.layout`). It fired unconditionally every pass before; once a
  window holds a float, an unnecessary one can only fail.

## Stage 2 — mirror remote floats as local floats

Give `Layout.Floats` a consumer: one local floating pane per remote float,
renderer wired like any other pane, geometry from the float cell, reasserted
with `resize-pane`/`move-pane`. Revert `ctl.go`'s `tool` and `carousel` verbs
from `split-window` to `new-pane` on the remote. The park/restore dance above
does not work, so a `select-layout` behind an open float drops the mirrored
floats and re-creates them instead — guarded by a geometry short-circuit that
skips the `select-layout` entirely when the window fit alone already reproduced
the remote's cells.

The open question is **settled**: the inset is 1 per side for every border
style — `heavy`, `simple`, `double` and the default all measured the same — and
0 only for `-B none`. It is a property of *having* a border, not of which one.
Since the mirror pins its own float border, the inset is a constant of that
choice rather than something read back per pane. Full design:
`../specs/2026-08-31-mirror-remote-floats-design.md`.

## Stage 3 — tolerate a purely local float

A local pane the remote knows nothing about must survive reconcile, which falls
out of Stage 1's id addressing plus excluding unknown panes from the tiled set.
Ungates `^o` and retires spec D7's refusal.
