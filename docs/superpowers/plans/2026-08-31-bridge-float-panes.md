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
- **park → `select-layout` → `join-pane` back → `break-pane -W` restores the
  tiled geometry exactly**, and pane ids survive the whole cycle, so renderer
  wiring and router registration survive with no re-seed.

## Stage 1 — float-aware pane addressing (this PR)

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
from `split-window` to `new-pane` on the remote. Needs the park/restore dance
above for the case where the remote's tiled tree moves while a float is open.

Open question for this stage: the float cell in a layout string is the *inner*
box — creating `-x 60 -y 20 -X 10 -Y 5` yields cell `58x18,11,6` — so cell
geometry and creation flags differ by the border inset. Pin the relation down
rather than assuming it holds for every `-B` value.

## Stage 3 — tolerate a purely local float

A local pane the remote knows nothing about must survive reconcile, which falls
out of Stage 1's id addressing plus excluding unknown panes from the tiled set.
Ungates `^o` and retires spec D7's refusal.
