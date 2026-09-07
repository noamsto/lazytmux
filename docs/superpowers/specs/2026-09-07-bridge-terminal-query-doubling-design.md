# Spec — #544: mirrored panes answer terminal queries twice

## Problem

A program started in a mirrored (remote-bridge) pane stalls at startup and
loses its first keystrokes. Reported for `ctrl+R` in fish (`fzf.fish`
`_fzf_search_history`): the float paints instantly, the first ~2.5s of typing
does nothing, then every later keystroke is instant.

A second symptom of the same class was reported separately: aeye's `\e[16t`
cell-size probe times out and its reply then leaks into the pane as literal
`^[[6;32;16t` text.

## Mechanism (measured, not inferred)

A mirrored pane's occupant issues terminal queries at startup. The **remote**
tmux answers each one itself, writing the reply into the remote pane's input —
exactly as it does when the session is detached. But the query bytes are also
**pane output**, so they cross the bridge in `%output`, reach the local mirror
pane's pty via the renderer, and the **local** tmux parses them as a query from
the renderer and answers them a **second** time. That second reply travels back
renderer stdin → daemon → `send-keys` into the remote pane as unsolicited
input.

Every reply therefore arrives **doubled** at the remote occupant, and the
duplicate desynchronizes its escape-sequence parser.

### Evidence

Offline, through the daemon's `--test-local` seam: two local tmux servers
(`next-3.8`), a real attached pty client on the host side, `fzf` 0.74.3.

A probe issuing each query in a remote pane and timing the reply:

| query | detached | bridged |
|-------|----------|---------|
| DA1 `CSI c` | `\e[?1;2;4c` @0.1ms | same, **twice** |
| DA2 `CSI > c` | `\e[>84;0;0c` @0.9ms | same, **twice** |
| DSR-5 `CSI 5n` | `\e[0n` @0.4ms | same, **twice** |
| DSR-6 `CSI 6n` | `\e[6;1R` @0.5ms | same, **twice** |
| DECRQM `CSI ?2004$p` | `\e[?2004;2$y` @0.5ms | same, **twice** |
| DECRQM `CSI ?2026$p` | `\e[?2026;2$y` @0.5ms | same, **twice** |
| XTVERSION `CSI > q` | `DCS >\|tmux next-3.8 ST` @0.3ms | same, **twice** |
| kitty `CSI ? u` | **no reply** (1.5s) | **no reply** |
| XTWINOPS `CSI 14t` | `\e[4;1248;1920t` @0.3ms | `\e[4;1152;1920t`, **twice** |
| XTWINOPS `CSI 16t` | `\e[6;32;16t` @0.5ms | same, **twice** |
| XTWINOPS `CSI 18t` | `\e[8;39;120t` @0.3ms | `\e[8;36;120t`, **twice** |
| OSC 10 | `rgb:cdcd/d6d6/f4f4` @0.5ms | same, **twice** |
| OSC 11 | `rgb:1e1e/1e1e/2e2e` @0.3ms | same, **twice** |
| **passthrough** `DCS tmux; ESC CSI 6n ST` | **no reply** | `\e[9;1R` @10.1ms, **once** |

Two things this table settles beyond the doubling:

- **The injected copy is not merely redundant, it is wrong.** `14t` and `18t`
  answer with the *local mirror pane's* geometry (`1152;1920`, `8;36;120`)
  rather than the remote pane's (`1248;1920`, `8;39;120`); DSR-6 answers with
  the renderer pane's cursor.
- **A passthrough-wrapped query is the one case that legitimately needs the
  local answer.** The remote tmux does not answer it (no reply detached), and
  across the bridge it is answered exactly once, by the local side. Stripping
  it would be a regression, not a fix.

fzf's own startup sequence, captured with `pipe-pane`, is `CSI 6n`,
`CSI ?2004$p`, `CSI 6n`.

Sweeping `CSI Ps t` for `Ps` 1–24 in a detached remote pane, tmux `next-3.8`
answers exactly **14, 15, 16, 18, 19** and nothing else — so those five are the
values that actually double today. The allowlist below is the wider standard
*report* set rather than that measured subset, so a future tmux that starts
answering `21t` (report window title) does not reintroduce the bug; stripping
the four it ignores today costs nothing, since tmux drops an unhandled
`CSI Ps t` rather than forwarding it.

Symptom repro — `seq 1 200 | fzf --height=100%` in the remote pane, then one
`send-keys` char:

- **detached**: the char lands at every delay tested (0.1s–0.5s).
- **bridged**: the char is swallowed at every delay tested (0.1, 0.2, 0.3, 0.4,
  0.5, 0.7, 1.0, 1.5, 2.0, 3.0s) and stays swallowed with settle times up to
  8s. fzf's own match counter reads `200/200` (empty filter), so this is not a
  capture artifact. Deterministic but not linear in keystroke count: 1 char
  lost, 2 chars lost, 3+ chars all land (3 runs each).

**Causal proof.** A throwaway daemon patch deleting `CSI 6n`, `CSI 5n`,
`CSI c`, `CSI ?2004$p`, `CSI ?2026$p`, `CSI 16t`, `CSI 14t` and `CSI 18t` from
the mirrored output stream — applied right after `keyneg`'s filter in the
`outputSink` pump — makes the swallow disappear completely (1 char, 2 chars,
and a char at +1.5s all land). Re-running the probe under that patch showed
exactly the patched sequences de-duplicated and the unpatched ones (DA2,
XTVERSION, OSC 10/11) still doubled.

**What the bridge delivers to the mirror pane** (captured with `pipe-pane` on
the local mirror pane) — the wrapper survives, so bare and passthrough-wrapped
queries are distinguishable in the stream:

```
BARE ^[[6n ...  PT ^[Ptmux;^[^[[6n^[\ END
```

## Fix

Strip terminal **query** sequences from a mirrored pane's output stream before
it reaches the local pty, so the local tmux never generates a reply. This is
the mirror image of `picker/remotebridge/keyneg/strip.go`, which strips
modifyOtherKeys **negotiation** from the same stream (#338), and it belongs in
that package, at the same call site.

### Why stripping is safe

The mirror pane's occupant is the renderer, which issues no queries of its own.
Every query byte reaching the local pty therefore came from the remote occupant
and has **already been answered by the remote tmux** — measured above, and
identical to the detached case, which is the behaviour we are restoring. tmux
never routes a pane's query to a client, so declining to answer locally loses
nothing.

The one exception is a query the remote tmux does *not* answer because it was
wrapped for passthrough. Those must be forwarded.

### Requirements

1. **Strip, when they appear bare in the stream:**
   - DA1 — `CSI c`, `CSI ? Ps c`
   - DA2 — `CSI > Ps c`
   - XTVERSION — `CSI > Ps q`
   - DSR — `CSI Ps n`, `CSI ? Ps n`
   - DECRQM — `CSI Ps $ p`, `CSI ? Ps $ p`
   - DECRQSS — `DCS $ q ... ST`
   - XTWINOPS **report** requests — `CSI Ps t` for `Ps` ∈ {11, 13, 14, 15, 16,
     18, 19, 20, 21} only
   - OSC colour **queries** — `OSC 4;…;? `, `OSC 10;?`, `OSC 11;?`, `OSC 12;?`,
     with either terminator (BEL or ST)
   - kitty keyboard query — `CSI ? u`
2. **Forward untouched:**
   - Anything inside a `DCS tmux; … ST` passthrough wrapper, including a query.
   - XTWINOPS **action** requests — `CSI Ps t` for `Ps` ∈ {1..10, 22, 23},
     which iconify/deiconify, move, resize, raise/lower, refresh, and push/pop
     the window title. These are not queries and changing them changes
     behaviour.
   - Every other sequence, byte-for-byte.
3. **Preserve `keyneg`'s existing behaviour** — `CSI > Ps m` / `CSI > Ps n`
   negotiation stays stripped (#338). Note the current filter deliberately
   *forwards* finals `c` and `q` under the `CSI >` prefix as "not ours"; DA2
   and XTVERSION are exactly those, so that decision is revisited here, not
   duplicated.
4. **Hold an incomplete trailing sequence across `Feed` calls**, with a bound,
   releasing held bytes as literal on overflow and on `Flush` — the existing
   `held`/`maxPending` contract, which callers already depend on.
5. **Keep the exported surface** (`NewFilter`, `Feed`, `Flush`) so the single
   call site in `daemon.go`'s `outputSink.start` pump is unchanged, and the
   filter still runs *before* `gfx.Filter`.

### Non-requirements

- No change to the input direction. With the query stripped, the local tmux
  generates no reply, so there is nothing to filter on the way back.
- No change to `%output` handling, seeds, or reconcile. Seeds come from
  `capture-pane`, which reproduces rendered cell content, not queries.
- Nothing on the remote host changes.

## Acceptance

- `ctrl+R` (fzf.fish history) in a mirrored pane consumes keystrokes within
  ~100ms of the float painting, matching a detached remote session. Offline
  proxy: the single-keystroke repro above lands the char at every delay.
- The probe run bridged returns exactly **one** reply per query, matching the
  detached column — for every row in the table, not only the ones fzf uses.
- A passthrough-wrapped query still gets its one reply.
- `nix build .#default`, `nix flake check`, `nix build .#lint` all pass.

## Risks

- **Rewriting `keyneg`'s scanner risks regressing #338.** Mitigated by keeping
  every existing `strip_test.go` case green and keeping the exported API.
- **XTWINOPS `Ps` classification is the sharp edge** — stripping an action
  value would silently break window operations a remote occupant performs.
  Mitigated by an explicit report-only allowlist and a test per boundary value.
- **The passthrough carve-out must be checked before the query match**, because
  a wrapped payload contains the query as a substring (`ESC ESC [ 6 n`). The
  graphics scanner already walks `\ePtmux;` regions the same way
  (`graphics/scan.go`), which is the precedent to follow.

## Amendments made during implementation

The requirements above were written before the code existed. Three of them were
refined once the strip table met the real parser; the plan
(`docs/superpowers/plans/2026-09-07-bridge-terminal-query-doubling.md`) carries
the full reasoning and the per-family matching rules.

1. **Queries only, never replies.** Requirement 1 names sequence *families*, and
   for DA1, DA2 and DSR the family covers the reply as well as the query —
   `CSI ? Ps c` is the DA1 reply, `CSI 0 n` the DSR-5 reply, and the existing
   `TestFeedPreservesUnrelatedPrivateCSI` asserts a DA2 reply survives. Only a
   query makes the local tmux answer, so only a query doubles; stripping a reply
   fixes nothing and destroys bytes a program is painting. Where query and reply
   share a final byte the discriminator is a parameter allowlist. Verified
   against the pinned tmux `input.c`: DA1/DA2/XTVERSION answer only on `Ps == 0`,
   DSR only on 5 and 6, DSR-private only on 996, XTWINOPS only on 14/15/16/18/19.

2. **Region boundaries must match tmux's state machine, not just ST/BEL.**
   `INPUT_STATE_ANYWHERE` (`input.c:367-371`) maps CAN (0x18) and SUB (0x1a) to
   ground and ESC (0x1b) to `esc_enter`, and both `input_state_osc_string_table`
   and `input_state_apc_string_table` begin with it — so any of those three ends
   an OSC or APC string. `input_state_dcs_handler_table` explicitly does **not**
   (`/* No INPUT_STATE_ANYWHERE */`), and `dcs_escape` maps `0x00-0x5b` back into
   the payload, so the ESC-doubling convention is **DCS-only**. Ending every
   region at ST alone let a crafted stream smuggle a query past the filter —
   `\x1b]0;\x1b[>4;2m\x07` is inert payload to a naive walker and a live
   modifyOtherKeys request to tmux, reopening #338. This was a regression against
   the pre-#544 filter, which had no region awareness and stripped it.

3. **The held colour-OSC scan is incremental.** Bounding it by `maxRegion` caps
   memory but not CPU: rescanning the accumulated buffer on every `Feed` is
   quadratic, ~2x10^9 byte comparisons per 64 KiB when fed a byte at a time, which
   is remote-controlled input on the daemon's output pump.

## What this does not close

Acceptance bullet 1 — the live `ctrl+R` latency in a real mirrored pane — was not
measured on a remote host; it needs one. The mechanism is proven offline instead:
the end-to-end bats case asserts the query byte never reaches the local mirror
pane's pty (observed red with the filter stubbed to identity), which is the whole
of the doubling mechanism, since the local tmux can only answer what reaches it.
