# Remote bridge M2 — full-parity multi-window mirror

**Issue:** #167
**Status:** approach approved, spikes done, ready to lock
**Builds on:** M1 (`docs/superpowers/specs/2026-07-16-remote-pseudo-session-bridge-design.md`) — one remote window's active pane rendered live as one native local pane; hardware-verified g5→g6.

## Goal

Make a bridged remote tmux session feel **indistinguishable from a local one**: every
remote window and pane rendered as a native local window/pane, your normal prefix +
keybinds acting on the remote, live-synced both ways, no inner status bar, no nested tmux.

## Requirements (from the user)

1. **Full parity** — create / split / close / rename / navigate remote windows *and* panes.
2. **Input model** — your normal local prefix + bindings act on the **remote** tmux. One
   prefix, no inner bar (nested tmux was rejected in M1 testing).
3. **Passthrough scope, all must-have** — copy-mode/scrollback, mouse (select/resize/scroll),
   clipboard (OSC 52), focus events.

## Architecture (Approach 1 — native per-pane)

Three components; the M1 package split (`controlmode` / `render`) already anticipated them.

- **Daemon** (`remotebridge/daemon`, grown from the M1 `controlmode` half). Owns the single
  `ssh -T -e none -CC attach-session` connection. Three jobs:
  1. **Mirror engine** — translate remote control-mode notifications (`%window-add`,
     `%window-close`, `%window-renamed`, `%window-pane-changed`, `%layout-change`,
     `%session-window-changed`) into local tmux structural commands (`new-window`,
     `split-window`, `select-layout`, `kill-pane`, `rename-window`) against the local
     `<host>-<sess>` session.
  2. **`%output` router** — demultiplex the all-panes firehose; forward each pane's bytes to
     that pane's renderer over a pipe.
  3. **Control socket** — a local unix socket that keybind scripts write to, so a local
     structural gesture becomes a remote command.
- **Renderer** (`remotebridge/render`) — one process per local pane. A **dumb painter +
  stdin forwarder**: reads its pane's bytes from the daemon and paints them (raw tty, from
  M1); reads local stdin and hands keystrokes to the daemon to `send-keys -H` at the matching
  remote pane. It does **not** open ssh and does **not** own the control stream or the seed
  handshake (those move to the daemon — see I2 below).
- **Keybind translation layer** (tmux config + a thin dispatcher script). Structural verbs
  are bound to a dispatcher gated on the `@bridge_ctl` session option: if set, forward the
  verb to the daemon's control socket; else run the normal local action.

### The invariant (corrected): directional source-of-truth

The **remote is the structural source of truth**, but authority is **directional**:

- **Structure** (window/pane topology, splits, names) flows **remote → local**. A discrete
  structural keypress never mutates local structure directly — it commands the remote, the
  remote emits a notification, the mirror applies it locally.
- **Size** (actual cell dims) flows **local → remote**. The daemon owns the single control
  client's size (= the local window content size) and pushes it onto the remote window, so
  each remote pane's dims equal the corresponding local pane's dims.

There **is** a small reconcile path (see C2) — local-native interactions that can't round-trip
per-event. "No reconcile" from the pre-spike sketch is deleted.

## What the spikes settled (2026-07-17, next-3.8 rev 5350da0)

### Spike 1 — sizing. `select-layout` rescales; it does not crop.

Verified empirically on next-3.8: applying a remote layout string to an attached-client
window does **not** force the window to the remote dims — it **rescales the layout
proportionally to the local client** (remote pane 40/190 → local 50/210, window untouched).
The real problem is only that local pane dims then ≠ the remote dims the bytes were formatted
for.

**Fix (verified):** size flows local → remote. Daemon sets control-client size = local window,
pushes it onto the remote window; the remote layout string is then already at local dims, and
`select-layout` locally yields **exact** pane-dim convergence (both sides 50×52 / 159×52).
Renderers never call `refresh-client` — there is one control client, therefore one size, owned
by the daemon.

*Exclusive-mode consequence:* a human concurrently attached to the remote at a different size
is resized (degraded). Accepted for M2; shared/co-attach is out of scope.

### Spike 2 — interception. A session-option gate diverts structural verbs cleanly.

Verified faithfully (real `prefix c` injected into an attached client): a global
`if-shell -F '#{@bridge_ctl}'` binding diverts on a bridge session and runs the local action
on a normal session. **No dedicated key-table required.** Inspecting next-3.8's built-in
bindings yields a **two-class input model**:

| Class | Examples | Handling |
|---|---|---|
| **Discrete structural verbs** | `prefix c` / `%` / `,` / `x`, new-session, swap-pane | Gate → forward to daemon → remote command. Clean divert. |
| **Continuous / local-native** | border-drag resize (`resize-pane -M`), pane selection (`select-pane`), copy-mode, wheel-scroll | Run **local**, sync final state to remote via a hook (resize → `resize-pane`; focus → `select-pane`), with echo suppression. |

Two extra findings:
- **Mouse events for remote apps** (`send-keys -M`) must be forwarded to the **remote** pane,
  not the local painter — the daemon forwards SGR mouse sequences to the remote pane so
  mouse-aware remote apps (vim) work.
- The **right-click mega-menu** (`MouseDown3Pane`) is a large structural surface
  (split/swap/kill/resize/zoom). It must be gated or replaced in bridge sessions, or it acts
  locally.

## Design decisions folded in from review

- **C1 sizing** → directional authority above; exclusive mode; renderers never resize.
- **C2 reconcile** → keep remote-as-truth *preference*; add: (a) respawn-in-place on
  `pane-died` (`remain-on-exit on` + `respawn-pane -k`) so a dead renderer is restored without
  mutating remote structure; (b) echo suppression — tag intents, ignore the matching
  notification, to break the focus/active ping-pong; (c) a `@bridge_win 1` opt-out tag that
  `tmux-reflow-windows`, `tmux-reconcile-window`, `tmux-update-icons`, enrich, and **tmux-remux**
  all respect. tmux-remux must **not** resurrect bridge windows as orphan renderers on restore.
- **C3 copy-mode** → **pre-seed** history at renderer start (`capture-pane -e -p -S -<N>`
  printed through the pty before the screen seed), not on-demand (tmux has no
  inject-into-scrollback primitive). Define re-seed behavior after `%continue` to avoid
  duplicated history.
- **I1 firehose** → `%pause` *discards* output; `%continue` mandates a fresh `capture-pane`
  re-seed of that pane. Daemon→renderer pipes are non-blocking (drop + mark-dirty) so one
  wedged renderer can't stall the control stream.
- **I2 renderer** → not "M1 verbatim". The seed handshake (daemon marks stream position →
  `capture-pane` → forwards from mark) and the daemon↔renderer protocol (seed blob, byte
  stream, size updates, input upstream, pause/continue-dirty) are specced work. Renderer becomes
  a dumb painter + stdin forwarder.
- **I4 daemon lifecycle** → daemon lives **outside** the panes it manages (detached process /
  systemd user unit, not a pane in `<host>-<sess>`). Renderers spawned by **absolute store
  path** (pane PATH is stale until server restart; also pins daemon/renderer protocol
  versions). Renderers exit on pipe EOF so panes fold. ssh keepalive
  (`ServerAliveInterval`) so a half-dead link is detected. **Reconnect-after-drop is a
  non-goal for M2** (laptop sleep drops the bridge; re-open manually).
- **I5 notifications** → **drop `refresh-client -B`**; `%window-add/close/renamed`,
  `%window-pane-changed`, `%session-window-changed` are native/unconditional. Route clipboard
  via native `%paste-buffer-changed` → `show-buffer` → local `load-buffer`/OSC 52 (fold into
  the M2.4 empirical probe rather than sniffing OSC 52 out of `%output`).

## Milestones

Each slice is independently shippable and testable; each de-risks one hard thing before the
next depends on it.

- **M2.1 — daemon + one window, multi-pane render.** Daemon owns the single connection;
  mirrors **one** remote window's panes into a native local multi-pane window (layout
  translation, size local→remote convergence); per-pane renderers fed by the daemon; live
  `%layout-change`. Like M1, it **filters the session-wide `%output` firehose to the target
  window** and accepts the transit cost — `pause-after` backpressure is deferred to M2.2 (see
  below). **No structural input yet** — splits made *on the remote* appear locally; you type
  and navigate. **First acceptance case: a 1-pane window must be byte-identical to M1
  behavior** (regression anchor). Proves daemon↔renderer IPC, `%output` demux, layout
  translation, pane-dim convergence.
- **M2.2 — all windows mirrored.** One local window per remote window; native notifications
  drive live window add / close / rename / active-changed. Local navigation. Now that every
  pane streams, add `pause-after` flow control with the mandatory `%continue` re-seed (I1).
- **M2.3 — structural input parity.** The keybind translation layer: gate → daemon → remote
  for `prefix c/%/,/x`, resize, swap; input routing follows local focus; local focus change →
  remote `select-pane` (echo-suppressed). Continuous interactions (border-drag) run local, sync
  final size to remote.
- **M2.4 — rich passthrough.** Copy-mode (local, pre-seeded); mouse (select/resize/scroll on
  mirrored structure + `send-keys -M` forwarding to remote apps; gate/replace the right-click
  mega-menu); clipboard via `%paste-buffer-changed`; focus events. OSC 52 / focus-forwarding
  resolved by an empirical next-3.8 probe.

## Package layout (from day one, so M2.1 → M2.4 isn't a rewrite)

```
picker/remotebridge/
  controlmode/   # M1: parser, encoder, reply Reader (unchanged core)
  daemon/        # NEW: connection owner, mirror engine, %output router, control socket, seed handshake
  render/        # M1 render, refactored: dumb painter + stdin forwarder fed by daemon
  main.go        # M1 single-pane bridge (kept as the renderer entrypoint)
```

## Testing

- **Unit (Go):** mirror-engine notification→command translation; `%output` demux routing;
  layout-string → local `select-layout` convergence (table-driven with recorded layout
  strings); `%pause`/`%continue` re-seed logic; echo-suppression tag matching.
- **Local integration (no ssh, in `nix flake check`):** daemon against a local second tmux
  `-C attach`, mirroring a throwaway multi-pane window into local panes; assert pane-dim
  convergence and live `%layout-change` reflection (follows the M1 integration-test precedent).
- **Manual:** g5 → tp-g6 over the personal tailnet — the vim/alt-screen acid test per pane,
  then a remote split appears locally (M2.1), then drive structure from local keybinds (M2.3).

## Non-goals (M2)

- Reconnect after link drop (laptop sleep).
- Shared/co-attach with a live human on the remote at a different size (exclusive mode only).
- kitty graphics / images (remote tmux consumes the DCS passthrough — as in M1).
- Picker integration and retiring arch-C (that is M3).

## Open questions (resolved during implementation)

- OSC 52 clipboard + focus-event duplication on next-3.8 — empirical probe in M2.4.
- Exact echo-suppression scheme (sequence tag vs compare-before-set) — settle in M2.3.
- Right-click mega-menu: gate the whole `MouseDown3Pane` binding vs ship a bridge-specific
  replacement menu — settle in M2.4.
