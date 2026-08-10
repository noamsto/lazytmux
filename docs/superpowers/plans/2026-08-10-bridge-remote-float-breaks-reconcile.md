# Bridge: Remote Floating Pane Breaks Mirror-Window Layout Reconcile

**Issue:** [#348](https://github.com/noamsto/lazytmux/issues/348)

## Root cause (measured)

tmux next-3.8 (the pinned `tmux-upstream` input) encodes a window's floating panes two ways in
the same `window_layout` string:

1. As ordinary leaves interleaved into the tiled split tree — e.g. two tiled panes plus one float
   serialize as `aaee,80x24,0,0{40x24,0,0,0,30x7,9,3,2,39x24,41,0,1}<30x7,9,3,2>`, where the float
   (`%2`) sits as a third child of the `{...}` split, between the two real tiled panes.
2. Again, as a trailing `<WxH,X,Y,id[,WxH,X,Y,id...]>` section listing exactly the float ids (one
   `<...>` wrapper holding all floats, comma-joined — not one `<...>` per float, verified by
   capturing a two-float layout).

tmux 3.7b has neither: no floats in the tree, no trailing section.

`picker/remotebridge/controlmode/layout.go`'s `cell()` parser handled only `,`, `{` and `[`. The
trailing `<...>` was never consumed, so `ParseLayout` failed its trailing-data check and
`readLayout`/`reconcileLayout` (`daemon/reconcile.go:30`) logged to stderr and returned — the
moment a bridged remote window opened any floating pane, that mirror window stopped reconciling
until something else reset it.

Fixing only the trailing-data parse isn't enough: the float leaf embedded in the tiled tree would
still (a) appear in `RemotePaneOrder`/`planPaneOps` as a phantom new tiled pane to split locally,
and (b) corrupt the layout string handed to `select-layout` — tmux rejects its own float-bearing
layout string when replayed (`select-layout -t @0 '...<...>'` → `invalid layout: ...`, verified by
self-applying a captured layout to the very window it came from, so pane count trivially matched).

## Fix

`ParseLayout` now:

1. Parses the trailing `<...>` section (if present) into `[]PaneCell` — the authoritative set of
   float ids for this window.
2. Prunes leaves with those ids out of the parsed tree (`pruneFloats`), collapsing any split left
   with a single surviving child — tmux layout syntax requires ≥2 children per split, and a window
   with exactly one tiled pane plus a float serializes the tiled pane inside a framing split purely
   to carry the float sibling (e.g. `[80x24,0,0,1,30x7,9,3,2]<30x7,9,3,2>` prunes to the bare
   `80x24,0,0,1`, matching what 3.7b emits with no floats at all).
3. Re-serializes the pruned tree (`writeCell`) and recomputes the layout checksum
   (`layoutChecksum`, tmux's `layout_checksum` rotate-right running sum from `layout.c` —
   `select-layout` validates the checksum against the body and rejects a mismatch, verified) to
   produce `Layout.Raw` — the string `PlanWindow`/`reconcileLayout` feed to `select-layout`. `Raw`
   is byte-identical to the input when there are no floats.
4. Exposes the parsed floats on `Layout.Floats` for follow-up work. They are not mirrored — the
   mirror window simply doesn't show them yet, which is the documented, expected behavior of this
   fix.

`Layout.Panes` (and therefore `RemotePaneOrder`/`planPaneOps`, both unchanged) now only ever see
the pruned, tiled-only tree, so a remote float can no longer produce a phantom local pane-op nor a
`select-layout` failure.

## Verification

- `controlmode/layout_test.go`: new fixtures captured live from the pinned
  `tmux-next-3.8` binary (single tiled pane + 1 float; 2 tiled panes + 1 float; 2 tiled panes + 2
  floats) plus a no-floats regression pin. Each reconstructed `Raw` string was independently
  confirmed against real tmux via `select-layout -t <win> '<reconstructed>'` → accepted.
- `daemon/mirror_test.go`: `TestRemotePaneOrderExcludesFloats` proves a remote float never reaches
  `RemotePaneOrder`, and that a mirror already rendering the tiled panes sees a true no-op
  (`planPaneOps` returns zero ops) rather than a phantom `Append` for the float.
- `nix build .#default`, `nix flake check` (incl. `picker-go-tests`), `nix build .#lint`: all
  green.

## Explicitly out of scope

- Actually mirroring the float into a local floating pane (separate, larger follow-up: local
  `new-pane -x -y -X -Y` at translated geometry, output routing, teardown, focus).
- Any change to `config/tmux.conf.nix` popup keybinds — owned by a concurrent worker.
