# Bridge Ctrl+R Key-Mode Leak Fix

**Issue:** [#338](https://github.com/noamsto/lazytmux/issues/338) — Ctrl+R (fish's history pager) does
nothing in remote-bridge mirror windows.

## Prior investigation (do not repeat)

A previous pass (see the issue's first comment) conclusively ruled out the *local* encoding
path: kitty emits plain `0x12` for Ctrl+R, tmux never requests the Kitty Keyboard Protocol,
fish only ever requests xterm `modifyOtherKeys` mode 1 (which preserves legacy encoding for
`'r'`), and a PTY harness against the real pinned tmux + real fish + this repo's config opens
the history pager every time. The reporter then narrowed scope: the bug is **remote-bridge-only**
— ordinary local windows are unaffected.

## Root cause

`picker/remotebridge/render/renderer.go`'s `Run` writes every `FrameSeed`/`FrameOutput` payload
straight to the mirror pane's own stdout (`out.Write(f.Payload)`) — a mirrored remote pane's raw
`%output` bytes land verbatim on the *local* tmux pane hosting the renderer. `%output` is not
filtered for terminal-mode-negotiation sequences (only kitty-graphics/sixel are, in
`picker/remotebridge/graphics`), so if the remote pane's own occupant writes an xterm
`modifyOtherKeys` request to negotiate *its own* keyboard encoding — fish sends mode 1 at
startup (safe: input-keys.c's mode-1 encoding still falls back to legacy VT10x for `'r'`), but
Claude Code and other agent CLIs send mode 2, which unconditionally extends *every* key,
Ctrl+R included — that escape sequence reaches the local mirror pane's pty.

Local tmux's CSI dispatch table (`input.c`: `{'m', ">", INPUT_CSI_MODSET}`,
`{'n', ">", INPUT_CSI_MODOFF}`) parses `\x1b[>4;2m` from *any* pane's own output as that pane
requesting extended-key reporting for itself — it cannot distinguish "the renderer asked for
this" from "this byte sequence was mirrored through from somewhere else." The mirror pane's
`pane_key_mode` therefore flips to `Ext 2` and stays there for the pane's whole life (nothing
ever resets it — the renderer never emits its own negotiation, and only a fresh `respawn-pane`
would reset the screen's mode state). Once in `Ext 2`, every subsequent physical keypress typed
into that mirror pane — including Ctrl+R — is re-encoded by local tmux as a multi-byte CSI-u
sequence (`\x1b[114;5u`) before it reaches the renderer's stdin. The renderer forwards this
byte-transparently (as designed — see #242 structural input parity), and
`controlmode.SendKeysArgs` hex-encodes each byte as a *separate* `send-keys -H` keystroke, so
the remote shell receives eight unrelated characters instead of one Ctrl+R. Silent no-op —
exactly the reported symptom.

### Verified, not theorized

Built the pinned `next-3.8` tmux (`nix build .#default`) and used a Python PTY harness
(`pty.openpty()` + a real `tmux attach` client process, bypassing `send-keys` which injects an
internal key representation and never exercises the encode path) to inject a raw `0x12` byte
exactly as a physical keypress would arrive:

- A pane whose *own output* carries `\x1b[>4;2m` (simulating the leaked mirror bytes) flips to
  `pane_key_mode = Ext 2`, confirmed via `#{pane_key_mode}`.
- With the pane in that state, injecting raw `0x12` through the client pty delivers
  `\x1b[114;5u` to the pane's foreground process (captured via `cat -v` in `stty raw -echo`
  mode) — not `0x12`. This is the mechanism, measured end to end.
- After the fix (the leaked bytes piped through the real `keyneg.Filter` before reaching the
  pane, exactly as `outputSink.start` now does), the same pane stays at `pane_key_mode = VT10x`
  and the same injected keypress arrives as raw `^R` (`0x12`).

## This is a class-wide bug, not Ctrl+R-specific — surveyed, not assumed

The dispatcher flagged mid-investigation that a fix should cover the *class* of modifier+key
combinations dying in a remote mirror, not just the reported Ctrl+R case, and asked for the
blast radius to be surveyed empirically while the harness was already instrumented.

tmux's own encode path confirms this is inherently class-wide, not per-key: `input-keys.c`'s
`input_key` only reaches the `EXTENDED_KEY_MODES` switch (lines 690-708) for a key carrying
modifier flags — a plain unmodified key returns earlier via the "trivial case" (line 632).
Inside that switch, `MODE_KEYS_EXTENDED_2` unconditionally routes *every* modified key through
`input_key_extended()` (line 696) — there is no per-key allowlist on tmux's side either. So
contaminating a pane's key mode breaks Ctrl+anything, Alt/Meta+anything, and any other modified
key uniformly, and a fix that prevents the contamination (rather than mapping specific keys back)
inherently covers the whole class.

Confirmed empirically with the same PTY harness (real injected keypress bytes, not `send-keys`)
across a spread of modifier classes — Ctrl-letters, Meta/Alt — comparing baseline (clean pane),
contaminated (unfiltered mode-2 leak), and fixed (leak piped through the real `keyneg.Filter`):

| Key | sent (physical bytes) | contaminated arrival | fixed arrival |
|-----|------------------------|------------------------|-----------------|
| Ctrl+R | `\x12` | `\x1b[114;5u` | `\x12` |
| Ctrl+A | `\x01` | `\x1b[97;5u` | `\x01` |
| Ctrl+W | `\x17` | `\x1b[119;5u` | `\x17` |
| Alt+b (Meta) | `\x1bb` | `\x1b[98;3u` | `\x1bb` |

All four are broken identically by the contamination and all four are restored identically by
the fix — confirming the fix is a transparency fix (it stops the pane from ever entering extended
mode, categorically) rather than a per-key mapping. Not surveyed: function keys and shifted
specials (`Shift+Tab`, `F5`, …) — these already travel as multi-byte CSI sequences in legacy mode
too, and `input_key_extended`'s uniform routing (cited above) applies to them the same way, but
this session did not get a clean empirical capture for that subclass before time ran out.

## Fix

Added `picker/remotebridge/keyneg`, a small package (mirroring the existing
`picker/remotebridge/graphics` filter's shape) that strips `CSI > <params> m` /
`CSI > <params> n` (modifyOtherKeys set/reset) sequences from a pane's `%output` stream, holding
a sequence split across frame boundaries the same way `graphics.Scanner` does. It leaves every
other escape sequence — including other private `CSI >` forms like DA2 (`c`) or XDA (`q`) —
untouched.

Wired into `picker/remotebridge/daemon/daemon.go`'s `outputSink.start`, unconditionally for every
`FrameOutput` frame (unlike the graphics proxy, this does not depend on `NewGraphics` being
configured — the bug is unrelated to graphics/aeye). `drainOutput`'s batched buffer is filtered
as a whole before the (optional) graphics filter sees it, so a negotiation sequence landing in a
frame the batch pulls in after the first is still caught. A held partial sequence is flushed, not
dropped, when the sink closes.

This is a fix on the *output* (mirrored-bytes) direction, not the keystroke-forwarding direction
— it adds no keystroke mapping/allowlist table and does not touch `send-keys -H`'s byte-transparent
forwarding or `config/tmux.conf.nix`'s `extended-keys`/`extended-keys-format` lines (both remain
untouched, per the issue's constraint — `extended-keys` is a server-scoped tmux option with no
per-pane override, so it could not have been fixed there anyway).

## Testing

- `picker/remotebridge/keyneg/strip_test.go` — unit tests: strips set/reset forms, preserves
  unrelated `CSI >` sequences (DA2) and ordinary SGR, handles a sequence or its prefix split
  across `Feed` calls, releases an overlong unterminated hold, `Flush` semantics.
- `picker/remotebridge/daemon/keyneg_integration_test.go` — end-to-end through a real
  `outputSink` (the exact code path `daemon.go` runs in production): a negotiation sequence
  written via `outputSink.Write`, whole or split across two writes, never reaches the far end of
  the pipe; the surrounding pane output is untouched. Uses `gfx == nil` to prove the fix does not
  depend on graphics/aeye being wired in.
- Manual: no live remote (`tp-g6`) was reached this session, so the real keypress test asked for
  in the issue was not run against an actual ssh-bridged host. What *was* run and is not
  speculative: a real pinned-tmux server, a real attached client, and a real injected `0x12` byte
  through the client pty (not `send-keys`) — before the fix reproducing the exact broken
  `\x1b[114;5u` encoding, after the fix showing the correct raw `0x12` delivery — using the
  daemon's actual `keyneg.Filter` code, not a reimplementation. This exercises the identical tmux
  mechanism a real remote bridge session would hit; it does not exercise the ssh transport itself.
