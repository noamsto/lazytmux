# Mirror remote floating panes as local floats (#409) — design

Stage 2 of `docs/superpowers/plans/2026-08-31-bridge-float-panes.md`. Stage 1
(float-aware pane addressing) landed in #420; this spec covers giving
`controlmode.Layout.Floats` a consumer, plus the one Stage 1 piece #420 left
behind.

## Problem

Inside a mirror window the tool binds (`prefix + p`/`g`/`G`/`y`) open a **split**
on the remote where locally they open a **float**. That is deliberate today
(`daemon/ctl.go`'s `tool` verb): `ParseLayout` prunes floats out of the tiled
tree into `Layout.Floats`, and nothing reads `Floats`, so a remote float would
simply vanish from the mirror. Running the tool locally instead is not an option
— prdash/lazygit/yazi have to run where the repo is.

## What Stage 1 already did, and what it did not

#420 landed the addressing half:

- `mirrorWindow.localPanes` holds the window's **tiled** ids only
  (`parseLocalPaneList` splits on `#{?pane_floating_flag,1,0}`), re-read from
  tmux after each structural change.
- Every pane-addressing command targets a `%N` id, never an ordinal.
- `applyLayout` skips `select-layout` when `L.Raw == w.layout`.

It did **not** settle what happens to a `select-layout` issued while the window
holds a float. That is fine today, because no mirror window ever holds one — but
it is a hard precondition for Stage 2: the moment a local float exists, any
`select-layout` for a genuinely-changed tiled tree fails with
`have N panes but need M`.

## Probed tmux facts (next-3.8) — measured in this worktree, not inherited

Probed against the pinned `mkTmux` next-3.8 binary on an isolated server
(`tmux -L probe409 -f /dev/null`). Several of these **correct** the Stage 1 plan
doc and the task brief.

- **`select-layout` fails on any window holding a float** — confirmed three
  ways: with the tiled-only string, `have 4 panes but need 3`; replaying tmux's
  own emitted `#{window_layout}`, `invalid layout`; and with the un-pruned tree
  (float kept as an ordinary leaf, trailing `<…>` section stripped, checksum
  recomputed), `size mismatch after applying layout` — a float leaf does not
  satisfy the tree's cell arithmetic. **There is no float-tolerant
  `select-layout`.**
- **A float's layout cell is the *inner* box, inset 1 per side.**
  `new-pane -x 60 -y 20 -X 10 -Y 5` yields cell `58x18,11,6`. Measured across
  every border style: `heavy`, `simple`, `double` and the default all inset by
  1; **only `-B none` has zero inset**. Since the mirror always creates its
  floats with one fixed border, the inset is a constant for this code.
- **`resize-pane -x/-y` and `move-pane -X/-Y` speak the *outer* box**, the same
  space as `new-pane`'s flags — not the cell. Measured: `resize-pane -x 60 -y 20`
  on a float created at `-x 60 -y 20` is a no-op, while `resize-pane -x 58 -y 18`
  shrinks the cell to `56x16`.
- **A float's cell IS its usable pane size** — the probe's `58x18` cell matched
  `#{pane_width}x#{pane_height}`. So cells feed `seedRenderer` dims and
  `wire.EncodeResize` unconverted; only tmux's create/resize/move *flags* take
  the inset.
- **`respawn-pane -k` on a float preserves floatness, geometry AND pane
  options** (`@bridge_pane`, `@float_geom` both survived). `spawnRenderer` needs
  no float-specific variant.
- **`new-pane -d` does not move the active pane**; `new-pane -d -P -F
  '#{pane_id}'` returns the new pane's id, so the Add path captures it in one
  call rather than re-listing.
- **`-A` floats survive a zoom**, and `#{window_layout}` keeps both the unzoomed
  tiled geometry and the trailing float section while zoomed — so `L.Raw` is
  zoom-stable, as #413/#420 require.
- **Float *presence* does not change `L.Raw`** (checksum included), because
  `pruneFloats` collapses the split the float was wedged into. Verified for a
  1-pane and a 3-pane window. **This is narrower than "`L.Raw` is stable"** —
  see the trigger set below.
- **`layout_resize` is deterministic and path-independent across both resize
  paths.** Two identical 4-pane windows resized to `97x31` — one as the remote
  is (`refresh-client -C`-driven), one as the mirror is (`resize-window`) —
  produced byte-identical cell geometry, and a stepped `120x40 → 110x36 → 97x31`
  matched the direct resize. This is what makes the short-circuit below hit.
- **Every remote float operation emits `%layout-change` to the control client.**
  Probed with a real `-CC` client attached while a second client created,
  resized, moved and killed a float: four `%layout-change` lines, in order,
  carrying the float section appearing, changing and vanishing. So a float
  opened or moved by a *human* on the remote drives `reconcileLayout` exactly as
  a daemon-initiated one does — R2/R3 do not depend on any extra trigger. The
  same trace re-confirms the inset independently: `resize-pane -x 70 -y 24`
  produced cell `68x22`, and `move-pane -X 20 -Y 8` produced cell at `21,9`.
- **`new-pane -t <window>` works from a client not attached to that window** —
  every probe in this document issued it from a detached command client and the
  float landed in the named window. (The adjacent `break-pane -W -t` failure is
  a `break-pane` semantic — its `-t` names the *new* window — not an attachment
  effect.)
- **The park/restore cycle in the Stage 1 plan doc does not work.** That doc
  claims park → `select-layout` → `join-pane` back → `break-pane -W` "restores
  the tiled geometry exactly". Measured, it does not: against target tiled
  `[60x20 , 60x19]` the cycle returned `[60x10 , 60x29]`. `join-pane` re-tiles
  the pane into the tree, and `break-pane -W` does not restore the pre-join
  geometry. **That bullet in the plan doc must be amended in this PR**, since
  both documents ship together.
- **There is no way to inject a float into an existing window from outside it.**
  `break-pane -d -W -s <parked> -t <mirrorWin> -x .. -y ..` reports success but
  leaves the pane floating in *its own* window — measured: the pane stayed at
  `@1.%1 float=1`, never entering `@0`.

## Requirements

**R1 — a remote float appears as a local float.** One local floating pane per
entry in `Layout.Floats`, wired through the *whole* renderer sequence a tiled
pane gets: create → `spawnRenderer` (respawn + `@bridge_pane` stamp) →
`collectHellos` → `seedRenderer(…, dims, cfg.graphicsFor(id))` → `pumpInput`.
The graphics proxy is explicitly part of this: `yazi` is one of the four float
tools and its image preview is the documented reason floats were chosen, so a
float without `graphicsFor` is a regression against Bridge Graphics.

**R2 — geometry matches.** The local float's cell equals the remote float's
cell, reasserted with `resize-pane`/`move-pane` when the remote's changes.

**R3 — lifecycle.** A float closed on the remote removes the local float and
tears down its renderer/conn/router entry; one opened adds it. Neither disturbs
the tiled panes. Floats present at mirror-open time are created too (not only
ones that arrive later).

**R4 — floats never break the tiled path,** *while only daemon-created floats
are open.* `select-layout` must still land when the tiled tree genuinely changes
behind a mirrored float, and must still be skipped when nothing changed. A
**non**-daemon float in a mirror window is a documented degradation, not a
handled case — see "Unknown local floats" below.

**R5 — the tool verbs open floats on the remote.** `ctl.go`'s `tool` verb
becomes `new-pane` with the same shape the local bind uses per tool
(prdash/yazi → `90% 85% 5% 8%`; lazygit/tmux-gh-dash → `90% 90% 5% 5%`), and
with `-B heavy` so the remote float's cell carries the same inset the mirror
assumes.

**R6 — `@float_geom` on local mirror floats.** One line, space-separated
`<w> <h> <x> <y>` in **outer-box absolute cells** — the same four values passed
to `new-pane`, which is the space `resize-pane -x/-y` and `move-pane -X/-Y`
speak. Stamping the *cell* instead would shrink every mirrored float by 2 cells
per axis on the first `window-resized`. Re-stamped on every geometry reapply, so
`tmux-float-refit` replays exactly what the daemon last applied.

**R7 — no local-first mutation.** Local float state is derived only from a
remote layout read.

## Design

### Float state on `mirrorWindow`

```go
localFloats map[string]string                 // remote float id -> local float id
floatGeom   map[string]controlmode.PaneCell   // remote float id -> last cell applied
```

`conns` keeps its existing key space (remote pane id) and now also holds float
conns — no second map. `localPanes`/`remotePanes` stay tiled-only and
index-parallel.

**`w.allRemotePanes()` = `remotePanes` ++ float ids** is introduced as the
float-inclusive set, because five subsystems key off `remotePanes` and each
silently excludes floats today:

| call site | set it must take |
|---|---|
| `cst.setWindowPanes` (reconcile.go, daemon.go) | **float-inclusive** — `parseCtl` rejects any pane not in `paneToWin` with a visible `--display-error` banner, so without this *every* bind pressed inside a mirrored float errors, including `after-select-pane`'s `focus` ctl on a mere click |
| `closeWindow`, `Run`'s `teardown` | **float-inclusive** — else a float's router sink leaks for the daemon's life |
| `sessionPin.reseed` | **float-inclusive** — else a float keeps a stale screen after a `%session-changed` excursion (#396, for floats) |
| `planPaneOps`, `localPaneAt`, `select-layout` | **tiled** (unchanged) |

`setWindowPanes` clears every pane mapped to the window before re-setting, so
passing it the tiled set alone would orphan float entries on each pass.

**Focus.** `focusLocalPane` resolves through `indexOf(order, remoteActive)` over
the tiled order and returns on `-1`, so a remote-active float is unresolvable
today: opening `prefix + g` (verb has `moves: true`) would leave local focus on
a tiled renderer while the remote focuses the float, and the user types into the
wrong pane. It gains a `localFloats` lookup before the tiled path, at **both**
call sites (`reconcileLayout` and `%window-pane-changed`), with
`cst.noteLocalFocus` still preceding the local `select-pane`. The zoom
reconcile resolves its target the same way and gets the same lookup.

### Float reconcile

`reconcileFloats` diffs `L.Floats` against `localFloats`:

1. **Remove** — unregister the router sink, close and delete the conn,
   `kill-pane -t <localFloat>`, drop both map entries.
2. **Add** — `new-pane -d -P -F '#{pane_id}' -t <localWin> -B heavy -A
   -x <W+2> -y <H+2> -X <X-1> -Y <Y-1>` (outer box), stamp `@float_geom`, then
   the frozen renderer sequence from R1. `-d` because a reconcile-driven add
   must not yank focus; when the remote's active pane *is* the new float,
   focus follows through `focusLocalPane`, not through the create.
3. **Move/resize** — `resize-pane -x/-y` then `move-pane -X/-Y` (outer box),
   re-stamp `@float_geom`, push `FrameResize` + a `capture-pane` re-seed (a
   renderer holds no back-buffer — #417).

It runs from `reconcileLayout` **and** at the end of `setupWindow`, so a window
that already holds a float when the bridge opens mirrors it immediately rather
than waiting for an unrelated `%layout-change`. The convergence re-read at the
loop tail compares the float set (ids + cells) alongside `fresh.Raw` and the
zoom flag, both of which are float-blind by construction.

### `select-layout` while a float is open

The trigger set is wider than float presence. `L.Raw` carries every cell's
`WxH,X,Y`, so it changes on **any** geometry event, not only a topology change:

- the 1s `watchResize` poll → `ConvergeCmd` → remote `resize-window` →
  `%layout-change` (i.e. any local terminal resize),
- a bridged `prefix + M-Left/Right` (`resize` verb),
- a remote-side border drag,
- a genuine tiled split/kill.

So "kill the floats whenever `L.Raw` moved" would respawn a mirrored `lazygit`
on every terminal resize — and `collectHellos` blocks the main control loop, so
that stall would hit every pane in the mirror. Two mitigations, in order:

**1. Geometry short-circuit (cheap, self-verifying).** When `L.Raw != w.layout`
and mirrored floats exist, `applyLayout` does the `FitWindowCmd` resize, then
re-reads the mirror window's own `#{window_layout}` through `LocalTmuxOut`,
parses it, and compares its `Panes` cell geometry (`W,H,X,Y` pairwise, in order)
against `L.Panes`. Pane **ids** differ between the two hosts, so this is a
geometry comparison, never a string compare. On a match the fit alone already
reproduced the remote's cells and `select-layout` is skipped entirely — no float
is touched. The determinism probe above says this is the common case for pure
resizes; it is *verified* per pass rather than assumed, so a miss is merely
slower, never wrong.

**2. Drop and re-add (the fallback).** Only when the geometry still disagrees:
kill the mirrored floats, `select-layout` against the now float-free window, and
let the Add path re-create them.

**Control flow — stated exactly, because the naive placement is broken.**
`applyLayout` is called twice per pass (once from `reconcileLayout`, once from
inside `applyPaneOps`), and every exit from the `maxReconcilePasses` loop is an
early `return` — including the *normal* convergence exit. So "kill above the
loop, re-add below it" would both reinstate the per-resize respawn and leave the
re-add unreachable on the dominant path. The spec therefore requires:

- **`applyLayout` reports whether the window carries `L`'s shape**, returning
  `ok bool` (it returns nothing today). `ok` is true when `L.Raw == w.layout`,
  when the short-circuit matched, or when `select-layout` succeeded; false only
  when `select-layout` failed. On a short-circuit hit it also sets
  `w.layout = L.Raw`, so later passes neither re-pay the local read nor look
  like the failure case.
- **A once-guard carries the drop decision.** `reconcileLayout` creates a single
  drop token for the whole call and threads it through `applyLayout` and
  `applyPaneOps`; `applyLayout` asks it to drop only when it is about to run
  `select-layout`, floats exist, and the short-circuit missed. The token fires
  at most once, so a multi-pass reconcile — or the second `applyLayout` call
  site — costs one respawn, not several.
- **The loop's exits become `break`, not `return`,** with a `reset` flag for the
  `ops.Reset` path. One `reconcileFloats` runs after the loop on every non-reset
  exit; the `ops.Reset` path skips it because `resetWindow` → `setupWindow`
  already rebuilt the window and its floats. This is what makes the re-add
  reachable from the convergence exit, which is the common case.

The remote float pane is never touched; only the local renderer painting it,
which is re-seeded from `capture-pane`.

**When the shape does not land, the broadcast must not run.** `reconcileLayout`
currently enqueues `FrameResize` with `L.Panes[i]` dims and a `capture-pane`
seed for *every* pane immediately after `applyLayout`. If the shape failed, the
local panes carry tmux's own auto-geometry, so that broadcast paints the
remote's screen at the remote's dims into differently-sized panes — exactly the
#233/#417 blank-mirror failure. Both the resize and the reseed are therefore
gated on `applyLayout` returning `ok`; on a miss the mirror keeps its last-good
screen instead.

### Unknown local floats — documented degradation

`bind-key b` (btop), `bind-key k` (k9s) and `bind-key i` (the enrich card) are
unconditional `new-pane` binds with no `bridgeGate`, so a mirror window can hold
a float this daemon did not create. `select-layout` counts it like any other,
and the daemon must not kill a pane it did not make.

Stage 2 behaviour, stated honestly: the drop kills only ids in `localFloats`; if
the window still holds a foreign float, `select-layout` fails, `applyLayout`
returns `ok == false`, and — per the gate above — the resize/reseed broadcast is
skipped. The mirror keeps its last-good screen with the remote's correct pane
*set* but stale tiled *geometry*. Nothing of the user's is destroyed and the
mirror does not garble, but **it does not self-heal either**: `reconcileLayout`
runs only on `%layout-change` or a ctl intent, so against an idle remote the
stale geometry persists until the user closes their float or something else
moves the layout. That is the real cost of deferring proper tolerance to
Stage 3, and it is why the deferral is a scope decision rather than a free one.

The failure is logged once per window per distinct `L.Raw`, so a repeating
reconcile against an unchanged remote does not spam; a later pass that shapes
successfully clears the record.

### Invariants and minor decisions

- **Short-circuit ordering invariant (load-bearing).** The pairwise cell compare
  is valid because `list-panes` order equals the layout's depth-first cell order
  — the same invariant `PlanWindow`/`applyPaneOps` already rely on. `swap-pane`
  preserves it (tmux exchanges panes in both the window list and their cells),
  `ParseLayout` prunes floats from both sides, and `FitWindowCmd` equalises
  `W/H` so absolute coordinates are comparable. Probed: tiled cells are
  byte-identical with and without a float — a float overlays, it never displaces
  — and `list-panes` tiled order matched DFS cell order exactly. A future
  refactor that breaks this quietly voids the short-circuit.
- **`setupWindow` ordering.** `cst.setWindowPanes` is called before the trailing
  `reconcileFloats`, so the float-inclusive set is re-asserted *after* the
  floats exist, not before.
- **A `LocalTmuxOut` error during the short-circuit read** degrades straight to
  drop-and-re-add (a visible respawn). Correct, just slower.
- **`tmux-float-refit` races benignly.** The daemon's own `FitWindowCmd` fires
  `window-resized`, so refit reasserts the last-stamped cells just before
  `reconcileFloats` applies the new ones. Transient and self-correcting, and the
  reason R6 insists the stamp is outer-box and re-stamped on every reapply.
- **Not addressed (cosmetic):** a mirrored float gets no `@pane_label`, so its
  border title is blank where a local float reads `lazygit`. `@bridge_proc`
  already carries the remote command and could feed it later.
- **Not probed:** `prefix + z` pressed *inside* a mirrored float (the `zoom`
  verb would target a remote float). Out of this PR's acceptance; the local zoom
  reconcile resolves its target through the float lookup either way.

### `ctl.go` changes

- `tool`: remote `split-window` → `new-pane` with the per-tool shape table and
  `-B heavy`. The **remote** float is never stamped with `@float_geom` — the
  remote's own `tmux-float-refit` would fight the mirror's authority.
- `carousel`: the primary path is aeye's `tmux-claude-images`, which lives in
  the `carousel-toggle` flake input and chooses its own shape on the remote — no
  `split-window` in this repo. Only its missing-binary fallback split is
  ctl.go's, and it becomes a `new-pane` float. See Acceptance for what this
  means for the brief's `prefix + I` criterion.
- The "split is deliberate" comment block is replaced.

### Teardown

`dropMirroredPanes` and `resetWindow` kill mirrored floats, unregister their
sinks, close their conns, **and clear `localFloats`/`floatGeom`** — otherwise
those maps keep entries pointing at killed panes and closed conns, and the next
`reconcileFloats` Remove step would `kill-pane` dead ids. `closeWindow` and
`Run`'s teardown unregister float sinks via `allRemotePanes()`. The "a float the
window also holds is not its to reap" comment narrows to "a float this daemon
did not create".

**Agent status needs no change, for a reason worth recording:** `agentShipper`
resolves local panes through `cfg.LocalPanes()`, which reads `@bridge_pane`
across the whole local session, and `spawnRenderer` stamps it on any pane it
respawns — so a mirrored float gets `@bridge_proc` and its agent state for free.
That is load-bearing; a future refactor that stamps `@bridge_pane` only on tiled
panes would silently break it.

## Out of scope

**Stage 3** — tolerating a purely local float the remote knows nothing about
(which ungates `prefix + s` `^o` and spec D7).

## Risks

- **The inset is border-conditional** (1/side except `-B none`). The mirror pins
  its own border style and derives the inset from that constant — never read
  back from a pane, and revisit if the border flag is ever made configurable.
- **The short-circuit costs one local tmux read** per reshape-with-floats pass.
  Local, not ssh, and only on the path that would otherwise respawn a renderer.
- **Drop-and-re-add still costs a reseed** when the short-circuit misses (a real
  topology change behind an open float). A float is not guaranteed to survive a
  remote split. The fix would be upstream (a tmux able to inject a float into an
  existing window), not a local workaround.

## Acceptance

- In a mirror window, `prefix + p`/`g`/`G`/`y` opens a float on the remote that
  appears as a local float with matching geometry.
- Closing the remote float removes the local one; resizing it on the remote
  moves the local one.
- A keybind pressed *inside* a mirrored float resolves instead of raising the
  `not mirrored by this bridge` banner, and focus follows a remote-active float.
- **`prefix + I` is knowingly not met by this PR.** The carousel's split is made
  by `tmux-claude-images` in the `carousel-toggle` (aeye) input, not by anything
  this repo controls, so it continues to mirror as a tiled split. Noted in the
  PR body as follow-up rather than silently dropped from the brief.
- A float created, resized, moved or killed *by a human on the remote* drives
  the mirror, not only daemon-initiated ones (the `%layout-change` probe).
- Existing bridge tests still pass; new unit tests cover the float diff, the
  cell↔flag conversion, the geometry short-circuit, the drop/re-add sequence
  firing **once** per reconcile (including across the second `applyLayout` call
  site), and the resize/reseed broadcast being skipped when the shape fails,
  against the `LocalTmux` fake.
- The superseded park/restore bullet in
  `docs/superpowers/plans/2026-08-31-bridge-float-panes.md` is amended.
- `nix build .#default`, `nix flake check`, `nix build .#lint` all pass.
