# Sequence the mirror zoom toggle before the reseed (#511)

## Problem

`prefix + z` on a split inside a mirror window expands the local pane, but its
content is not repainted for the new size, so the pane renders garbled.

`prefix + z` in a mirror sends the `zoom` ctl verb (#413) rather than a local
`resize-pane -Z`: a local zoom grows the renderer pane without telling the
remote, so the remote program keeps rendering at its old size. The remote zooms,
emits `%layout-change`, and `reconcileLayout` runs. In that pass
(`picker/remotebridge/daemon/reconcile.go`) the order is:

| line | step |
|---|---|
| 94 | `applyLayout(cfg, w, L)` — **skips `select-layout` when `L.Raw == w.layout`, which is exactly a zoom-only reconcile** |
| 97-101 | push each pane its `FrameResize` dims, derived from `L.Panes[i]` |
| 113-127 | **reseed every pane** — `PaneSeeds` → `enqueueSeedWithReplay` |
| 134-140 | **then** toggle zoom — `resize-pane -Z` on the local mirror |

Verified against `ea11590`: the zoom toggle runs after the seeds that depend on
it.

Two things are inconsistent at paint time:

1. **Content vs. local size.** The remote zoomed first — that is what emitted
   the `%layout-change` — so the `capture-pane` seed carries the remote's
   *zoomed* screen, sized to the whole window. It is painted into a local pane
   that is still at its unzoomed cell. Which axis mismatches depends on the
   split: a `-v` split zooms in **rows** (the seed is taller than the pane, so
   the top scrolls away), a `-h` split zooms in **columns** (every captured
   line is wider than the pane, so each one wraps). `resize-pane -Z` only lands
   afterwards.
2. **The pushed dims are the unzoomed ones.** `L` comes from
   `#{window_layout}`, deliberately the **unzoomed** geometry (see
   `readLayout`'s doc comment and #413), so the `FrameResize` names the cell the
   pane no longer occupies.

The reseed block's own comment states the invariant this violates:

> After the reshape, never before (#233): a seed sized for the new geometry
> painted into a pane still at the old size leaves the mirror blank.

A zoom *is* a reshape; it just is not one `select-layout` performs. The toggle
is sequenced outside the reshape it belongs to.

Because the seed is `enqueue`d (async, drained by the sink pump) while
`resize-pane -Z` is a synchronous `LocalTmux` call issued right after, the two
race — which is why the result is *garbled* rather than reliably blank, and why
it can occasionally look correct.

## Measured tmux behaviour

All on the repo's own `tmux next-3.8`, 190x45 window, two panes:

| observation | value |
|---|---|
| unzoomed `#{window_layout}` | `4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}` |
| zoomed `#{window_layout}` | unchanged — the same string |
| zoomed `#{window_visible_layout}` | `c0be,190x45,0,0,1` — the zoomed pane's cell **is** the layout root |
| hidden pane while zoomed | keeps its unzoomed cell, untouched |
| `select-layout` with the *same* layout string while zoomed | zoom flag 1 → 0: **it unzooms** |
| `resize-window -x/-y` while zoomed | zoom flag stays 1 |
| with `pane-border-status top` (this repo sets it globally, `config/tmux.conf.nix:1171`) | cell `190x22` vs pane `190x21`; zoomed cell `190x45` vs pane `190x44`, `capture-pane` = 44 lines |
| **main** screen, pane grows in height | tmux pulls scrolled lines back out of history — `LINE01` returns to row 1 |
| **main** screen, pane widens | tmux rejoins `GRID_LINE_WRAPPED` rows — a 60-char line wrapped at 40 becomes one row again at 80 |
| **alternate** screen, pane grows in height | nothing returns — `LINE13` stays at row 1; the alternate grid has no history |
| **alternate** screen, pane widens | stays wrapped — no reflow while a saved grid exists |
| `resize-pane -Z` on a non-active pane | moves the active pane (1 → 0) but fires `after-select-pane` **0 times**; an explicit `select-pane` fires it once |

The four reflow rows are what decide the repro below, and they are the reason
the first two drafts of this spec picked the wrong one twice.

The last row is why this spec talks about layout **cells** throughout and never
claims a `FrameResize` describes a pane's real row count: under
`pane-border-status`, `L.Panes[i]` already differs from `pane_height` by the
border row, for every pane. The convention is cells, and has always been.

## Goals

1. On the paths `reconcileLayout` itself owns (the non-reset branches of the
   pass loop), the local zoom state matches the remote's **before** any
   `FrameSeed` that pass produces.
2. A zoomed pane's `FrameResize` names the layout cell it actually occupies,
   consistent with the `L.Panes[i]` convention used for every other pane.
3. Both directions: zoom **and** unzoom.

## Non-goals

- Changing the toggle's decision logic. It stays a **comparison** of the
  remote's `#{window_zoomed_flag}` against `localZoomed`, firing only on a
  mismatch (#420).
- Switching to `#{window_visible_layout}` (#413).
- Moving the seed later. The seed stays after the reshape (#233/#417); the zoom
  moves earlier.
- Any other change to the pass loop. Worker `fern` is concurrently restructuring
  the same loop for #409 (mirror floats), so the diff must stay surgical.
- The `setupWindow` zoom gap — see **Known gaps** below. Named, not fixed.

## Design

### 1. Move the zoom toggle into the reshape

Relocate the zoom-toggle block verbatim to immediately after
`applyLayout(cfg, w, L)`, ahead of the `FrameResize` push and the reseed. The
resulting pass order is: **reshape (fit → select-layout → zoom) → dims →
seeds**.

The toggle must stay *after* `applyLayout`, not before it: measured above,
`select-layout` unzooms the window it shapes, so a zoom applied first would be
undone on a structural pass. Reading `localZoomed` after `applyLayout` also
means the read reflects whatever `applyLayout` just did, which is what keeps
the comparison correct on a structural-plus-zoom pass.

The toggle's target expression, `localPaneAt(w, indexOf(newRemote, remoteActive))`,
is valid at the new position because **nothing between `reconcile.go:94` and
`:140` mutates `w.localPanes`** — the old and new call sites read the identical
slice. (Not because `applyPaneOps` ran: on the primary repro `structural` is
false and it never runs at all.)

### 2. Dims for a zoomed pane

`#{window_layout}` is the unzoomed geometry, so ordering alone still names the
pre-zoom cell for the pane whose seed is post-zoom. Options:

| option | verdict |
|---|---|
| `#{window_visible_layout}` | **rejected** — reports a zoomed window as single-pane; reconcile would read the hidden panes as closed and kill their renderers on every toggle (#413). |
| Skip the `FrameResize` for the zoomed pane | **rejected** — leaves the renderer holding a record it cannot tell is stale, and drops the "layout is daemon-authoritative" property for exactly the pane that moved. |
| **Change nothing** — keep `L.Panes[i]` for every pane | **rejected, but it was close.** `FrameResize` is advisory: `render.Run` hands the dims to `recordResize`, and `cmd/renderer/main.go:22` passes `func(int, int) {}`, so no production renderer reads them. The null option therefore costs nothing observable and avoids a conflict hunk in the loop fern is extending. It loses on one point: under the daemon's own cell convention, `L.Panes[i]` for a zoomed pane is not merely imprecise, it names a cell the pane has **left** — `#{window_visible_layout}` shows the zoomed pane's cell is the layout root. That is an internal inconsistency in the daemon's description of its own geometry, and it is 4 lines to remove. |
| **Derive the zoomed pane's dims from the layout root** | **chosen** — see below. |

When the remote reports `zoomed`, the pane whose id equals `remoteActive` (a
zoomed pane is by definition the active one) gets `(L.W, L.H)` — the layout
root, which is the cell a zoomed pane occupies. Every other pane keeps
`L.Panes[i]`, which is correct rather than a concession: tmux's zoom removes the
other panes from the layout **without resizing them** (measured above), so their
`capture-pane` content is still at the pre-zoom size that `L.Panes[i]` names.

This needs no new round-trip — `zoomed` and `remoteActive` already come back
from the same `readLayout` reply — and no coupling to the toggle block: the
comparison is `zoomed && id == remoteActive` over `newRemote`, which is
`RemotePaneOrder(L)`, i.e. **tiled panes only**. If `remoteActive` is ever a
float (`L.Floats` is a separate list), no tiled id matches it and no pane is
given root dims, so the rule is float-safe by construction — which matters
because fern's #409 adds floats to this same loop.

### 3. What this does and does not repaint

Because `recordResize` is a production no-op, §2 corrects the daemon's outgoing
description of geometry and by itself repaints nothing. The repaint comes from
the ordering change in §1. Stating this plainly matters for the claims below.

## Known gaps (named, not fixed)

Three paths still seed a pane while the local zoom state is stale. All are
pre-existing, none is #511's reported symptom, and all are out of scope here:

1. **`setupWindow` discards the zoom flag** — `daemon.go:1004` is
   `L, _, _, err := readLayout(...)`. A mirror opened on a remote window that
   is *already* zoomed is seeded and shaped unzoomed, and stays that way until
   some unrelated layout change. This is a real sibling of #511 and the one a
   reader is most likely to assume this change covers. It does not follow from
   the toggle's position, needs its own verification, and lives in a file fern
   is also editing.
2. **`ops.Reset` (`reconcile.go:67-74`) and `errLocalPanesDesynced`
   (`:77-86`)** both `return false` after `resetWindow` → `setupWindow` has
   reseeded every pane — i.e. before the toggle, at either position. Same root
   cause as (1).
3. **`applyPaneOps` seeds each newly appended pane itself** (`seedRenderer`,
   reached from `reconcile.go:315`), above the toggle at either position.
   Harmless: the outer reseed repairs it, which the comment at
   `reconcile.go:102-108` already says.

Gap (1) is a user-visible bug in its own right and warrants its own tracking
issue, not just a PR-body line. This worker did not open one: creating a ticket
is an outward-facing write that needs explicit approval in the conversation, and
no human is watching this session. It is therefore surfaced in the PR body under
a follow-up heading, with the recommendation that an issue be filed — reported,
not silently absorbed, and not quietly filed either.

## Verification

The premise "this needs a live two-host bridge" is **false**, and the first
draft of this spec was wrong to settle for an ordering assertion alone. This is
a *local* paint-order bug — one tmux server painting a seed into a pane whose
size it has not yet changed — and `tests/remote-m2-integration.bats` already
drives the daemon in `--test-local` mode against two local tmux servers with
real renderer panes.

1. **m2 integration content test** (primary evidence). Modelled on
   `tests/remote-m2-integration.bats:430` ("daemon repaints mirrored content
   after a remote geometry change"), which ends in
   `[ "$dst_screen" = "$src_screen" ]`. Fill the remote pane with content,
   `$CTL zoom`, then poll on `dst_screen == src_screen` on the house
   `60 × 0.15s` budget. The existing zoom test at `:967` asserts flags and dims
   only and is structurally blind to content — the failure mode this repo has
   been bitten by before.

   **The repro must be a `-v` split with the remote pane on the alternate
   screen.** Both halves are load-bearing, and both were got wrong in earlier
   drafts of this spec:

   - **Alternate screen, because the main screen self-heals on either axis.**
     `render.Seed` paints the capture with `\r\n` separators, so an over-tall
     paint into a short pane scrolls — and on the main screen tmux pulls those
     lines back out of history the moment the pane grows (measured). The
     symmetric hazard on the other axis is worse: tmux *reflows* on a width
     change, rejoining wrapped rows, so a widened pane un-wraps a mangled paint
     (measured). Either way the comparison can pass against unfixed code. The
     alternate grid has no history and is not reflowed while a saved grid
     exists (both measured), so a wrong-geometry paint there is permanent.
     `render.Seed` already emits `\x1b[?1049h` when the remote pane reports
     `alternate_on` (`render/snapshot.go:10-12`), and the mode-set crosses the
     bridge as ordinary output anyway, so putting the *remote* pane on the
     alternate screen is enough to exercise it end to end.
   - **`-v`, because only the height axis makes the remote hold content the
     unzoomed local pane cannot.** The mismatch has to be in what the remote's
     grid holds *after* zooming. Zooming a `-v` split grows the remote pane's
     row count, so its `capture-pane` returns ~44 lines where the local pane has
     ~21 — painting them scrolls the content irrecoverably off an alternate
     grid, and the later `resize-pane -Z` grows the pane into blank rows. Zooming
     an `-h` split widens the remote pane, but its alternate grid is not
     reflowed, so it keeps holding lines wrapped at the *unzoomed* width — every
     captured line still fits the narrow local pane, nothing is mangled, and the
     test would pass pre-fix. `-h` is the wrong axis here precisely *because*
     the alternate screen suppresses reflow.

   So: `-v` split, remote pane switched to the alternate screen and filled with
   **more numbered lines than the unzoomed pane has rows** (~40 for a 21-row
   pane), then zoom. Filling only to the pane's height would work solely if
   `capture-pane -e -p` emits the zoomed pane's trailing blank rows as empty
   lines — almost certainly true, but the one link in the chain that was never
   measured; overfilling makes the repro independent of it, since the real
   content alone then exceeds the local pane's rows. Pre-fix the local pane
   paints ~44 lines into ~21 rows and loses the overflow off a historyless
   alternate grid; post-fix it is ~44 rows before the paint and matches.
   Non-vacuity is established by step 4, not by construction.

   Two harness details the repro depends on. Give the remote pane an explicit
   command (`split-window -v -t rem "sh -c '…'"`) rather than `send-keys` into a
   prompt: the pane's shell is whatever `default-shell` resolves to, this repo
   has been bitten repeatedly by that being fish, and a prompt redrawing into
   the alternate screen mid-poll perturbs the comparison. And re-read **both**
   captures inside the poll loop, unlike the `:430` template which reads them
   once after polling on dims.

2. **Go ordering test** (cheap regression net, not the primary evidence). A
   scripted reconcile asserting the local `resize-pane -Z` is issued **before**
   the `capture-pane` that produces the seed. Both are synchronous calls on
   `reconcileLayout`'s own goroutine — the local one through `Config.LocalTmux`,
   the remote one through the control stream's writer — so one ordered log over
   the two seams is deterministic, with no async in the assertion.

   This deliberately **over-constrains**: the invariant that matters is
   zoom-before-`FrameSeed`, and zoom-before-`capture-pane` implies it (a seed
   cannot be enqueued before the capture whose reply it is) but also forbids a
   hypothetical future batching that captures earlier while still zooming
   before the seed. Accepted: the stricter form is the one that can be asserted
   without racing the sink pump, and a spurious failure would be loud and
   obviously about ordering.

3. **Go dims test.** A scripted reconcile of a zoomed two-pane window asserting
   the zoomed pane's `FrameResize` carries `(L.W, L.H)` and the hidden pane's
   carries its own cell.

4. **Non-vacuity.** Every new test shown failing against the unfixed code
   (revert the fix, run, paste output).

Claim to make at merge, subject to (1) actually passing: *the mirror's content
matches the remote's after a zoom, asserted by an integration test that fails
without this change, and the ordering invariant is pinned by a unit test.* Not:
*observed fixed on hardware* — no two-host bridge was driven.

## Regression traps

- Toggle stays a comparison, never an application (#420).
- No `#{window_visible_layout}` in `readLayout` (#413).
- The dedup early-return must keep letting a zoom-only change fall through —
  `L.Raw` alone is not enough (#431 + #413); covered by the existing
  `TestReconcileLayoutFallsThroughOnZoomChange`.
- Seeds stay after the reshape (#233/#417).
- Mirror windows addressed by tmux window id (#411) — unchanged here.
- No nested round-trip inside a per-pane seed callback. The moved block issues
  only local tmux calls and no round-trip, so the main-loop rule is unaffected —
  and it now sits between two round-trip batches (`readLayout` and `PaneSeeds`),
  not inside one.
- A failed `resize-pane -Z` only logs (`reconcile.go:136-138`). At the new
  position that means the pass's seeds are painted into the wrong geometry;
  previously they were already out the door regardless, so this is not a
  regression, but it is now the one failure that can still produce the #511
  symptom. Left as-is: the next `%layout-change` reconciles again.
- `resize-pane -Z` no-ops on a single-pane window, so a mirror whose local pane
  count has desynced re-attempts the toggle on every event. Pre-existing and
  unchanged by the move.
- The local `resize-pane -Z` **does** move the local active pane to the zoomed
  pane, but fires **no** `after-select-pane` hook (measured: 0 firings for the
  zoom, 1 for an explicit `select-pane`), so it produces no ctl `focus` echo
  through `config/tmux.conf.nix:1157`. Nor does the silent move diverge from the
  remote: the pane it lands on is the one the remote has just made active by
  zooming it. Unchanged by the move either way — the toggle stays **before**
  `focusLocalPane`, so their relative order is what it was.

## Merge with #409 (fern)

Two hunks are owned here:

- the block move at `reconcile.go:128-140` → just after `:94`;
- the zoomed-dims branch inside the `FrameResize` loop at `:97-101`.

If fern lands first and the loop has been restructured, resolve by keeping
**both** behaviours: floats reconciled separately from the tiled diff, **and**
the zoom toggle sequenced before the reseed. The dims rule stays scoped to
tiled panes (`newRemote`), never floats — see §2. If the resolution is not
confidently derivable, stop and report rather than guess.
