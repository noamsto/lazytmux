# Remote pseudo-sessions via a tmux control-mode bridge

**Issue:** #167
**Status:** approach approved (post-spike pivot), pre-implementation
**Supersedes:** the grouped-session approach (rejected — see below); long-term the arch-C reverse-socket promotion (#155)

## Goal

Native local windows (one per remote window), live-synced, no inner status bar —
by consuming the remote tmux's **control-mode (`-CC`) stream** and projecting it into
the local tmux. tmux is out of the remote-rendering path: a bridge paints each pane and
forwards input.

## Why the bridge (and why the cheaper paths were rejected)

- **arch-C promotion (#155):** produces a nested tmux (inner status bar, inner prefix).
  Rejected in live testing.
- **Grouped / `link-window` session-sharing (rejected after an M0 spike, 2026-07-16):**
  on the live g5→g6 lazytmux environment, setting chrome-suppression options
  (`status off` / `prefix None`) on a mirror session that **shares a window** with the
  human's session **propagates those values to the human's session** — killing the
  human's real status bar and prefix. This is undocumented (the tmux manual says grouped
  session options are independent, and a config-free test agreed), **inconsistent** (a
  manual `set` bled; reflow's `set` did not), and **order-dependent** (chrome-off-before-link
  avoids the bleed, but then lazytmux's reflow hook resets the mirror's `status` to `2`,
  restoring the inner bar). Building on an undocumented, order-dependent quirk that
  corrupts the live session — on a tmux tracked at a moving `next-3.8` — is building on
  sand. Fable predicted this ("prototype before locking; record why"); the spike rejected it.
- **Control-mode bridge (chosen):** **zero blast radius** — never creates remote sessions,
  never shares windows, never sets remote options. It only *reads* the `-CC` stream and
  sends `send-keys`/`refresh-client`. The entire class of failure above cannot occur.

## Architecture (Milestone 1)

**Component:** `lztmux-remote-bridge` — a Go binary (third main in the `picker/` module,
alongside picker + splash). It runs **inside a fresh local tmux pane**: its **stdin** =
local keystrokes, **stdout** = the pane's display.

It wraps one control-mode connection:
```
ssh -T -e none <host> -- env TMUX_TMPDIR=<remote-runtime-dir> <abs-tmux> \
    -C attach-session -t <sess>
```
- `-T -e none` — no remote pty on the control channel, no `~` escape (a pty mangles CR/LF
  and adds echo).
- `env TMUX_TMPDIR=<runtime>` + `<abs-tmux>` — the remote tmux is not reliably on the
  non-interactive ssh PATH, and lazytmux servers live under `$XDG_RUNTIME_DIR`, not
  `/tmp/tmux-$UID` (both gotchas confirmed repeatedly during #155 and this spike).

**Data flow (single window / active pane for M1):**
| Direction | Mechanism |
|-----------|-----------|
| Render | parse control lines; for `%output %<pane> <data>` → **byte-oriented** octal unescape (`\NNN`→byte; multibyte UTF-8 may split across `%output` lines) → write raw bytes to stdout |
| Input | read stdin bytes → `send-keys -H -t %<pane> <hex...>`, chunked under the command-size cap |
| Resize | debounced SIGWINCH → `refresh-client -C WxH` |
| Teardown | `%window-close`/`%exit`/EOF → bridge exits, pane closes; killing the pane drops ssh (remote session untouched) |

**Initial-state snapshot** (control mode has NO scrollback replay — the acid test is
attaching to a pane running vim): attach → buffer target-pane `%output` → issue
`capture-pane -e -p` → because command replies and `%output` serialize through the remote
server's single event loop, everything before the capture is inside the snapshot and
everything after arrives after `%end` → apply snapshot (content + cursor CUP + terminal
mode flags — `#{alternate_on}`, `#{cursor_flag}`, `#{wrap_flag}`, `#{mouse_any_flag}`,
`#{keypad_*}` — re-emitted as mode-set sequences) → stream from there. No dedup heuristics.

**Terminal discipline:** the bridge puts its pane stdin into raw mode (echo off; ISIG off
so `Ctrl-C` forwards as a byte, not a signal), restores on every exit path incl. SIGHUP.
The **outer tmux is the terminal-state keeper** — it parses DECCKM/alt-screen/mouse modes
from the byte stream and re-encodes local keys — which is what makes this viable; the
snapshot replay covers modes set *before* attach that the outer tmux can't observe.

**Command-reply framing:** every command produces a `%begin`/`%end`|`%error` block;
correlate by block number (the initial attach emits an empty block + `%session-changed`
before any output — don't treat it as your reply). Surface `%error` (e.g. target pane
died) to the pane before exiting. `%unlinked-*` notifications (other sessions) are filtered.

**Package split from day one** (so M1 → M2 is not a rewrite): `controlmode` (line
parser / encoder / reply correlation), `renderer` (raw tty, snapshot applier, byte pump),
`cmd` (wiring). The renderer half is the future per-pane renderer verbatim; the controlmode
half becomes the future daemon. M1 is one in-pane process — **not** the daemon.

**Naming:** the bridge window lives inside a local `<host>-<sess>` session (reusing
arch-C's naming so the M3 picker lands in existing structure).

## Milestones

- **M1 — `lztmux-remote-open <host> [<sess>]`:** one remote window rendered live as one
  native local window (output + input + resize + initial snapshot). Standalone command;
  not yet wired to picker or arch-C. Acceptance: attach a shell **and** a vim/alt-screen
  session and drive both correctly.
- **M2 — multi-window:** one control connection → many native local windows (daemon +
  per-pane renderers); `refresh-client -B` format subscriptions for live window
  add/close/rename; `pause-after` flow control for the all-panes `%output` firehose;
  layout translation for multi-pane windows.
- **M3 — integration:** `prefix + s` picker remote section; retire the arch-C reverse-socket
  promotion (this design subsumes it, outbound-ssh only).

## Known limitations (documented)

- **All-panes firehose:** `-CC` delivers `%output` for the whole attached session. M1
  filters to the target pane and accepts the transit cost (one window, bounded); M2 adds
  `pause-after` + `%continue`. (A scratch-session/`link-window` scoping is deliberately
  avoided — it would reintroduce remote-session footprint.)
- **Shared window size:** `refresh-client -C` sets the control client's size, but the
  remote window size negotiates across attached clients. M1 supports exclusive-attach;
  uses `attach -f ignore-size` so a concurrent human isn't resized; on remote > local
  mismatch (from `%layout-change` dims) it stops painting and shows a one-line notice
  instead of wrapped garbage. Debounce reflow-driven SIGWINCH.
- **kitty graphics / images** won't render — the DCS passthrough (`\ePtmux;…`) is consumed
  by the remote tmux. OSC 52 clipboard and focus-event duplication need an empirical check
  on next-3.8.

## Testing

- **Unit (Go):** byte-oriented octal unescape, hex encoder + chunking, control-line parser,
  `%begin/%end/%error` block correlation, `%unlinked-*` filtering.
- **Local integration (no ssh, in `nix flake check`):** run the bridge against a **local**
  second tmux `-C attach` — mirror one throwaway local session's window into a local pane
  (follows the #155 live-tmux integration-test precedent).
- **Golden transcripts:** record real `-C` sessions (a vim/alt-screen attach; a fast
  output flood) and replay through the parser to pin escaping/interleaving against future
  tmux bumps (we track `next-3.8`, a moving target).
- **Manual:** g5 → tp-g6 over the personal tailnet.

## Security / trust

Outbound ssh only; no listening socket, no reverse forward, no remote sessions, no remote
option changes. Strictly less exposed than arch-C, and the intended replacement for it.

## Open questions

- Exact set of terminal mode flags to replay in the snapshot — resolve during M1 (vim is
  the forcing function).
- `lztmux-remote-open` with no `<sess>`: default to the remote's most-recent session (M1);
  the picker handles explicit choice (M3).
