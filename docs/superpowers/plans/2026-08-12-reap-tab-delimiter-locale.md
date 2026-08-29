# Plan: locale-proof the tmux `-F` field delimiters

Design: `docs/superpowers/specs/2026-08-12-reap-tab-delimiter-locale-design.md`.
Fixes #373 and #366.

One-sentence mechanism: tmux rewrites a literal TAB to `_` in `-F` output when
the querying client has no UTF-8 locale, so `claude_reap_dead_panes` marks no
pane live and deletes every agent state file — which in the nix sandbox happens
on one second in five and silently skips the `@remux_relaunch` stamper.

Steps are ordered so the repo is never left red between them: the product fix and
every fixture that parses those formats land together (step 1), then the guard
(step 2), then the test work (steps 3-4), then docs (step 5).

---

## Step 1: switch all four `scripts/` formats and every parser to `|`

Product:

- [ ] `scripts/tmux-update-icons.sh:58` — `-F` literal TAB → `|`.
- [ ] `scripts/tmux-update-icons.sh:68` — `IFS=$'\t' read -r pid cmd piped` → `IFS='|'`.
- [ ] `scripts/lib-claude.sh:118` — `IFS=$'\t' read -r pid rest` → `IFS='|'`, and
      update the `claude_reap_dead_panes` doc comment at `:105-111`, which states
      the contract as `"%N<TAB>..."`. Say *why* it is `|` (locale), pointing at
      the same reasoning as `tmux-update-icons.sh:111-122`.
- [ ] `scripts/claude-status-update.sh:74` — `-F` TAB → `|`; `:72` `IFS` → `'|'`.
- [ ] `scripts/tmux-float-refit.sh:27` — `$'#{pane_id}\t#{@float_geom}'` →
      `'#{pane_id}|#{@float_geom}'`; `:19` `IFS` → `'|'`. **Leave `:20`'s inner
      `read -r width height xoff yoff` on the default IFS** — the outer `IFS='|'`
      is an assignment prefix and must not be hoisted to a plain assignment, or
      the space-separated geometry stops splitting (no test covers this file).
- [ ] `scripts/tmux-reflow-windows.sh:223` — `-F` TAB → `|`; `:217` `IFS` → `'|'`.
      One-character change only; do **not** delete the (provably dead) loop.
- [ ] `scripts/tmux-update-icons.sh:198` — `tmux set -pq` → `tmux set -p`, so a
      lost write returns non-zero and says why. Without this the stderr capture
      in step 3 surfaces an empty file and AC #5 cannot be met. Justification for
      the inline comment: a `#()` job's stderr is `/dev/null` (verified), the
      script carries no `set -e`, and `:175` already `continue`s for any pane
      absent from `pane_to_win`, so the exposure is a sub-second race.

Fixtures that feed those parsers — all must move to `|` in the same step or their
suites go red:

- [ ] `tests/prune-stale-state.bats:74,91,105`
- [ ] `tests/agent-liveness.bats:207` **and** `:290` (two fake-tmux blocks)
- [ ] `tests/agent-detect-arm.bats:16`
- [ ] `tests/claude-issues.bats:361,369,378,386,408,416` + the "TSV" comment at `:344`

Verify: `nix flake check` is green. This step alone should already make
`update-icons-resume-guard-tests` stop flaking.

Extra verification for `tmux-float-refit.sh`, the one changed file with **no test
coverage anywhere** — a half-applied change there (format switched, `IFS` left on
tab, or the reverse) goes red nowhere. Drive it by hand: create a real float,
resize the window, and confirm the refit still fires under
`env -u LANG -u LC_ALL -u LC_CTYPE`.

A guard rule of the form "no `IFS=$'\t'` in a file containing `#{`" was considered
as a permanent gate for that class and **rejected**: it false-positives on
`tmux-worktree-match.sh:113` (parses `awk` output) and `tmux-pr-enrich.sh:314,354`
(parses `jq` output), neither of which crosses a tmux format. Rule (b) still
catches the `-F` half of a half-applied change; the `IFS` half is covered by the
manual drive above.

## Step 2: add the delimiter guard check

- [ ] Write the scanner at **`tests/check-tmux-format-delimiters.sh`**, modelled
      on `tests/check-portability.sh` (wired at `flake.nix:134`). It must **not**
      live in `scripts/`: `config/tmux.conf.nix:235` ships every `scripts/*.sh`
      as a tmux binary, and the scanner's own match patterns contain both `#{`
      and `\t`, so it would fail its own rule when scanning `${./scripts}`.
- [ ] It takes a directory argument and, for every line containing `#{`, fails on
      **(a)** a TAB preceded by a non-TAB or appearing after the `#{`, or any
      other C0 byte / DEL; **(b)** the escape text `\t`, `\x09`, or `\x1f`. Allow
      multi-byte UTF-8 and `\n` (both load-bearing — see the design doc).
      Following `check-portability.sh:28-48`, accumulate status and print **one
      line per offending file**, rather than exiting on the first hit.
- [ ] Add `checks.<system>.tmux-format-delimiter-assertions` in `flake.nix`,
      modelled on `float-conf-assertions` (`:400`). Run the scanner over
      `${./scripts}` (must pass), then over the fixtures dir and assert **all
      three fixture filenames appear** in its output — a single non-zero exit
      would let an inverted rule (b) ship green.
- [ ] Three fixtures: a literal TAB, a `$'…\t…'`, and a tab inside a `FMT=`
      variable used as `-F "$FMT"`. Constraints: the `$'…\t…'` fixture must
      **not** be tab-indented (rule (a) would catch it and it would stop proving
      rule (b)); and give the fixtures no shebang and a non-`.sh` extension so
      `nix build .#lint`'s `shellcheck`/`shfmt` (`flake.nix:128-129`) don't
      classify them as shell and fail on their deliberate weirdness.

Verify: the new check passes over `scripts/`, names all three fixtures, and fails
when any one of step 1's format changes is temporarily reverted.

## Step 3: make the resume-guard suite deterministic and falsifiable

- [ ] `scripts/lib-claude.sh:25` — make `CLAUDE_NOW` an env seam, fork-free:
      `[[ -n ${CLAUDE_NOW:-} ]] || printf -v CLAUDE_NOW '%(%s)T' -1`. Comment it
      as a test seam, same shape as `CLAUDE_ASSUME_DEAD_AFTER`.
- [ ] `tests/update-icons-resume-guard.bats` `setup()` — `export
      CLAUDE_NOW=$(( $(date +%s) / 5 * 5 ))` so `arm_agent_detect`'s `% 5` gate
      fires on **every** run. Floored from the real clock, not a constant.
- [ ] `run_update_icons` — stop discarding stderr; send it to a log file.
- [ ] Add an assertion helper that reports expected vs actual, using `<unset>`
      when `tmux show -pv` returns non-zero, and replace the four bare `[ "$(tmux
      show -pv …)" = … ]` comparisons with it.
- [ ] Add a `teardown` that, when `BATS_TEST_COMPLETED` is unset, prints the
      captured update-icons stderr and whether the pane's state file survived.
- [ ] Tests 1 and 4: assert the pane's state file still exists after the run, so
      they stop passing vacuously on a wiped state dir. **Name the right file in
      each**: test 1 writes `screen/$PANE_ID`, test 4 writes `panes/$PANE_ID` —
      asserting on `panes/` in test 1 is itself a vacuous pass.
- [ ] Add a case that points the stamper at a dead pane and asserts the write is
      loud: non-zero, with the rewritten pane id in the captured stderr. This is what
      makes AC #5 observable rather than asserted.
- [ ] Note the `CLAUDE_NOW` seam's one in-repo side effect in the step's comment:
      `tests/agent-detect-enum.bats:14` sets `CLAUDE_NOW=100000` *before*
      `setup_claude_status_functions` sources lib-claude, so today it is clobbered
      and after the change it survives. Benign — its fixtures derive every
      timestamp from the same variable and `read_pane_state` never compares
      `CLAUDE_NOW` against a real mtime — but the shift should not be invisible.

Verify: `nix build .#checks.x86_64-linux.update-icons-resume-guard-tests` green;
and with step 1 stashed, tests 2 and 3 go red **every** run rather than 1-in-5.
This red-before-green is **environment-bound** — it reproduces in the nix
sandbox, not under `nix develop` on a UTF-8 host, where nothing forces the
hostile locale. Only step 4's test is red in both.

## Step 4: add the seam test

- [ ] In `tests/update-icons-resume-guard.bats`, add a test that sources
      lib-claude, runs the real sweep format through
      `env -u LANG -u LC_ALL -u LC_CTYPE tmux list-panes -a -F …`, feeds the rows
      to `claude_reap_dead_panes`, and asserts the live pane's state file
      survives. Forcing the locale is what makes it red on unfixed code in
      `nix develop` too — do not rely on the sandbox being stripped.
- [ ] Do not re-pin tmux's own tab-mangling behaviour; that assertion already
      exists against the *shipped* tmux at
      `tests/worktree-match-integration.bats:74-78`.

Verify: red with step 1 stashed, green with it applied, in **both**
`nix develop -c bats …` and the nix check.

## Step 5: docs, follow-up issue, and the determinism run

- [ ] `CLAUDE.md` — document the `|`-delimiter convention for tmux `-F` formats
      and the locale hazard, and mention the new guard check. (The design doc
      found CLAUDE.md does *not* currently state this; only `flake.nix:569-574`
      and `tests/worktree-match-integration.bats:63-78` do.)
- [x] Filed #378 covering the seven deferred sites
      (`modules/home-manager.nix:1364,1419`; `picker/main.go:125,185,224,736`;
      `picker/statusline/main.go:55`) and the dead `win_procs` loop.
- [ ] Run `nix build .#checks.x86_64-linux.update-icons-resume-guard-tests
      --rebuild` **≥ 30 times**, record the observed pass count, and put it in the
      PR body. State plainly that aarch64-darwin cannot be exercised locally.
- [ ] Full gate: `nix build .#default`, `nix flake check`, `nix build .#lint`.
- [ ] PR body: `Closes #373` and `Closes #366`; the scope widening; the
      `## Escalated` section (see below).

---

## Escalated (spec-critic, at the 2-revision cap)

The final spec-critic pass returned `revise` rather than `accept`. Both findings
were verified against the repo and applied as errata rather than left open:

1. The guard's C0-byte rule, taken literally, would have failed the build on 34+
   legitimately tab-indented `#{`-bearing lines across 11 files. Qualified to
   "a TAB preceded by a non-TAB, or after the `#{`" — verified to match exactly
   the three real sites and nothing else.
2. Two further fixture files feed the changed parsers and would have gone red:
   `tests/agent-detect-arm.bats:16` and `tests/claude-issues.bats` (six
   `PANE_LIST` sites), plus a *second* fake-tmux block at
   `tests/agent-liveness.bats:290`. All added to step 1.

No finding was dismissed; the verdict is recorded because the cap was reached.
