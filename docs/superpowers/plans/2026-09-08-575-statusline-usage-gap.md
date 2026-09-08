# Status line 0: agent-usage block separated from the pane-cmd group by a dead run of blank cells (#575)

## Diagnosis (measured, not guessed)

Line 0's tail is built by `picker/statusline`'s `renderLine` as one `#()` job
output: `#[align=right]` + `usage` (the agent-usage block) + `paneSlot(icon,
cmd)` (the active-pane icon+command, fixed-width per #260) + a trailing
literal space.

There are two visually blank regions in a wide client:

1. **Between the left group and the usage block.** This is `#[align=right]`
   itself: everything after that marker is right-justified to the terminal's
   right edge, so the gap is simply the terminal's leftover width once both
   sides are laid out. It shrinks as the client narrows and vanishes once
   content fills the line — ordinary status-bar slack, not a defect.

2. **Between the usage block and the pane-icon+command text.** This comes
   from `paneSlot`'s `#{p-17:...}` wrapper (`paneSlotPad = paneSlotKeep + 1 =
   17`, `picker/statusline/main.go:262-277`), added in #260 to stop the usage
   segment's start column from jittering as the active pane's command changes
   length across windows. `p-N` **left**-pads to N cells (confirmed
   empirically below — the man page's prose is inverted from actual
   behaviour; `main.go`'s own comment states the `p-N`=left fact but doesn't
   itself cross-reference the man page), so any command
   shorter than the 16-cell budget (sized for `cursor-agent`, the longest
   command in routine use) leaves blank cells **before** the text, i.e.
   directly after the usage block.

   Measured empirically via `tmux display-message -p -F` against a real
   tmux (`./result/bin/tmux` from `nix build .#default`, next-3.8; also
   reproduced against a plain `tmux -L <sock> -f /dev/null` server — the
   `remotebridge/cmd/daemon` test-helper pattern, so no baked config is
   needed to observe this):

   - icon="", cmd="bash": `#{p-17:#{=/16/…:#{l: bash}}}` expands to 13
     leading spaces + `bash` (17 cells total) — a fixed 13-cell dead run,
     **independent of client width**, immediately after `usage`'s own
     trailing `"  "`.
   - icon="⚙" (2-cell), cmd="process-compose" (15 chars, the longest name in
     `config/process-icons.nix`): content already fills the full 17-cell box
     (0 blanks needed) — confirming the box is sized for the rare long name,
     not the common short one.

   Unlike gap 1, this gap does **not** depend on terminal width — it is a
   constant reservation that shows up whenever the active pane's command is
   short (the common case: `bash`, `fish`, `nvim`, `git`, …), which is exactly
   what the report describes ("visibly wider than one or two cells", present
   regardless of how wide the client is).

**Verdict: gap 1 is natural align=right slack (not a bug); gap 2 (paneSlot's
internal left-pad) is the actual defect** — a reservation sized to the
*worst-case* pane command, paid on *every* render regardless of how short the
actual command is.

## Constraint: don't reintroduce #260

#260 fixed a real jitter bug: without a fixed-width pane slot, the *whole*
right-aligned tail's rendered width changes with the active pane's command
length, so the usage segment visibly jumps sideways as you switch windows
(measured at the time: 3-12 cells). `#[align=right]` anchors the tail's
**end**, not its start (`main.go:263-265`'s own comment states this), so the
invariant that actually matters is: **`usage`'s own start column must not
depend on the active pane's command length.** Since `usage` is emitted
*before* `paneSlot` in the tail, and nothing else in the tail depends on
command length, that invariant reduces to: **`paneSlot`'s own total rendered
width must be constant** — which is exactly what `paneSlotPad`/`p-17` already
guarantees today, regardless of which side of the content the padding lands
on.

This constraint rules out solutions that insert a command-length-dependent
reservation *before* `usage` (an earlier draft of this plan tried exactly
that, using tmux arithmetic to move the padding ahead of `usage` — plan
review caught that it slides `usage`'s start column by the same amount it
was trying to eliminate, since anything with variable width placed before
`usage` in the string moves `usage`, full stop). The reservation must stay
inside `paneSlot`, on one side or the other of the command text.

## Fix

`paneSlot`'s padding modifier is `#{p-%d:...}` (`main.go:299`) — a **minus
sign** selects left-pad (blank cells inserted **before** the content, so
short commands sit flush against the trailing space/right edge with the
gap on their *left*, adjacent to `usage`). Dropping the minus
(`#{p%d:...}`) selects right-pad instead: blank cells go **after** the
content, so the command sits flush against `usage` (zero gap) with the
reserved slack pushed to the command's *right*, before the final trailing
literal space.

`paneSlot`'s total rendered width is exactly `paneSlotPad` (17) cells
regardless of command length either way — truncation (`=/16/…`) caps content
at 16+1 cells, and the pad tops it up to 17 from whichever side — so the
#260 invariant (`paneSlot`'s width, hence `usage`'s start column, constant
regardless of command length) is preserved *exactly as it is today* whichever
side the pad is on. Only the **visible position of the blank cells within the
box** changes.

**Gate the flip on whether `usage` is non-empty.** There is nothing to be
"adjacent to" when no agent is running, so left-pad's existing flush-right
behavior is harmless there and changing it would be an unrequested,
always-on visual change for the (likely far more common) no-agent-usage
render — out of scope for a fix that's specifically about the with-usage
case. `paneSlot` therefore takes a third parameter,
`adjacentToUsage bool`, and `renderLine` passes `usage != ""`. This also
means the pad direction only ever flips at the same moment `usage` itself
appears or disappears — a layout change already happening at that instant
(the usage block's own width goes from 0 to non-zero) — rather than on
ordinary window switching with no other state change, which is what made
#260's jitter jarring in the first place.

Verified empirically against a scratch `tmux -L <sock> -f /dev/null` server:

```
$ tmux display-message -p -F "[#{p-17:#{l:bash}}]"
[             bash]                     # today: left-pad, flush right
$ tmux display-message -p -F "[#{p17:#{l:bash}}]"
[bash             ]                     # fixed: right-pad, flush left

$ tmux display-message -p -F "[#{p17:#{=/16/…:#{l:I bash}}}]"
[I bash           ]                     # 6 + 11 = 17 cells, short command
$ tmux display-message -p -F "[#{p17:#{=/16/…:#{l:I a-very-long-command-name-indeed}}}]"
[I a-very-long-co…]                     # already 17 cells, 0 padding needed

$ tmux display-message -p -F "USAGE#{p17:#{=/16/…:#{l:I bash}}} |END"
USAGEI bash            |END             # zero chars between USAGE and I
```

The first two lines confirm the pad direction empirically (the man page's
prose — "a positive width pads on the left" — is inverted from actual
behaviour; `main.go`'s existing comment only states the `p-N` fact, it does
not itself cross-reference the man page, so Step 1 is where this measured
`p-N`=left / `pN`=right fact needs to be written down for the next reader).
The third/fourth lines confirm the width invariant holds for both a short
and a maximally-truncated-long command. The fifth line confirms the actual
bug-fix property: the command text is now immediately adjacent to whatever
precedes it in the string (`usage`), with the reserved blank cells relocated
to *after* the command instead of before it.

**What "adjacent" means precisely, once `usageSegment`'s own separator is
accounted for:** `usageSegment` returns `strings.Join(blocks, "  ") + "  "`
(`usage.go:163`) — a hardcoded 2-cell trailing separator — and
`paneSlot`'s content is `slotSafe(icon) + " " + slotSafe(cmd)`
(`main.go:299-300`), which contributes one further leading space whenever
`@active_pane_icon` is empty (the common case, e.g. `bash` with no icon set).
So the real post-fix residual between `usage`'s last visible glyph and the
command's first visible glyph is **2 cells with an icon, 3 without** — not
zero. The fifth repro line above shows 0 only because its `USAGE` stand-in
has no trailing spaces and uses a 1-char icon (`I`) with no leading-space
wrinkle. The fixed 13-cell dead run is what disappears; what's left is the
same 2-3 cell inter-segment spacing every other pair of line-0 segments
already uses (compare `dirDisplay`'s own `"  "` separators in `renderLine`)
— "adjacent" in the acceptance criteria means *that*, not "zero whitespace
at all."

**Trade-off, stated explicitly:** the command text ("bash") no longer sits
flush against the terminal's true right edge — there is now a
command-length-dependent blank strip between the command and the trailing
literal space / right edge, mirroring where the gap used to be on the other
side. This is unavoidable: `usage`-position-stability (needed to not
reintroduce #260), adjacency-to-`usage` (the acceptance criterion this issue
is about), and edge-flushness are mutually exclusive — a fixed-width box's
reserved slack has to render *somewhere* inside it, and it can only be on one
side of the content at a time. The acceptance criteria for #575 name
adjacency to the usage block, not edge-flushness, so this is the correct side
to give up.

**Two more esoteric alternatives were considered and rejected** as
unnecessary complexity for a one-character fix's problem:
- Moving a computed compensator *before* `usage` via tmux arithmetic
  (`#{e|-|:...}`, `#{w:...}`, `#{R: ,...}`) — this is what the constraint
  section above rules out (it moves the jitter, doesn't remove it), and it
  would add three tmux format modifiers this repo doesn't otherwise use, a
  new comma-argument-list safety analysis, and materially more test surface,
  all to reach a worse outcome than the one-character flip.
- Reworking `usageSegment`'s own trailing `"  "` or the pane slot's leading
  icon/space cell to further tighten the visual result — out of scope: the
  acceptance criteria ask for the *dead run* to go away, not for the minimum
  achievable inter-segment spacing to shrink further.

### Files

- `picker/statusline/main.go`:
  - `paneSlot` (`main.go:298-301`): add an `adjacentToUsage bool` parameter;
    select `#{p-%d:...}` (unchanged) when `false`, `#{p%d:...}` (no minus)
    when `true`.
  - `renderLine` (`main.go:315`): pass `usage != ""` as the new argument.
  - Update the doc comments on `paneSlotKeep`/`paneSlotPad` (`main.go:262-277`)
    and on `paneSlot` itself (`main.go:295-297`) to describe *why* the pad
    flips side (#575 — adjacency to `usage`, gated on `usage` being present)
    while the width invariant that matters for #260 is unchanged regardless
    of which side is chosen. Reference the measured pad-direction fact (`p-N`
    = left, `pN` = right) since a future reader will otherwise "fix" this
    back by matching the man page's inverted prose.
  - No other production code changes — `usageSegment` (usage.go) and
    `slotSafe` are untouched.
- `picker/statusline/main_test.go`:
  - `TestPaneSlot`, `TestPaneSlotEmptyIcon`, `TestPaneSlotStripsFormatChars`:
    add `false` as `paneSlot`'s new third argument (preserves today's
    left-pad expectation, `want` literals unchanged).
  - `TestRenderLineFull`, `TestRenderLineBridgeWinSuppressesDir`,
    `TestRenderLineBridgeHost`, `TestRenderLineBridgeStateDisconnected`: no
    change needed beyond what `renderLine` already does internally — they
    all pass `usage=""`, so `renderLine` now calls `paneSlot(..., false)`
    itself and the expected tail (`#{p-17:...}`) is unchanged.
  - **New: `TestPaneSlotAdjacentToUsage`** — a literal format-string test
    asserting `paneSlot(icon, cmd, true)` produces `#{p17:...}` (no minus),
    mirroring `TestPaneSlot`'s style for the `false` case, plus a second row
    with an empty icon (`paneSlot("", "bash", true)`, mirroring
    `TestPaneSlotEmptyIcon`'s rationale) since that's the common real render
    and the case with the extra leading-space wrinkle noted above.
  - **New: a `renderLine` test with a non-empty `usage` string**, asserting
    (as a literal format-string comparison, same style as the rest of the
    file) that `usage`'s output is immediately followed by
    `#[fg=...]#{p17:...}` (no minus — `adjacentToUsage=true` took effect)
    with no characters in between — the actual regression guard for #575 at
    the format-string level.
  - **New: a live-tmux test** (`TestPaneSlotPadDirectionLiveTmux`, matching
    the `remotebridge/cmd/daemon`'s
    `TestReflowRunShellArgsSurvivesFormatInjection` pattern **verbatim,
    including its fail-not-skip branch**: `exec.LookPath("tmux")` failing
    calls `t.Fatal` when `LAZYTMUX_REQUIRE_TMUX` is set and `t.Skip`
    otherwise, then `tmux -L <sock> -f /dev/null new-session -d`. This branch
    is load-bearing, not boilerplate: `picker/default.nix:72` lists
    `statusline` in `subPackages`, so the *plain* `picker` derivation — built
    by `nix build .#default`, with no tmux and no `LAZYTMUX_REQUIRE_TMUX` —
    also runs `go test ./statusline`; only `pickerChecked`'s overridden
    `checkPhase` (`flake.nix:118-132`, reached by `nix flake check`) sets
    `LAZYTMUX_REQUIRE_TMUX=1` and adds `mkTmux`. Wrap every rendered value in
    sentinel brackets exactly as the manual repro above does (e.g.
    `"[" + format + "]"`) and trim only the trailing `\n` from
    `exec.Command(...).Output()` — anything that also trims leading/trailing
    spaces (a stray `strings.TrimSpace`) would erase the exact cells this
    test exists to measure.

    Runs `paneSlot(icon, cmd, false)` and `paneSlot(icon, cmd, true)` through
    a real `tmux display-message -p -F` for a short command and a long
    (past-truncation-boundary) command, and asserts, on the **rendered**
    (not literal-format) output:
    - all four combinations expand to exactly `paneSlotPad` (17) display
      cells — the #260 guard, proving the width invariant holds at runtime
      for both pad directions, not just on paper;
    - for the short command: `adjacentToUsage=false` (`#{p-17:...}`) renders
      with **leading** blanks and **no** trailing blank (matches the plan's
      own repro at `[             bash]` above); `adjacentToUsage=true`
      (`#{p17:...}`) renders with **no** leading blank and **trailing**
      blanks instead (`[bash             ]`) — i.e. the rendered value equals
      `content + strings.Repeat(" ", paneSlotPad-len([]rune(content)))`
      exactly, phrased that way (not just "no leading blank") because with
      an empty icon `content` itself starts with its own single leading
      space (`#{l: bash}`), which must not be confused with pad-added
      blanks. This proves both pad directions actually behave as claimed on
      the pinned tmux — the fact the whole fix depends on, which the
      format-string literal tests can't observe (they'd pass identically
      regardless of which way `p17`/`p-17` actually pad, since that's
      runtime tmux behaviour, not something Go's string construction
      encodes).
    This is ASCII-only test input (no real icon), so Go can compare against
    `len([]rune(...))` directly without needing a nerd-font-aware width
    library — consistent with "tmux does the measuring, not Go".

## Steps

- [ ] **Step 1: add the `adjacentToUsage` parameter to `paneSlot`, wire
  `usage != ""` into its `renderLine` call site, update its doc comments and
  the `paneSlotKeep`/`paneSlotPad` comments** (picker/statusline/main.go)
- [ ] **Step 2: pass `false` at the three existing `paneSlot` call sites in
  main_test.go; add `TestPaneSlotAdjacentToUsage`; add the non-empty-`usage`
  `renderLine` adjacency test; add the new live-tmux
  `TestPaneSlotPadDirectionLiveTmux`** (picker/statusline/main_test.go)
- [ ] **Step 3: verify** — `go test ./statusline/...` from `picker/` (the
  live-tmux test needs `tmux` on `PATH`); render at a couple of synthetic
  client widths via a scratch `tmux -L <sock> -f /dev/null` server plus
  `display-message -p -F` with a real non-empty `usage` string and a real
  short pane command, confirming the fixed 13-cell dead run is gone and what
  remains between `usage`'s last visible glyph and the command's first is
  exactly `usageSegment`'s own 2-cell trailing separator (3 cells with no
  `@active_pane_icon` set) — not zero, and not the old ~13-cell run; run the
  full local gate (`nix build .#default`, `nix flake check`, `nix build
  .#lint`); capture the gate output for the PR body.
- [ ] **Step 4: commit**
  `docs/superpowers/plans/2026-09-08-575-statusline-usage-gap.md` alongside
  the code, per CLAUDE.md's "Plans and Specs".

## Acceptance mapping

- "no spurious blank run... at several client widths" — satisfied: the
  padding side flip removes the fixed ~13-cell dead run between `usage` and
  the pane command; what remains is `usageSegment`'s own 2-cell trailing
  separator (3 with no icon) — the same inter-segment spacing every other
  pair of line-0 segments already uses, not an additional avoidable gap —
  and the fix touches nothing that depends on terminal width
  (`#[align=right]`, which is what makes gap 1 legitimately width-dependent,
  is untouched).
- "segment set varying (monthly below/above threshold, one/several agents)" —
  `usage` is passed through unchanged from `usageSegment`; the fix only
  changes which side of `paneSlot`'s own fixed-width box the reserved blank
  cells render on, not how `usage` itself is built or where `usage` starts.
- "segment still vanishes entirely when no agent pane is running" — untouched:
  `usageSegment`/`agentsRunning` gating in `main()` is unmodified, and
  gating the pad flip on `usage != ""` means the `usage == ""` render is
  byte-for-byte identical to today's (`paneSlot(..., false)` emits the same
  `#{p-17:...}` it always has) — no behavior change at all when no agent is
  running.
