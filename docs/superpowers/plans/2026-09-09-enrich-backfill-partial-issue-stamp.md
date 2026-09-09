# Plan: backfill a partial issue stamp instead of stranding it (#599)

_Revision 3 — incorporates plan-critic findings B1-B5 (revision 2) and the
Step-4 explicit-id wipe finding from the revision-2 re-review (revision 3),
all verified against the live code before being accepted._

## Problem

`tmux-issue-stamp` is a one-shot worktrunk `post-switch` hook. Its Linear (and
GitHub) provider derives `@issue_id` from the branch regex with no CLI needed,
then separately enriches `@issue_title`/`@issue_url` via CLI calls bounded by
`timeout 15`. If those calls fail or time out, the script still exits 0 having
written the id but leaving title/url empty — permanently, since nothing ever
revisits that window. `prefix + i` then renders a blank title and `[o] issue`
opens nothing, with no recovery short of closing the window.

## Design decision

Add a self-contained `--backfill` / `--backfill-run` tick mode to
`scripts/tmux-issue-stamp.sh` itself, wired into `status-format[0]` beside the
existing `tmux-pr-enrich --tick` / `tmux-agent-usage --tick` calls.

Rejected alternatives:
- **Fold into `tmux-pr-enrich.sh`**: it's already a per-window tick poller, but
  it is the *PR* poller — grouping windows by repo and running `gh` calls.
  Adding issue-provider (`linear`) calls widens its job and its already-large
  file (487 lines) for a concern it doesn't own.
- **`r`-key-only recovery**: cheapest, but leaves a window silently wrong
  until the user notices and presses it — fails acceptance criterion 1
  outright (no auto-recovery).

The chosen design mirrors precedent already in the repo:
`tmux-agent-usage` lives in its own script despite being a fellow
`status-format[0]` poller, specifically so its concern (agent usage) doesn't
bleed into a neighbor's (PR enrichment). Issue-identity backfill gets the same
treatment.

## Bounding retries (criterion 3)

Every stamp write (post-switch, explicit-id, branch-transition trigger, and
now backfill) already goes through one code path in `tmux-issue-stamp.sh`.
That path is extended to track a `@issue_backfill_tries` window option:
- id resolved AND title+url both non-empty → unset the counter (success).
- id resolved but title or url still empty → increment the counter — **unless
  the branch just changed underneath this window** (see B4 fix below), in
  which case the counter resets to 1 (this is attempt #1 on the new branch,
  not a continuation of the old branch's failures).
- no id resolved at all → unset the counter (existing full-clear path).

The backfill sweep filters out any window whose counter has reached
`BACKFILL_MAX_TRIES` (5). This is a plain attempt cap, not exponential
backoff — simpler to reason about and test, and a host permanently missing
`linear`/`gh` on PATH converges to "stopped retrying" within
`5 * BACKFILL_TICK_SECONDS` (~2.5 min) of the first partial stamp, not
forever. Chosen over backoff because the failure modes here (missing CLI,
CLI auth expired, branch has no such issue) are static conditions that don't
benefit from spaced retries.

## Criterion 4 (no regression, lock still honored)

The per-window `acquire_lock` guard is untouched and sits above all dispatch
(explicit-id vs branch-derived vs the new backfill trigger) — a backfill
re-invocation that races a live stamp just sees the lock held and exits via
the existing `stamp_skip_locked` no-op path. No new locking code needed. The
new `--backfill`/`--backfill-run` flag dispatch sits **above** the
positional-arg parsing that leads to the lock, exactly like the existing
`explicit_id`/branch-derived dispatch already does — the sweep's *recursive*
per-window calls are what go through the lock, not the sweep dispatch itself.

## Fixes from plan-critic review (B1-B5, all verified in-code)

**B1 — `nix flake check` does not glob `tests/*.bats`.** Every suite is wired
as its own `pkgs.runCommand` derivation in `flake.nix` (e.g. `issue-stamp-tests`
at line 669). `tests/issue-backfill.bats` needs the same treatment or it never
runs. Added to Files/Steps below.

**B2 — Explicit-id windows lose their id on backfill.** `claude-status-update
enrich <ID>` (`scripts/claude-status-update.sh:359-390`, the #137 mid-session
path) calls `tmux-issue-stamp "$target" "$worktree" "$branch" "$explicit_id"`
where `$branch` is the real current branch — which, by design, does **not**
encode the issue (that's the whole reason explicit-id mode exists). If that
write lands partial (id set, title/url empty from a transient CLI failure)
and the backfill sweep re-invokes with only 3 args, branch-derived resolution
finds nothing and the "no id matched" branch **wipes the correct id**. Fix:
persist the raw `$4` value as a new window option `@issue_explicit_id` in the
explicit-id branch (unset it in the branch-derived branch and in the
full-clear branch), carry it as a sweep field, and pass it back as arg 4 on
recursive invocation when present.

**B3 — An unresolvable checkout dir wipes a valid stamp.** The sweep as
originally scoped had no check that `@worktree`/`@git_root` still resolves to
a real directory (a reclaimed worktree, window still open). Without it, a
recursive call with `dir=""` makes both providers return empty (github: empty
`git remote get-url` origin fails the `*github.com*` check; linear: skips the
CLI dir-scoped calls), and the clear branch wipes the existing correct stamp.
Fix: skip any sweep candidate whose resolved dir (`${wt:-$gr}`) doesn't exist
on disk (`[[ -d $d ]]`) — lighter than `tmux-pr-enrich.sh`'s full
`git rev-parse --git-common-dir` check, but sufficient: the existing one-shot
path already trusts `$worktree` at face value with no deeper validation
(`tmux-issue-stamp.sh:50-53`), so this matches the file's existing standard.

**B4 — Stale tries counter survives a branch change.** The same window is
re-stamped on an in-place branch transition
(`scripts/tmux-update-icons.sh` branch-change trigger) and a worktree re-tag
(`scripts/tmux-reconcile-window.sh:59-60`), with no explicit id. A window that
exhausted 5 tries on branch A and then moves to branch B would inherit
tries=5→6 on B's very first write — B gets zero backfill attempts. Fix: read
the **old** `@issue_branch` value before overwriting it; if it differs from
the incoming `$branch`, treat the current attempt as attempt #1 (reset the
counter) instead of incrementing the inherited count.

**B5 — The `issue-stamp.bats` fake tmux can't express multi-window
scenarios.** Its `display-message` fake hardcodes window_id to `@1` for every
target, and `show-options`/`set-option` key state on the option name alone,
ignoring `-t` (`tests/issue-stamp.bats:20-36`) — every synthetic window in
that harness shares one namespace and one lock. `tests/issue-backfill.bats`
needs its own fake `tmux` that derives a per-target key from the `-t` argument
(so distinct targets get distinct window ids, option namespaces, and locks)
and answers `list-windows -a -F` from a fixture file the test writes. Detailed
in Step 5 below.

## Files

- `scripts/tmux-issue-stamp.sh` — backfill tick mode + tries counter + branch-
  change reset + explicit-id carry-through.
- `config/tmux.conf.nix` — wire the tick into `status-format[0]`; wire
  `--issue-stamp-bin` into the `prefix+i` card invocation; fix the now-stale
  "three tickers" comment (N6).
- `picker/enrichcard/main.go`, `picker/enrichcard/model.go`,
  `picker/enrichcard/model_test.go` — `r` refreshes issue identity too.
- `tests/issue-backfill.bats` (new) — backfill sweep behavior, own fake tmux.
- `flake.nix` — new `issue-backfill-tests` check derivation (B1).
- `tests/issue-stamp.bats` — no changes expected (verified: no existing
  assertion enumerates the full `setlog` or forbids extra option writes), but
  re-run after Step 1 to confirm.

## Steps

- [ ] **Step 1: `tmux-issue-stamp.sh` — tries counter + branch-change reset +
  explicit-id carry-through**
  In the existing "id found" write branch:
  - Before overwriting `@issue_branch`, read the old value:
    `old_branch="$(tmux show-options -t "$target" -wqv @issue_branch 2>/dev/null)"`.
  - After writing `@issue_branch "$branch"`, decide the counter:
    ```
    if [[ -n $title && -n $url ]]; then
        tmux set-option -t "$target" -wu @issue_backfill_tries 2>/dev/null
    else
        if [[ $old_branch == "$branch" ]]; then
            tries="$(tmux show-options -t "$target" -wqv @issue_backfill_tries 2>/dev/null)"
            [[ $tries =~ ^[0-9]+$ ]] || tries=0
        else
            tries=0
        fi
        tmux set-option -t "$target" -w @issue_backfill_tries "$((tries + 1))"
    fi
    ```
  - Also write `@issue_explicit_id`: `if [[ -n $explicit_id ]]; then tmux
    set-option -t "$target" -w @issue_explicit_id "$explicit_id"; else tmux
    set-option -t "$target" -wu @issue_explicit_id 2>/dev/null; fi`.
  In the existing "no id matched" clear branch, add both
  `@issue_backfill_tries` and `@issue_explicit_id` to the options unset.

- [ ] **Step 2: `tmux-issue-stamp.sh` — backfill sweep + tick gate**
  Add `BACKFILL_TICK_SECONDS=30`, `BACKFILL_MAX_TRIES=5`, and
  `BACKFILL_SWEEP_CAP=20` (defensive cap on candidates per sweep, mirroring
  `tmux-pr-enrich.sh`'s 30-branch cap — N3) constants near the top.

  Add a `run_backfill_pass` function (defined before it's called, right after
  the constants, since bash needs the definition executed before the call
  site further down) that:
  ```
  mapfile -t windows < <(tmux list-windows -a -F \
      '#{session_id}:#{window_id}|#{@issue_id}|#{?#{@issue_title},1,}|#{?#{@issue_url},1,}|#{@issue_branch}|#{@issue_explicit_id}|#{@worktree}|#{@git_root}|#{@bridge_win}|#{@issue_backfill_tries}' \
      2>/dev/null)
  ```
  Note the title/url fields are presence-booleans (`#{?#{@issue_title},1,}`),
  not the raw text — `sanitize_title` (`lib-enrich.sh:66-73`) strips CR/LF/ESC
  but not `|`, so a title containing a literal pipe would corrupt the `-F`
  parse; since the sweep only needs emptiness, never the text, this sidesteps
  the risk entirely (N2). Branch/worktree/git_root stay literal, matching the
  existing precedent in `tmux-pr-enrich.sh:405`'s own `-F` format (same
  theoretical risk, already accepted there).

  For each line: parse `tgt|id|has_title|has_url|br|explicit_id|wt|gr|bw|tries`.
  Skip when: `id` or `br` empty; `bw == 1`; `has_title == 1 && has_url == 1`
  (already complete); `tries` (default 0 if unset/non-numeric) `>=
  BACKFILL_MAX_TRIES`; resolved dir `d="${wt:-$gr}"` is empty or not a real
  directory (B3); total candidates already at `BACKFILL_SWEEP_CAP`. Otherwise
  background-invoke: `if [[ -n $explicit_id ]]; then "${BASH_SOURCE[0]}" "$tgt"
  "$d" "$br" "$explicit_id" & else "${BASH_SOURCE[0]}" "$tgt" "$d" "$br" & fi`
  (B2). `wait` at the end of the function.

  Before the existing positional-arg parsing (`target="${1:-}"`), add dispatch:
  - `--backfill`: `mkdir -p "$ENRICH_CACHE_DIR" 2>/dev/null` (N5 — must
    precede the `touch`, mirroring `tmux-pr-enrich.sh:481`), then the cheap
    `.last-backfill-tick`-file TTL gate (`$ENRICH_CACHE_DIR/.last-backfill-tick`
    — reuses the PR poller's own cache dir constant rather than the lock dir,
    matching the `tmux-pr-enrich`'s `.last-tick` precedent at lines 474-478
    exactly), then `detach "${BASH_SOURCE[0]}" --backfill-run`; exit 0.
  - `--backfill-run`: call `run_backfill_pass` directly; exit 0.

- [ ] **Step 3: wire the tick + the `r`-refresh bin into `config/tmux.conf.nix`**
  In the `status-format[0]` string (~line 1115), add
  `${lib.optionalString enrichEnable "#(echo; ${script.tmux-issue-stamp}/bin/tmux-issue-stamp --backfill)"}`
  alongside the existing `tmux-pr-enrich --tick` / `tmux-agent-usage --tick`
  segments. In the `prefix+i` card bind (~line 952-963), add
  `--issue-stamp-bin '${script.tmux-issue-stamp}/bin/tmux-issue-stamp'` to the
  `tmux-enrich-card` invocation args. Update the comment at line 1106
  ("The three tickers below") to drop the specific count (N6) since there are
  now four (update-icons is unconditional, pr-enrich/agent-usage/issue-stamp
  are each conditional).

- [ ] **Step 4: `picker/enrichcard` — `r` refreshes issue too**
  **Corrected in revision 3**: the card reads live window options
  (`options.go:21-77`) and `r` is enabled whenever `m.win.branch != ""`
  (`model.go:270`) — `@branch` is written unconditionally by
  `tmux-issue-stamp.sh:50-53`, including on explicit-id windows. So pressing
  `r` on an explicit-id window (stamped via `claude-status-update enrich
  <ID>`, #137) is reachable, and a 3-arg branch-derived re-invocation would
  hit the exact same wipe hazard as B2 — the branch doesn't encode the issue,
  no provider matches, and the clear branch unsets everything. This must be
  fixed the same way B2 was fixed for the sweep, not assumed away.

  `picker/enrichcard/options.go`: add `issueExplicitID` to `winState` and a
  `case "@issue_explicit_id":` arm in `parseWindowOptions` that sets it (same
  pattern as the existing `@issue_id`/`@issue_title` arms).

  `main.go`: add `flag.StringVar(&c.issueStampBin, "issue-stamp-bin",
  "tmux-issue-stamp", "path to the issue-identity stamp binary")` and the
  `issueStampBin` field on `cfg`.

  `model.go`: extract a small pure helper, e.g. `issueStampArgs(target, dir,
  branch, explicitID string) []string`, returning `{target, dir, branch}` and
  appending `explicitID` when non-empty — the same 3-vs-4-arg shape as Step
  2's sweep. Change `refreshCmd` to run both the issue-stamp call
  (`exec.Command(c.issueStampBin, issueStampArgs(c.target, dir, w.branch,
  w.issueExplicitID)...)`) and the existing PR-enrich call concurrently (two
  goroutines + a `sync.WaitGroup`), returning `refreshDoneMsg{}` once both
  finish. Guard on `c.issueStampBin != ""` so a test harness that doesn't set
  the flag doesn't attempt an empty exec.

  `model_test.go`: add `issueStampBin: "/bin/true"` to `testCfg()`, plus a
  unit test on `issueStampArgs` asserting the 4th element is present iff
  `explicitID` is non-empty (testable without exec, per the critic's note).

  Accepted-with-notes from the final review round: guard the issue-stamp
  goroutine on `dir != ""` (a reclaimed worktree would otherwise let `r` wipe
  a valid stamp the same way B3 guards the sweep — this is a new data-loss
  path the `r`-refreshes-issue feature itself introduces, since today `r`
  only ever touches the PR poller); guard on `c.issueStampBin != "" &&
  w.branch != ""` (belt-and-braces alongside the handler's existing branch
  check).

  Note (N1, accepted as-is): `tmux-issue-stamp.sh` already kicks its own
  `@pr_enrich@ --force` in the background when it resolves an id
  (`tmux-issue-stamp.sh:119`), so a manual `r` press launches two concurrent
  `--force` PR fetches (the card's own PR leg, and the one the issue-stamp
  leg kicks). Both are idempotent writes to the same cache/target — harmless
  double work, not a correctness bug — call this out in the PR body rather
  than adding sequencing complexity to avoid it.

- [ ] **Step 5: tests — `tests/issue-backfill.bats` (new)**
  A dedicated fake `tmux` (NOT reusing `issue-stamp.bats`'s, per B5) that:
  - Derives a filesystem-safe key from the `-t`/`$3` target argument (e.g.
    `tr -c 'A-Za-z0-9_' '_'`) and uses it to namespace `display-message
    '#{window_id}'` (returns `@<key>`, so distinct targets get distinct
    window ids and thus distinct lock dirs) and every `opt_<key>_<option>`
    state file for `show-options`/`set-option`.
  - Answers `list-windows -a -F ...` by `cat`-ing a fixture file
    (`$STATE/windowlist`) the test writes with literal lines matching the
    exact `-F` format the script requests.
  - Reuses fake provider scripts in the same style as `issue-stamp.bats`
    (branch-pattern-matched canned responses), namespaced per test as needed.
  - **`chmod +x` the generated stamp script**, once in `setup()`, before any
    invocation — `issue-stamp.bats:99-107` builds its copy with
    `sed ... >"$STAMP"` and never chmods it, which is masked there because
    every test invokes it via `bash "$STAMP" ...` explicitly. This harness's
    `run_backfill_pass` execs `"${BASH_SOURCE[0]}"` directly for the
    per-window recursion, and `--backfill` itself execs it via `detach`
    (`nohup "$@"`, `lib-log.sh:73-75`) — both need the exec bit. A missing one
    means "permission denied", a silently empty provider log, and several
    negative-assertion cases (2, 3, 4, 6 below) passing for the wrong reason.
    Case 1 (a positive assertion that the provider *was* called) is the
    control that would catch this.

  Cases:
  1. A window with `@issue_id` set, title/url empty, tries=0, on a branch the
     fake provider now resolves fully → after `--backfill-run`, its
     `opt_<key>_@issue_title`/`@issue_url` are populated and
     `opt_<key>_@issue_backfill_tries` is unset.
  2. A window at `tries=5` (== cap) → after the sweep, its provider is never
     invoked (assert the provider log has no entry naming that window's
     branch) and its stored state is unchanged.
  3. A bridge window (`@bridge_win=1`, otherwise partial) → never swept
     (provider log has no entry for it).
  4. A window with both title and url already set → never swept (no
     provider call).
  5. An explicit-id window (`@issue_explicit_id` set, partial, branch that
     would NOT resolve on its own) → after the sweep, the provider is invoked
     in explicit mode again (asserted via its argv) and the id is **not**
     wiped (B2 regression test).
  6. A window whose `@worktree`/`@git_root` point at a nonexistent directory
     (partial, otherwise eligible) → never swept, stamp unchanged (B3
     regression test).
  7. A window whose `@issue_branch` differs from a fresh incoming branch
     conceptually — implemented as: pre-seed `tries=5` under the *old*
     branch's option state, then run the one-shot script directly (not via
     the sweep) with a *different* branch and a provider that returns partial
     for it; assert the resulting `@issue_backfill_tries` is `1`, not `6`
     (B4 regression test — this one exercises `tmux-issue-stamp.sh`'s normal
     positional path, not the sweep, since that's where the branch-compare
     logic lives).

- [ ] **Step 6: wire the new test into `flake.nix` (B1)**
  Add an `issue-backfill-tests` derivation immediately after
  `issue-stamp-tests` (~line 677), copied verbatim from that pattern:
  ```nix
  issue-backfill-tests =
    pkgs.runCommand "issue-backfill-tests" {
      nativeBuildInputs = [pkgs.bats pkgs.coreutils];
    } ''
      cp -r ${./scripts} scripts
      cp -r ${./tests} tests
      bats tests/issue-backfill.bats
      touch $out
    '';
  ```
  Confirm it's picked up by whatever aggregates `checks.<system>` (the same
  mechanism that already exposes `issue-stamp-tests`) — no separate wiring
  should be needed if it follows the exact sibling pattern.

- [ ] **Step 7: re-run `tests/issue-stamp.bats` after Step 1** to confirm no
  existing assertion broke (expected: none do, per the file-list note above).

## Verification

Run all three (none subsumes another), per `CLAUDE.md`:
```
nix build .#default
nix flake check
nix build .#lint
```
`nix flake check` runs `tests/issue-backfill.bats` (via the new
`issue-backfill-tests` check) and `tests/issue-stamp.bats` (via
`issue-stamp-tests`). The `picker/enrichcard` Go tests run as part of the base
`picker` derivation's default per-subpackage `checkPhase` under
`nix build .#default` (N7 — not via `picker-go-tests`, which overrides its
checkPhase to a narrower subpackage list that excludes `enrichcard`).

## Out of scope

- No new `programs.lazytmux.enrich.*` Nix option for the tick interval or
  cap/sweep-size — all hardcoded constants in the script, matching the scope
  of a standard-tier fix.
- `scripts/tmux-issue-stamp-linear.sh`, `scripts/tmux-issue-stamp-github.sh`,
  `scripts/lib-enrich.sh` are untouched — the retry/cap logic lives entirely
  in the dispatcher (`tmux-issue-stamp.sh`), not the providers.
- Does not address #596 (re-stamp when a pane moves to another repo) — a
  different trigger (same window, different repo). Both converge on the same
  `tmux-issue-stamp.sh` write path and lock, so no collision expected.
- A sweep holding a window's lock when the user presses `r` makes that manual
  refresh a silent no-op for the issue leg (existing `stamp_skip_locked`
  path) — not in the acceptance criteria, worth one sentence in the PR body
  (N8) but not a fix in itself, since making it wait would need a blocking
  variant of `acquire_lock` this codebase doesn't have.
- Each backfilled window that resolves an id also kicks its own `--force` PR
  fetch (`tmux-issue-stamp.sh:119`, pre-existing behavior) — with
  `BACKFILL_SWEEP_CAP=20` that's up to 20 forced `gh` calls in one sweep until
  the tries cap drains the candidate set. Bounded by the same cap that bounds
  everything else here; worth a one-line mention in the PR body alongside the
  N1 double-PR-fetch note, not a design change.
