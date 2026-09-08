# Plan: assert mirror zoom with `if -F`, drop `localZoomed`

Closes #568. Cursor worker — plan-critic skipped (engine carve-out).

## Mechanism

tmux zoom is a toggle (`resize-pane -Z`). Reconcile used to `display-message` the
mirror's `#{window_zoomed_flag}` (`localZoomed`) and toggle only on mismatch.
That fork is per reconcile; a missed or doubled toggle is the parity bug.

`if -F` asserts the flag in one local command:

```
# zoom, idempotent
if -F -t <win> '#{window_zoomed_flag}' '' 'resize-pane -Z -t <pane>'
# unzoom, idempotent
if -F -t <win> '#{window_zoomed_flag}' 'resize-pane -Z -t <win>' ''
```

Empty branches are accepted. Condition is window-scoped; zoom-on still targets
the tiled local pane that renders `remoteActive` (window `-Z` would zoom whatever
is locally active). Unzoom may target the window. Skip zoom-on when
`remoteActive` is a float (#517) — same as today.

Do **not** touch `ctl.go`'s `zoom` verb (remote). Do **not** read
`#{window_visible_layout}` for tiled geometry.

## Order (the issue's open decision)

1. `applyLayout` (fit / maybe `select-layout`). `select-layout` unzooms; asserting
   before it would be undone. Unchanged `L.Raw` still skips `select-layout`.
2. Assert zoom with `if -F` (after applyLayout, before FrameResize/reseed).
3. Dims + reseed keyed on whether the assert **succeeded** (same as today's
   `localIsZoomed` after a landed toggle). Failure → cell dims, reseed anyway.
4. `reconcileFloats` stays after the pass loop (drop-and-re-add must not run
   between `select-layout` and the assert, and must not precede a `select-layout`).

## Dedup without `localZoomed`

Keep the cheap skip (`L.Raw == w.layout` and zoom already matches) so duplicate
`%layout-change` does not FitWindow + reseed. Store last successfully asserted
flag on `mirrorWindow` (`appliedZoom bool`). Compare that to the remote flag
from `readLayout` — never a `display-message` of the mirror.

- Zoom-only event: `L.Raw` unchanged, `appliedZoom != zoomed` → fall through,
  assert, set `appliedZoom`.
- Repeat still-zoomed: `appliedZoom` already matches → skip (parity-safe because
  assert is idempotent even if we did not skip).
- Zero value is unzoomed; matches existing fixtures that set `w.layout` and a
  remote flag of `0`.

## Files

- `picker/remotebridge/daemon/reconcile.go` — early-out + post-`applyLayout` block
- `picker/remotebridge/daemon/localpanes.go` — delete `localZoomed`
- `picker/remotebridge/daemon/` window struct (wherever `mirrorWindow` is defined)
- `picker/remotebridge/daemon/reconcilezoom_test.go`
- `picker/remotebridge/daemon/reconcilededup_test.go`
- `picker/remotebridge/daemon/reconcileordering_test.go` — drop `localZoomOut` where it only fed `localZoomed`
- `CLAUDE.md` — zoom bullet: `if -F` assert, no `localZoomed`

## Tests to add/rewrite

- Zoom-on / unzoom LocalTmux argv is `if -F …`, not a bare `resize-pane -Z`.
- Second reconcile of a still-zoomed window does not invert zoom (dedup or
  idempotent `if -F`; assert either no second `-Z` effect or `if -F` only).
- Repeated `if -F` zoom / unzoom (table or comment + unit) — unit captures argv;
  live `if -F` idempotency is re-verified once against pinned tmux before coding.
- Keep: zoomed pane gets layout-root dims; unzoom reseeds every pane; float-active
  skip; tiled-beside-float still asserts onto the tiled pane; zoom-only falls
  through dedup; `#{window_layout}` remains unzoomed geometry.
- Drop/replace: unknown-`localZoomed` (`ok=false`) cases — that seam is gone.
  Keep a failure-leg test: `if`/`LocalTmux` error → cell dims, reseed still runs.

## Validation

```
# live if -F idempotency on pinned next-3.8 (scratch -L server)
nix build .#default   # not required for Go-only, but repo gate
# scoped:
go test ./picker/remotebridge/daemon/ -count=1
nix flake check       # if time; at least picker-go-tests via flake or go test
nix build .#lint
```

Do not run whole-repo flake check as the inner loop; scoped Go tests first.
