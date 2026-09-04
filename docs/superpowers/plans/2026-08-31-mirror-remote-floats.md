# Plan — mirror remote floating panes as local floats (#409, Stage 2)

Design: `docs/superpowers/specs/2026-08-31-mirror-remote-floats-design.md`.
Structure constraint: `2026-08-31-mirror-remote-floats-decomposition.md` — every
step maps to one component and stays inside its boundaries, or declares the
deviation inline.

Stage 1 landed in #420 and is **not** re-implemented. Stage 3 stays out of scope.
`WORKER_TASK.md` is untracked scaffolding — keep it out of the commit.

## Re-validation against current `main` (`cc66e41`)

This plan and its design were written on 2026-08-31 against `7aa9432`. That
attempt died before committing, and 58 commits landed underneath it — including
#504 (`resetWindow`/`wireRenderer` and the reconcile failure paths) and #510
(`outputSink.done`/`Wait`, kitty-store replay), both in the files this plan
edits. The Aug-31 *code* was therefore discarded rather than rebased: a
clean-applying patch can still be semantically broken by a refactor that landed
under it. What follows is what survived that re-check and what did not.

**Survived unchanged — re-verified, not assumed:**

- Every probed tmux fact in the design. Nothing in the 58 commits touches tmux
  semantics, and the probes were measured against the pinned next-3.8 binary.
- `floatgeom.go` and `floatdiff.go` verbatim. Both are pure and depend only on
  `controlmode.PaneCell` (unchanged) and, for the stamp's field order,
  `scripts/tmux-float-refit.sh`'s `read -r width height xoff yoff` (unchanged).
  Confirmed by building and testing them against `cc66e41` before any other
  edit.
- `applyLayout`'s skip contract (`L.Raw == w.layout`), `focusLocalPane`'s
  signature and both call sites, `dropMirroredPanes`' "a float the window also
  holds is not its to reap" comment, `closeWindow`/teardown's loops over
  `mw.remotePanes`, and `ctlState`'s `paneToWin`/`setWindowPanes`/`forgetWindow`.

**Did not survive — re-derived against `cc66e41`:**

- **The renderer wiring sequence (#504).** Step 5 below was written around
  `collectHellos` + a `connCh`. Both are gone: hello collection is now a
  `helloWaiter func(n int) (map[string]net.Conn, error)` threaded through
  reconcile, and seeding split into a batched `PaneSeeds` plus `wireRenderer`.
  The float Add path uses `waitHellos(n)` and then `seedRenderer` — which still
  exists and is exactly the single-pane form — so `reconcileFloats`' signature
  takes `waitHellos helloWaiter`, never a channel.
- **The seed-failure contract (#504).** A failed seed no longer strands a pane:
  `wireRenderer` registers the sink regardless and wires the pane unseeded, and
  the trailing reseed repairs it. So a float whose seed fails must be left wired
  and pumping, not unregistered — the opposite of what the Aug-31 code did.
- **`resetWindow`'s conn merge (#504) — a hazard the design never saw.** Reset
  now snapshots `oldConns` and, on the failure path, re-registers any conn the
  rebuild did not replace. Float conns share `w.conns`' key space, so a reset
  would re-register sinks for float ids whose local panes `dropMirroredPanes`
  just killed. Float conns are therefore closed and dropped outright on reset;
  `reconcileFloats` rebuilds them from `L.Floats` anyway.
- **The re-seed broadcast (#510).** It is now a batched `PaneSeeds` feeding
  `enqueueSeedWithReplay` (which replays retained kitty stores). Step 6's "gate
  the broadcast on `ok`" is unchanged in intent but applies to that shape.
- **`sessionPin.reseed` is now the free function `reseedPanes(reg, router, rt,
  reason)`** in `sessionpin.go`. Same fix, different name and shape.
- **`remoteTools` is three tools, not four.** #453 removed `tmux-gh-dash`, so
  Step 4's shape table loses that row: prdash + yazi → floatShort, lazygit →
  floatFull.
- **`reconcileLayout` gained a `retire bool` return and `resetLostWindow`.**
  Step 6's loop restructure must leave those error exits as `return`s that skip
  the post-loop float work, not convert them to `break`.

## Step 1: amend the superseded plan doc — `docs`

*Deviation: `docs` may-touch reads `docs/superpowers/plans/2026-08-31-*.md`
(new); this edits an existing file under that glob, which the design's
Acceptance explicitly requires.*

- [ ] In `docs/superpowers/plans/2026-08-31-bridge-float-panes.md`, replace the
      "park → `select-layout` → `join-pane` back → `break-pane -W` restores the
      tiled geometry exactly" bullet with the measured result, stated so the
      next reader does not re-derive it: target tiled `[60x20,60x19]` came back
      `[60x10,60x29]`, and a parked float cannot be returned —
      `break-pane -d -W -s <parked> -t <win>` reports success and leaves it
      floating in its own window. Point at the new design doc.
- [ ] Mark that doc's Stage 1 section as landed (#420) and its Stage 2 open
      question (the border inset) as settled: 1 per side for every border style
      except `-B none`.

## Step 2: cell ↔ flag geometry — `float-geom`

New `picker/remotebridge/daemon/floatgeom.go` + `floatgeom_test.go`.

- [ ] `floatBorder = "heavy"` and `floatInset = 1` as named constants, with the
      probe result in a comment: the inset is a property of *having* a border,
      not of which one, and is 0 only for `-B none`.
- [ ] `outerFromCell(c controlmode.PaneCell) (w, h, x, y int)` — `W+2, H+2,
      X-1, Y-1`. Cells are the inner box; `new-pane -x/-y/-X/-Y`,
      `resize-pane -x/-y`, `move-pane -X/-Y` and the `@float_geom` stamp all
      speak the outer box.
- [ ] Clamp: never emit a negative offset (a cell at `X=0`/`Y=0`), and never
      exceed the window — a remote float made with `-B none` has a zero-inset
      cell, so `cell + 2` could otherwise overflow.
- [ ] `floatCreateArgv(localWin, c)` →
      `new-pane -d -P -F '#{pane_id}' -t <win> -B heavy -A -x .. -y .. -X .. -Y ..`
      (`-d` so a reconcile-driven add never yanks focus; `-A` so the float
      survives a zoom — both probed), plus `floatResizeArgv`, `floatMoveArgv`,
      and `floatGeomStamp` (the same four outer-box integers, space-separated).
- [ ] Tests: a cell round-trips to the flags that produce it (probe values
      `58x18,11,6` ↔ `-x 60 -y 20 -X 10 -Y 5`); each argv builder's exact shape;
      both clamps.

## Step 3: float state + pure diff — `float-state`

- [ ] `mirrorWindow` (in `windows.go`, struct + doc comment) gains
      `localFloats map[string]string`, `floatGeom map[string]controlmode.PaneCell`
      (both keyed by *remote* float pane id) and `floatsDropped bool` (the
      per-reconcile drop token — see Step 6).
- [ ] Initialise the maps in `registry.add` alongside `conns`, **and** lazily on
      first write. Both, not either: six existing tests build `mirrorWindow`
      literals directly (`reconcileshape_test.go:60`, `reconcilereseed_test.go:25`,
      `localpanes_test.go:64,107,146,175`) and would panic on a nil-map write;
      (`daemon_test.go` and `sessionpin_test.go` go through `reg.add`, so they
      are already covered by the constructor); updating them
      all to carry maps they do not care about is churn that would hide the next
      real break.
      *Deviation: `float-state` may touch `windows.go` "not `registry`", but
      `registry.add` is `mirrorWindow`'s only constructor and already
      initialises `conns`.*
- [ ] `allRemotePanes()` returns `remotePanes` ++ float ids. Document at the
      struct which call sites take it and which stay tiled-only.
- [ ] New `floatdiff.go`: `planFloatOps(have map[string]controlmode.PaneCell,
      want []controlmode.PaneCell) floatOps` with `Remove []string`,
      `Add []controlmode.PaneCell`, `Move []controlmode.PaneCell`. Pure, no I/O;
      `Move` only when a surviving id's cell differs. Deterministic order:
      remove, add, move.
- [ ] `floatdiff_test.go`: add-only, remove-only, move-only, no-op, and a
      simultaneous add+remove+move.

## Step 4: remote tool/carousel verbs open floats — `ctl-float-verbs`

- [ ] Per-tool shape table matching `config/tmux.conf.nix:613-614`: prdash +
      yazi → `90% 85% 5% 8%` (floatShort), lazygit + tmux-gh-dash →
      `90% 90% 5% 5%` (floatFull). `remoteTools` stays the whitelist.
- [ ] `tool` verb: `split-window -t %p -c '#{pane_current_path}' <script>` →
      `new-pane -t %p -c '#{pane_current_path}' -B heavy -A <shape> <script>`.
      `-B heavy` matches the shape the local bind produces. (The inset driving
      the *conversion* is the **local** border, which the mirror pins itself;
      the remote's border only affects the remote cell, mirrored verbatim.)
      Do **not** stamp `@float_geom` on the remote pane.
- [ ] `carousel` verb: its missing-binary fallback `tmux split-window -t "$src"
      -l 3 …` becomes a `new-pane` float. The primary path is aeye's
      `tmux-claude-images` and is untouched.
- [ ] Replace the comment block documenting the split as deliberate.
- [ ] Preserve the file's quoting discipline: the resolve scripts must still
      contain zero single quotes so the double `tmuxQuote` only wraps.
- [ ] `ctl_test.go`: exact remote command string for each of the four tools and
      the carousel fallback; assert no `@float_geom` on the remote.

## Step 5: the float reconcile — `float-reconcile` (implement: opus)

- [ ] `reconcileFloats(cfg, w, L, send, router, connCh, rt)` in `reconcile.go`,
      driving `planFloatOps` against `w.floatGeom`:
      - **Remove**: extract as a named primitive
        `removeFloat(cfg, w, router, remoteID)` — Step 6's drop path reuses it
        verbatim, so it must be callable on its own, not inline. It does
        `router.Unregister(id)`, closes + deletes `w.conns[id]`,
        `kill-pane -t <local>`, and deletes both map entries.
      - **Add**: `LocalTmuxOut(floatCreateArgv(...))` captures the new local pane
        id in one call; stamp `@float_geom` — with a comment giving the
        non-obvious WHY: nothing here enforces it for daemon floats
        (`float-conf-assertions` greps `bind` lines in the generated conf, and
        these are not binds), but `tmux-float-refit` reasserts it on
        `window-resized`, so an unstamped or cell-valued stamp would fight the
        daemon. Then `spawnRenderer` (respawn + `@bridge_pane`; probed to
        preserve floatness, geometry and pane options) → `collectHellos` →
        `seedRenderer(…, cell, cfg.graphicsFor(id))` → `go pumpInput(...)`. The
        cell feeds the renderer **unconverted**: a float's cell is its usable
        size.
      - **Move**: `resize-pane` then `move-pane` (outer box), re-stamp
        `@float_geom`, enqueue `FrameResize` with the cell + a `PaneSeed`
        re-seed (renderers hold no back-buffer — #417).
- [ ] Collect hellos for all adds in one batch, mirroring `applyPaneOps`.
- [ ] **Best-effort, never fatal.** It logs and continues; it must not return a
      fatal error into `setupWindow`, whose every other step *is* fatal
      (`addWindow` kills the local window on error, `Run` aborts the bridge). A
      `collectHellos` timeout or a failed `new-pane` for one decorative float
      must not take down the mirror — let alone the bridge open — for a remote
      session that merely had `lazygit` floating.
- [ ] Call it at the end of `setupWindow`, then re-assert
      `cst.setWindowPanes(mw.remoteID, mw.allRemotePanes())` **after** it, so the
      float-inclusive set is registered once the floats exist.
      *Deviation: `float-reconcile` names `daemon.go` must-not-touch; the design
      (§Float reconcile, "runs from `reconcileLayout` **and** at the end of
      `setupWindow`") requires this one call site.*
- [ ] `reconcilefloats_test.go` against the `LocalTmux`/`LocalTmuxOut` fakes:
      add wires a renderer and stamps both options; remove unregisters and
      kills; move emits resize-then-move plus a re-stamp; `graphicsFor` is
      passed through; a failing add logs and does not return a fatal error.

## Step 6: select-layout with floats present — `layout-drop-readd` (implement: opus)

- [ ] `applyLayout` becomes `applyLayout(cfg, w, L, router) (ok bool)`. `ok` is
      true when `L.Raw == w.layout`, when the short-circuit matched, or when
      `select-layout` succeeded; false only when `select-layout` failed.
      **`applyPaneOps`'s signature is deliberately unchanged** — it already
      carries `router` and `w`, so the token rides on `w` and the three existing
      test call sites (`reconcileshape_test.go:70`, `localpanes_test.go:116,154`)
      keep compiling. No test calls `applyLayout`.
- [ ] Geometry short-circuit: after `FitWindowCmd`, when `L.Raw != w.layout` and
      `len(w.localFloats) > 0`, read the mirror window's own `#{window_layout}`
      via `LocalTmuxOut`, `ParseLayout` it, and compare `.Panes` pairwise on
      `W,H,X,Y` against `L.Panes`. **A length mismatch is a miss**, as is a read
      error — both fall through to the drop path. On a match set
      `w.layout = L.Raw` and skip `select-layout` entirely; no float is touched.
- [ ] Drop token: `reconcileLayout` clears `w.floatsDropped` at the top of the
      call. `applyLayout` drops the mirrored floats only when it is about to
      `select-layout`, floats exist, and the short-circuit missed — and only if
      the token is unset, which it then sets. One drop per `reconcileLayout`
      call, covering both `applyLayout` call sites (58 and 208).
- [ ] Restructure the pass loop's exits. **A bare `break` is wrong**: the
      `ops.Reset` and `applyPaneOps`-error exits sit inside a `switch`, so
      `break` would leave the switch and fall through into the broadcast, the
      zoom toggle and the trailing re-read. Use a labelled `break passes` for
      the `applyPaneOps`-error exit and for the convergence exit. The
      `ops.Reset` path must additionally skip the post-loop work, so give it
      either a `reset` flag consumed after the loop or simply a plain `return` —
      `resetWindow` has already rebuilt everything by then.
- [ ] Guard the loop tail: the "didn't converge after N passes" log and
      `w.remotePanes = remote` must be reachable **only from loop exhaustion**.
      With break-based exits an unguarded tail logs a false non-convergence on
      the dominant path, and on the reset path overwrites the fresh pane list
      `setupWindow` just computed with the stale pre-reset slice, so the next
      `planPaneOps` diffs against a lie.
- [ ] Call `reconcileFloats(cfg, w, L, send, router, connCh, rt)` once after the
      loop, on every non-reset exit. The `ops.Reset` path skips both it and the
      tail — `resetWindow` → `setupWindow` already rebuilt the window, its
      floats and its pane list.
- [ ] Then re-assert `cst.setWindowPanes(w.remoteID, w.allRemotePanes())` after
      that call. The in-loop call at `reconcile.go:111` runs *before* the float
      exists, so without this the first gesture inside a freshly mirrored float
      (including the `focus` ctl that `after-select-pane` fires on a mere click)
      is rejected by `parseCtl` and surfaces as a `--display-error` banner.
- [ ] Extend the trailing re-read's convergence test: today
      `fresh.Raw == L.Raw && freshZoom == zoomed`, which is float-blind, so a
      float changing mid-pass leaves the loop with the stale `L` and the
      post-loop `reconcileFloats` applies the old float set. Compare float-set
      equality (ids + cells) alongside `Raw` and the zoom flag — explicitly
      `fresh.Floats` against **`L.Floats`** (two remote reads), never against
      `w.floatGeom`: after a drop that map is empty, and comparing to it would
      never converge, looping to exhaustion every pass.
- [ ] Gate the `FrameResize` + `PaneSeed` broadcast on `ok`: painting the
      remote's dims and screen into panes that never took the shape is the
      #233/#417 blank-mirror failure. Note in a comment that `applyPaneOps`'s
      own inner seed is deliberately *not* gated (a newly appended pane has no
      last-good screen to keep).
- [ ] Log a failed shape once per window per distinct `L.Raw`; clear on success.
- [ ] Do not touch the zoom toggle-on-mismatch semantics (#413/#420).
- [ ] Tests: short-circuit hit issues no `select-layout` and kills no float; a
      length mismatch falls through; a miss drops once and re-adds once even
      across both `applyLayout` calls in one pass; a failed shape suppresses the
      broadcast; a float appearing in the trailing re-read forces another pass;
      the reset path skips both the tail and `reconcileFloats`.

## Step 7: float-aware focus and ctl routing — `float-focus-routing`

*Deviation: this component's may-touch lists no `daemon.go`; the design's
float-inclusive-set table requires the `setupWindow` and `%window-pane-changed`
call sites, which live there.*

- [ ] Pass `w.allRemotePanes()` to `cst.setWindowPanes` at both call sites
      (`reconcile.go:111`, `daemon.go:574`) so `paneToWin` resolves float ids.
- [ ] `focusLocalPane` resolves a float id through `w.localFloats` before the
      tiled `indexOf` path, at both call sites (`reconcileLayout` and
      `%window-pane-changed`, `daemon.go:459`). `cst.noteLocalFocus` still runs
      first.
- [ ] The zoom reconcile's target resolution takes the same lookup.
- [ ] Do not touch `focus.go` — the echo-suppression algorithm is pane-id
      generic and window-keyed.
- [ ] Tests: a ctl request naming a float pane resolves — including a float
      added during that same reconcile; focus follows a remote-active float.

## Step 8: teardown reaps mirrored floats — `teardown-reap`

*Deviation: `sessionpin.go` is must-not-touch for this component and unclaimed
by any other; the design's float-inclusive-set table requires
`sessionPin.reseed` to take it.*

- [ ] `dropMirroredPanes` and `resetWindow`: kill mirrored floats, unregister
      their sinks, close their conns, and **clear `localFloats`/`floatGeom`** —
      otherwise the maps keep entries pointing at killed panes and the next
      Remove would `kill-pane` dead ids.
- [ ] `closeWindow` (`daemon.go:680`) and `Run`'s `teardown` closure
      (`daemon.go:349`) unregister via `allRemotePanes()`.
- [ ] `sessionPin.reseed` (`sessionpin.go:78`) iterates `allRemotePanes()`, else
      a float keeps a stale screen after a `%session-changed` excursion (#396,
      for floats).
- [ ] Narrow the "a float the window also holds is not its to reap" comment to
      "a float this daemon did not create".
- [ ] Tests: teardown unregisters float sinks; reset clears the maps.

## Step 9: docs + gate — `docs`

- [ ] `CLAUDE.md`: the bridge notes stop describing the tool binds as opening a
      remote split; add a bullet for the float mirror (cell vs outer box, the
      short-circuit, the drop/re-add fallback, the unknown-float degradation).
- [ ] Refresh `controlmode/layout.go`'s now-stale `Floats` doc comment ("Not
      mirrored yet; exposed for follow-up work"). *Deviation: `layout.go` is
      must-not-touch for every component; this is a one-line comment fix that
      this PR falsifies, no logic change.*
- [ ] Run the full gate — `nix build .#default`, `nix flake check`, and
      `nix build .#lint`. None subsumes another.
- [ ] PR body: `Closes #409`. State plainly that `prefix + I` in a mirror still
      opens a remote **split** and that closing it needs an aeye-side change
      (`tmux-claude-images` ships from the `carousel-toggle` input, so ctl.go
      owns only the missing-binary fallback); that Stage 3 is the follow-up; and
      **which acceptance criteria are unverified** — "float appears", "geometry
      matches" and "close/resize follows" cannot be shown by `LocalTmux` fakes
      and were not driven against a real remote in this PR. Do not stretch this
      PR toward aeye, and do not widen it to the `float-conf-assertions`
      coverage gap — both are tracked separately.
