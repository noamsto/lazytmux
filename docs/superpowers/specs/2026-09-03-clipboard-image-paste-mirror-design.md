# Design spec: clipboard image paste into a mirror pane (#361)

## Problem

In a local pane, `ctrl+v` with an image on the clipboard attaches that image
to the agent's prompt (Claude Code shows `[Image #N]`). In a **remote-bridge
mirror pane** the same keypress does nothing useful: the image is on the
*local* clipboard, but the agent reading it runs on the *remote* host. The
bridge carries keystrokes and output, not clipboard payloads, so the remote
agent inspects a remote clipboard that is empty or absent (headless).

## Evidence (measured, not assumed)

The issue's open questions were settled by reading the Claude Code 2.1.258
bundle and by live experiments in a throwaway `tmux -L` server on this
machine.

### What a local `ctrl+v` image paste actually is

The keypress delivered to the agent's stdin is **one byte, `0x16`** — no
payload crosses stdin. On receiving it, Claude Code reads the *system
clipboard itself* (Linux: `xclip -selection clipboard -t TARGETS -o` /
`wl-paste -l` to probe for `image/(png|jpeg|jpg|gif|webp|bmp)`, then
`xclip -t image/png -o` / `wl-paste --type image/png` to extract), saves to a
temp file, converts BMP→PNG, and attaches the image in-memory as base64,
rendering `[Image #N]` in the prompt. Text content pastes as
`[Pasted text #N]` the same way (measured: 4-line clipboard →
`[Pasted text #1 +3 lines]`). An empty clipboard is a silent no-op.

**Consequence:** in a mirror pane the daemon forwards `0x16` verbatim
(`send-keys -H 16`), and the remote agent reads the *remote* clipboard. On a
headless remote both `xclip` and `wl-paste` are absent, so the probe fails
and nothing happens. The fix cannot be "make the remote read harder" — the
bytes must cross the bridge.

### What a remote injection can look like

Claude Code also attaches images whose **paths** appear in the prompt text:
at submit, existing paths matching `/\.(png|jpe?g|gif|webp)$/i` are resolved
as attachments (`inlinedImagePaths`). This is the drag-and-drop path, and it
was verified live twice:

- bare prompt `/tmp/paste-test.png` + Enter → `Read 1 file`, model described
  the image content correctly;
- mid-sentence `what colour is /tmp/paste-test2.png ? …` + Enter →
  `Read 1 file`, model answered "Blue" (correct).

So the remote half of the paste is: **write the image to a remote temp file
with a real image extension, then `send-keys -l` the path into the remote
pane.** The agent receives the image at submit — the same user-visible
outcome as a local paste, modulo the prompt showing a path instead of an
`[Image #N]` chip.

Note the regex excludes `bmp`: a BMP-only clipboard cannot ride the path
mechanism, and no BMP→PNG converter is guaranteed on either host. v1 reports
it as unsupported rather than converting.

### Which agents need this

- **Claude Code: yes** — the whole mechanism above, verified.
- **Codex: no** — the installed binary contains no clipboard strings at all
  (`strings | grep -iE 'clipboard|wl-paste|xclip'` is empty); it has no
  `ctrl+v` clipboard read to fix.
- **Cursor agent: unverified** — no evidence either way.

v1 gates interception on `pane_current_command == "claude"` (verified live:
that is what tmux reports for a CC pane, nix wrapper included). The gate is a
set, so another agent joins it once its path-inlining is verified the same
way.

## Design

The bridge daemon is the only component that sees both sides: it is a *local*
process (can read the local clipboard) that owns the ssh `ControlMaster` to
the remote (can ship bytes) and already sends every keystroke to the remote
pane (`pumpInput`). Interception lives there, in a new file
`picker/remotebridge/daemon/paste.go`; the graphics package is the opposite
direction (remote→local) and is not reused beyond the socket pattern.

### Flow

1. `pumpInput` scans each `FrameInput` payload for `0x16` **outside** a
   bracketed-paste bracket (`ESC[200~` … `ESC[201~` — a `0x16` inside pasted
   content is data, not the gesture; a tiny two-state scan defines that false
   positive out of existence).
2. A candidate `0x16` is gated on the pane: one `list-panes` fork reads
   `#{@bridge_pane}|#{@bridge_proc}` for the mirror session; if the pane's
   remote foreground command is not in the agent set, the byte is forwarded
   unchanged (readline quoted-insert in a shell keeps working).
3. For an agent pane, the handler probes the **local** clipboard (same
   xclip/wl-paste probe CC uses). **No image → the byte is forwarded
   unchanged** — text paste and every other `ctrl+v` meaning behave exactly
   as today.
4. Image present → the byte is swallowed and the rest runs **async**, off the
   input pump, so a slow transfer never freezes the pane:
   - extract the image bytes (png preferred; jpeg/gif/webp pass through with
     matching extension; bmp-only → visible "unsupported" message);
   - cap at 8 MiB (the graphics fetcher's `gfx-max-bytes` default);
   - ship over the daemon's own ssh `ControlMaster` socket —
     `ssh -S <sock> -T <host> -- sh -c '<store>' _ <ext>` with the bytes on
     stdin, bounded by a 5s `context.WithTimeout` reaching
     `exec.CommandContext` (async, so slightly longer than the fetcher's 2s
     is affordable; still bounded so a hung socket leaks nothing);
   - the remote store script sweeps the paste dir, writes `mktemp`
     0600, prints the path (details below);
   - the daemon validates the returned path against a strict regex, then
     injects it with `send-keys -H` (hex, via the existing
     `controlmode.SendKeysArgs`) — hex sidesteps every quoting layer, and
     the payload is the path **plus a trailing space**, so typing
     immediately after the paste cannot merge into the path token and
     silently break inlining.
5. Every failure after the swallow (transfer timeout, remote dir
   unwritable, oversize, malformed reply) surfaces as a local
   `display-message` on the mirror session's client — a visible no-op with a
   legible message, never a silently swallowed paste.

### Remote store script

Runs under `sh -c` (the remote login shell is fish; `graphics/fetch.go`'s
quoting pattern is followed exactly — the script is single-quoted as one
argv element, so fish never parses it):

```sh
d=/tmp/lazytmux-paste
umask 077
mkdir -p "$d" || exit 1
find "$d" -type f -mmin +60 -delete 2>/dev/null
f=$(mktemp "$d/img-XXXXXXXX.$1") || exit 1
cat > "$f" || { rm -f "$f"; exit 1; }
printf '%s' "$f"
```

- **Cleanup answer (issue question):** the janitor is the sweep on line 4 —
  every paste deletes sibling files older than 60 minutes. The file must
  outlive prompt editing (the path is read at *submit*, which can be minutes
  later), so delete-on-inject is wrong; a time-boxed sweep needs no daemon
  state and no extra round trip. `/tmp` on the remote is the right home:
  tmpfs, per-boot, self-cleaning.
- `ext` (`$1`) is validated daemon-side against `^(png|jpe?g|gif|webp)$`
  before it is ever interpolated.
- The returned path is validated against
  `^/tmp/lazytmux-paste/img-[A-Za-z0-9]+\.(png|jpe?g|gif|webp)$` before it
  reaches a `send-keys` command line.

### Seam and sibling boundaries

#487 is live in `daemon/daemon.go`'s seed/`wireRenderer` path and #468 in
`daemon/router.go`'s `Register` seam. This work stays out of both:

- all logic is new code in `daemon/paste.go`;
- `pumpInput` (bottom of `daemon.go`, input path — not the seed path) gains
  one parameter, a `*pasteHandler` (nil = forward everything, which is also
  what `--test-local` and tests pass); its two call sites change by one
  argument each;
- `cmd/daemon/main.go` builds the upload closure capturing `host` +
  `ctlSock`, exactly like the `NewGraphics` closure, and nils it when there
  is no ssh transport;
- `daemon.Config` gains one field (`PasteUpload`), mirroring `NewGraphics`.

### Wiring

`Config.PasteUpload func(ctx context.Context, ext string, data []byte)
(string, error)` — nil disables interception entirely. The handler
(`pasteHandler`) is built in `daemon.Run` from `cfg`: the clipboard probe and
`@bridge_proc` lookup are fields on the handler so unit tests inject fakes
and never touch ssh, tmux, or a clipboard.

### Clipboard tools on PATH

The daemon is spawned by `lztmux-remote-open` and inherits its PATH. The
probe tries `xclip` then `wl-paste` (CC's own order); a host with neither
cannot know whether the clipboard holds an image, so the byte is forwarded
unchanged — pre-#361 behaviour, which also preserves a *remote*-clipboard
paste on the rare bridged host that has one. No new nix dependency is
required for correctness; adding `wl-clipboard`/`xclip` to the daemon's
wrapped PATH is a packaging nicety the plan may take if the wrapper has a
natural seam.

## Failure matrix

| case | behaviour |
| --- | --- |
| `ctrl+v`, pane runs a shell | byte forwarded (quoted-insert untouched) |
| `ctrl+v`, agent pane, text/empty clipboard | byte forwarded (today's behaviour) |
| `ctrl+v`, agent pane, image ≤ 8 MiB | swallowed; path injected async |
| image is BMP-only | swallowed; "unsupported image format" message |
| image > 8 MiB | swallowed; "image too large" message |
| ssh/transfer timeout or error | swallowed; "upload failed" message |
| remote dir unwritable / bad reply | swallowed; legible message |
| daemon mid-reconnect (no socket) | swallowed; "bridge reconnecting" message |

"Swallowed + message" is deliberate: forwarding `0x16` after a *failed image
transfer* would make the remote agent report its own clipboard (empty),
which lies about what happened.

## Non-goals

- **Text clipboard shipping.** `ctrl+v` with text in a mirror pane is as
  broken today as image paste, and fixing it (bracketed-paste semantics,
  multi-line collapse) is a separate design. Forwarding the byte preserves
  today's behaviour exactly.
- **Codex/cursor-agent gating.** The gate set is a list; each addition needs
  the same path-inlining measurement done for CC here.
- **BMP conversion.** No guaranteed converter on either host; reported as
  unsupported instead.
- **`[Image #N]` chip rendering.** The prompt shows the path, not a chip;
  the chip is CC-internal state no external mechanism can set. The agent
  receives the same image bytes at submit.

## Testing

- `daemon/paste_test.go`: byte-scan (bare `0x16`, mid-chunk, inside
  bracketed paste, repeated), gating (agent vs shell vs unknown pane),
  no-image → forward, image → upload + hex `send-keys` of path+space, each
  failure row of the matrix → notify + no injection. All with injected
  fakes; no ssh, no tmux.
- `cmd/daemon` wiring: `PasteUpload` is nil under `--test-local` (no
  ControlMaster exists there), asserted by the existing offline harness
  continuing to pass.
- Manual: `tests/test-display.sh`-style pass — paste a real screenshot into
  a mirrored Claude pane.
