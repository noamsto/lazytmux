# Remote bridge — kitty graphics through the mirror

**Issue:** [#280](https://github.com/noamsto/lazytmux/issues/280)
**Cross-repo dependency:** [noamsto/aeye#177](https://github.com/noamsto/aeye/issues/177) — id
namespacing + `AEYE_BRIDGED` frame policy. aeye ships first, then the `flake.lock` bump here.
**Status:** design locked 2026-08-05, pre-implementation.
**Builds on:** M2.3 (`2026-07-30-remote-bridge-m2.3-design.md` — the gated-bind pattern and the
general layout reconcile this milestone reuses wholesale); M2.2
(`2026-07-17-remote-bridge-m2-design.md`, the authoritative M2 design);
`2026-07-16-remote-pseudo-session-bridge-design.md` for the architecture rationale — **and for
the non-goal this milestone retires** (see Empirical ground truth).
**tmux:** probes below were run against the local `next-3.8` (`tmux -V` = `tmux next-3.8`), the
same rev pinned as `tmux-upstream` in `flake.nix`.

## Goal

Pressing `prefix + I` inside a `@bridge_win` opens the aeye carousel for the **remote** agent
pane, as a split in the mirror window, with **crisp images** — the same kitty-graphics rendering
a local carousel gets. Diagrams, zoom, and keyboard pan all work; on a local terminal that can't
do kitty graphics, the carousel silently degrades to block art rather than breaking.

The graphics proxy that gets us there is aeye-agnostic: any remote program emitting kitty
graphics (yazi previews, a plot dumped to the terminal) renders through the bridge as a
side effect.

## Empirical ground truth

Every claim below was measured, not reasoned. Two of them retire a standing non-goal.

### G1 — control mode carries raw pre-parse pty bytes, DCS passthrough included

`2026-07-16-remote-pseudo-session-bridge-design.md:111` records:

> **kitty graphics / images** won't render — the DCS passthrough (`\ePtmux;…`) is consumed by
> the remote tmux.

**This is false.** A control-mode client attached to a `next-3.8` server, with a pane emitting
both a passthrough-wrapped kitty APC and a bare one:

```
%output %0 \033Ptmux;\033\033_Gi=31,a=q,q=2\033\033\134\033\134
%output %0 \033_Gi=32,a=q,q=2\033\134
```

Both arrive verbatim, ESC-doubling intact. tmux buffers raw pty data for control clients, so
the parse that would have consumed the DCS never happens on this path. `allow-passthrough` was
on for the probe, but the bare APC on the second line shows the payload does not depend on it.

**Consequence:** the graphics stream already crosses the bridge. Nothing needs to be tunnelled.

### G2 — the placement half needs no work at all

aeye's crisp path is kitty **unicode placeholders**, where store and placement travel
separately:

- the store is an APC (`\e_Gi=…,a=T,U=1,…`) that emits **no visible cells**;
- the placement is ordinary grid text — U+10EEEE runes plus row/column diacritics, with the
  image id encoded in the cell's fg colour.

Grid text crosses the bridge through the existing text path, `capture-pane -e -p` reseeds
(`daemon/seed.go:26`) included. So a reseed restores placements for free, and scrollback,
copy-mode and redraws behave like any other text.

### G3 — what is actually host-bound

Both of aeye's transmit shapes send a **file path**, base64'd after the `;`:

- `transmitVirtual` (`aeye/gallery_render.go:370`) — `f=100,t=f` (PNG by path);
- `transmitVirtualRaw` (`aeye/gallery_render.go:490`) — `f=32,s=…,v=…,t=f` (raw RGBA by path).

A local kitty cannot read a remote path. This — plus id collisions (G5) — is the entire defect.
One rewrite rule covers both shapes.

### G4 — a control client's `client_termname` is the ssh TERM

```
client-91015 term=xterm-kitty flags=attached,focused,control-mode,UTF-8
```

aeye picks its backend from `#{client_termname}` (`aeye/gallery.go:1101`), and
`chooseGridBackend` (`aeye/gallery_render.go:333`) allowlists `xterm-kitty` / `xterm-ghostty`
for the placeholder path. So **the daemon steers the remote backend choice** by what TERM its
ssh advertises — including steering it *away* from kitty when the local terminal can't render
it, which is the whole of the fallback story.

### G5 — image ids collide across servers

`paneImageIDBase` (`aeye/gallery.go:83`) seeds the id block from the bare pane number, unique
per tmux *server*. A bridged carousel is a second server: remote `%5` and local `%5` land on
block 300 and overwrite each other in the one kitty store.

### G6 — the pan path is bandwidth-hostile by design

`storePreviewCrop` (`aeye/gallery_zoom.go:251`) deliberately skips the PNG encode and writes raw
RGBA, because a local kitty reads it off disk 2.5× faster (6.7ms → 2.6ms on a 1220×814 box —
"the dominant per-pan cost"). That is **~4 MB per frame**, and `panFrameGap`
(`aeye/gallery_zoom.go:280`) is **8ms**, permitting up to 125 stores/second. Across ssh that
trade inverts completely.

Mitigating fact: **there is no mouse handling anywhere in the daemon or renderer** — mouse
forwarding is the unbuilt M2.4 — so drag-pan cannot be triggered from a bridged carousel at all.
The reachable worst case is key-repeat, ~30 frames/s.

### G7 — the renderer paints verbatim

`render/renderer.go:52-53` writes `f.Payload` straight to the pane tty for both `FrameSeed` and
`FrameOutput`, with no filtering. A rewritten sequence reaches the local tmux untouched, and the
local tmux (`allow-passthrough on`, `config/tmux.conf.nix:605`) unwraps it to the outer terminal.

## Settled decisions

### D1 — the carousel runs on the remote, mirrored back

`prefix + I` gates on `bridgeGate` (`config/tmux.conf.nix:286`) exactly like the other structural
verbs, and a new ctl verb runs the toggle remotely. The split then mirrors back through
machinery that already exists and is tested: `mirrorWindow`'s (N-1) `split-window` + one
`select-layout` (`daemon/mirror.go:22-31`), the general reconcile applying the remote's raw
layout string (`daemon/reconcile.go:70-78`), and `planPaneOps`, which was written for precisely
this mid-list insert (`daemon/panediff.go:28-36`).

**Rejected — a local carousel reading synced remote files.** It cannot be a pane in the mirror
window (the pane-diff would delete a pane the remote doesn't have), so it would need a separate
local window or a kitty split — losing adjacency, restricting the feature to kitty (not
ghostty), and requiring manifest sync plus change notification. It also fixes nothing for any
other remote program.

**Rejected — text-only (`backendSymbols`) in bridged panes.** This is the *fallback*, not the
target, and G4 gives it to us for free without designing for it.

### D2 — rewrite the payload; never rewrite the placements

The proxy touches only APC control/payload bytes. Image ids are left exactly as the remote wrote
them, and the fg colours encoding them in the text stream are never parsed.

**Rejected — daemon-side id remapping.** Remapping `i=` alone is trivial and wrong: the
placements carry the same id as 24-bit fg RGB, so a correct remap needs a stateful SGR parser
that rewrites a colour only once it lands on a U+10EEEE cell — blind rewriting corrupts ordinary
coloured text that happens to match an id. The collision is fixed upstream instead (D6), which
also fixes the general two-hosts-one-store case.

### D3 — fetch over a daemon-owned ControlMaster

The daemon spawns its control ssh with `-M -S <runtime>/lztmux-bridge-<host>.sock`; each fetch is
a multiplexed exec on that same TCP connection (~10-20ms warm). Independent of whatever the
user's `ssh_config` does. The socket's lifetime is the daemon's.

**Rejected — in-band `run-shell 'base64 …'` over the control stream.** Zero new connections, but
image bytes would share one channel with live terminal output (+33% base64), so a transfer
stalls every pane behind it and collides with the `pause-after` flow control tuned for the
output firehose.

**Rejected — sshfs/rsync of the image dir.** Turns a fetch into a local read, but adds a mount
dependency outside the bridge's lifetime (macFUSE on the local side), stale-cache semantics on
rewritten scratch frames, and a failure mode that hangs on I/O instead of erroring.

### D4 — hold the stream at the sequence boundary, bounded by a deadline

The pane's byte stream pauses at a sequence needing a fetch, so bytes stay ordered and a store
always precedes the placements referencing it. Serialisation is **per-pane**; other panes keep
flowing. The hold is bounded (~2s); on timeout the sequence is dropped and the stream resumes,
because a frozen pane is worse than a missing image.

### D5 — coalesce stores per image id

Where a store for image id N is followed by a newer store for N, only the newest is forwarded.

**Mechanism, settled during planning:** with the proxy on the pane's pump goroutine (D4), fetches
are serial per pane, so there is never a second store arriving "while a fetch is in flight" to
compare against. The pump instead **batch-drains** every `FrameOutput` already queued behind the
one it woke on and hands the proxy the whole burst, so a superseded store is visible in the same
buffer as the store that supersedes it. Draining stops at the first non-output frame: reordering
a seed or resize past output would break the frozen wire invariant.

This is safe **because of how aeye pans**: `transmitPreviewOnly`
(`aeye/gallery_zoom.go:269`) re-places under the same id with `a=T` and no delete, so every
store is a full replacement and intermediate frames are pure waste. The link therefore sets the
frame rate instead of the sender, with bounded staleness (the newest framing always wins).

Rule: **never coalesce across an intervening `a=d` for that id** — a delete is a real state
transition, not a superseded frame.

### D6 — the id fix and the frame policy live upstream in aeye

Per G5 and G6, two changes in `noamsto/aeye#177`: seed `paneImageIDBase` from hash(hostname,
pane); and under `AEYE_BRIDGED=1` take the PNG branch `storePreviewCrop` already has as its
`writeRaw`-failure fallback, plus a wider `panFrameGap`. The launch verb (D1) owns the
carousel's environment, so setting the variable costs nothing.

### D7 — never emit a transmit whose payload could not be localised

The governing failure rule. A stale local path renders the **wrong** image; a dropped store
renders blank and self-heals on aeye's next repaint. Blank always beats wrong.

### D8 — the daemon advertises the local client's termname

The control ssh exports the termname of the **local** client (what the local tmux reports as
`#{client_termname}` for the human's client — not `$TERM` inside tmux, which is
`tmux-256color`). Per G4 that is what remote aeye reads, so one knob decides the remote backend:
kitty-class local terminal → placeholders and the proxy path; anything else → block art over
plain text, with no proxy involvement and nothing to fall back *from*.

## The proxy

A new `remotebridge/graphics` package sitting between `%output` decode
(`controlmode/parse.go:9-12`) and the renderer frame write, one scanner per pane.

**Scan.** A resumable state machine, because a sequence can split across `%output` lines — the
same caveat `Unescape` already documents for UTF-8 runes. It recognises both forms:

```
bare      \e_G<keys>;<payload>\e\\
wrapped   \ePtmux;\e\e_G<keys>;<payload>\e\e\\\e\\
```

**Canonicalise.** Unwrap to the bare form so there is exactly one shape to reason about.

**Classify** on the `t=` key:

| Form | Action |
|------|--------|
| `t=f`, `t=t` | base64-decode payload as a remote path → fetch → re-encode the **local** path |
| `t=d` | pass through unchanged — the payload is self-contained |
| `a=d` (delete), any sequence with no payload | pass through unchanged |
| `t=s` (shared memory) | drop + log — cannot cross a host boundary; aeye never emits it |

Every other key (`i=`, `a=`, `c=`, `r=`, `f=`, `s=`, `v=`, `U=`, `q=`) is copied verbatim.

**Re-wrap** exactly once, for the local tmux, and hand to the renderer.

**Fetch.** One round trip over the ControlMaster socket returning `mtime size` then bytes, into
`$XDG_CACHE_HOME/lztmux-bridge/<host>/`. Cache key `(path, mtime, size)` — **mtime is
load-bearing**, since aeye rewrites scratch frames at a stable path during pan/zoom. LRU by
total bytes, 256 MB default, pruned on daemon start. Per-image size cap (8 MB default): over-cap
drops and logs, which is what guards the raw-RGBA path if `AEYE_BRIDGED` is ever absent.

## Failure behaviour

| Failure | Behaviour |
|---|---|
| Local terminal not kitty-class | G4: remote aeye picks `backendSymbols`, block art over the plain text path; the proxy never engages |
| `tmux-claude-images` missing on remote | The remote command falls back to opening a short split carrying the error, which mirrors back into the window the human is looking at. **Amended 2026-08-05 (planning):** the original "`display-message` locally" is not reachable — the only client attached to the remote is the daemon's control client, which has no status line for a message to land on. Never a silent no-op either way |
| Fetch fails (file gone, perms, link drop) | Drop the sequence, log, resume; placements render empty until aeye's next repaint |
| Fetch exceeds the hold deadline | Drop and resume (D4) |
| Image over the size cap | Drop + log |
| `t=s` | Drop + log |
| Bridge reseed | Free — placements are grid text (G2); the store persists in the long-lived local kitty |
| Local kitty store lost (terminal restart) | Pre-existing aeye condition, handled by its repaint path; the bridge changes nothing |

**Known limitation — rasterisation resolution.** `queryCellPx` (`aeye/cellsize.go:41`, CSI 16 t)
has no terminal to answer it through the bridge, so it times out and falls back to estimates.
Placement is by cell box (`c=`/`r=`), so layout is exact; only the resolution the image is
rasterised at may drift from the local terminal's true cell size. Fix, if it shows: carry the
local cell size in via the launch environment, alongside `AEYE_BRIDGED`.

## Remote host requirements

- `tmux-claude-images` on PATH — the carousel toggle.
- `resvg` on PATH — sharp zoom re-rasterises the crop window through it
  (`aeye/gallery_vector.go:49`); absent, aeye falls back to the bitmap crop (blurrier, still
  functional).

## Acceptance criteria

1. `prefix + I` in a `@bridge_win` opens the carousel as a split in the mirror window, with the
   remote's exact layout geometry; in a non-bridge window the bind behaves **byte-identically**
   to today (the zero-blast-radius rule every gated bind follows).
2. A d2 diagram rendered on the remote displays crisp in the mirrored carousel.
3. Zoom re-rasterises sharply (given resvg on the remote); keyboard pan tracks without queueing
   — on a slow link it lags and stays current, never backlogs.
4. Two carousels — one local, one bridged, same numeric pane id — display their own images.
5. On a non-kitty local terminal the carousel opens and renders block art.
6. Killing the daemon leaves no ControlMaster socket and no growth in the fetch cache beyond its
   LRU bound.
7. A fetch failure yields a blank image and a log line, never a wrong image (D7).

## Testing strategy

**Unit** (`remotebridge/graphics`; the fetcher is an injected interface, following the existing
`rawSetup` / `LocalTmux` seams — no ssh in tests):

- scan: bare form, wrapped form, sequence split across two `%output` frames, partial sequence at
  EOF, control-only sequence with no `;` payload;
- rewrite: path round-trip; `i=`/`c=`/`r=`/`f=`/`s=`/`v=` preserved byte-identically;
- wrap/unwrap: exactly one wrapper on output regardless of input form;
- passthrough: `t=d` and `a=d` byte-identical; `t=s` dropped;
- coalescing: a superseded store for the same id is replaced; a store separated from its
  predecessor by an `a=d` is **not**;
- cache: key varies with mtime at a stable path; size cap drops.

**Integration**, on the existing `--test-local` seam so it runs in `nix flake check`: a local
pane emits a real `transmitVirtual` sequence pointing at a temp PNG; assert the renderer-bound
frame carries the rewritten local path, one wrapper, and untouched control keys.

**bats** (`tests/remote-m2-integration.bats` style): the `prefix I` gate — bridge window routes
to the ctl verb, non-bridge window keeps the existing local behaviour verbatim.

**aeye** (upstream): same pane id on two hostnames yields disjoint id blocks.

**Manual** (g5 → tp-g6 over the tailnet, in kitty and in ghostty): a d2 diagram, a photo,
keyboard pan/zoom, `s` split flip, close; then the same with a non-kitty local terminal to
confirm the block-art fallback.

## Non-goals

- **Sixel / OSC 1337 rewriting.** Self-contained byte streams that would likely pass through
  untouched, but the remote DA1 probe has no terminal behind it so aeye won't select them.
  Not designed for, not tested.
- **Kitty animation (`a=a`) and non-virtual placements.** Passed through unchanged rather than
  pretending to support them.
- **OSC 72 drag-out.** Already gated on `TMUX == ""` upstream; cannot cross tmux at all.
- **kitty-pane mode from a bridged pane.** No local kitty socket exists on the remote; the ctl
  verb forces tmux-split mode.
- **Mouse-driven pan.** Depends on M2.4 mouse forwarding, unbuilt (G6).
- **Reconnect after link drop.** Standing M2 non-goal, untouched.
