# Plan — #544: stop mirrored panes answering terminal queries twice

Implements [the design spec](../specs/2026-09-07-bridge-terminal-query-doubling-design.md). One package changes (`picker/remotebridge/keyneg`); the
daemon call site keeps its exported surface and needs only a comment fix.

## The refinement this plan settles

The spec's requirement 1 lists sequence *families*, and for three of them the
family covers both the query and its reply:

| family | query form | reply form | why it matters |
|--------|-----------|------------|----------------|
| DA1 | `CSI c`, `CSI 0 c` | `CSI ? Ps ; … c` | spec says strip `CSI ? Ps c` — that is the reply |
| DA2 | `CSI > c`, `CSI > 0 c` | `CSI > Pp ; Pv ; Pc c` | `TestFeedPreservesUnrelatedPrivateCSI` asserts the reply survives |
| DSR | `CSI 5 n`, `CSI 6 n` | `CSI 0 n`, `CSI 3 n` | query and reply share the final byte `n` |

Only a **query** makes the local tmux emit an answer, so only a query doubles.
Stripping a reply fixes nothing and destroys bytes a program may legitimately
be painting. The rule this plan adopts:

> **Be generous within query forms, strict about never matching a reply form.**
> Over-stripping a query is harmless — a query paints nothing, and the remote
> tmux has already answered it. Over-stripping a reply is a data-loss bug.

Where query and reply share a final byte (`CSI Ps n`, `CSI ? Ps u`, `CSI Ps t`),
the discriminator is an explicit **parameter allowlist**, not the final byte.

### Grounding: what the pinned tmux actually answers

Read from the pinned upstream `input.c` (`tmux-upstream` = 578e07f, the rev
`flake.nix` builds), `input_csi_dispatch` and `input_csi_dispatch_winops`:

- **DA1 / DA2 / XTVERSION** reply **only when `Ps == 0`** (`input_get(ictx,0,0,0)`
  switch has a single `case 0`). This is why `\x1b[>1;95;0c` — param 0 is `1` —
  is genuinely not a query, so `TestFeedPreservesUnrelatedPrivateCSI` stays
  green on its merits rather than as a carve-out.
- **DSR** answers `5` and `6` only; **DSR-private** answers **996 only**.
- **XTWINOPS** answers `14, 15, 16, 18, 19` — matching the spec's own sweep —
  and parses a **parameter list** (`while ((n = input_get(ictx, m, 0, -1)) != -1)`
  with `m++` at the loop tail), so a multi-value winops is real, not theoretical.
- **OSC 10/11/12** reply when the payload is exactly `?`; **OSC 4** replies when
  any `idx;?` element in its list is `?`.
- There is **no `{'u', "?"}` row** in tmux's CSI table, so tmux never answers the
  kitty keyboard query — matching the spec's table ("no reply" both columns).
  Stripping it is forward-compat, not a fix for today.
- `CSI u` with no private prefix is **RCP** (restore cursor). tmux has no bare
  `q` row at all (its CSI table ends at `input.c:347`), so bare `CSI Ps q` —
  DECLL in the DEC/ECMA vocabulary — is simply ignored; DECSCUSR is
  `CSI Ps SP q`, a *space intermediate*. None can collide with what we strip.

## Strip table (the implementation contract)

Match only when the sequence appears **bare** — never inside a `DCS tmux;`
passthrough wrapper, and never inside any other DCS/APC region.

| # | Family | Strip | Forward |
|---|--------|-------|---------|
| 1 | DA1 | `CSI c`, `CSI 0 c` | `CSI ? … c` (reply); `CSI Ps c`, Ps ≠ 0 |
| 2 | DA2 | `CSI > c`, `CSI > 0 c` | `CSI > Pp;Pv;Pc c` (reply — param 0 non-zero) |
| 3 | DA3 | `CSI = c`, `CSI = 0 c` | reply is a DCS, unambiguous |
| 4 | XTVERSION | `CSI > q`, `CSI > 0 q` | bare `CSI Ps q` (**DECLL**) and `CSI Ps SP q` (**DECSCUSR**, space intermediate) |
| 5 | DSR | `CSI 5 n`, `CSI 6 n` | `CSI 0 n`, `CSI 3 n` (replies); any other Ps |
| 6 | DSR-DEC | `CSI ? Ps n`, Ps ∈ {6, 15, 25, 26, 996} | every other `CSI ? Ps n` (reply space) |
| 7 | DECRQM | `CSI Ps $ p`, `CSI ? Ps $ p` | reply is `… $ y` — different final, unambiguous |
| 8 | DECRQSS | `DCS $ q … ST` | reply is `DCS Ps $ r … ST` — unambiguous |
| 9 | XTWINOPS report | `CSI Ps … t` where **parameter 0** ∈ {11,13,14,15,16,18,19,20,21} | parameter 0 ∈ {1..10,22,23} (actions); every reply (`CSI 4;h;w t`, `CSI 5;h;w t`, `CSI 6;ch;cw t`, `CSI 8;r;c t`, `CSI 9;h;w t` — first param ∈ {4,5,6,8,9}, disjoint from the allowlist) |
| 10 | OSC colour query | `OSC 10;?`, `OSC 11;?`, `OSC 12;?` (payload exactly `?`); `OSC 4; …` with any `?` element — either terminator (BEL or ST) | **every other OSC**, by number and by payload — including OSC 5/13/14/17/19 colour queries, deliberately left alone (outside the spec's measured set) |
| 11 | kitty keyboard | `CSI ? u` — **empty params only** | `CSI ? Ps u` (reply); `CSI = … u`, `CSI > … u`, `CSI < … u` (push/set/pop actions) |
| 12 | modifyOtherKeys (existing, #338) | `CSI > Ps m`, `CSI > Ps n` | unchanged from today |

Rows 1–11 are new; row 12 must stay green. Rows 2 and 4 revisit keyneg's current
"finals `c` and `q` under `CSI >` are not ours, forward" decision, exactly as
spec requirement 3 anticipates — narrowed to the zero/empty-param query forms.

**Deliberate deviations from the spec, each justified:**

- Row 1 forwards `CSI ? Ps c`, which spec requirement 1 lists as strip. It is the
  DA1 *reply*; see the rule above.
- Row 3 (DA3) appears nowhere in the spec. Added because `CSI = 0 c` is the same
  query family as rows 1–2 and costs nothing; harmless under the generosity rule.
- Rows 5, 6, 9, 11 narrow the spec's family-wide wording to parameter allowlists.
- **Known under-strips, all safe:** xterm's two-param queries `CSI 13;2t` and
  `CSI 14;2t` are covered by row 9's parameter-0 rule; DEC-DSR `CSI ? 53/62/63/75 n`
  are queries left forwarded (tmux answers none of them); OSC 5/13/14/17/19 as
  noted in row 10. And because winops loops (`m++` at `input.c:2218`), a form
  like `CSI 22;0;18t` reaches `case 18` and *does* reply, yet its parameter 0 is
  `22`, so row 9 forwards it and it keeps doubling — a deliberate decision, not
  an oversight: matching on any list member would over-strip the `22`/`23`
  title-stack actions spec requirement 2 protects. Under-stripping only means a
  sequence keeps doubling as it does today — never a regression.

## Region handling

`keyneg` runs **before** `gfx.Filter`, so kitty graphics APCs, sixel DCSs and
`\ePtmux;` passthrough wrappers are still in the stream. The scanner must walk
*past* them, never into them.

**The naive "skip until the first `ESC \`" is wrong, and this repo already says
so.** `graphics/seq.go`'s `unwrapPassthrough` documents it: tmux passthrough
**doubles every ESC in the payload**, so `\e\e\\` contains `\e\\` at its second
byte and a first-ST scan cuts at the *inner* terminator. Two literals already in
the repo's tests break the naive rule — `graphics/proxy_test.go:149`
(`"\x1bPtmux;\x1b\x1b]52;c;aGk=\x1b\x1b\\\x1b\\"`) and `graphics/scan_test.go:70`
(`"…aGk=\x07\x1b\\tail"`, where a BEL clause would end the region mid-payload).
**BEL terminates OSC only — never DCS or APC.**

Design:

- **Walk the region un-doubling ESC**, exactly as `unwrapPassthrough` does:
  `ESC ESC` → consume both and stay in region; `ESC \` → end region. Forward
  every byte verbatim (we are not decoding, only skipping).
- **`ESC P` peek**: `$q` immediately after → DECRQSS, strip per row 8 (short,
  bounded hold). Anything else → skip region.
- **Region state must survive `Feed` boundaries.** A pending `ESC` (and pending
  `ESC ESC`) at a buffer end goes in the `Filter` struct, not a local. An APC or
  passthrough straddling two `%output` frames is the *normal* case — that is why
  `graphics.Scanner` holds rather than streams.
- **The region is bounded, and overflow EXITS it.** `outputSink` drops frames
  under backpressure (`s.dropped` / `reseedDropped`), so an ST can be discarded
  and never arrive — the condition `graphics/scan.go` bounds with
  `maxPartial = 64 << 10`. Use a matching `maxRegion = 64 << 10` byte counter and
  on overflow **leave the region** so the filter re-arms. Latching `skipping`
  true forever would silently disable the whole fix for that pane's life, with no
  test failing and no observable signal.
- **OSC is bounded by `maxRegion`, NOT by `maxPending`.** Only the colour
  *queries* are short; every other OSC is not — a fish window title
  (`\x1b]0;fish /home/noams/Data/git/…\x07`), an OSC 8 hyperlink, an OSC 52
  clipboard body (the repo's own `graphics/scan_test.go:70` fixture). BEL or ST
  ends it.
- **Overflow and abort must RESUME scanning, never return the remainder.** This
  is a deliberate change to today's overflow shape. `strip.go:63-66` currently
  does `out = append(out, buf...); return out` — it forwards the *entire*
  remaining buffer and stops filtering it. That is survivable today because only
  an unterminated `\x1b[>` run longer than 32 bytes reaches it; it becomes a
  live bug the moment OSC is parsed, because `daemon.go:1720-1722` drains many
  `%output` frames into one `buf`, so a fish prompt's long OSC and fzf's
  `CSI 6n` / `CSI ?2004$p` land in the same `kn.Feed` call — and the first would
  disable the strip for the second.

  **Rule (held-run overflow or abort only):** emit the introducer `ESC` **alone**
  as literal, advance exactly one byte, and continue scanning. The rest of the
  released run is then re-scanned and falls out as ordinary literal, so the net
  output is byte-identical to today's release — do **not** emit the whole held
  run *and* resume before it, which would duplicate it.
  `TestFeedOverlongPendingReleasedAsLiteral` catches that wrong reading
  deterministically. It stays green either way for the right reading: its input
  has nothing after the released run, the only case today's `return` gets away
  with.

  **This rule does not apply to a *region* overflow.** There the introducer's
  `ESC` is up to 64 KiB back — possibly in a previous `Feed` — and its bytes were
  already forwarded verbatim, so there is nothing to rewind to. The region rule
  above governs: exit the region at the current position and keep scanning.

## Parser rules (Step 2 must implement these literally)

- CSI: `ESC [`, optional private prefix `<`, `=`, `>`, `?` (0x3C–0x3F), parameter
  bytes **0x30–0x3F**, intermediate bytes **0x20–0x2F**, final **0x40–0x7E**.
- A byte outside all of those after `ESC [` (e.g. `ESC [ \n`) **aborts the
  sequence** — today's `scanParams` consumes any non-param byte as the final,
  which a strict parser must not do. On abort, forward the bytes as literal.
- A lone trailing `ESC`, `ESC [`, `ESC ]`, `ESC P`, `ESC P $`, `ESC _` at a
  `Feed` boundary must be **held** — `ESC P $` because the DECRQSS peek needs two
  bytes after `ESC P`. Today's `partialPrefixTail` knows only `ESC` / `ESC [`.
- **`Flush` must return `nil`, not an empty slice, once drained** —
  `strip_test.go:72` asserts `f.Flush() != nil` on the second call. Easy to lose
  in a rewrite that builds a return buffer.
- **8-bit C1 introducers** (0x9B CSI, 0x9D OSC, 0x90 DCS, 0x9F APC) are a
  **deliberate non-goal** — outside the spec's measured scope. Say so in the
  package doc rather than leaving it silent.
- Keep the identifier `maxPending` — `strip_test.go:80` references it, and the
  "eight tests unmodified" claim depends on it.

## Steps

- [ ] **Step 1: pin the contract down as tests first.** In
      `picker/remotebridge/keyneg/strip_test.go`, add table-driven cases for every
      strip-table row — for each family a strip case and its paired forward case
      (the reply; for row 9 the boundary values 10/11 and 21/22; for row 2 the
      existing `\x1b[>1;95;0c`). Region cases — note only the **third** genuinely
      discriminates against the naive first-ST rule; the first two are
      forward-verbatim pins that a naive scanner also passes (it ends the region
      early but then forwards the leftover as an unknown escape), and the second
      fails only against a rule that wrongly treats BEL as a DCS terminator.
      Keep all three, but do not mistake them for three independent strands:
      - `"\x1bPtmux;\x1b\x1b]52;c;aGk=\x1b\x1b\\\x1b\\"` — forwarded byte-for-byte.
      - `"\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\tail"` — forwarded byte-for-byte.
      - `"\x1bPtmux;\x1b\x1b]11;?\x1b\x1b\\\x1b\x1b[6n\x1b\\"` — forwarded
        byte-for-byte (a multi-sequence wrapper; the naive rule strips the `6n`).
        **This is the load-bearing region case.**
      - `Feed("\x1b_Ga=t;abc\x1b")` then `Feed("\\\x1b[6n")` — the APC survives
        and the `\x1b[6n` **is** stripped (cross-`Feed` region state).
      - an unterminated APC longer than `maxRegion` followed by `\x1b[6n` — the
        `6n` is still stripped (overflow re-arms).
      - `Feed("\x1b]0;" + strings.Repeat("x", maxRegion+1) + "\x07\x1b[6n")` —
        assert the **full output byte-for-byte** (OSC released exactly once, `6n`
        gone). Asserting only "the `6n` is stripped" passes even under the
        double-emitting misreading of the overflow rule.
      - a long-but-terminated OSC followed by a query in the same `Feed`:
        `"\x1b]0;fish /home/noams/Data/git/lazytmux\x07\x1b[6n"` — OSC forwarded
        intact, `6n` stripped. This is the drained-batch shape from
        `daemon.go:1720-1722` and the closest unit-level proxy for acceptance
        bullet 1.
      - a query split across two `Feed` calls; a lone trailing `ESC`.
      Also add sink-level cases to
      `picker/remotebridge/daemon/keyneg_integration_test.go` for `\x1b[6n`,
      `\x1b[?2004$p`, `\x1b[16t` (stripped) and `\x1bPtmux;\x1b\x1b[6n\x1b\\`
      (forwarded verbatim). Keep the eight existing `strip_test.go` tests
      unmodified.

      **Which of these are actually red today** (today's scanner only searches for
      `\x1b[>`, so do not hunt a phantom red): every strip-table row, the
      cross-`Feed` APC case (today's `partialPrefixTail` holds the trailing `\x1b`,
      so the `6n` survives), and both overflow cases. The three passthrough-wrapper
      fixtures — **including the load-bearing third one** — already pass unchanged,
      because nothing today matches inside them. They are pins against regression,
      not proof of the fix.

- [ ] **Step 2: rewrite the scanner.** Replace the single `\x1b[>`-prefix search
      in `strip.go` with a scanner implementing the parser rules and the strip
      table above, plus the region state machine. Keep `maxPending` semantics and
      its name; add `maxRegion`. **Exported surface unchanged** —`NewFilter`,
      `Feed`, `Flush` only (spec requirement 5). Rewrite the package doc: it
      currently describes only #338. The doc must name (a) the doubling mechanism,
      (b) the query-not-reply rule, (c) 8-bit C1 as a non-goal, and (d) that
      `CSI > flags u` — the kitty-keyboard *push*, the protocol twin of #338's
      `CSI > 4;2 m` — is **not** covered here, so the next reader does not assume
      `keyneg` now owns that whole class.

- [ ] **Step 3: fix the call site's comment and re-check its assumptions.**
      `daemon.go:1681-1686` describes `kn` as stripping "modifyOtherKeys
      negotiation sequences … (#338)", which is now a misdescription — rewrite it.
      Verify ordering (`kn.Feed` → `gfx.Filter`), the close path
      (`kn.Flush()` → `gfx.Filter(tail)`), and that a `Feed` returning empty still
      hits the `len(f.payload) == 0` continue. Note explicitly that **`FrameSeed`
      bypasses `kn` entirely** — the filter runs only under
      `f.typ == wire.FrameOutput` (`daemon.go:1715`) — which is correct per spec
      non-requirement 2 (seeds come from `capture-pane`, rendered cells, never
      queries), and is the one place a reader will wonder. In
      `keyneg_integration_test.go`, `TestKeyNegAndGraphicsFlushOrderOnClose`'s
      premise at line 65 — "an incomplete modifyOtherKeys sequence (held by kn)" —
      becomes **false** under the new APC skip region (`\x1b[>4;` inside an
      unterminated `\x1b_Ga=t,f=100;` is forwarded, not held; the test still
      passes only because `gfx` holds everything). Correct the comment, or
      **correct the comment** at `keyneg_integration_test.go:63-65` to say the
      `\x1b[>4;` is forwarded inside the still-open APC region and that `gfx` is
      what holds it. (Do not restructure the case — there is no design behind the
      alternative, and the assertion at line 74 is still correct as written.)

- [ ] **Step 4: end-to-end proof for acceptance bullet 2.** The spec measured its
      probe table **offline**, through the daemon's `--test-local` seam — the same
      seam `tests/remote-m2-integration.bats` already drives in CI (two isolated
      tmux servers, no ssh; `flake.nix` `remote-m2-integration-tests`). Add a case
      there, built on the existing `bridge_up` helper (`bats:714-751`, which
      leaves a live mirror with `$SRC` = remote and `$DST` = local).

      **Instrument: `pipe-pane` on the LOCAL mirror pane.** Not `capture-pane` —
      a query paints no cells (`input.c` `INPUT_CSI_DSR` / winops call
      `input_reply` only, never `screen_write_*`), so a `capture-pane | ! grep`
      assertion is green before *and* after the fix and cannot fail. `pipe-pane`
      sees the renderer's raw byte stream, which is exactly what this filter
      edits.

      Recipe — mechanics that will otherwise bite: the mirror pane is
      `host-sess:1.0` (`bats:667-669`; the daemon forces `pane-base-index 0` on
      mirror windows, which is why `mirror_contains` indexes from 0); **expand the
      capture path at send time** (`"cat >> $f"`, double-quoted) because the tmux
      server's environment has no `$f`; **poll** for MARKER before asserting
      absence, since `pipe-pane` writes asynchronously and every other case in this
      suite polls; and grep the **real byte**, `grep -F -- $'\033[6n'` — `send-keys`
      echoes the typed command line containing the literal text `\033[6n`, so a
      grep for `[6n` false-positives.

      `$DST pipe-pane -o -t host-sess:1.0 "cat >> $f"`, then
      `$SRC send-keys -t rem "printf '\\033[6n%s\\n' MARKER" Enter`, then assert
      against `$f`:
      - `\x1b[6n` is **absent** — the query never reached the local pty, so the
        local tmux cannot emit a second reply. This is acceptance bullet 2's
        mechanism in full: one reply, because only the remote answered.
      - `MARKER` is **present** — proof the pipe actually captured the stream, so
        the absence above is a real result and not an empty file. Without this
        the test is unfalsifiable, which is the trap `capture-pane` falls into.

      **Observe it red once.** Step 4 is authored after Step 2, so the case is
      never seen failing. Revert `strip.go` (or stub `Feed` to identity), run just
      this bats case, confirm it fails, restore. That is the standard the rest of
      this suite is held to.

      Optionally strengthen with the reply-echo count the spec itself observed
      (`^[[6;32;16t` leaking as literal pane text, SPEC.md:10-12): a doubled
      reply echoes twice at the remote shell's prompt, so
      `$SRC capture-pane -p -t rem` can assert one occurrence, not two. Take it
      if it proves stable in the harness; the `pipe-pane` assertion is the
      contract either way.

- [ ] **Step 5: move the docs into the tracked tree.** Write `SPEC.md` to
      `docs/superpowers/specs/2026-09-07-bridge-terminal-query-doubling-design.md`
      and this plan to
      `docs/superpowers/plans/2026-09-07-bridge-terminal-query-doubling.md`, then
      **delete both root copies** (`SPEC.md` *and* `PLAN.md`) with `gtrash put`
      per the repo's safe-deletion convention, never `rm` — all three root docs are
      untracked, so git cannot recover them.
      `WORKER_TASK.md` is never committed — stage explicit paths, never
      `git add -A`.

- [ ] **Step 6: run the gate.** `go test ./...` from `picker/`, then
      `nix build .#default`, `nix flake check`, `nix build .#lint`. Loop to green.

## Verification

- **Unit**: `go test ./remotebridge/keyneg/...` — the strip table plus the region
  and cross-`Feed` cases are the contract.
- **Sink-level**: `go test ./remotebridge/daemon/...` —
  `keyneg_integration_test.go` through the real `outputSink` pump.
- **End-to-end**: the new `remote-m2-integration.bats` case (Step 4) — acceptance
  bullet 2.
- **Regression net for #338**: the eight pre-existing `strip_test.go` tests and
  the three in `keyneg_integration_test.go` pass, unmodified apart from the
  Step-3 comment correction.
- **Repo gate**: `nix build .#default`, `nix flake check` (which runs
  `picker-go-tests` — `pickerChecked` has `doCheck = true` over the whole picker
  module, so new keyneg cases are picked up with no wiring), `nix build .#lint`.
- **Manual only**: acceptance bullet 1, the live `ctrl+R`-in-a-mirror latency,
  needs a real remote. The spec's causal proof already covers the mechanism
  offline; the PR says which bullets were machine-checked and which were not.

## Out of scope (spec non-requirements)

No input-direction filtering, no `%output`/seed/reconcile change, nothing on the
remote host, no 8-bit C1 support, no kitty-keyboard push filtering.
