# Decomposition — mirror remote floating panes as local floats (#409)

Consumes: the task brief and `docs/superpowers/specs/2026-08-31-mirror-remote-floats-design.md`
(the accepted design — its probed tmux facts are authoritative; do not re-derive
them, and do not resurrect the superseded park/restore or break-pane-inject
ideas). All Go paths below are repo-relative.

## components

### float-geom
One-line: pure geometry mapping between a float's layout cell (inner box, what
`Layout.Floats` and `FrameResize`/seed dims speak) and tmux's outer-box flags
(what `new-pane -x/-y/-X/-Y`, `resize-pane -x/-y`, `move-pane -X/-Y`, and the
`@float_geom` stamp speak), with the inset pinned as a constant of the mirror's
own `-B heavy` border (1/side; only `-B none` is 0), plus the argv builders for
create/resize/move and the `@float_geom` outer-cell stamp value.

- boundaries:
  - may-touch: `picker/remotebridge/daemon/floatgeom.go` (new),
    `picker/remotebridge/daemon/floatgeom_test.go` (new).
  - must-not-touch: `picker/remotebridge/controlmode/layout.go`,
    `scripts/tmux-float-refit.sh`, `config/tmux.conf.nix`, `flake.nix`,
    `picker/remotebridge/daemon/reconcile.go`,
    `picker/remotebridge/daemon/ctl.go`.
- risk: low

### float-state
One-line: `mirrorWindow` gains `localFloats map[string]string` (remote float
pane id → local float pane id) and `floatGeom map[string]controlmode.PaneCell`
(remote float pane id → last cell applied locally), plus a pure diff of that
tracked state against `Layout.Floats` yielding add/remove/move sets — no tmux
calls, no I/O.

- boundaries:
  - may-touch: `picker/remotebridge/daemon/windows.go` (the `mirrorWindow`
    struct and its doc comment only — not `registry`, not the window-name
    helpers), `picker/remotebridge/daemon/floatdiff.go` (new),
    `picker/remotebridge/daemon/floatdiff_test.go` (new).
  - must-not-touch: `picker/remotebridge/daemon/panediff.go` (`planPaneOps`
    stays tiled-only), `picker/remotebridge/daemon/localpanes.go` (the
    tiled-only meaning of `localPanes`), `picker/remotebridge/daemon/mirror.go`,
    `picker/remotebridge/controlmode/layout.go`.
- risk: low

### ctl-float-verbs
One-line: `ctl.go`'s `tool` verb goes from remote `split-window` to remote
`new-pane` with a per-tool shape table (prdash/yazi → `90% 85% 5% 8%`
= floatShort; lazygit/tmux-gh-dash → `90% 90% 5% 5%` = floatFull), the
`carousel` verb's missing-binary fallback split becomes a `new-pane` float
(the aeye primary path is untouched — deliberate narrowing, per the spec), and
the "split is deliberate" comment block is replaced.

- boundaries:
  - may-touch: `picker/remotebridge/daemon/ctl.go` (the `verbs` table entries
    for `tool`/`carousel`, `toolResolveScript`, `carouselResolveScript`, and
    their comments only), `picker/remotebridge/daemon/ctl_test.go`.
  - must-not-touch: `config/tmux.conf.nix` (the `bridgedTool`/`mkFloat`/
    `floatShort`/`floatFull`/`carouselBind` definitions and every bind — the
    local binds already route through the ctl verbs and stay as they are),
    `flake.nix`, `picker/remotebridge/wire/protocol.go` (no version bump — verb
    names and arities are unchanged), `parseCtl`/`submit`/`ctlState` machinery,
    the `theme` verb, `remoteTools`/`remoteThemes` whitelists.
- risk: medium (remote command text is built by hand and must survive the
  double-tmuxQuote / zero-single-quote script rules already documented in the
  file; the remote pane must NOT get an `@float_geom` stamp)

### float-reconcile
One-line: `reconcileFloats` — the first consumer of `Layout.Floats` — runs as
its own step in `reconcileLayout` after `applyLayout` and the tiled re-seed:
Remove (unregister sink → close+delete conn → `kill-pane` → drop map entries),
Add (`new-pane` with inset-corrected geometry, local pane id captured at
creation, `@float_geom` stamped, then `spawnRenderer` → hello → seed → register
→ pump, exactly the appended-tiled-pane sequence), Move/resize (`resize-pane`
then `move-pane`, re-stamp `@float_geom`, push `FrameResize` + `capture-pane`
re-seed), and the convergence re-read at the loop tail extended to compare the
float set (`L.Raw` and the zoom flag are float-blind by construction).

- boundaries:
  - may-touch: `picker/remotebridge/daemon/reconcile.go`,
    `picker/remotebridge/daemon/localpanes.go` (only to expose the float half
    `parseLocalPaneList` already computes, if the Add path captures the new
    pane id by re-listing rather than `new-pane -P` via `LocalTmuxOut`),
    `picker/remotebridge/daemon/reconcilefloats_test.go` (new),
    `picker/remotebridge/daemon/reconcileshape_test.go` /
    `reconcilereseed_test.go` (only if a touched signature forces it).
  - must-not-touch: `picker/remotebridge/daemon/daemon.go` (`stream`,
    `outputSink`, `Router` wiring, `spawnRenderer`, `seedRenderer`,
    `collectHellos`, `pumpInput`, `reseedDropped` — reuse them, do not edit
    them), `picker/remotebridge/daemon/seed.go`,
    `picker/remotebridge/daemon/router.go`, `picker/remotebridge/wire/`,
    `picker/remotebridge/render/`, `picker/remotebridge/graphics/`,
    `picker/remotebridge/controlmode/layout.go`,
    `picker/remotebridge/daemon/panediff.go`.
- risk: high

### layout-drop-readd
One-line: `applyLayout`'s guarded expensive path — when `L.Raw != w.layout` AND
the window holds mirrored floats, kill the local mirrored floats first (the
float-reconcile Remove path, verbatim), run `select-layout` against the now
float-free window, and let the same pass's float-reconcile Add path re-create
them from `L.Floats`; the `L.Raw == w.layout` skip stays exactly as #420 left
it (it is what keeps a zoom-only reconcile and a float open/close from ever
issuing a doomed `select-layout`).

- boundaries:
  - may-touch: `picker/remotebridge/daemon/reconcile.go` (`applyLayout`,
    `reconcileLayout` sequencing, `applyPaneOps`'s mid-surgery `applyLayout`
    call site), plus the drop/re-add command-sequence test (in
    `reconcilefloats_test.go` or its own file).
  - must-not-touch: the zoom toggle-on-mismatch block's semantics (#413/#420 —
    read CLAUDE.md "Zoom crosses the bridge as a ctl verb" first),
    `focusLocalPane`'s `structural` gate for tiled ops,
    `picker/remotebridge/daemon/mirror.go` (`PlanWindow`/`FitWindowCmd`),
    `picker/remotebridge/controlmode/`.
- risk: high (depends on float-reconcile's Remove/Add being callable as
  primitives; ordering vs FitWindowCmd, the tiled re-seed, and #233/#417 rules
  is where regressions live)

### float-focus-routing
One-line: float pane ids reach `ctlState.paneToWin` so a keybind pressed inside
a local mirrored float (whose `@bridge_pane` the renderer respawn already
stamps) resolves instead of being rejected as "not mirrored by this bridge",
and focus follows a remote-active float: `focusLocalPane` (or a float-aware
sibling) resolves a float id through `localFloats`, float adds/removes count as
`structural` for focus only when the remote's active pane is a float, and
`noteLocalFocus` still precedes the local `select-pane` (echo suppression).

- boundaries:
  - may-touch: `picker/remotebridge/daemon/ctl.go` (`setWindowPanes` — or a
    companion that registers/unregisters float ids — and `forgetWindow`'s
    coverage of them), `picker/remotebridge/daemon/reconcile.go`
    (`focusLocalPane` call sites), `picker/remotebridge/daemon/ctl_test.go`,
    `picker/remotebridge/daemon/focus_test.go`.
  - must-not-touch: `picker/remotebridge/daemon/focus.go` (the
    FIFO/echo-suppression algorithm is pane-id-generic and window-keyed —
    it needs no change), the `verbs` table (that is ctl-float-verbs'),
    `config/tmux.conf.nix` binds.
- risk: medium

### teardown-reap
One-line: every teardown path reaps mirrored floats — `dropMirroredPanes` and
`resetWindow` kill/unregister them, `closeWindow` unregisters their sinks and
kills them with the window, `Run`'s `teardown` closure unregisters float ids
alongside `mw.remotePanes` — and the "a float the window also holds is not its
to reap" comment narrows to "a float this daemon did not create" (a user's own
local float stays untouched, which is the Stage 3 seam).

- boundaries:
  - may-touch: `picker/remotebridge/daemon/reconcile.go` (`dropMirroredPanes`,
    `resetWindow`), `picker/remotebridge/daemon/daemon.go` (`closeWindow` and
    the `teardown` closure's per-window loop ONLY), matching tests
    (`daemon_test.go` / `reconcilefloats_test.go`).
  - must-not-touch: everything else in `daemon.go` (`Run`'s main loop,
    `stream`, sinks, `watchResize`, `sessionpin.go`),
    `picker/remotebridge/daemon/agentstatus.go` (its live-set diff in `apply`
    already drops files for panes that leave `cfg.LocalPanes()` — no change),
    `picker/remotebridge/cmd/daemon/main.go`.
- risk: medium

### docs
One-line: the plan document lands under `docs/superpowers/plans/` in the same
PR (house rule), and CLAUDE.md's bridge notes stop describing the tool binds /
carousel as opening a remote split (Key Conventions bullets + the "What the
Remote Host Needs on PATH" table row), plus a bullet for the float mirror's
drop-and-re-add invariant; the PR body notes Stage 3 as the follow-up.

- boundaries:
  - may-touch: `CLAUDE.md`, `docs/superpowers/plans/2026-08-31-*.md` (new).
  - must-not-touch: `docs/superpowers/specs/2026-08-31-mirror-remote-floats-design.md`
    (accepted as-is), `README.md`, other specs/plans.
- risk: low

## ordering

1. `float-geom` ∥ `float-state` ∥ `ctl-float-verbs` ∥ `docs`
   (mutually file-disjoint; no shared symbols)
2. `float-reconcile` (needs float-geom's converters/argv builders and
   float-state's fields+diff)
3. `layout-drop-readd` (needs float-reconcile's Remove/Add as callable
   primitives; same file — never in parallel with it)
4. `float-focus-routing` → `teardown-reap`
   (logically independent of each other, but both edit `reconcile.go` — and
   focus-routing also edits `ctl.go` — so serialize; either order works, this
   one keeps ctl.go edits adjacent to their tests)

`ctl-float-verbs` is behaviour-visible only once 2-4 land (a remote float that
nothing mirrors just vanishes, today's status quo), so it may merge early but
the acceptance drive needs the whole chain.

## interfaces

Contracts that must hold across the split — checkable against the paths named.

- **`Config.LocalTmux(args ...string) error` / `Config.LocalTmuxOut(args
  ...string) (string, error)`** (`daemon/daemon.go`) are the only seams to the
  local tmux; every float create/resize/move/kill/stamp goes through them (the
  unit tests fake them). No new `Config` field: if the Add path needs the new
  pane's id, it uses `LocalTmuxOut` (`new-pane` + print-id, or a re-list parsed
  by `parseLocalPaneList`'s float half).
- **`controlmode.Layout{W, H, Panes, Floats, Raw}` and `PaneCell{ID, W, H, X,
  Y}`** (`controlmode/layout.go`) are frozen. `ParseLayout`'s prune keeps
  `Raw` tiled-only and select-layout-safe; `Floats` cells are INNER boxes and
  equal the pane's usable size (probed), so they feed `seedRenderer` dims and
  `wire.EncodeResize` unconverted — only tmux create/resize/move FLAGS take the
  ±1 inset (float-geom owns that conversion, pinned to `-B heavy`).
- **`mirrorWindow`** (`daemon/windows.go`): `remotePanes`/`localPanes` stay
  index-parallel and tiled-only; `conns` stays keyed by remote pane id and now
  also holds float conns (same key space, no second map); `layout` keeps
  meaning "last tiled layout string applied locally, `\"\"` = invalidated".
  New fields `localFloats`/`floatGeom` are keyed by remote float pane id.
- **`planPaneOps(have, want []string) paneOps`** (`daemon/panediff.go`):
  signature and tiled-only semantics frozen; floats never enter `have`/`want`.
- **`applyLayout` skip contract** (`daemon/reconcile.go`): `L.Raw == w.layout`
  ⇒ no `select-layout` (this is what keeps the zoom-only path of #413/#420
  correct and makes remote float open/close free, since `L.Raw` is
  byte-identical with and without a float); pane surgery still sets
  `w.layout = ""`. The new guard adds "and mirrored floats exist ⇒ drop them
  first"; it must never touch a local float NOT in `localFloats`.
- **`reconcileLayout` pass order**: converge → plan/apply tiled ops →
  `applyLayout` (with the float drop inside its changed-Raw branch) → tiled
  `FrameResize` + `PaneSeed` re-seed (after the reshape, never before — #233,
  #417) → zoom toggle-on-mismatch (unchanged; mirror floats are created `-A`
  so they coexist with zoom) → `focusLocalPane` gated on `structural` →
  `reconcileFloats` → trailing re-read whose convergence test is
  `fresh.Raw == L.Raw && freshZoom == zoomed` EXTENDED with float-set equality
  (id + cell), or a float change mid-pass is silently dropped.
- **Renderer wiring sequence (frozen, reused verbatim by the float Add path)**:
  create pane → `spawnRenderer(cfg, localPaneID, remotePaneID)` (which is
  `respawn-pane -k` + the `@bridge_pane` stamp; probed: respawn preserves
  floatness, geometry and pane options) → `collectHellos(connCh, n,
  helloTimeout)` → `seedRenderer(rt, router, conn, remotePane, dims,
  cfg.graphicsFor(remotePane))` (register-then-enqueue: FrameSeed then
  FrameResize) → `go pumpInput(conn, remotePane, send)`. Never pass the
  renderer as `new-pane`'s command.
- **`wire` frame protocol frozen**: `FrameHello/Seed/Output/Resize/Input/Ctl/
  CtlAck` and `CtlProtocolVersion = "4"` unchanged — no new verb, no arity
  change; only the `tool`/`carousel` REMOTE command text moves.
- **`Router`** (`daemon/router.go`): Register/Unregister/Route/`sink`/
  `dirtyPanes` keyed by remote pane id; `Unregister` closes the sink (stops the
  pump). Float sinks registered through `seedRenderer` get `%pause`/
  `%continue` handling and `reseedDropped` coverage for free — no Router edits.
- **`@bridge_pane`** (local pane option): written only by `spawnRenderer`;
  read by the bridge keybinds (`config/tmux.conf.nix` `bridgeGate` sites),
  `cmd/daemon/main.go` `localPaneMap` (feeds `Config.LocalPanes` → agent-status
  shipper), and `scripts/lztmux-remote-theme.sh`. A local mirror float carries
  it exactly like a tiled mirror pane — that is the whole reason agent-status
  polling (`daemon/agentstatus.go`, unchanged) and in-float ctl gestures work.
- **`ctlState.paneToWin`** (`daemon/ctl.go`): must resolve float pane ids;
  `setWindowPanes`'s clear-all-for-window-then-set idiom must not orphan float
  entries when the tiled set is re-stamped, and `forgetWindow` must drop them.
- **`@float_geom`** (local pane option on daemon-created floats): one line,
  space-separated `<w> <h> <x> <y>` in OUTER-box absolute cells (the space
  `resize-pane -x/-y` / `move-pane -X/-Y` speak), stamped at create and
  re-stamped on every geometry reapply — so `scripts/tmux-float-refit.sh`'s
  `window-resized` reassert replays exactly what the daemon last applied
  (consistent no-op, R6). The REMOTE float pane is never stamped (ctl verbs).
- **`flake.nix` `float-conf-assertions`**: scans generated tmux.conf `bind`
  lines only; daemon-created floats are not binds, so no flake change — and no
  existing `mkFloat` bind may lose its stamp.
- **Focus**: `focusState`/`planFocusLocked`/`applyRemoteFocus`
  (`daemon/focus.go`) are frozen; float focus rides through them once
  `paneToWin` covers float ids. `focusLocalPane`'s tiled `indexOf` miss on a
  float id must resolve via `localFloats` instead of silently no-opping, and
  `cst.noteLocalFocus` still runs before the local `select-pane`.
- **Resize watcher** (`watchResize`, `daemon/daemon.go`): untouched; a local
  client resize reaches floats via converge → remote `%layout-change` →
  `reconcileLayout` → `reconcileFloats` geometry reassert. No float logic on
  the watcher goroutine (it holds no `rt`).
- **Mirror invariant (R7)**: local float state is derived only from a remote
  layout read; no path writes a local float first and reconciles the remote to
  it. `picker/remotepick.go`'s `^o` float (a purely local float in a
  NON-mirror window) and Stage 3 tolerance of a local float inside a mirror
  window are out of scope — `picker/remotepick.go` is must-not-touch for every
  component.
