# Design — #320: real graphics in a bridged carousel on a non-kitty local terminal

## Problem

Through the remote bridge, a non-kitty local terminal gets block art in the
mirrored carousel even when it could paint real pixels. Two independent things
force that:

1. **lazytmux drops it.** `graphics.Scanner` recognises sixel (bare and
   passthrough-wrapped) purely so it can *discard* it (#319). A complete sixel
   is consumed and nothing is emitted.
2. **aeye refuses to emit it.** `chooseRelayBackend` (noamsto/aeye#186) returns
   `backendKitty` for a kitty/ghostty termname and `backendSymbols` for
   everything else — never a raster backend.

This spec covers **(1) only**. (2) is a different repository; the interface it
needs from us is pinned under *Cross-repo contract*.

## What was measured, not assumed

Every claim below was checked on this machine against the pinned tmux
(`next-3.8`, `flake.nix`'s `tmux-upstream` input) and the working tree.

| Claim | Evidence |
|---|---|
| A control client receives a pane's sixel **verbatim as `%output`** | scratch server, held-stdin `tmux -C attach`: `%output %0 \033Pq"1;1;16;8#0;2;100;0;0#0!16~-!16~\033\134` |
| A bare sixel written into a pane is **eaten by tmux's DCS parser** — it can never land as pane text | scratch server: `cat` of a 37-byte sixel left the pane clean, the following `AFTERSIXEL` intact |
| The pinned tmux **is built with sixel** | `strings` on `…-tmux-next-3.8/bin/tmux`: `sixel_parse_repeat`, `screen_write_sixelimage`, `tty_cmd_sixelimage`, `SIXEL IMAGE (%ux%u)` |
| tmux **positions and clips** a sixel to the pane | `tty.c:tty_cmd_sixelimage` calls `tty_clamp_area` then `tty_cursor(tty, x, y)` |
| tmux does **neither** for a passthrough | `tty.c:tty_cmd_rawstring` is `tty_add` + `tty_invalidate`, nothing else |
| tmux renders the image only for a client with the sixel capability, else a text placeholder | same function: `if ((~tty->term->flags & TERM_SIXEL) && !tty_term_has(tty->term, TTYC_SXL)) fallback = 1;` |
| `TERM_SIXEL` is set **only** by the `sixel` terminal-feature — there is no DA1 auto-detection | `tty-features.c`: `tty_feature_sixel` is the sole definer of `TERM_SIXEL`; no other assignment exists in the tree |
| tmux's **default** feature table enables `sixel` for **nobody** — not foot, not WezTerm, not iTerm2 | `tty-features.c:tty_default_features` table, read entry by entry |
| lazytmux sets no `sixel` feature either | `config/tmux.conf.nix` emits only `<term>*:RGB:extkeys`, `*:hyperlinks`, `xterm-kitty*:progressbar` |
| `#{client_termfeatures}` reports `sixel` once the feature is configured, and is **always empty for a control-mode client** | scratch attach → `FEATS=[bpaste,focus,RGB,sixel,title]`; this repo's live server → five `ctrl=1` clients, all `feats=[]` |
| aeye's raster path emits chafa output **bare**, never `tmuxPassthrough` | aeye `gallery_raster.go:paintRasterAt` writes `\x1b7 CUP <payload> \x1b8` straight to the tty |
| aeye picks OSC 1337 **only outside tmux** | `chooseGridBackend`'s `if !inTmux` guard around both `formatITerm` branches |
| **A real chafa sixel is megabytes, not kilobytes** | `chafa -f sixels` on a 1200×800 PNG: `40x20` → 478 KB, `80x24` → 1.39 MB, `120x40` → 3.10 MB, `240x60` → 7.64 MB |
| A `keyneg` region walk forwards a sixel untouched today | `keyneg/strip.go` `classifyDCS` → `vRegion` → `walkRegion`, which forwards verbatim and re-arms on `maxRegion` overflow |
| PR #550 is **already merged** (commit `b648e7a`), not in flight | `git merge-base --is-ancestor b648e7a HEAD` |
| **A partial sixel that overflows the budget leaks its TAIL as pane text today** | scratch Go test against `graphics`: after `\x1bPq` + `maxPartial+1` bytes, the next `Feed` emitted `"MOREBODY~~~~\x1b\\"` as `Literal`. The overflow branch drops the held prefix and leaves `s.held` nil, so the continuation — which contains no ESC — is literal by construction. |
| `set-environment -t <sess>` over the control stream is session-scoped and inherited by later panes | scratch server: sent over `tmux -C attach`; `show-environment -t s1` → `sixel`, `-t s2` → `unknown variable`, a window created after → `GOT=sixel` |
| `\x1b]1337;File=` is **12** bytes | `len()` of the literal |

## The correction this forces on the issue's plan

Issue #320 proposes re-wrapping the relayed sequence in a `\ePtmux;`
passthrough "so the local tmux forwards it to the outer sixel-capable
terminal". **That is the wrong mechanism for sixel, and it is not needed.**

The local renderer pane is a tmux pane. tmux *itself* parses sixel, stores the
image in the pane's screen, clamps it to the pane, positions it at the pane's
cursor, and re-emits it to any client whose terminal carries the sixel
capability. That is how sixel works in a normal unbridged tmux session.

The two `tty.c` functions in the evidence table are the whole argument:
`tty_cmd_sixelimage` clamps and positions; `tty_cmd_rawstring` — the
passthrough sink — does neither. Relaying sixel therefore means **forwarding
the bytes unchanged**, and all the design work is in *when* and *how safely*,
never in *how to wrap*.

`allow-passthrough` on both ends is untouched: it is the kitty path's
requirement, and this change neither uses nor weakens it.

## Why OSC 1337 cannot be relayed, and why nothing is lost

The dispatcher asked for both protocols at equal priority. Sixel is delivered.
OSC 1337 is **structurally impossible** to relay correctly, on source-level
evidence rather than preference:

- tmux has no inline-image handling at all, so the only route out of a pane is a
  passthrough wrapper.
- `tty_cmd_rawstring` performs no `tty_cursor` and no `tty_clamp_area`. A relayed
  OSC 1337 image lands wherever tmux last left the *real* cursor, unclipped by
  the pane, and unknown to tmux — so the next redraw destroys it.
- A mirror pane is *always* inside tmux, so there is no configuration in which
  this comes out right. aeye already encodes the same conclusion: `formatITerm`
  is reachable only under `if !inTmux`.

**No user-facing capability is lost.** foot, WezTerm and iTerm2 all speak sixel,
so the sixel path covers the entire named set; OSC 1337 would only ever have
been a second, broken route to terminals the first route already reaches. The
gate in R1 is capability-driven rather than a termname list, so this holds
without depending on anyone's recollection of which terminal speaks what.

What OSC 1337 *does* get here is the #319 **safety** half (R3) — a deliberate
scope addition, argued as such in that requirement rather than presented as free.

## Requirements

**R1 — Relay a complete sixel when, and only when, the local terminal can paint
one.** Forward the bytes unchanged.

- The condition is **exactly** tmux's own render condition: the local client
  carries the `sixel` terminal-feature. Anything narrower relays images tmux
  will only draw as `SIXEL IMAGE (WxH)` placeholder text, which is the #319
  symptom; anything wider is unsound.
- The signal is read from the **invoking client**, targeted by `$TMUX_PANE`
  (`display-message -p -t "$TMUX_PANE"`), not a bare `display-message`. A bare
  one resolves to the server's most-recently-used session and is wrong on any
  server with more than one attached client — the bug
  `scripts/lztmux-remote-open.sh` already documents at its `COLORTERM` read and
  still has at its `client_termname` read one screen above.
- Because tmux enables `sixel` for **no terminal by default** (evidence table),
  the change must also provide the declaration: a way to say "my terminal does
  sixel" that emits `set -as terminal-features '<pattern>*:sixel'`. It must be
  usable **without an emulator preset**: `terminalTerm` is `null` unless a
  preset is active, and the presets that exist are kitty/ghostty — neither of
  which is a sixel user — so a declaration that can only attach to
  `terminalTerm` is inert for exactly the foot/WezTerm/iTerm2 users this change
  is for. The term pattern must be declarable on its own. `terminal.term`
  already exists as the manual escape hatch, so pairing the sixel flag with it
  is the natural shape — but then an unset `terminal.term` recreates the same
  inertness one level down, so whichever shape is chosen must make the
  "no preset, no `terminal.term`" case either work or fail loudly.
- **Relay-off must be observable, at a stated place and rate.** When sixel is
  dropped because the client has no sixel feature, the proxy logs it **once per
  pane**, not once per sequence — at multi-MB repaint rates a per-sequence line
  is spam. It lands on the daemon's stderr, which the launcher redirects to
  `${sock}.log`; the PR must name that path, since a user cannot be expected to
  know it.

**R2 — Preserve the #319 invariant absolutely.** A stray or truncated sequence
must never reach the mirror pane as text.

- Partial handling is **unconditional**: a partial sequence is dropped on
  hold-budget overflow and on `Flush` whether relaying is on or off. Policy
  governs complete sequences only.
- Precisely: only a sequence the scanner has **seen a terminator for** is ever
  forwarded. That is not an integrity guarantee — a sink frame dropped
  mid-image leaves a corrupt body followed by a real terminator, which the
  scanner will call complete. That stays *inside* the invariant (tmux's DCS
  parser eats it; nothing lands as text) and must not be described as more.

**R10 — Fix the overflow tail leak.** *(Split out of R2 because it is a bug fix,
not a property to preserve — the current code does not have this property.)*

The overflow branch drops the held prefix and leaves nothing behind, so the
sequence's **continuation** — which contains no ESC — is emitted as `Literal`
on the next `Feed` and painted as text. Measured: 478 KB is the *smallest*
real chafa sixel and the budget is 64 KiB, so this fires on every real image,
not on a corner case. #319's protection is therefore already broken for real
payloads; only the absence of any sixel sender over the bridge hides it.

- On overflow of a raster sequence the scanner must enter a **discard** state,
  consuming the remainder of the corrupt sequence rather than emitting it.
- **The exit must be reachable in the case that caused the overflow.** The event
  that overflows the budget is a dropped sink frame — the same event that can
  carry away the terminator. So "discard until the terminator" is itself the
  latch this requirement forbids: with no ST in the stream it swallows the
  pane's whole output until some unrelated `\e\\` wanders past. A second bound
  is mandatory, and `keyneg` already needs one for the same reason
  (`strip.go`'s `maxRegion` re-arm: *"The terminator may have gone with a
  dropped frame … latching it on would disable the strip for this pane's life"*).
- **The second bound is the next ESC, not a byte count.** Discard until the next
  `\x1b`, which re-arms normal scanning; if that ESC is the sequence's own ST,
  it is consumed as the terminator. A sixel body contains no ESC (its bytes are
  `?`–`~`, `#`, `!`, `-`, `$`, digits and `;`), so the discard runs to exactly
  the end of the corrupt image and can never swallow a later escape sequence —
  self-limiting, with no constant to tune. A byte-budget re-arm was rejected
  because re-arming into *forwarding* drops straight back into the leak, making
  it a bounded leak rather than a fix. The accepted cost is that plain text
  between the corrupt image and the next ESC is discarded with it; that is
  bounded by the next escape sequence and is strictly better than painting
  megabytes of sixel body as text.
- **The wrapped form leaks identically and must be covered.** `isPartialSixel`
  already recognises a `\ePtmux;`-wrapped partial, and its tail leaks the same
  way. The scan differs — the outer terminator is a lone `\e\\` while payload
  ESCs are doubled — so an implementer closing only the bare case leaves half
  the bug.
- **The bats test must use a payload larger than the non-relay budget.** A test
  with a small sixel passes against the current code and proves nothing about
  this.

**R3 — Recognise `OSC 1337;File=` and drop it, whole or partial.** A deliberate
scope addition beyond what #320 needs, justified because the hazard becomes live
the moment the aeye follow-up enables a raster backend on the relay path, and
because it is the same defect class as R10.

- Scoped to the **inline-image verb only** (`\x1b]1337;File=`). The OSC 1337
  namespace also carries iTerm2/WezTerm shell integration — `CurrentDir=`,
  `SetUserVar=`, `RemoteHost=` — which a remote shell emits routinely. Those
  keep their present forward-verbatim behaviour.
- **An OSC has four exits: ST, BEL, CAN/SUB, and a bare ESC.** `keyneg` is the
  in-repo statement of this — `regionAPC` is documented as "ST, CAN, SUB or a
  bare ESC" and `regionOSC` as "as APC, plus BEL" — and tmux's
  `INPUT_STATE_ANYWHERE` maps all of them out of an OSC while the DCS handler
  deliberately does not, so sixel correctly ends on ST alone. A hold waiting for
  ST alone would sit through a BEL-terminated, cancelled, or ESC-interrupted
  sequence and swallow the following pane output up to the budget — destroying
  real text, strictly worse than today. The bare-ESC case is reachable exactly
  where this requirement operates: a dropped frame splicing the head of another
  escape sequence into a truncated `File=`.
- **Consumption differs by exit, and must follow `keyneg`'s classification.** ST
  and BEL are terminators and are consumed with the sequence. CAN/SUB are aborts
  and are consumed. A bare ESC is **not** consumed — it introduces whatever comes
  next, and swallowing it would destroy the following sequence.
- **The discriminating prefix is 12 bytes** (`\x1b]1337;File=`). The
  steady-state rule is the one that matters: an `\x1b]` prefix at a `Feed`
  boundary is **held** until the discriminator either resolves or is ruled out,
  so the next `Feed` can decide. Forward-verbatim applies only at `Flush` — and
  is harmless there because the local tmux discards an unknown OSC.
- **Bare form only.** Unlike sixel, the `\ePtmux;`-wrapped form needs nothing:
  a wrapped payload that is truncated is swallowed by tmux's DCS parser rather
  than painted, so there is no text-leak path to close. Stated so the asymmetry
  with #319's bare-and-wrapped sixel handling is deliberate rather than an
  oversight.

**R4 — Size the hold budget for a real image, keep it off the wire's cliff, and
keep the append amortised.** `maxPartial` is 64 KiB, chosen when every held
sequence named a path. A screen-sized chafa sixel is 3–8 MB (measured), so
64 KiB drops every real image.

- The **relay budget is a named constant with a stated number**, justified
  against the measurement and settable by a daemon flag beside `-gfx-max-bytes`.
- **The non-relay bound stays 64 KiB.** It has a second, unrelated job — it is
  when a stuck ordinary escape self-heals by being forwarded verbatim — and
  raising it would multiply the worst-case stall for every held escape.
- **With relay off, a sixel is held at the 64 KiB non-relay budget**, not the
  raster budget: the budget is spent only on a sequence we intend to relay. That
  is also what makes R10's overflow→discard path the one R8's test exercises.
- **The hold must be amortised O(1) per byte.** `Scanner.Feed` currently does
  `s.held = append([]byte(nil), buf...)` on every call — a full re-copy of
  everything held so far. Invisible at 64 KiB; at 3–8 MB across N `%output`
  batches it is O(N × size) of memcpy on the pane's own pump goroutine, which is
  the goroutine that must also drain that pane's frames. The result is the
  frozen-feeling pane the whole design exists to avoid, plus sink drops during
  the freeze that then lose the image to R2 anyway. The defensive copy is only
  needed when the held buffer aliases the caller's `p`; when the scanner already
  owns its backing array it must retain it rather than re-copy.
- **A relayed payload must not be written as one oversized frame.**
  `wire.maxFrameSize` is 16 MiB and `ReadFrame` *errors* above it, which returns
  from the renderer's read loop and kills the pane — turning "missing image"
  into the dead-renderer path `healDeadRenderers` exists to repair. Since the
  renderer writes each payload verbatim and in order, splitting a large buffer
  across frames is byte-identical at the pty. The split belongs in `wire`, which
  owns the cap and where the constant is unexported — so the daemon never
  handles a size and the graphics package never handles a frame. It is valid for
  the **byte-stream frame types only** (`FrameOutput`, `FrameSeed`): `FrameCtl`
  carries NUL-separated argv and `FrameResize`/`FrameHello` carry structured
  payloads, and splitting any of those corrupts them.

**R5 — Publish the local terminal's graphics capability to the remote, and
withdraw it.** The remote viewer cannot discover it: a control client has no
terminal, so its `client_termfeatures` is empty and `client_termname` alone
cannot tell a sixel-capable `xterm-256color` from an incapable one.

- The daemon publishes it with a control-mode `set-environment` against the
  bridged remote **session**, the way it already sends `PassthroughAllCmd`.
  Verified: session-scoped, and inherited by panes created afterwards.
- Deliberately **not** the ssh `env` prefix carrying `TERM`/`COLORTERM`/
  `TERM_PROGRAM`: those reach remote panes only because the remote's own
  `update-environment` allowlist names them, so a new variable on that channel
  is silently inert against any remote whose config predates it.
- **Send site:** once per `Run()`, against the remote session, at the point in
  `Run()` where the session-scoped setup already happens. Not re-sent on repair:
  a reattach verifies server identity, so the session table it wrote to is the
  same one and the value is still there. (Sited *near* the
  `themeToggleAvailable` call but deliberately not justified by it — CLAUDE.md
  describes that probe as "once per bridge connect", a different cadence.)
- **Always sent, including when the capability set is empty.** A previous bridge
  from a sixel-capable terminal leaves `sixel` in that session's table; skipping
  the write when we have nothing to say would let that stale value stand and
  make the remote emit megabytes we then drop.
- **Unset on teardown.** Every other piece of remote state the daemon owns is
  cleaned up (CLAUDE.md: "Teardown deletes what it wrote", "Teardown unsets what
  it wrote"); `PassthroughAllCmd` is the documented exception and argues for
  itself. This variable must not join it: it describes the *local* terminal of a
  bridge that no longer exists, so a stale `sixel` read by someone later
  attaching to that remote session **directly**, from a terminal with no sixel,
  reproduces the #319 symptom on a screen the bridge is not part of.
- **A daemon killed with SIGKILL cannot unset it.** The residue is then
  corrected by the next bridge's unconditional write; a direct attach in between
  can still read a stale value. Stated as a known, bounded residue rather than
  solved.

**R6 — One capability signal, two consumers.** The value gating the local relay
(R1) and the value published to the remote (R5) is the same value, computed
once, so the local drop policy and the remote backend choice cannot disagree. A
disagreement in either direction is a bug: the remote emitting what we drop
wastes megabytes; the remote withholding what we would relay is the status quo.
The shell hands the daemon tmux's **raw** `client_termfeatures`, and the daemon
derives the capability from it — so nothing wider than tmux's own condition can
be injected at the boundary.

**R7 — The kitty path is untouched, on every transport.** `t=f`/`t=t` fetch and
localisation, the `a=d` delete ordering, `Coalesce`, `retain`/`Replay` and
`EncodeWrapped` keep their present behaviour and their present tests.

This binds R8. Putting a proxy where there has never been one would otherwise
change four things on a same-machine transport, none of which an identity
Localizer neutralises: `t=s` would start being dropped though shared memory is
genuinely reachable there; `t=t` would be rewritten to `t=f`, leaking the
sender's own temp file, with the rewrite's stated justification ("the contract
was with a temp file that never crosses the bridge") false; a **bare** kitty APC
would gain a `\ePtmux;` wrapper it did not have; and `Coalesce` would begin
discarding stores that reach the terminal today.

- **Resolution: a proxy with no Localizer is relay-only.** It applies the raster
  policy (R1/R3/R10) and nothing else — kitty sequences are forwarded
  byte-identically, and `Coalesce` does not run. That satisfies R7 exactly on
  same-machine transports rather than trading it away, and it is the behaviour
  R8's seam needs.

**R8 — Prove it offline, and make the proof possible.**

- **The proxy must run under `--test-local`.** Today `NewGraphics` returns `nil`
  whenever there is no ssh control socket, and the pump's `if gfx != nil` skips
  filtering entirely, so the bats seam cannot see this feature at all. Under
  R7's resolution the proxy placed there is relay-only, so it adds the raster
  policy without touching kitty behaviour.
- **The RED-first assertion is the drop, not the relay.** Verified live: with
  the daemon under `--test-local` today, a bare sixel emitted on the remote pane
  arrives on the mirror pane's pty, introducer and body — nothing filters. So
  "relay works" is vacuously green and proves nothing alone. The load-bearing
  test is: with relay off, a sixel must **not** appear on the mirror pane's pty.
  The relay test is its paired opposite on the same harness with the gate
  flipped; the two together prove the gate, and must be described that way.
- Per R10, the sixel used must exceed the non-relay budget.
- Instrument: `pipe-pane` on the mirror pane, the pattern the keyneg query test
  already uses. `capture-pane` cannot see this — tmux's DCS parser eats a sixel,
  so it is green whether or not the bytes crossed.
- **Acceptance bullet 1 will not be run.** No live non-kitty terminal is
  available, and iTerm2 additionally means macOS. It must be named as unproven
  in the PR, not implied.

**R9 — Update the documentation the change falsifies.** CLAUDE.md's "Bridge
Graphics" section states the proxy drops sixel; that stops being true. This spec
and the plan are committed with the code, per repo convention.

## Non-goals

- **Relaying OSC 1337.** Reasoned above; the follow-up ask is below.
- **Surviving a reseed.** `capture-pane` returns text, so a bridge reseed loses
  a sixel image as it loses any non-text cell content. The kitty path's
  `retain`/`Replay` cannot be reused: a kitty virtual placement is
  position-independent, while a sixel is painted at the cursor the sender left,
  so replaying one after a reseed would paint it in the wrong place. The image
  returns on the viewer's next repaint — **unverified**: a `reseedDropped`
  repair is a purely local `capture-pane` push with no corresponding remote
  redraw, so an image may stay gone until the user presses a key.
- **Editing aeye.** Out of repo.
- **Changing what tmux does with a sixel.** We forward; tmux decides.
- **Making the bridge cheap for sixel.** It is not (see Risks).

## Cross-repo contract (what noamsto/aeye#186 needs)

Pinned so an aeye implementer can write it from this section alone.

- **Variable:** `LZTMUX_RELAY_GRAPHICS`, set by the daemon in the bridged remote
  **session** environment (readable as `tmux show-environment` on that session,
  and inherited by panes created after the bridge attaches).
- **Value grammar:** a comma-separated set of protocol tokens the *local*
  terminal can actually paint. Exactly one token is defined today: `sixel`.
  The list shape exists so a future capability is additive rather than a
  format change.
- **Absent, empty, or unrecognised** all mean *no relayable raster capability* —
  the current block-art behaviour. There is no "unknown, try it" state: an
  unrecognised token must not be treated as permission.
- **What aeye changes:** `chooseRelayBackend` gains the signal and, when it
  contains `sixel`, returns `backendRaster` with `formatSixel`.
- **What aeye must never do:** return `formatITerm` on the relay path, for the
  reasons under *Why OSC 1337 cannot be relayed*. A relayed viewer always paints
  into a tmux pane.
- Without that aeye change this PR is **inert but correct**: nothing emits sixel
  over the bridge, so nothing is relayed, and the safety net is strictly
  stronger than before.

## Risks

| Risk | Handling |
|---|---|
| A sixel-incapable local terminal sees `SIXEL IMAGE` placeholder boxes — the #319 symptom | R1's gate is tmux's own render condition, so we and tmux cannot disagree. Default is today's drop. |
| A user *declares* sixel for a terminal that cannot paint one | Then that terminal receives raw sixel and paints garbage — worse than a placeholder box. Not preventable: it is identical to any wrong `terminal-features` line in plain tmux, and the declaration is what makes the feature reachable at all. Named rather than implied away. |
| The gate never fires because nobody has the feature enabled | R1 requires the declaration mechanism, and R1's log line makes an unconfigured terminal diagnosable rather than mysterious. |
| **Sixel over the bridge is megabytes per repaint** where the kitty path sends a short path | Named, not solved. The sink's bounded buffer may drop frames under such a burst; a truncated sixel is then dropped by R2 and repaired by the existing `reseedDropped` path. A residual risk in the PR body, not a claim of parity with kitty. |
| A too-large frame kills the renderer | R4's split, below `wire.maxFrameSize`. |
| The held budget becomes a per-pane memory cost | Bounded per pane, spent only on a sequence we intend to relay; overflow drops rather than grows. |
| The capability is read once at launch and the user later attaches from a different terminal | Identical lifetime to the existing `TERM` forwarding, which has the same property. Out of scope; noted rather than silently inherited. |
| Acceptance bullet 1 is unverifiable here, and iTerm2 additionally means macOS | Stated as an unproven residual risk in the PR body. Everything provable offline is proven at the two seams in R8. |
