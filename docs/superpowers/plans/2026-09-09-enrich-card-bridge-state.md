# Plan — enrich card reads bridge state in mirror windows

Closes #598. Design: `docs/superpowers/specs/2026-09-09-enrich-card-bridge-state-design.md`.
Decomposition: `docs/superpowers/plans/2026-09-09-enrich-card-bridge-state-decomposition.md`.

Every step maps to exactly one decomposition component and stays inside its
`boundaries`. `consumers-frozen` is a boundary, not work: `scripts/tmux-reflow-windows.sh`,
`picker/main.go` and `picker/statusline/main.go` are not touched by any step.

## Wave 1 — parallel-safe (disjoint files, meeting only at fixed names)

### Step 1: extend the carried wire format and its sanitizers — `wire-format` (implement: escalated)

Files: `picker/remotebridge/daemon/windowlabels.go`, `windowlabels_test.go`. One
atomic edit; a partial one is a shifted row that stamps wrong values under the
*existing* nine option names.

- [ ] Add `const windowLabelFields = 19` and use it for `SplitN` in
  `parseWindowLabels`, replacing the literal `10`. The test fixture `oneRow`
  (`windowlabels_test.go:51`) hardcodes `make([]string, 10)` — point it at the
  same constant so the parser and the fixture cannot drift.
- [ ] Rewrite `windowLabelFormat` to the 19 positions in interface I1. Positions
  0–7 stay byte-identical; `@window_label_id`/`@window_label_rest_long` move to
  17/18 and stay trailing; each new field at 8–16 is wrapped
  `#{s/[|]/ /:…}`, and position 14 is
  `#{s/[|]/ /:#{?@worktree,#{@worktree},#{@git_root}}}` — the remote resolves
  `worktree || git_root`, the card must not.
- [ ] Add the nine fields to `labelRow` (keep it comparable — the unchanged-row
  check is a struct compare).
- [ ] Add `cleanLabelValueExact(v, maxRunes)`: same `stripWindowName` and same
  whole-value rejection of a leading `-` and of a bare `;` as
  `cleanLabelValue`, but returns `""` when the value exceeds the cap instead of
  truncating. Document why at the function: a truncated URL opens the wrong
  page, a truncated branch refreshes the wrong branch, a truncated path names a
  directory that is not the one on screen — and every consumer already renders
  absent correctly, so a silent wrong value is the worse failure.
- [ ] Add the validators: `issueIDRe` `^[A-Za-z0-9_-]+$`, `urlRe`
  `^https?://\S+$`, `prDraftRe` `^1$`, `branchRe` `^\S+$`, `dirRe` `^/\S*$`.
  Reuse `lowerWordRe` for the provider. Wire each field to its cleaner and cap
  per the spec's D2 table (identity fields exact; both titles truncating at
  `labelTextMaxRunes`).
- [ ] Add the nine `bridgeLabelOptions` entries. This is the whole of teardown
  and of the per-field diff — `clear` and `apply` both walk that one ordered
  list, so neither needs an edit.
- [ ] Rewrite the two comments this change makes false. `windowlabels.go:31-35`
  says `@window_label_rest_long` is "the one genuinely free-form field — so it
  goes last"; it becomes the one field *not* pipe-stripped remotely, kept last so
  its own `|` lands inside it rather than shifting the row, with the new
  free-form fields wrapped instead. `:239-240` says a bare mirror's first pass
  writes "nine `-u`"; that is eighteen now.
- [ ] **Re-index the existing per-field assertions, and make `rowField`
  exhaustive.** Widening `oneRow`'s fixture is not enough: the tests plant a
  value at an *index*, and `@window_label_id`/`@window_label_rest_long` move
  8/9 → 17/18. `rowField` (`windowlabels_test.go:119-131`) has a `default:` arm
  returning `r.labelRest`, so after the move the lone-`;` table (`:112-116`,
  over indices 1/7/8/9) and the leading-`-` case (`:104-107`, index 9) would
  both read an empty field and **pass vacuously** — the suite stays green while
  the two drop rules that exist because `cfg.LocalTmux` execs without a shell
  (`windowlabels.go:129-135`) lose their coverage completely. Re-point every
  index at its new position, and replace the `default:` arm with explicit cases
  plus a `t.Fatalf` on an unmapped index, so the next field addition cannot
  repeat this silently.
- [ ] Extend the lone-`;` and leading-`-` tables to the two new **truncating**
  title fields. They have no validator to fall back on, so `cleanLabelValue`'s
  whole-value drops are their only guard.
- [ ] Tests: extend `TestParseWindowLabels` for a 19-field row and a short row;
  per-field cases through `oneRow` for each new validator, including
  drop-not-truncate (a 513-char URL → `""`, not a cut URL) and truncate-not-drop
  for the titles; a row where a title contains `|` after the remote
  substitution has already run. `TestLabelShipperApply`/`Clear`/
  `RestampsRebuiltMirror` count against `len(bridgeLabelOptions)` and
  self-adjust — confirm they still pass rather than editing them.

### Step 2: the `enrich-refresh` ctl verb and the protocol bump — `ctl-verb` (implement: escalated)

Files: `picker/remotebridge/daemon/ctl.go`, `ctl_test.go`,
`picker/remotebridge/wire/protocol.go` (the version constant only).

- [ ] Bump `wire.CtlProtocolVersion` `"4"` → `"5"`. Required by that constant's
  own contract (`protocol.go:29-33`) for any verb-table change.
- [ ] Add `enrichRefreshScript(win string) string`, a sibling of
  `toolResolveScript`: POSIX body, **zero `'` characters**, PATH restored from
  `tmux show-environment -g PATH` guarded on `PATH=?*`, parameter expansion
  spelled `${p#*=}` only, every other literal `#` doubled (`run-shell`
  format-expands the whole string first). It resolves the remote window's own
  `@branch` and `@worktree`/`@git_root` via `tmux show-options -wqv -t <win>`,
  then **guards both** — `[ -n "$b" ] && [ -n "$d" ] || exit 0` — before running
  `tmux-pr-enrich --target <win> --branch "$b" --dir "$d" --force`.
- [ ] That guard is not defensive padding; both empties are live bugs. An empty
  branch falls *through* the poller's single-target guard
  (`tmux-pr-enrich.sh:463`) into tick mode, where `force=1` skips the age gate,
  touches the remote's `.last-tick` and detaches a whole-server `run_full_pass`
  (`:405`) — one keypress enriching every window on the host. An empty dir skips
  the conditional `cd` (`:234`), so `gh pr list --head "$b"` runs in the remote
  tmux server's cwd and can write another repo's PR onto the remote window's own
  `@pr_*`, which then ships home as `@bridge_pr_*`. Put the reason in the
  comment — a future reader will otherwise read the guard as redundant with the
  card's `[r] no branch` footer state, which reads a different value on a
  different host.
- [ ] Add the `verbs` entry: `args: 0`, no `windows`/`layout`/`moves` (it
  changes no remote structure — the result comes back as a
  `%subscription-changed` label row, never a reconcile), no `needsView`/`probe`.
  `build` uses `pane` for `run-shell -b -t` and `win` for the target.
- [ ] **Target `win`, never `<sess>:<win>`.** `Config.RemoteSession` may contain
  spaces (`daemon.go:34`) and would cross run-shell's expansion, two
  `tmuxQuote` layers and `sh`; quoting it needs exactly the single quotes this
  body bans. Say so in a comment.
- [ ] Tests: a `TestParseCtlVerbTranslation` case asserting the built command
  targets `win`, that the **body** contains no `'` (not the whole command — the
  two `tmuxQuote` wrap layers necessarily add them at its boundaries, which is
  what the sibling `TestToolVerbBuildsRemoteFloatInRemoteCwd` and
  `TestThemeVerbBuildsSilentRemoteApply` already assert), and — the important one
  — that the body contains **both** `[ -n "$b" ]` and `[ -n "$d" ]`. Nothing
  under `nix flake check` ever executes this script against a real remote
  `run-shell`, so a substring assertion is the only regression net that guard
  will ever have, and the hazard it prevents is a whole-server pass plus a
  wrong-repo write. Same shape as the no-`'` assertion beside it. Plus a
  `TestParseCtlRejects` case for a stray argument.

### Step 3: the card reads bridge state in a mirror — `card-read`

Files: `picker/enrichcard/options.go`, `options_test.go`, a new
`picker/enrichcard/bridge.go`, `main.go` (base-branch guard and construction),
and `picker/enrichcard/model.go` — forced, not optional: `readWindowState`
changes return type, and it has three call sites (`model.go:241`, `model.go:247`
in `Update`'s `tickMsg`/`refreshDoneMsg` branches, and `main.go:33`). The
decomposition already permits `model.go` for this component (`winState`
consumers); `handleKey`/`footer`/`refreshCmd` remain Step 4's. Does **not** wait
on Step 1 — it depends on the option *names*, and an absent name is already the
card's documented fallback.

- [ ] Parse into `winOpts{ local, bridge winState; mirror bool }` — the bridge
  side reuses `winState` rather than growing eighteen parallel fields, because
  the mapping is 1:1 (`@bridge_issue_id` → `bridge.issueID`, and so on) and it
  makes `resolve` a choice between two values of one type instead of a
  field-by-field copy. `readWindowState` keeps its single
  `show-options -w -t <target>` fork and returns `winOpts`; `mirror` is
  `@bridge_win == "1"`.
- [ ] New pure `resolve(winOpts) winState` in `bridge.go`: mirror → `bridge`
  with **no fallback** to the local names; otherwise `local`, byte-identical to
  today. `@bridge_dir` arrives already resolved as `worktree || git_root`, so it
  lands in `bridge.worktree` and `bridge.gitRoot` stays empty — the existing
  `worktree || gitRoot` chain in `branchBlock`/`refreshCmd` then reads it
  correctly with no special case. This mirrors `bridgeOpt`
  (`config/tmux.conf.nix:346`), the established semantics for the same question.
- [ ] **`mirror` persists on `model`**, not only inside `winOpts`: `main.go`
  needs it to skip `detectBaseBranch` at construction, Step 4's footer table
  needs it on every render, and both `Update` call sites re-read options every
  tick — so `model` gains a `mirror bool` set at construction and refreshed from
  `winOpts.mirror` on each read. Both those call sites become
  `opts := readWindowState(…); m.win, m.mirror = resolve(opts), opts.mirror`.
- [ ] The three always-local fields are carried over onto the resolved value:
  `task`, `claudeAgo`, `paneIcon` come from `local` in both modes. Note in the
  comment that `paneIcon` is separately dead — `@active_pane_icon` is stamped
  session-scoped (`tmux-update-icons.sh:470`) and this read is `-w` only — which
  is pre-existing and deliberately not fixed here.
- [ ] `@window_task`, `@window_claude_ago` and `@active_pane_icon` stay local —
  already bridge-correct via `agentShipper` and `@bridge_proc`. Comment it so
  it reads as a decision.
- [ ] `main.go`: skip `detectBaseBranch` when in mirror mode. Its `git -C <dir>`
  would run against a path on the *remote*; the malignant case is that the same
  path exists locally as an unrelated repo, and the card then shows a base
  branch from the wrong checkout.
- [ ] Tests: `resolve()` over three fixtures — mirror with full bridge state,
  bare mirror (every field empty, and specifically **not** the local residue),
  non-mirror (unchanged). Extend `TestParseWindowOptions` for the new names.

## Wave 2 — after wave 1

### Step 4: route `[r]` to the remote — `card-refresh`

Files: `picker/enrichcard/model.go`, `main.go`, `model_test.go`. Sequenced after
Step 3 for two reasons: they share those three files, and Step 3 creates the
`model.mirror` field this step's footer table reads — a genuine data dependency,
not only a file collision.

- [ ] `cfg` gains `bridgeCtlBin`, `bridgeSock`, `bridgePane`; register the three
  flags in `main.go`.
- [ ] New `bridgeRefreshCmd`: a `tea.Cmd` (already a goroutine, so the UI never
  blocks) that runs `<bridgeCtlBin> --sock <sock> enrich-refresh <pane>` with
  **combined output captured** — never inherited. The card is a bubbletea
  altscreen program and an uncaptured child paints over it; `--display-error`
  is deliberately unused because it needs a tmux client name this process does
  not have. Bounded by the ctl's own `overallTimeout` of 2 s.
- [ ] It returns a msg carrying the error text, if any. On success flash
  `refresh sent ↗`; on failure flash the ctl's own message (`bridge daemon
  unreachable`, `this bridge daemon does not speak the ctl protocol — reopen the
  bridge`, `unknown verb …`). Never an unconditional success flash — that would
  be a false success, worse than the silent no-op the task forbids, and the
  protocol bump makes it the common case for the first press after this lands.
- [ ] **Give `flash` an expiry the tick honours.** `Update`'s `tickMsg` branch
  currently does `m.flash = ""` unconditionally on a 1 s `tickCmd()`, so a
  message that lands at an arbitrary phase — and the ctl is bounded at 2 s, so
  it will — is wiped 0-1000 ms later. Unreadable, which would gut the whole
  point of reporting the ctl's real outcome. Store a deadline alongside the text
  and let the tick clear it only once passed. A `time.Time` deadline, never a
  "clear after one tick" counter — `tickCmd` is a 1 s `tea.Tick` whose phase
  relative to the keypress is arbitrary, so a counter reproduces the same
  0-1000 ms window. Durations come from the house precedent, not invention:
  **5 s for a ctl error**, matching the ctl's own `display-message -d 5000`
  (`cmd/ctl/main.go:67`), and **2 s** for `opened ↗` / `refresh sent ↗`.
  `refreshDoneMsg` already leaves `flash` alone, which fits a deadline model.
  This also repairs `opened ↗`, which has the same 0-1000 ms lifetime today —
  the same field, the same fix, so scoping the repair to the error path only
  would leave one field with two lifetimes for no gain. The spec's
  "no behaviour change on a non-mirror window" criterion has been narrowed to
  admit this deliberately, rather than the work quietly failing its own gate.
- [ ] `footer`/`handleKey` per the spec's four-row table: non-mirror unchanged
  (blocking local poller + spinner); mirror with no branch → `[r] no branch`,
  which stays inert on the bridged path deliberately — it is the local half of
  Step 2's guard, and removing it is how the whole-server fall-through gets
  provoked;
  mirror with no ctl handle → `[r] no bridge`, inert; mirror with a handle →
  `[r] refresh`, the ctl route, no spinner (nothing to converge on).
- [ ] Add a `sending` flag on the bridged path — set on dispatch, cleared by the
  result msg — so held presses cannot queue remote `--force` passes (each is a
  `gh` call on the remote). Distinct from `refreshing`, which `prBlock` renders
  as `⧗ #N refreshing…` and would misdescribe a local socket write as a PR
  refresh.
- [ ] Tests: the four `[r]` states over `model`, that the bridged path builds the
  expected argv, and that a second press while `sending` is a no-op.

### Step 5: pass the ctl handle from the bind — `bind`

File: `config/tmux.conf.nix`, the `floatBind "i" floatCard …` block only.

- [ ] Add `--bridge-ctl-bin '${picker-bridge-ctl-bin}'`,
  `--bridge-sock '#{@bridge_sock}'`, `--bridge-pane '#{@bridge_pane}'`.
- [ ] It stays a plain `floatBind` — **not** `bridgeGate`d, **not**
  `bridgedFloatTool`. The card has nothing to run on the remote, and launching
  it there would put `[o]`/`[p]`'s `xdg-open` on a machine with no display.
  Leave a comment saying that, since the next reader will ask.
- [ ] `@float_geom` and `remain-on-exit off` keep coming from `mkFloat`
  (`float-conf-assertions` fails the build otherwise).

### Step 6: extend the m2 integration fixture — `integration-fixture`

File: `tests/remote-m2-integration.bats`, the "daemon ships the remote's window
labels onto the mirror windows" case only.

- [ ] Stamp the nine new options on the source server and assert their
  `@bridge_*` copies on the mirror, including a title carrying a literal `|`
  (which must arrive as a space, proving the remote substitution) and a URL.
- [ ] The existing `bare_labels` assertion already proves a bare mirror holds no
  `@bridge_*` label option, so the new names' unset coverage comes free — do not
  add a second bare-mirror case.
- [ ] This check has two known flake modes (green-TAP-nonzero-exit; darwin
  reconnect cases). A failure here gets read before it is attributed to this
  change.

## Wave 3

### Step 7: docs — `docs`

- [ ] `CLAUDE.md`: extend "Remote Window Labels" with the carried set and the
  two cleaning policies; note the card's mirror-mode rule and that
  `detectBaseBranch` is skipped there; add `enrich-refresh` where the ctl verbs
  are listed; add `tmux-pr-enrich` to "What the Remote Host Needs on PATH" for
  the bridged `[r]`.
- [ ] Fix the `tmux-update-icons` Script Roles row (`CLAUDE.md:49`), which reads
  "Sets `@window_icon_display` …, `@window_icon_padded` …, `@active_pane_icon`
  **per window**". The third is session-scoped
  (`scripts/tmux-update-icons.sh:470`, `set -q -t '$s'`, and that script's own
  comment at `:193`). In scope because Step 3 relies on that fact and this file
  is already being edited — leaving the row contradicting the new comment is how
  the next reader gets it wrong.
- [ ] Commit this plan, the decomposition and the design spec alongside the
  code, per CLAUDE.md.

## Gate

`nix build .#default`, `nix flake check`, `nix build .#lint` — all three, none
subsumes another. Then `/deslop`, push, PR.

## Hardware verification

The m2 checks assert dims, not content, and are historically blind to this
class (#196, #181). Drive a real bridge to a host with an enriched remote
window, press `prefix + i`, and capture the float. The PR body states plainly
what was verified by hand and what was not — including that a live bridge
predating this change must be reopened before `[r]` works, a direct consequence
of the protocol bump.
