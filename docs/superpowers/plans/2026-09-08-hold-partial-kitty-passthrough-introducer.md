# Hold a partial kitty/passthrough introducer across a Feed boundary

Closes #572.

## Root cause

`indexSeqStart` (`picker/remotebridge/graphics/scan.go`) locates the kitty APC
introducer (`apcStart = "\x1b_G"`) and the tmux-passthrough introducer
(`passStart = "\x1bPtmux;"`) with a plain `bytes.Index`, which only finds a
**complete** occurrence. Its sibling, `indexSixelStart`, additionally treats a
trailing run at the end of the buffer that is still a viable (incomplete)
prefix of the sixel introducer as a match — so the caller holds it. The two
fixed-string patterns never got that tolerance.

`Feed`'s no-match arm (`i := indexSeqStart(buf); if i < 0 { flush as Literal;
return }`) fires whenever `indexSeqStart` returns -1, so a chunk boundary that
lands 1–6 bytes into either introducer is flushed as `Literal` immediately.
When the rest of the introducer arrives on the next `Feed`, it's already gone
— the sequence can never be reassembled.

Traced by hand: once `indexSeqStart` hands `Feed`'s `buf` a slice that starts
at the correct (partial) position, `decodeSeq` already handles it correctly —
`bytes.HasPrefix` against `passStart`/`apcStart` returns `false` on a
too-short buffer (Go's `HasPrefix` requires `len(s) >= len(prefix)`), so it
falls through to the `!ok` branches that return `(nil, 0, dropNone)` — "hold
for more bytes". So **no change is needed to `decodeSeq`, `Feed`'s hold
branch, or `Flush`** — only to `indexSeqStart` finding the position in the
first place.

## Fix

Add one helper, `indexFixedStart`, that both `apcStart` and `passStart` use
in `indexSeqStart` in place of the bare `bytes.Index`:

```go
// indexFixedStart returns the offset of pat's next occurrence in b: a
// complete match, or — mirroring indexSixelStart's tolerance for the sixel
// introducer — a trailing run at the end of b that is still a viable prefix
// of pat and must be held rather than mistaken for literal text, since a Feed
// boundary can split pat anywhere.
func indexFixedStart(b []byte, pat string) int {
	if i := bytes.Index(b, []byte(pat)); i >= 0 {
		return i
	}
	max := len(pat) - 1
	if max > len(b) {
		max = len(b)
	}
	for l := max; l > 0; l-- {
		if bytes.Equal(b[len(b)-l:], []byte(pat[:l])) {
			return len(b) - l
		}
	}
	return -1
}
```

`indexSeqStart` changes its loop from `bytes.Index(b, []byte(pat))` to
`indexFixedStart(b, pat)` — a one-line change per call site, same loop
structure, `indexSixelStart` untouched.

Only the trailing suffix of `b` needs checking for the partial case: any
partial match not at the true end of the buffer is already resolved one way
or the other by the following byte (either it completes into a full match,
already caught by `bytes.Index`, or it diverges and is correctly not a match
at all).

**Divergence from `indexSixelStart`, not a mirror of it, at the 1-byte case.**
`indexSixelStart` explicitly declines to hold a lone trailing ESC (`// A
trailing ESC with no following byte can't be \eP yet. return -1`).
`indexFixedStart`'s `l == 1` branch *does* hold it — required, since a lone
ESC is a genuine 1-byte prefix of both `apcStart` and `passStart` and must
survive to the next `Feed` to be reassembled. This is a deliberate, new
behavior this fix introduces (a pane frame ending in a bare ESC — a split CSI,
an interactive ESC keypress echo — is now held one `Feed` instead of forwarded
immediately), not an existing invariant being reused. Verified safe: held
alone, `Flush("\x1b")` still emits it as a `Literal` (see below).

## Judgement calls

- **Flush — and the sixel-ambiguity exception.** A held partial introducer has
  no terminator coming once `Flush` is called — emit it as a literal (the
  issue's suggestion), which is what the existing `return []Chunk{{Literal:
  held}}` does whenever `isPartialSixel` is `false`. That covers every new
  partial-introducer prefix **except one**: `"\x1bP"` (the exact 2-byte cut
  inside `passStart`) is indistinguishable from a partial bare sixel DCS —
  `isSixelPrefix("\x1bP")` returns `true` (`j := 2; j >= len(b)` → `true`,
  `seq.go`), so `isPartialSixel` is `true` and `Flush` **drops** it, same as
  any other partial sixel. That is the existing, deliberate sixel-drop policy
  from `isPartialSixel`/`Flush`, not a defect this issue is about — #572 is
  about reassembly across a `Feed` boundary, not `Flush` disposal, and
  changing sixel-ambiguity resolution is explicitly out of scope ("No change
  to the sixel … path"). Record the asymmetry rather than "fixing" it: the
  Flush test (Step 3) asserts `Literal` for every new partial prefix *except*
  `"\x1bP"`, which it asserts is dropped (`nil`).
- **Shape of the fix.** One shared helper (`indexFixedStart`), used by both
  `apcStart` and `passStart`, per the issue's suggestion. No third bespoke
  index function.
- **Hold budget / `holdLimit` / `rasterScanned`.** `rasterScanned` and
  `holdLimit` don't exist on `main` (they're `#320`-branch-only). The only
  relevant cap here is `maxPartial` (64 KiB) in `Feed`'s `n == 0` branch,
  gating the *hold*, not the *index*. A partial introducer is at most 6 bytes
  — nowhere near the cap — and `indexFixedStart` doesn't hold anything itself,
  it only returns an offset; `Feed` still does the actual capping. No
  interaction. `TestScanOversizedPartialFlushesAsLiteral` starts from a
  *complete* `apcStart` match, so `bytes.Index` still wins there and that test
  path is unaffected.
- **"No change to … OSC 1337 … path" is vacuous on `main`.** There is no OSC
  1337 handling anywhere in this package — the only OSC in play is OSC 52,
  carried by the generic complete-passthrough-forward arm
  (`TestScanCompleteNonGraphicsPassthroughForwardsVerbatim`), which is
  unaffected since its input begins with a *complete* `passStart` and
  `bytes.Index` still wins. Carrying the OSC-1337 criterion over from the
  issue unchecked would send the next reader hunting for a path that doesn't
  exist on this branch.

## Steps

- [ ] **Step 1: RED test.** In `picker/remotebridge/graphics/scan_test.go`,
  add a table-driven test that splits `bareSeq` (kitty APC) at every offset
  inside its 3-byte `\x1b_G` introducer (1, 2 bytes) and a
  `tmuxPassthrough(...)`-wrapped sequence at every offset inside its 7-byte
  `\x1bPtmux;` introducer (1–6 bytes): feed `buf[:cut]` then `buf[cut:]`,
  assert nothing is emitted early and the second `Feed` reassembles to the
  same chunks (`chunkKinds` + byte-identical payload) as an unsplit feed.
  Include the 1-byte lone-ESC split explicitly (it's the shared ambiguous
  prefix of both introducers). **Note before running:** offset 2 of the
  `passStart` split (buffer tail `"\x1bP"`) is already held today via
  `indexSixelStart` (it returns 0 for that tail regardless of this fix), so
  that one sub-case is expected GREEN even against unmodified `scan.go` — RED
  is expected at offset 1 (lone ESC) and offsets 3–6 of the `passStart` split,
  and at offsets 1–2 of the `apcStart` split. Run `go test
  ./picker/remotebridge/graphics/... -run TestScanPartialIntroducer` and
  confirm that pattern (partial RED, offset-2-passStart green) against the
  unmodified `scan.go` — paste the failure output in the commit description or
  PR body, per house standard.
- [ ] **Step 2: implement `indexFixedStart`** in `scan.go` and wire it into
  `indexSeqStart` for both `apcStart` and `passStart`, per the Fix section
  above. While here, amend the now-stale comment on `decodeSeq` (`seq.go`,
  "Feed only calls this at an `indexSeqStart` hit, so a head that isn't a
  passthrough or sixel is an `apcStart`: `decodeBare` can only fail for want
  of the ST") — after this fix, `decodeSeq` is also reachable with a *partial*
  `apcStart`/`passStart` head, where `decodeBare` fails for want of the
  introducer, not just the ST. Behavior at that branch is unchanged (still
  `(nil, 0, dropNone)`); only the comment needs updating to stay accurate.
- [ ] **Step 3: Flush test.** Add a test pinning `Flush` behavior on every new
  held partial prefix: `"\x1b"`, `"\x1b_"` (apc side), and `"\x1bPt"` through
  `"\x1bPtmux"` (pass side, excluding the 2-byte `"\x1bP"` case) each resolve
  to a `Literal` chunk on `Flush`. Separately assert `"\x1bP"` alone resolves
  to `nil` (dropped) — the sixel-ambiguity exception above — so the asymmetry
  is pinned rather than left to be rediscovered as a regression.
- [ ] **Step 4: verify green + no regressions.** Run `go test
  ./picker/remotebridge/graphics/...` — confirm the new tests pass and nothing
  else broke (sixel, OSC-forwarding, oversized-partial, malformed-drop tests
  all still green). Then the repo's full local gate, all three commands: `nix
  build .#default`, `nix flake check`, `nix build .#lint` (the last one runs
  `typos`/`trim-trailing-whitespace` etc. over the new test file too).

## Acceptance (from the issue)

- A sequence split at every offset inside `\x1b_G` and `\x1bPtmux;`
  reassembles and is emitted as a `Seq`, byte-identical to the unsplit feed.
- `Flush` behaviour on a held partial introducer is explicit and tested,
  including the one sixel-ambiguous exception (`"\x1bP"`, dropped not
  literal) documented above.
- No change to the sixel / discard paths. (OSC 1337 doesn't exist on this
  branch — see Judgement calls.)
