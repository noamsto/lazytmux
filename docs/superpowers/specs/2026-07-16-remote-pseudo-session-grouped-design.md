# Remote pseudo-sessions via grouped sessions

**Issue:** #167
**Status:** design approved, pre-implementation
**Supersedes (long-term):** the arch-C reverse-socket promotion from #155

## Goal

Bring a remote tmux session into the local tmux server as **native local windows —
one per remote window — live-synced, with no visible inner status bar.** Switching
between remote windows uses the local (outer) prefix, exactly like local windows.

## Background

Arch-C promotion (#155) works but produces a **nested tmux**: the ssh pane is moved
into one local window, and inside it runs the remote tmux with its own status bar.
Remote windows are reachable only through the inner prefix. This was rejected in live
testing on 2026-07-16 as not feeling native.

A design review (Fable) compared two routes that are **visually identical** to the user:

1. **Control-mode bridge** — a Go program parses the remote tmux `-CC` stream and paints
   each pane itself, forwarding keys via `send-keys -H`. Truly un-nested, unified
   scrollback, independent sizing — but precedent-free (no tmux→tmux control client
   exists), multi-week, and high-risk (initial-state/alt-screen snapshot, the all-panes
   `%output` firehose, shared-size flip-flop). **Deferred.**
2. **Grouped sessions** (this design) — tmux does all rendering via its own attach path;
   the only custom code is a small notification watcher. ~half-day + a small binary.
   Still *technically* nested (chrome suppressed, not absent), but the nesting is invisible.

Grouped sessions were chosen: same user-visible result, a fraction of the effort and
risk. The bridge remains the fallback if the hidden nesting proves noticeable (M0 gate).

Both routes are **outbound-ssh only** — a strictly better trust model than arch-C's
reverse-forwarded socket (whose stale-socket `-R` binding and root-on-trustedHost
exposure we debugged in #155). This feature subsumes the arch-C listener/shim long-term.

## Architecture

### Grouped session per remote window

A tmux *grouped session* (`new-session -t <target>`) shares the target session's window
list but keeps its **own current window and its own session options**. We exploit both:

For a remote host `<host>` and session `<sess>`, build a local session `<host>-<sess>`
(reusing arch-C's naming so later picker/promote work lands in existing structure). For
each remote window index `i`, create one local window whose single pane runs:

```
ssh -T -e none <host> -- <abs-tmux> \
    new-session -A -s lztmux-mir-<sess>-<i> -t <sess> \; \
    set-option status off \; \
    set-option prefix None \; \
    set-option destroy-unattached on \; \
    select-window -t <sess>:<i>
```

- `-T -e none` — no remote pty on the ssh control channel and no `~` escape char, so bytes
  pass through cleanly (a pty mangles CR/LF and adds echo).
- `<abs-tmux>` — absolute path to the remote tmux; the remote `tmux` is not reliably on the
  non-interactive ssh PATH on NixOS (a PATH gotcha hit twice in #155). Resolve via the
  arch-C shim's knowledge or a fixed profile path.
- `new-session -A -s lztmux-mir-<sess>-<i> -t <sess>` — attach-or-create a grouped mirror
  session sharing `<sess>`'s windows.
- `set-option` **without `-g`** sets options on the current (mirror) session only, so
  `status off` and `prefix None` apply to the mirror and **never** to the g6 human's
  `<sess>`. This is the load-bearing property.
- `prefix None` makes the mirror tmux transparent to keys: the outer local tmux owns the
  prefix, and everything else passes through to the programs.
- `destroy-unattached on` — when the local window's ssh drops, the mirror session is
  destroyed (the human's `<sess>` is untouched).
- `select-window` pins this mirror to remote window `i`.

Result: N native local windows, one per remote window, one (outer) status bar, outer prefix.

### Live sync watcher

`lztmux-remote-watch` — a small Go binary (third main in the `picker/` module, alongside
picker + splash) — holds **one** ssh connection running the remote tmux in control mode,
attached notification-only:

```
ssh -T -e none <host> -- <abs-tmux> -C attach-session -t <sess> -f no-output
```

`-f no-output` suppresses the `%output` data firehose (control clients otherwise receive
output for every pane of the attached session), leaving only structural notifications. The
watcher reacts to:

- `%window-add <@id>` → spawn a new local window running the grouped-attach for the new index.
- `%window-close <@id>` → kill the corresponding local window.
- `%window-renamed <@id> <name>` → rename the local window.

`%unlinked-window-*` (windows in *other* sessions on the remote server) are filtered out.
Command replies are framed in `%begin`/`%end`/`%error`; the parser correlates by block and
surfaces `%error`. Notifications do not interleave inside a `%begin`/`%end` block (verify
once on next-3.8; load-bearing for a line-oriented state machine).

### Naming

- Local session: `<host>-<sess>` (e.g. `g6-work`).
- Local windows: the remote window name (mirrors rename via the watcher).
- Mirror sessions on the remote: `lztmux-mir-<sess>-<i>` (namespaced so they never collide
  with the human's sessions and are trivially GC-able).

## Milestones

### M0 — spike gate (no code, ~30 min)

Hand-verify the core assumption before building anything: ssh in, `tmux new -A -s probe -t
<sess>`, `set status off`, `set prefix None`, `select-window`, and eyeball that it feels
native — no inner bar, outer prefix works, switching is seamless. **If the hidden nesting is
noticeable, stop and reconsider the control-mode bridge.** This is the go/no-go gate.

### M1 — `lztmux-remote-open <host> [<sess>]`

Snapshot the *current* remote windows into a local `<host>-<sess>` session: query remote
windows (`list-windows -t <sess> -F ...`), create one local window per remote window running
the grouped-attach command, switch the client to the new session. No live sync yet (remote
window changes require re-open). Clean teardown: closing local windows destroys the mirror
sessions; the human's `<sess>` survives.

Acceptance: from a local pane, `lztmux-remote-open g6 work` yields a local `g6-work` session
whose windows mirror `work`'s windows, each showing the live remote content, navigable with
the outer prefix, no inner bar.

### M2 — live watcher

Add `lztmux-remote-watch`; wire M1 to launch it. Remote window add/close/rename is reflected
in the local session live. Watcher exits when the local session is destroyed.

### M3 — integration

- `prefix + s` session picker gains a **remote** section/toggle: list configured remote
  hosts' sessions; selecting one runs `lztmux-remote-open`.
- Retire arch-C's reverse-socket promotion in favor of this outbound-only path (remove the
  listener service, shim, and nixosModule sshd bits once parity is reached).

## Error handling & teardown

- ssh drop / connection EOF → the affected local window's pane exits (mirror session
  destroyed via `destroy-unattached on`); the watcher reconnects or the session is torn down.
- Remote `<sess>` destroyed → all mirrors drop; local `<host>-<sess>` is cleaned up.
- Remote tmux absent / wrong PATH → surfaced as a clear pane message, not a silent blank.
- `%error` from the watcher's control connection → logged and surfaced.

## Known limitations (documented, deferred)

- **Remote copy-mode / scrollback**: `prefix None` makes the mirror transparent, so the
  remote tmux's copy-mode is not reachable by prefix. A dedicated outer keybinding to enter
  the remote copy-mode (targeting the mirror session) is deferred past M2.
- **Shared window size**: grouped mirrors participate in the remote window-size negotiation,
  so a local status reflow (2–4 lines) could resize a concurrently-attached g6 human's
  window. **Exclusive-attach is the supported case** for now; `attach -f ignore-size` (mirror
  drops out of size math, letterboxes on mismatch) is the later refinement, with
  `window-size manual` + `resize-window` as the escape hatch.
- **kitty graphics / images** inside mirrored panes will not render: the DCS passthrough
  (`\ePtmux;…`) is consumed by the remote tmux, which is still in the path.

## Testing

- **Unit** (Go): control-mode line parser, `%begin/%end/%error` block correlation,
  notification filtering (`%unlinked-*` dropped), window-index → local-window mapping.
- **Local integration** (no ssh, in `nix flake check`): the whole mechanism works against a
  *local* second tmux server — `lztmux-remote-open` with host resolving to a local socket,
  mirroring one throwaway local session's windows into another. Follows the live-tmux
  integration-test precedent from #155 (`tests/remote-integration.bats`).
- **Golden transcripts**: record real `-C` sessions (including a vim/alt-screen attach and a
  fast-output flood) and replay through the parser to pin escaping/interleaving against
  future tmux bumps (we track `next-3.8`, a moving target).
- **Manual**: g5 → g6 over the personal tailnet.

## Security / trust

Outbound ssh only; no listening socket, no reverse forward, no sshd changes on the remote.
The only remote state is the namespaced `lztmux-mir-*` grouped sessions, GC'd on detach.
This is strictly less exposed than arch-C and is the intended replacement for it.

## Open questions

- Remote host/session discovery for M3's picker: a configured `remote.hosts` list, or probe
  known ssh hosts? (Defer to M3.)
- Should `lztmux-remote-open` with no `<sess>` pick the remote's most-recent session, or
  prompt? (M1: default to most-recent; picker handles choice in M3.)
