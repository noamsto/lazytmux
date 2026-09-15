# Rich PR Badge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Carry check progress, review decision and auto-merge in the window-name PR badge without widening it, and poll checks every ~30s while they are pending.

**Architecture:** The poller stamps three new window options (`@pr_review`, `@pr_auto_merge`, `@pr_check_progress`). `build_window_label` swaps the pending glyph for a pie slice picked from the progress. Every renderer splits the badge at its `#`: the glyph half keeps today's state colour, the `#<n>` half takes a review colour and an auto-merge underline. The bridge ships the three options as `@bridge_pr_*`. A per-repo `<sha>.checks-pending` marker opens a shorter check cadence and a checks-only tick.

**Tech Stack:** bash (scripts/, bats tests), tmux 3.8 formats, Go (picker/, enrichcard, enrichstate, remotebridge daemon), Nix flake checks.

**Spec:** GitHub issue #633 (option B, chosen from the "PR Badge Options" mockups).

## Global Constraints

- Badge width is unchanged: `<glyph> #<n>`, plus the existing draft prefix. No new cells anywhere.
- Option values: `@pr_review` ∈ `approved` | `changes_requested` | `review_required` | empty; `@pr_auto_merge` ∈ `1` | empty; `@pr_check_progress` = `<finished>/<total>` only while `@pr_check_state` is `pending`, else empty.
- Review colour and underline apply only while `@pr_state` is `open`. With no review decision the `#<n>` keeps the glyph's colour, which is today's look.
- Colours: approved → `@thm_green`, changes requested → `@thm_red`, review required → `@thm_overlay_0`, auto-merge → `underscore`.
- Pie glyphs are `nf-md-circle_slice_1…8`, byte-identical to `CLAUDE_SPINNER_FRAMES` in `scripts/lib-claude.sh:15`. Slice index is `finished * 7 / total`.
- Pending cadence: `PENDING_CHECK_SECONDS=30`, never above `CHECK_REFRESH_SECONDS`. Settled repos keep `prCheckRefreshSeconds`; a checks-only pass never re-runs the identity batch.
- tmux `-F` formats stay `|`-delimited (`tmux-format-delimiter-assertions`).
- `config/tmux.conf.tmpl` and `config/tmux.conf.reference.nix` must produce byte-identical output (`tmux-conf-extraction-assertions`).
- Commit from inside `nix develop` so the pre-commit hooks run. Before pushing, run `nix build .#default`, `nix flake check` and `nix build .#lint`.

---

### Task 1: Rollup progress, pie glyph and badge split in lib-enrich

**Files:**
- Modify: `scripts/lib-enrich.sh:16-26` (constants), `:96-121` (`collapse_check_rollup`), `:195-296` (`build_window_label`)
- Test: `tests/enrich.bats`

**Interfaces:**
- Produces: `collapse_check_rollup JSON` now also sets `REPLY_PROGRESS` (`"<finished>/<total>"` when `REPLY` is `pending`, else `""`).
- Produces: `pr_pie_glyph PROGRESS` → `REPLY` slice glyph or `""`.
- Produces: `ENRICH_PIE_GLYPHS` (bash array, 8 entries).
- Produces: `build_window_label` takes a 14th arg `PR_PROGRESS`.
- Produces: `split_pr_badge BADGE` → `REPLY_GLYPH` (`" <glyph> "`, trailing space kept) and `REPLY_NUM` (`"#<n>"`).

- [ ] **Step 1: Write the failing tests** — append to `tests/enrich.bats`:

```bash
@test "collapse_check_rollup: pending sets REPLY_PROGRESS to finished/total" {
	collapse_check_rollup '[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""},{"__typename":"StatusContext","state":"PENDING"}]'
	[ "$REPLY" = "pending" ]
	[ "$REPLY_PROGRESS" = "1/3" ]
}

@test "collapse_check_rollup: settled states carry no progress" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-success.json)"
	[ -z "$REPLY_PROGRESS" ]
	collapse_check_rollup "$(cat tests/fixtures/rollup-failure.json)"
	[ -z "$REPLY_PROGRESS" ]
}

@test "collapse_check_rollup: malformed JSON → none, no progress" {
	collapse_check_rollup 'not json'
	[ "$REPLY" = "none" ]
	[ -z "$REPLY_PROGRESS" ]
}

@test "pr_pie_glyph: slice tracks the share finished" {
	pr_pie_glyph 0/8
	[ "$REPLY" = "${ENRICH_PIE_GLYPHS[0]}" ]
	pr_pie_glyph 3/8
	[ "$REPLY" = "${ENRICH_PIE_GLYPHS[2]}" ]
	pr_pie_glyph 7/8
	[ "$REPLY" = "${ENRICH_PIE_GLYPHS[6]}" ]
	pr_pie_glyph 1/3
	[ "$REPLY" = "${ENRICH_PIE_GLYPHS[2]}" ]
}

@test "pr_pie_glyph: unusable progress is empty" {
	local p
	for p in "" 3 0/0 9/8 a/b; do
		pr_pie_glyph "$p"
		[ -z "$REPLY" ]
	done
}

@test "build_window_label: pending PR with progress uses the pie slice" {
	build_window_label short linear ENG-1 "t" 9 open pending br /x mergeable "" "" "" 3/8
	[ "$REPLY_PR" = " ${ENRICH_PIE_GLYPHS[2]} #9" ]
}

@test "build_window_label: pending PR without progress keeps the pending glyph" {
	build_window_label short linear ENG-1 "t" 9 open pending br /x mergeable "" "" "" ""
	[ "$REPLY_PR" = " P #9" ]
}

@test "build_window_label: draft marker sits ahead of the pie" {
	build_window_label short linear ENG-1 "t" 9 open pending br /x mergeable "" "" 1 5/8
	[ "$REPLY_PR" = " D ${ENRICH_PIE_GLYPHS[4]} #9" ]
}

@test "split_pr_badge: glyph half keeps its trailing space, number half starts at #" {
	split_pr_badge " D S #247"
	[ "$REPLY_GLYPH" = " D S " ]
	[ "$REPLY_NUM" = "#247" ]
	split_pr_badge ""
	[ -z "$REPLY_GLYPH" ]
	[ -z "$REPLY_NUM" ]
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `nix build .#checks.x86_64-linux.enrich-tests -L`
Expected: FAIL. `pr_pie_glyph`/`split_pr_badge` are "command not found", and `REPLY_PROGRESS` is empty.

- [ ] **Step 3: Implement** — in `scripts/lib-enrich.sh`, after `ENRICH_ICON_DRAFT` (:26):

```bash
# The pending glyph's progress variant, filled by share of checks finished. Not a
# user icon key: nf-md-circle_slice_1…8, the same frames as CLAUDE_SPINNER_FRAMES.
ENRICH_PIE_GLYPHS=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
```

Replace `collapse_check_rollup` (:96-121) with:

```bash
# collapse_check_rollup ROLLUP_JSON
# Maps a gh `statusCheckRollup` array (a CheckRun | StatusContext union) to a
# single state. CheckRun entries carry .status + .conclusion; StatusContext
# entries (Travis/CircleCI-v1/commit-status API) carry .state instead.
# Priority: empty → none;
#   any FAILURE/ERROR/CANCELLED/TIMED_OUT/ACTION_REQUIRED/STALE → failure;
#   else any IN_PROGRESS/QUEUED/PENDING/EXPECTED, or an unfinished CheckRun
#     (empty conclusion) → pending;
#   else success.
# Sets REPLY to one of: failure | pending | success | none, and REPLY_PROGRESS
# to "<finished>/<total>" when REPLY is pending ("" otherwise). One jq fork.
collapse_check_rollup() {
	local state="" progress=""
	{
		IFS= read -r state
		IFS= read -r progress
	} < <(jq -r '
		def pending:
			((.status // "") | ascii_upcase | (. == "IN_PROGRESS" or . == "QUEUED" or . == "PENDING"))
			or ((.state // "") | ascii_upcase | (. == "EXPECTED" or . == "PENDING"))
			or (.__typename == "CheckRun" and ((.conclusion // "") == ""));
		(if length == 0 then "none"
		elif any(.[]; (.conclusion // .state // "") | ascii_upcase
			| . == "FAILURE" or . == "ERROR" or . == "CANCELLED"
			or . == "TIMED_OUT" or . == "ACTION_REQUIRED" or . == "STALE") then "failure"
		elif any(.[]; pending) then "pending"
		else "success"
		end),
		"\([.[] | select(pending | not)] | length)/\(length)"
	' <<<"$1" 2>/dev/null)
	REPLY="${state:-none}"
	REPLY_PROGRESS=""
	if [[ $REPLY == pending ]]; then REPLY_PROGRESS="$progress"; fi
}

# pr_pie_glyph PROGRESS
# "<finished>/<total>" → the ENRICH_PIE_GLYPHS slice filled to the share
# finished. Sets REPLY, or "" when PROGRESS is not a usable count.
pr_pie_glyph() {
	REPLY=""
	[[ $1 =~ ^([0-9]+)/([0-9]+)$ ]] || return 0
	local finished=${BASH_REMATCH[1]} total=${BASH_REMATCH[2]}
	((total > 0 && finished <= total)) || return 0
	REPLY="${ENRICH_PIE_GLYPHS[finished * 7 / total]}"
}

# split_pr_badge BADGE
# Splits a REPLY_PR badge (" <glyph> #<n>") at its number so a renderer can style
# the halves apart. Sets REPLY_GLYPH (" <glyph> ", trailing space included) and
# REPLY_NUM ("#<n>"); both "" for an empty BADGE.
split_pr_badge() {
	REPLY_GLYPH=""
	REPLY_NUM=""
	[[ -n $1 ]] || return 0
	REPLY_GLYPH="${1%#*}"
	REPLY_NUM="#${1##*#}"
}
```

In `build_window_label`, extend the usage comment's third line to `#                    [AI_NAME] [PR_DRAFT] [PR_PROGRESS]`. Extend the locals at :221:

```bash
	local pr_mergeable="${10:-}" task="${11:-}" ai_name="${12:-}" pr_draft="${13:-}" pr_progress="${14:-}"
```

and replace the `pending)` arm at :284:

```bash
			pending)
				pr_pie_glyph "$pr_progress"
				pr_glyph="${REPLY:-$ENRICH_ICON_PENDING}"
				;;
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `nix build .#checks.x86_64-linux.enrich-tests -L`
Expected: PASS, including every existing `collapse_check_rollup` and `build_window_label` case.

- [ ] **Step 5: Commit**

```bash
nix develop -c git add scripts/lib-enrich.sh tests/enrich.bats
nix develop -c git commit -m "feat(enrich): rollup progress, pie glyph and badge split helpers (#633)"
```

---

### Task 2: Poller stamps review, auto-merge and progress

**Files:**
- Modify: `scripts/tmux-pr-enrich.sh:8-9` (header), `:44` + `:62-90` (mock flags), `:141-165` (`write_pr_options`), `:236` (fallback fields), `:256-299` (`apply_cache_to_target`), `:350-351` (batch fields), `:448-453` (mock write)
- Modify: `scripts/tmux-reconcile-window.sh:79-86`
- Test: `tests/enrich-budget.bats`, `tests/reconcile.bats:185,190`

**Interfaces:**
- Consumes: `collapse_check_rollup` → `REPLY`, `REPLY_PROGRESS` (Task 1).
- Produces: `write_pr_options TARGET NUMBER TITLE STATE CHECK URL MERGEABLE BRANCH [DRAFT] [REVIEW] [AUTO_MERGE] [PROGRESS]`.
- Produces: window options `@pr_review`, `@pr_auto_merge`, `@pr_check_progress`.
- Produces: identity `--json` field string `number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest,headRefName`; fallback string `number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest`.

- [ ] **Step 1: Write the failing tests** — in `tests/enrich-budget.bats`:

1. Replace `GH_BATCH_JSON` (:61) with:

```bash
	export GH_BATCH_JSON='[{"number":7,"title":"t","url":"u","state":"OPEN","statusCheckRollup":[],"mergeable":"MERGEABLE","isDraft":false,"reviewDecision":"APPROVED","autoMergeRequest":{"enabledAt":"2026-09-15T00:00:00Z"},"headRefName":"feat/has-pr"}]'
```

2. Change the field strings at :76 and :96 to `'--json number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest,headRefName'`, and at :104 to `'--head feat/has-pr --state open --limit 1 --json number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest,statusCheckRollup'`.

3. Append:

```bash
@test "pass: review decision and auto-merge are stamped from the identity batch" {
	run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	grep -q -- '@pr_review approved' "$TMUX_LOG"
	grep -q -- '@pr_auto_merge 1' "$TMUX_LOG"
	grep -q -- '@pr_check_progress $' "$TMUX_LOG"
}

@test "pass: a pending rollup stamps finished/total progress" {
	GH_CHECK_JSON='[{"headRefName":"feat/has-pr","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]}]' \
		run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	grep -q -- '@pr_check_state pending' "$TMUX_LOG"
	grep -q -- '@pr_check_progress 1/2' "$TMUX_LOG"
}
```

4. Read `tests/reconcile.bats:180-195`. Add `@pr_review @pr_auto_merge @pr_check_progress` to the unset option list(s) it asserts at :185 and :190, in the same form as the existing `@pr_draft` entry.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `nix build .#checks.x86_64-linux.enrich-budget-tests -L`
Expected: FAIL. The field-string counts are 0 and `@pr_review approved` is missing from the log.

- [ ] **Step 3: Implement** — in `scripts/tmux-pr-enrich.sh`:

Header (:8-9):

```bash
# Always exits 0. Writes @pr_number @pr_title @pr_state @pr_check_state @pr_url
# @pr_mergeable @pr_draft @pr_branch @pr_review @pr_auto_merge @pr_check_progress.
```

Mock vars (:44) and flags (insert before `*) ;;` at :91):

```bash
mock_number="" mock_state="" mock_check="" mock_title="" mock_url="" mock_mergeable="" mock_draft=""
mock_review="" mock_auto_merge="" mock_progress=""
```

```bash
	--mock-review)
		mock_review="$2"
		shift
		;;
	--mock-auto-merge)
		mock_auto_merge="$2"
		shift
		;;
	--mock-check-progress)
		mock_progress="$2"
		shift
		;;
```

`write_pr_options` (:141-165) becomes:

```bash
# write_pr_options TARGET NUMBER TITLE STATE CHECK URL MERGEABLE BRANCH [DRAFT] \
#                  [REVIEW] [AUTO_MERGE] [PROGRESS]
write_pr_options() {
	# Only the badge-driving options are captured before writing so we can skip
	# the (cache-bypassing) reflow when unchanged.
	local prev
	prev=$(tmux display-message -t "$1" -p '#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@pr_draft}|#{@pr_review}|#{@pr_auto_merge}|#{@pr_check_progress}')
	tmux set-option -t "$1" -w @pr_number "$2"
	tmux set-option -t "$1" -w @pr_title "$3"
	tmux set-option -t "$1" -w @pr_state "$4"
	tmux set-option -t "$1" -w @pr_check_state "$5"
	tmux set-option -t "$1" -w @pr_url "$6"
	tmux set-option -t "$1" -w @pr_mergeable "${7:-}"
	# "1"/empty — an additive badge marker, not a state of its own.
	tmux set-option -t "$1" -w @pr_draft "${9:-}"
	# Style the badge's #<n> half: approved|changes_requested|review_required, and "1"/empty.
	tmux set-option -t "$1" -w @pr_review "${10:-}"
	tmux set-option -t "$1" -w @pr_auto_merge "${11:-}"
	# "<finished>/<total>" while checks are pending — picks the pie slice.
	tmux set-option -t "$1" -w @pr_check_progress "${12:-}"
	# Tags the branch this PR data describes so displays can hide it once the
	# pane cd's to a different branch (no wt switch re-stamps @pr_*). Mirrors
	# @issue_branch.
	tmux set-option -t "$1" -w @pr_branch "${8:-}"
	log_enabled && log_event enrich event pr target "$1" number "$2" state "$4" check "$5" mergeable "${7:-}" draft "${9:-}" review "${10:-}" auto_merge "${11:-}" progress "${12:-}"
	if [[ $prev != "$2|$4|$5|${7:-}|${9:-}|${10:-}|${11:-}|${12:-}" ]]; then
		@reflow@ "$(tmux display-message -t "$1" -p '#{session_name}')" --force >/dev/null 2>&1 &
	fi
	notify_pr_change "$1" "$prev" "$2" "$3" "$4" "$5"
}
```

`notify_pr_change`'s `IFS='|' read -r _ p_state p_check _ _` needs no change: its last `_` absorbs the appended fields.

Fallback fields (:236):

```bash
		local json="" fields="number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest"
```

Batch fields (:351):

```bash
			--json number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest,headRefName 2>/dev/null)" &&
```

In `apply_cache_to_target` (:272-298), replace the identity read through the `write_pr_options` call with:

```bash
	local number title url state mergeable draft review auto_merge
	{
		IFS= read -r number
		IFS= read -r state
		IFS= read -r mergeable
		IFS= read -r draft
		IFS= read -r review
		IFS= read -r auto_merge
		IFS= read -r url
		IFS= read -r title
	} < <(jq -r '
		(.[0].number // "" | tostring),
		(.[0].state // "" | ascii_downcase),
		(.[0].mergeable // "" | ascii_downcase),
		(if .[0].isDraft then "1" else "" end),
		(.[0].reviewDecision // "" | ascii_downcase),
		(if .[0].autoMergeRequest then "1" else "" end),
		(.[0].url // ""),
		(.[0].title // "")
	' <<<"$json")
	local check_cache="${cache%.json}.checks.json" rollup="[]"
	if [[ -f $check_cache ]]; then
		rollup="$(jq -c '.[0].statusCheckRollup // []' <"$check_cache")"
	elif jq -e '.[0].statusCheckRollup' >/dev/null 2>&1 <<<"$json"; then
		# Preserve check state from combined cache entries during an upgrade.
		rollup="$(jq -c '.[0].statusCheckRollup // []' <<<"$json")"
	fi
	collapse_check_rollup "$rollup"
	local check="$REPLY" progress="$REPLY_PROGRESS"
	sanitize_title "$title"
	write_pr_options "$tgt" "$number" "$REPLY" "$state" "$check" "$url" "$mergeable" "$br" "$draft" "$review" "$auto_merge" "$progress"
```

Mock write (:451):

```bash
	write_pr_options "$target" "$mock_number" "$REPLY" "$mock_state" "$mock_check" "$mock_url" "$mock_mergeable" "$branch" "$mock_draft" "$mock_review" "$mock_auto_merge" "$mock_progress"
```

In `scripts/tmux-reconcile-window.sh`, after `@pr_draft` (:85):

```bash
	tmux set-option -t "$target" -wu @pr_review 2>/dev/null
	tmux set-option -t "$target" -wu @pr_auto_merge 2>/dev/null
	tmux set-option -t "$target" -wu @pr_check_progress 2>/dev/null
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `nix build .#checks.x86_64-linux.enrich-budget-tests -L` then `nix build .#checks.x86_64-linux.reconcile -L`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
nix develop -c git add scripts/tmux-pr-enrich.sh scripts/tmux-reconcile-window.sh tests/enrich-budget.bats tests/reconcile.bats
nix develop -c git commit -m "feat(enrich): stamp PR review decision, auto-merge and check progress (#633)"
```

---

### Task 3: Poll checks every ~30s while a repo has pending checks

**Files:**
- Modify: `scripts/tmux-pr-enrich.sh:22-33` (constants), `:45-94` (arg parse), `:256` (`apply_cache_to_target`), `:341-392` (`enrich_repo_group`), `:398-445` (`run_full_pass`), `:455-487` (tick modes)
- Test: `tests/enrich-budget.bats`

**Interfaces:**
- Consumes: `apply_cache_to_target` (Task 2).
- Produces: `APPLIED_CHECK` global, set by `apply_cache_to_target` to the collapsed check state (`""` when nothing was applied).
- Produces: `pending_marker REPO_ID` → `REPLY` = `$ENRICH_CACHE_DIR/<sha1>.checks-pending`.
- Produces: `enrich_repo_group DIR REPO_ID BRANCHES WINDOWS REFRESH_CHECKS REFRESH_IDENTITY`.
- Produces: `run_full_pass [PENDING_ONLY]`.
- Produces: the `--tick-run-pending` mode.

- [ ] **Step 1: Write the failing tests** — append to `tests/enrich-budget.bats`:

```bash
PENDING_CHECK_JSON='[{"headRefName":"feat/has-pr","statusCheckRollup":[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]}]'

# markers — the pending markers currently in the cache dir, one per line.
markers() {
	compgen -G "$OG_ENRICH_CACHE_DIR/*.checks-pending" || true
}

@test "pending: a pending rollup leaves a repo marker, a settled one clears it" {
	GH_CHECK_JSON="$PENDING_CHECK_JSON" run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	[ "$(markers | wc -l)" -eq 1 ]
	# Backdate past PENDING_CHECK_SECONDS so the next pass is due for this repo.
	touch -t 200001010000 "$(markers)"
	run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	[ "$(gh_calls '--json headRefName,statusCheckRollup')" -eq 2 ]
	[ -z "$(markers)" ]
}

@test "pending: a fresh marker does not re-poll checks yet" {
	GH_CHECK_JSON="$PENDING_CHECK_JSON" run bash "$PR_ENRICH_SCRIPT" --tick-run
	GH_CHECK_JSON="$PENDING_CHECK_JSON" run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	[ "$(gh_calls '--json headRefName,statusCheckRollup')" -eq 1 ]
}

@test "pending: a checks-only pass skips the identity batch" {
	GH_CHECK_JSON="$PENDING_CHECK_JSON" run bash "$PR_ENRICH_SCRIPT" --tick-run
	touch -t 200001010000 "$(markers)"
	GH_CHECK_JSON="$PENDING_CHECK_JSON" run bash "$PR_ENRICH_SCRIPT" --tick-run-pending
	[ "$status" -eq 0 ]
	[ "$(gh_calls '--json number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest,headRefName')" -eq 1 ]
	[ "$(gh_calls '--json headRefName,statusCheckRollup')" -eq 2 ]
	grep -q -- '@pr_check_state pending' "$TMUX_LOG"
}

@test "pending: a checks-only pass with nothing due calls gh not at all" {
	run bash "$PR_ENRICH_SCRIPT" --tick-run-pending
	[ "$status" -eq 0 ]
	[ ! -s "$GH_LOG" ]
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `nix build .#checks.x86_64-linux.enrich-budget-tests -L`
Expected: FAIL. No marker is written, and `--tick-run-pending` falls through to tick mode.

- [ ] **Step 3: Implement** — in `scripts/tmux-pr-enrich.sh`:

After `TTL_TERMINAL=3600` (:33):

```bash
# A repo whose checks were last seen pending re-polls them on this shorter clock,
# so the badge's progress pie moves while CI runs. Capped by the settled cadence.
PENDING_CHECK_SECONDS=30
((PENDING_CHECK_SECONDS > CHECK_REFRESH_SECONDS)) && PENDING_CHECK_SECONDS=$CHECK_REFRESH_SECONDS
```

Arg parse, after `--tick-run) mode="tickrun" ;;` (:48):

```bash
	--tick-run-pending) mode="tickrunpending" ;;
```

Add after `branch_cache_key` (:174):

```bash
# pending_marker REPO_ID — sets REPLY to the repo's pending-checks marker. Its
# presence means the repo's last applied rollup had a pending PR; its mtime is
# when that repo's checks were last refreshed.
pending_marker() {
	branch_sha1 "$1"
	REPLY="$ENRICH_CACHE_DIR/$REPLY.checks-pending"
}
```

In `apply_cache_to_target`, make the first body line `APPLIED_CHECK=""`, and set `APPLIED_CHECK="$check"` right after `local check="$REPLY" progress="$REPLY_PROGRESS"`.

Replace `enrich_repo_group` (:332-392) with the version below. The identity batch and per-branch fetch loop are unchanged, just wrapped in `refresh_identity`:

```bash
# enrich_repo_group DIR REPO_ID BRANCHES WINDOWS REFRESH_CHECKS REFRESH_IDENTITY
# One repo's slice of the full pass. REPO_ID is the repo's git common dir
# (already resolved by run_full_pass; reused for cache keys so no git forks
# happen here). BRANCHES is newline-separated; WINDOWS is newline-separated
# "target|branch" lines. One gh call indexes the repo's open PRs by head branch
# (headRefName; each value is a single-element array matching the per-branch
# cache format), so the common case — each worktree has an open PR — costs a
# single API round-trip per repo. A successful batch is authoritative for open
# PRs: heads missing from it have none, which is a terminal answer
# (fetch_terminal_pr). REFRESH_IDENTITY=0 is a checks-only pass: identity is
# served from its cache files as they stand.
enrich_repo_group() {
	local d="$1" repo_id="$2" refresh_checks="$5" refresh_identity="$6"
	local branches=() wlines=()
	mapfile -t branches <<<"$3"
	mapfile -t wlines <<<"$4"

	local br ck cache
	if ((refresh_identity)); then
		declare -A open_pr
		local all_json head obj batch_ok=0
		if command -v gh >/dev/null 2>&1 &&
			all_json="$(cd "$d" 2>/dev/null && gh pr list --state open --limit 100 \
				--json number,title,url,state,mergeable,isDraft,reviewDecision,autoMergeRequest,headRefName 2>/dev/null)" &&
			[[ -n $all_json ]]; then
			batch_ok=1
			while IFS=$'\t' read -r head obj; do
				[[ -n $head ]] && open_pr[$head]="$obj"
			done < <(jq -r '.[] | "\(.headRefName)\t\([.])"' <<<"$all_json")
		fi

		for br in "${branches[@]}"; do
			[[ -z $br ]] && continue
			branch_sha1 "$repo_id|$br"
			ck="$REPLY"
			cache="$ENRICH_CACHE_DIR/$ck.json"
			if [[ -n ${open_pr[$br]+x} ]]; then
				printf '%s' "${open_pr[$br]}" >"$cache.tmp.$$" && mv -f "$cache.tmp.$$" "$cache"
			elif ((batch_ok)); then
				# No open PR for this head, on the batch's authority: only merged,
				# closed or none is left, and the next batch catches a PR opened later.
				cache="$(fetch_terminal_pr "$d" "$br" "$ck")"
			else
				# The batch itself failed (no gh, offline, rate-limited): nothing has
				# been ruled out, so run the full lookup — it serves the cache on
				# failure rather than wiping to "none".
				cache="$(fetch_branch_pr "$d" "$br" "$ck")"
			fi
		done
	fi
	if ((refresh_checks)); then
		refresh_repo_checks "$d" "$repo_id" "$3"
	fi

	local line tgt b2 any_pending=0
	for br in "${branches[@]}"; do
		[[ -z $br ]] && continue
		branch_sha1 "$repo_id|$br"
		cache="$ENRICH_CACHE_DIR/$REPLY.json"
		for line in "${wlines[@]}"; do
			IFS="|" read -r tgt b2 <<<"$line"
			[[ $b2 == "$br" ]] || continue
			apply_cache_to_target "$tgt" "$cache" "$br"
			[[ $APPLIED_CHECK == pending ]] && any_pending=1
		done
	done

	pending_marker "$repo_id"
	if ((!any_pending)); then
		rm -f "$REPLY"
	elif ((refresh_checks)) || [[ ! -f $REPLY ]]; then
		touch "$REPLY"
	fi
}
```

In `run_full_pass`, change its head (:398-403) to:

```bash
# run_full_pass [PENDING_ONLY] — enrich every window that carries a @branch.
# Windows are grouped by repo (git common dir, derived from @worktree/@git_root);
# each group runs concurrently as one enrich_repo_group. Multi-repo setups pay
# one round-trip per repo, all in flight at once. PENDING_ONLY=1 is the
# checks-only pass: it runs just the repos whose pending marker is due.
run_full_pass() {
	local pending_only="${1:-0}"
	local refresh_checks=0 check_tick="$ENRICH_CACHE_DIR/.last-check-tick"
	if ((!pending_only)) && { [[ ! -f $check_tick ]] || ((EPOCHSECONDS - $(file_mtime "$check_tick") >= CHECK_REFRESH_SECONDS)); }; then
		refresh_checks=1
		touch "$check_tick"
	fi
```

and replace the group loop (:440-444) with:

```bash
	local k due
	for k in "${!grp_branches[@]}"; do
		pending_marker "$k"
		due=0
		if [[ -f $REPLY ]] && ((EPOCHSECONDS - $(file_mtime "$REPLY") >= PENDING_CHECK_SECONDS)); then
			due=1
		fi
		((pending_only && !due)) && continue
		enrich_repo_group "${grp_dir[$k]}" "$k" "${grp_branches[$k]}" "${grp_windows[$k]}" \
			"$((refresh_checks || due))" "$((!pending_only))" &
	done
	wait
```

After the `tickrun` block (:455-460), add:

```bash
# --- tickrunpending: a checks-only pass for repos with pending checks ---
if [[ $mode == "tickrunpending" ]]; then
	mkdir -p "$ENRICH_CACHE_DIR" 2>/dev/null
	run_full_pass 1
	exit 0
fi
```

Replace the tick gate (:474-478) with:

```bash
last_tick="$ENRICH_CACHE_DIR/.last-tick"
if ((force == 0)) && [[ -f $last_tick ]]; then
	tick_age=$((EPOCHSECONDS - $(file_mtime "$last_tick")))
	if ((tick_age < REFRESH_SECONDS)); then
		# compgen is a builtin, so a tick with nothing pending still forks nothing.
		compgen -G "$ENRICH_CACHE_DIR/*.checks-pending" >/dev/null || exit 0
		pending_tick="$ENRICH_CACHE_DIR/.last-pending-tick"
		if [[ -f $pending_tick ]] && ((EPOCHSECONDS - $(file_mtime "$pending_tick") < PENDING_CHECK_SECONDS)); then
			exit 0
		fi
		touch "$pending_tick"
		detach "${BASH_SOURCE[0]}" --tick-run-pending
		exit 0
	fi
fi
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `nix build .#checks.x86_64-linux.enrich-budget-tests -L`
Expected: PASS. That covers the four new cases, the five original budget tests and Task 2's cases.

- [ ] **Step 5: Run shellcheck**

Run: `nix develop -c shellcheck scripts/tmux-pr-enrich.sh scripts/lib-enrich.sh`
Expected: no findings.

- [ ] **Step 6: Commit**

```bash
nix develop -c git add scripts/tmux-pr-enrich.sh tests/enrich-budget.bats
nix develop -c git commit -m "feat(enrich): re-poll checks every 30s while a repo has pending checks (#633)"
```

---

### Task 4: Ship review, auto-merge and progress across the bridge

**Files:**
- Modify: `picker/remotebridge/daemon/windowlabels.go:43-50` (format, count), `:54-74` (`labelRow`), `:84-106` (`bridgeLabelOptions`), `:108-142` (caps, regexes), `:165-187` (`parseWindowLabels`), `:341` (the `-u` count comment)
- Test: `picker/remotebridge/daemon/windowlabels_test.go`

**Interfaces:**
- Consumes: remote window options `@pr_review`, `@pr_auto_merge`, `@pr_check_progress` (Task 2).
- Produces: local window options `@bridge_pr_review`, `@bridge_pr_auto_merge`, `@bridge_pr_check_progress`.
- Produces: `windowLabelFields = 22`; field positions 17 review, 18 auto-merge, 19 progress, 20 label id, 21 label rest.

- [ ] **Step 1: Write the failing tests** — in `windowlabels_test.go`:

Replace the `full` fixture (:20-22):

```go
	full := "@1|nova|#89b4fa|123|open|success|mergeable| PR #123|" +
		"github|460|https://github.com/o/r/issues/460|https://github.com/o/r/pull/123|1|" +
		"feat/460-card|/home/noams/wt/card|Card reads bridge state|Ship the card|" +
		"approved|1|3/8|GH #460| ship it"
```

Replace `piped` (:26), where the title is followed by six `|`:

```go
	piped := "@2|orbit|#[fg=red]|none|OPEN|success|unknown|||||||||a  piped title|||||| a #[fg=red]title | with a pipe"
```

Add to `want0` (:49-61), after `prTitle: "Ship the card",`:

```go
		prReview: "approved", prAutoMerge: "1", prProgress: "3/8",
```

Change any `oneRow(t, 17, …)` label-id and `oneRow(t, 18, …)` label-rest cases to `20` and `21`. Find them with `rg -n 'oneRow\(t, 1[78]' picker/remotebridge/daemon/windowlabels_test.go`.

Append:

```go
func TestWindowLabelReviewAutoMergeProgress(t *testing.T) {
	reviews := []struct{ in, want string }{
		{"approved", "approved"}, {"changes_requested", "changes_requested"},
		{"review_required", "review_required"}, {"APPROVED", ""}, {"maybe", ""}, {"", ""},
	}
	for _, c := range reviews {
		if got := oneRow(t, 17, c.in).prReview; got != c.want {
			t.Errorf("prReview(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	autos := []struct{ in, want string }{{"1", "1"}, {"true", ""}, {"", ""}}
	for _, c := range autos {
		if got := oneRow(t, 18, c.in).prAutoMerge; got != c.want {
			t.Errorf("prAutoMerge(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	progress := []struct{ in, want string }{
		{"3/8", "3/8"}, {"0/1", "0/1"}, {"3/", ""}, {"a/b", ""}, {"3 / 8", ""}, {"-3/8", ""},
	}
	for _, c := range progress {
		if got := oneRow(t, 19, c.in).prProgress; got != c.want {
			t.Errorf("prProgress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `(cd picker && nix develop .. -c go test ./remotebridge/daemon/ -run 'TestParseWindowLabels|TestWindowLabel')`
Expected: FAIL. The build breaks on the unknown `prReview` field.

- [ ] **Step 3: Implement** — in `windowlabels.go`:

Format and count (:43-50):

```go
const windowLabelFormat = "#{window_id}|#{@crew_name}|#{@crew_color}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@window_pr_plain}|" +
	"#{s/[|]/ /:@issue_provider}|#{s/[|]/ /:@issue_id}|#{s/[|]/ /:@issue_url}|#{s/[|]/ /:@pr_url}|#{s/[|]/ /:@pr_draft}|" +
	"#{s/[|]/ /:@branch}|#{s/[|]/ /:#{?@worktree,#{@worktree},#{@git_root}}}|#{s/[|]/ /:@issue_title}|#{s/[|]/ /:@pr_title}|" +
	"#{s/[|]/ /:@pr_review}|#{s/[|]/ /:@pr_auto_merge}|#{s/[|]/ /:@pr_check_progress}|" +
	"#{@window_label_id}|#{@window_label_rest_long}"

// windowLabelFields is windowLabelFormat's field count, shared with the test
// fixture so the parser and the fixture cannot drift apart.
const windowLabelFields = 22
```

`labelRow`: after `prTitle string`, add:

```go
	prReview      string
	prAutoMerge   string
	prProgress    string
```

`bridgeLabelOptions`: after the `@bridge_pr_title` entry, add:

```go
	{"@bridge_pr_review", func(r labelRow) string { return r.prReview }},
	{"@bridge_pr_auto_merge", func(r labelRow) string { return r.prAutoMerge }},
	{"@bridge_pr_check_progress", func(r labelRow) string { return r.prProgress }},
```

Caps (in the const block after `dirMaxRunes`):

```go
	reviewMaxRunes   = 17 // len("changes_requested")
	progressMaxRunes = 16
```

Regexes (in the var block after `dirRe`):

```go
	reviewRe   = regexp.MustCompile(`^(approved|changes_requested|review_required)$`)
	progressRe = regexp.MustCompile(`^[0-9]+/[0-9]+$`)
```

`parseWindowLabels`: replace the last two fields (:185-186) with:

```go
			prReview:      matching(cleanLabelValueExact(at(17), reviewMaxRunes), reviewRe),
			prAutoMerge:   matching(cleanLabelValueExact(at(18), prDraftMaxRunes), prDraftRe),
			prProgress:    matching(cleanLabelValueExact(at(19), progressMaxRunes), progressRe),
			labelID:       cleanLabelValue(at(20), labelTextMaxRunes),
			labelRest:     cleanLabelValue(at(21), labelTextMaxRunes),
```

Update the comment at :341, which counts the `-u` unsets, to the new length of `bridgeLabelOptions` (twenty-one).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `(cd picker && nix develop .. -c go test -race ./remotebridge/daemon/)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
nix develop -c git add picker/remotebridge/daemon/windowlabels.go picker/remotebridge/daemon/windowlabels_test.go
nix develop -c git commit -m "feat(bridge): ship PR review, auto-merge and check progress to mirrors (#633)"
```

---

### Task 5: Status-line renderers style the badge halves apart

**Files:**
- Modify: `scripts/tmux-reflow-windows.sh:130-136` (FMT + read), `:176-189` (clear, build calls), `:322-382` (display segments), `:418-437` (stamps), `:480-483` (`bopt`), `:503-523` (PR fragments, ENTRY)
- Modify: `config/tmux.conf.tmpl:352` (+ its comment block :341-350), `config/tmux.conf.reference.nix:651` (+ matching comment)
- Modify: `generator/render/status.go:64-71`

**Interfaces:**
- Consumes: `split_pr_badge`, the `PR_PROGRESS` arg of `build_window_label` (Task 1); `@pr_review`, `@pr_auto_merge`, `@pr_check_progress` (Task 2); `@bridge_pr_review`, `@bridge_pr_auto_merge` (Task 4).
- Produces: window options `@window_pr_glyph`, `@window_pr_num`, `@window_pr_pad`. `@window_pr_disp` is removed; its only reader was reflow's own ENTRY. `@window_pr_plain` stays unchanged for `automatic-rename-format`, the picker and the bridge.

- [ ] **Step 1: Reflow data path** — in `scripts/tmux-reflow-windows.sh`:

FMT (:130). Insert `#{@pr_check_progress}` after `#{@pr_draft}`, since it is enum-safe:

```bash
FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}|#{@issue_provider}|#{@issue_id}|#{@issue_title}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@pr_draft}|#{@pr_check_progress}|#{@issue_branch}|#{@crew_name}|#{@window_ai_name}|#{@bridge_win}|#{@bridge_label_id}|#{@bridge_label_rest_long}|#{@bridge_pr_plain}|#{@bridge_crew_name}|#{window_name}|#{@window_bridge_name}|#{@window_task}'
```

Read (:136): `... prmerge prdraft prprog ibranch crew ...`

Stale-branch clear (:178): `prnum="" prstate="" prcheck="" prmerge="" prdraft="" prprog=""`

Both `build_window_label` calls (:181, :189) gain a trailing `"$prprog"`.

Declare (:322): `declare -A win_disp win_pr_glyph win_pr_num win_pr_pad win_id_disp`

In the per-window display loop (:323), directly after the `cur_rest` if/else, add:

```bash
	split_pr_badge "${win_pr[$idx]}"
	win_pr_glyph[$idx]="$REPLY_GLYPH"
	win_pr_num[$idx]="$REPLY_NUM"
	win_pr_pad[$idx]=""
```

In the single-line branch, delete `win_pr_disp[$idx]="${win_pr[$idx]}"` (:335). Replace the multi-line padding (:380-381) with:

```bash
	printf -v pad '%*s' "$((pr_colw - win_pr_dw[$idx]))" ''
	win_pr_pad[$idx]="$pad"
```

In the stamp loop (:434-436), replace the `@window_pr_disp` line with:

```bash
		set -w -t "$target" @window_pr_glyph "${win_pr_glyph[$idx]}" ';' \
		set -w -t "$target" @window_pr_num "${win_pr_num[$idx]}" ';' \
		set -w -t "$target" @window_pr_pad "${win_pr_pad[$idx]}" ';' \
```

Update the comment above the stamp loop from "the 9 sets" to "the 11 sets".

- [ ] **Step 2: Reflow format** — `bopt` (:481):

```bash
for o in crew_color pr_number pr_state pr_check_state pr_mergeable pr_review pr_auto_merge; do
```

After `PRCOLOR` (:510), add:

```bash
# The #<n> half: tinted by review decision and underlined for a queued
# auto-merge, open PRs only. With no decision it keeps PRCOLOR's tint.
PRNUM="#{?#{==:${bopt[pr_state]},open},#{?#{==:${bopt[pr_review]},approved},#[fg=#{@thm_green}],#{?#{==:${bopt[pr_review]},changes_requested},#[fg=#{@thm_red}],#{?#{==:${bopt[pr_review]},review_required},#[fg=#{@thm_overlay_0}],}}}#{?${bopt[pr_auto_merge]},#[underscore],},}"
```

Update the PRCOLOR comment's last sentences (:506-509). `@window_pr_disp` becomes `@window_pr_glyph`, and it now colours only the glyph half.

ENTRY (:523). The underline ends before the pad, so the column padding never renders underlined:

```bash
ENTRY="#[range=window|#{window_index}]#[nobold]${BASE}${IDX}: ${CREW}${LABEL_Z}${ICONFG} ${ICON}${PRCOLOR}#{@window_pr_glyph}${PRNUM}#{@window_pr_num}#[nounderscore]#{@window_pr_pad}${AGO}#[norange]"
```

- [ ] **Step 3: Single-line global format** — in `config/tmux.conf.tmpl:352`, replace this exact substring:

```
#{?#{&&:{{index .BridgeOpt "pr_number"}},#{!=:{{index .BridgeOpt "pr_number"}},none}},#{?#{==:{{index .BridgeOpt "pr_state"}},closed},#[fg=#{@thm_overlay_0}],#{?#{||:#{==:{{index .BridgeOpt "pr_check_state"}},failure},#{==:{{index .BridgeOpt "pr_mergeable"}},conflicting}},#[fg=#{@thm_red}],#{?#{==:{{index .BridgeOpt "pr_check_state"}},pending},#[fg=#{@thm_peach}],#{?#{==:{{index .BridgeOpt "pr_state"}},merged},#[fg=#{@thm_mauve}],#[fg=#{@thm_green}]}}}},}#{@window_pr_plain}
```

with this, which puts merged first to match reflow and the Go renderers, and splits the badge:

```
#{?#{&&:{{index .BridgeOpt "pr_number"}},#{!=:{{index .BridgeOpt "pr_number"}},none}},#{?#{==:{{index .BridgeOpt "pr_state"}},merged},#[fg=#{@thm_mauve}],#{?#{==:{{index .BridgeOpt "pr_state"}},closed},#[fg=#{@thm_overlay_0}],#{?#{||:#{==:{{index .BridgeOpt "pr_check_state"}},failure},#{==:{{index .BridgeOpt "pr_mergeable"}},conflicting}},#[fg=#{@thm_red}],#{?#{==:{{index .BridgeOpt "pr_check_state"}},pending},#[fg=#{@thm_peach}],#[fg=#{@thm_green}]}}}},}#{@window_pr_glyph}#{?#{==:{{index .BridgeOpt "pr_state"}},open},#{?#{==:{{index .BridgeOpt "pr_review"}},approved},#[fg=#{@thm_green}],#{?#{==:{{index .BridgeOpt "pr_review"}},changes_requested},#[fg=#{@thm_red}],#{?#{==:{{index .BridgeOpt "pr_review"}},review_required},#[fg=#{@thm_overlay_0}],}}}#{?{{index .BridgeOpt "pr_auto_merge"}},#[underscore],},}#{@window_pr_num}#[nounderscore]
```

In `config/tmux.conf.reference.nix:651`, make the same replacement with `${bridgeOpt "<name>"}` in place of every `{{index .BridgeOpt "<name>"}}`.

In both files' comment blocks describing the PR colouring (tmpl :341-350 and the reference's twin), state the order (merged, closed, failure/conflict, pending, success) and that the `#<n>` half takes the review tint and auto-merge underline. Keep the two comments identical.

`generator/render/status.go:64-71`: append `"pr_review", "pr_auto_merge",` to `bridgeOptNames`.

- [ ] **Step 4: Verify build and extraction diff**

Run: `nix build .#default -L` then `nix build .#checks.x86_64-linux.tmux-conf-extraction-assertions -L`
Expected: both succeed; the generated-vs-reference diff is empty.

- [ ] **Step 5: Verify a live render on a scratch server**

```bash
export TMUX_TMPDIR=/tmp/lzt-633 CLAUDE_STATUS_DIR=/tmp/lzt-633/cs OG_ENRICH_CACHE_DIR=/tmp/lzt-633/pr
mkdir -p "$TMUX_TMPDIR"
./result/bin/tmux -L probe new-session -d -s s -x 200 -y 50
./result/bin/tmux -L probe set -w -t s:1 @branch feat/1-x
./result/bin/tmux -L probe set -w -t s:1 @issue_branch feat/1-x
./result/bin/tmux -L probe set -w -t s:1 @pr_number 42
./result/bin/tmux -L probe set -w -t s:1 @pr_state open
./result/bin/tmux -L probe set -w -t s:1 @pr_check_state pending
./result/bin/tmux -L probe set -w -t s:1 @pr_check_progress 3/8
./result/bin/tmux -L probe set -w -t s:1 @pr_review approved
./result/bin/tmux -L probe set -w -t s:1 @pr_auto_merge 1
./result/bin/tmux -L probe run-shell 'tmux-reflow-windows s --force'
./result/bin/tmux -L probe show -w -t s:1 @window_pr_glyph
./result/bin/tmux -L probe show -w -t s:1 @window_pr_num
./result/bin/tmux -L probe display -p -t s:1 '#{E:status-format[1]}'
./result/bin/tmux -L probe kill-server
```

Expected:
- `@window_pr_glyph` is `" 󰪠 "`.
- `@window_pr_num` is `#42`.
- The expanded format contains `#[fg=#{@thm_green}]` or the resolved green, then `#[underscore]#42#[nounderscore]`.

- [ ] **Step 6: Commit**

```bash
nix develop -c git add scripts/tmux-reflow-windows.sh config/tmux.conf.tmpl config/tmux.conf.reference.nix generator/render/status.go
nix develop -c git commit -m "feat(status): style the PR badge's number by review and auto-merge (#633)"
```

---

### Task 6: Window picker row styles the badge halves apart

**Files:**
- Modify: `picker/main.go:270-289` (`winInfo`), `:307` (field count), `:326-376` (parse), `:392` (format), `:1224-1253` (`prColors`, `colorPRBadge`)
- Modify: `picker/tui.go:2154` (`prCols`), `:2230` (call)
- Modify: the `windowData` struct and its `winInfo` → `windowData` copy. Find them with `rg -n 'prMergeable' picker/*.go`.
- Test: `picker/enrich_test.go:107-148`, plus any `parseWindowPaneRows` fixture carrying 30 fields (`rg -n 'parseWindowPaneRows' picker/*_test.go`)

**Interfaces:**
- Consumes: `@pr_review`, `@pr_auto_merge` (Task 2); `@bridge_pr_review`, `@bridge_pr_auto_merge` (Task 4).
- Produces: `colorPRBadge(prPlain, state, check, mergeable, review, autoMerge string, c prColors) string`; `prColors` gains `required` and `underline`.

- [ ] **Step 1: Write the failing tests** — replace `TestColorPRBadge` in `picker/enrich_test.go`:

```go
func TestColorPRBadge(t *testing.T) {
	c := prColors{success: "<s>", failure: "<f>", pending: "<p>", merged: "<m>", closed: "<c>", required: "<q>", underline: "<u>", reset: "<r>"}
	cases := []struct {
		name, prPlain, state, check, mergeable, review, autoMerge string
		want                                                      string
	}{
		{"no pr", "", "open", "success", "mergeable", "", "", ""},
		{"conflict wins over success", " G #1", "open", "success", "conflicting", "", "", "<f>G <r><f>#1<r>"},
		{"failing checks", " G #2", "open", "failure", "mergeable", "", "", "<f>G <r><f>#2<r>"},
		{"pending checks", " G #3", "open", "pending", "mergeable", "", "", "<p>G <r><p>#3<r>"},
		{"merged", " G #4", "merged", "success", "mergeable", "approved", "1", "<m>G <r><m>#4<r>"},
		{"merged wins over leftover failure", " G #7", "merged", "failure", "unknown", "", "", "<m>G <r><m>#7<r>"},
		{"closed", " G #8", "closed", "success", "mergeable", "", "", "<c>G <r><c>#8<r>"},
		{"clean success", " G #5", "open", "success", "mergeable", "", "", "<s>G <r><s>#5<r>"},
		{"approved tints the number", " G #9", "open", "pending", "mergeable", "approved", "", "<p>G <r><s>#9<r>"},
		{"changes requested", " G #10", "open", "success", "mergeable", "changes_requested", "", "<s>G <r><f>#10<r>"},
		{"review required", " G #11", "open", "success", "mergeable", "review_required", "", "<s>G <r><q>#11<r>"},
		{"auto-merge underlines", " G #12", "open", "success", "mergeable", "approved", "1", "<s>G <r><s><u>#12<r>"},
		{"draft prefix stays in the glyph half", " D G #13", "open", "success", "mergeable", "", "", "<s>D G <r><s>#13<r>"},
		{"no # keeps one colour", " weird", "open", "success", "mergeable", "approved", "", "<s>weird<r>"},
	}
	for _, c2 := range cases {
		if got := colorPRBadge(c2.prPlain, c2.state, c2.check, c2.mergeable, c2.review, c2.autoMerge, c); got != c2.want {
			t.Errorf("%s: got %q, want %q", c2.name, got, c2.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `nix build .#default -L`
Expected: FAIL. The picker test build breaks on `colorPRBadge`'s arity and the unknown `prColors` fields.

- [ ] **Step 3: Implement**

`picker/main.go` `colorPRBadge` (:1224-1253):

```go
// prColors holds the PR badge tints, mirroring the status bar's coloring.
type prColors struct{ success, failure, pending, merged, closed, required, underline, reset string }

// colorPRBadge tints a plain PR badge (" <glyph> #<n>" from @window_pr_plain),
// mirroring the status bar. The glyph half takes the state tint: merged/closed
// PRs → terminal (so a leftover pending/failed rollup can't mask them — closed
// is a dead/superseded PR, dimmed so it can't read as live), a conflicting merge
// or failing checks → failure, pending → pending, else success. The #<n> half of
// an open PR takes its review tint and an auto-merge underline instead. Returns
// "" when there is no PR.
func colorPRBadge(prPlain, state, check, mergeable, review, autoMerge string, c prColors) string {
	badge := strings.TrimSpace(prPlain)
	if badge == "" {
		return ""
	}
	var col string
	switch {
	case state == "merged":
		col = c.merged
	case state == "closed":
		col = c.closed
	case mergeable == "conflicting", check == "failure":
		col = c.failure
	case check == "pending":
		col = c.pending
	default:
		col = c.success
	}
	// A mirror's badge is remote-derived and sanitized, not guaranteed shaped.
	i := strings.LastIndex(badge, "#")
	if i < 0 {
		return col + badge + c.reset
	}
	numCol, underline := col, ""
	if state == "open" {
		switch review {
		case "approved":
			numCol = c.success
		case "changes_requested":
			numCol = c.failure
		case "review_required":
			numCol = c.required
		}
		if autoMerge == "1" {
			underline = c.underline
		}
	}
	return col + badge[:i] + c.reset + numCol + underline + badge[i:] + c.reset
}
```

Format (:392): append `|#{@pr_review}|#{@pr_auto_merge}|#{@bridge_pr_review}|#{@bridge_pr_auto_merge}` after `#{@bridge_proc}`. Field count (:307): `if len(parts) != 34 {`.

`winInfo` (:281-284): add `prReview string` and `prAutoMerge string` after `prMergeable`. In the parse (:330-350), set `prReview := field(parts, 30)` and `prAutoMerge := field(parts, 31)` beside `prMergeable`. In the `if bridgeWin {` block, add `prReview = field(parts, 32)` and `prAutoMerge = field(parts, 33)`. Set `prReview: prReview, prAutoMerge: prAutoMerge,` in the `winInfo` literal (:360-376).

Carry both fields through `windowData` and the `winInfo` → `windowData` copy, beside `prMergeable`.

`picker/tui.go:2154`:

```go
	prCols := prColors{success: cGreen, failure: ansiFg(thmRed), pending: ansiFg(thmPeach), merged: cMauve, closed: ansiFg(thmOverlay0), required: ansiFg(thmOverlay0), underline: "\033[4m", reset: reset}
```

`picker/tui.go:2230`:

```go
		prBadge := colorPRBadge(w.prPlain, w.prState, w.prCheck, w.prMergeable, w.prReview, w.prAutoMerge, prCols)
```

Extend every `parseWindowPaneRows` test fixture row from 30 to 34 `|`-separated fields by appending `||||`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `nix build .#default -L`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
nix develop -c git add picker/main.go picker/tui.go picker/enrich_test.go picker/*_test.go
nix develop -c git commit -m "feat(picker): style the window row's PR number by review and auto-merge (#633)"
```

---

### Task 7: Enrich card shows the pie, review tint, auto-merge and progress

**Files:**
- Modify: `picker/enrichstate/enrichstate.go`
- Create: `picker/enrichstate/review_test.go`
- Modify: `picker/enrichcard/options.go:10-17` (`winState`), `:52-120` (parse)
- Modify: `picker/enrichcard/model.go:55-68` (`colorFor`), `:123-139` (`prBlock`)
- Modify: `flake.nix:125-132` (checkPhase)
- Test: `picker/enrichcard/model_test.go`, `picker/enrichcard/options_test.go`

**Interfaces:**
- Consumes: `@pr_review`, `@pr_auto_merge`, `@pr_check_progress` and their `@bridge_*` twins (Tasks 2, 4).
- Produces: `enrichstate.ColorReviewRequired`; `enrichstate.ReviewColor(state, review string) (ColorRole, bool)`; `enrichstate.AutoMerge(state, autoMerge string) bool`; `enrichstate.PieSlices [8]string`; `enrichstate.Pie(progress string) string`.
- Produces: `winState.prReview`, `winState.prAutoMerge`, `winState.prProgress`.

- [ ] **Step 1: Write the failing tests**

`picker/enrichstate/review_test.go`:

```go
package enrichstate

import "testing"

func TestReviewColor(t *testing.T) {
	cases := []struct {
		state, review string
		want          ColorRole
		ok            bool
	}{
		{"open", "approved", ColorSuccess, true},
		{"open", "changes_requested", ColorFailure, true},
		{"open", "review_required", ColorReviewRequired, true},
		{"open", "", 0, false},
		{"merged", "approved", 0, false},
		{"closed", "changes_requested", 0, false},
	}
	for _, c := range cases {
		got, ok := ReviewColor(c.state, c.review)
		if got != c.want || ok != c.ok {
			t.Errorf("ReviewColor(%q, %q) = %v, %v; want %v, %v", c.state, c.review, got, ok, c.want, c.ok)
		}
	}
}

func TestAutoMerge(t *testing.T) {
	if !AutoMerge("open", "1") || AutoMerge("merged", "1") || AutoMerge("open", "") {
		t.Error("AutoMerge must be true only for an open PR carrying the flag")
	}
}

func TestPie(t *testing.T) {
	cases := map[string]string{
		"0/8": PieSlices[0], "3/8": PieSlices[2], "7/8": PieSlices[6], "8/8": PieSlices[7], "1/3": PieSlices[2],
		"": "", "3": "", "0/0": "", "9/8": "", "a/b": "", "-1/8": "",
	}
	for in, want := range cases {
		if got := Pie(in); got != want {
			t.Errorf("Pie(%q) = %q, want %q", in, got, want)
		}
	}
}
```

Append to `picker/enrichcard/options_test.go`:

```go
func TestParseWindowOptionsReviewAndProgress(t *testing.T) {
	var o winOpts
	parseWindowOptions("@pr_review approved\n@pr_auto_merge 1\n@pr_check_progress 3/8\n"+
		"@bridge_pr_review changes_requested\n@bridge_pr_auto_merge 1\n@bridge_pr_check_progress 1/2\n", &o)
	if o.local.prReview != "approved" || o.local.prAutoMerge != "1" || o.local.prProgress != "3/8" {
		t.Errorf("local = %+v", o.local)
	}
	if o.bridge.prReview != "changes_requested" || o.bridge.prAutoMerge != "1" || o.bridge.prProgress != "1/2" {
		t.Errorf("bridge = %+v", o.bridge)
	}
}
```

Append to `picker/enrichcard/model_test.go`, and add `"github.com/noamsto/tmux-og/picker/enrichstate"` to its imports:

```go
func TestCardPendingPieAndProgress(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, win: winState{
		prNumber: "103", prState: "open", prCheck: "pending", prProgress: "3/8", branch: "b"}}
	out := render(m)
	if !strings.Contains(out, enrichstate.PieSlices[2]+" #103") {
		t.Errorf("expected pie badge %q\n%s", enrichstate.PieSlices[2]+" #103", out)
	}
	if !strings.Contains(out, "3/8 checks") {
		t.Errorf("expected progress text\n%s", out)
	}
}

func TestCardPendingWithoutProgressKeepsGlyph(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, win: winState{
		prNumber: "103", prState: "open", prCheck: "pending", branch: "b"}}
	out := render(m)
	if !strings.Contains(out, "P #103") || strings.Contains(out, "checks") {
		t.Errorf("expected plain pending badge and no progress text\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `(cd picker && nix develop .. -c go test ./enrichstate/ ./enrichcard/)`
Expected: FAIL to compile. `ReviewColor`, `Pie` and `prProgress` are undefined.

- [ ] **Step 3: Implement**

`picker/enrichstate/enrichstate.go`. Add `ColorReviewRequired` as the last `ColorRole` constant with comment `// dim overlay (review still owed)`. Add `"strconv"` and `"strings"` imports, then append:

```go
// ReviewColor is the tint for the badge's #<n> half. ok is false when the number
// keeps Classify's color: no review decision, or a PR that is no longer open.
func ReviewColor(state, review string) (ColorRole, bool) {
	if state != "open" {
		return 0, false
	}
	switch review {
	case "approved":
		return ColorSuccess, true
	case "changes_requested":
		return ColorFailure, true
	case "review_required":
		return ColorReviewRequired, true
	}
	return 0, false
}

// AutoMerge reports whether the badge's #<n> half is underlined.
func AutoMerge(state, autoMerge string) bool {
	return state == "open" && autoMerge == "1"
}

// PieSlices are nf-md-circle_slice_1…8, the same frames as the Claude spinner;
// the shell's ENRICH_PIE_GLYPHS must stay byte-identical.
var PieSlices = [8]string{"󰪞", "󰪟", "󰪠", "󰪡", "󰪢", "󰪣", "󰪤", "󰪥"}

// Pie returns the slice filled to the share of checks finished, from a
// "<finished>/<total>" progress value, or "" when progress is not usable.
func Pie(progress string) string {
	f, t, ok := strings.Cut(progress, "/")
	if !ok {
		return ""
	}
	fin, err1 := strconv.Atoi(f)
	tot, err2 := strconv.Atoi(t)
	if err1 != nil || err2 != nil || tot <= 0 || fin < 0 || fin > tot {
		return ""
	}
	return PieSlices[fin*7/tot]
}
```

`picker/enrichcard/options.go` `winState` (:13-14) becomes:

```go
	prNumber, prTitle, prState, prCheck, prURL, prMergeable string
	prDraft, prReview, prAutoMerge, prProgress              string
```

In `parseWindowOptions`, add beside `@pr_draft`:

```go
		case "@pr_review":
			o.local.prReview = val
		case "@pr_auto_merge":
			o.local.prAutoMerge = val
		case "@pr_check_progress":
			o.local.prProgress = val
```

and beside `@bridge_pr_draft`:

```go
		case "@bridge_pr_review":
			o.bridge.prReview = val
		case "@bridge_pr_auto_merge":
			o.bridge.prAutoMerge = val
		case "@bridge_pr_check_progress":
			o.bridge.prProgress = val
```

Read `picker/enrichcard/bridge.go` `resolve`. If it copies fields one by one rather than returning `o.bridge` whole, copy the three new ones the same way.

`picker/enrichcard/model.go` `colorFor`: add before `default:`:

```go
	case enrichstate.ColorReviewRequired:
		return m.cfg.overlay0
```

`prBlock` (:131-138) becomes:

```go
	cr, gr := enrichstate.Classify(w.prState, w.prCheck, w.prMergeable)
	glyph := m.glyphFor(gr)
	progress := ""
	if gr == enrichstate.GlyphPending {
		if pie := enrichstate.Pie(w.prProgress); pie != "" {
			glyph, progress = pie, w.prProgress
		}
	}
	if enrichstate.Draft(w.prState, w.prDraft) {
		glyph = c.icDraft + " " + glyph
	}
	numStyle := m.sty(m.colorFor(cr))
	if rc, ok := enrichstate.ReviewColor(w.prState, w.prReview); ok {
		numStyle = m.sty(m.colorFor(rc))
	}
	if enrichstate.AutoMerge(w.prState, w.prAutoMerge) {
		numStyle = numStyle.Underline(true)
	}
	badge := m.sty(m.colorFor(cr)).Render(glyph+" ") + numStyle.Render("#"+w.prNumber)
	if progress != "" {
		badge += m.sty(c.overlay0).Render("  " + progress + " checks")
	}
	title := m.sty(c.fg).Render(truncate(w.prTitle, m.titleWidth()))
	return lipgloss.JoinVertical(lipgloss.Left, badge, title)
```

`flake.nix` checkPhase (:128-131): add `go test ./enrichstate/...` after `go test ./tmuxformat/...`, because nothing ran the package's tests before.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `(cd picker && nix develop .. -c go test ./enrichstate/ ./enrichcard/)` then `nix build .#checks.x86_64-linux.picker-go-tests -L`
Expected: PASS. The existing `TestCardDraftGlyph` (`"D S #103"`) and `TestCardMergedGlyph` still pass.

- [ ] **Step 5: Commit**

```bash
nix develop -c git add picker/enrichstate picker/enrichcard flake.nix
nix develop -c git commit -m "feat(enrichcard): pie, review tint, auto-merge and check progress (#633)"
```

---

### Task 8: Documentation and full gate

**Files:**
- Modify: `CLAUDE.md` — Script Roles `tmux-window-picker` row (:55), `tmux-pr-enrich` row (:64), `lib-enrich.sh` bullet (:79), "Remote Window Labels" cleaning policies (:288-301), "PR + Issue Enrichment" (:376-413)
- Modify: `modules/home-manager.nix:553-561` (`prCheckRefreshSeconds` description)

- [ ] **Step 1: Edit CLAUDE.md**

1. `tmux-pr-enrich` row. Add `@pr_review`/`@pr_auto_merge`/`@pr_check_progress` to the written options. Add: "a repo whose last rollup was pending keeps a `<sha1>.checks-pending` marker and re-polls checks every 30s through a `--tick-run-pending` checks-only pass that skips the identity batch."
2. `tmux-window-picker` row. The PR badge is tinted by `@pr_check_state`/`@pr_mergeable`; its `#<n>` takes `@pr_review`'s tint and a `@pr_auto_merge` underline.
3. `lib-enrich.sh` bullet. Add `pr_pie_glyph` and `split_pr_badge`.
4. Remote Window Labels. Add `@bridge_pr_review`, `@bridge_pr_auto_merge` and `@bridge_pr_check_progress` to the identity (drop-whole) class, and `pr_review`/`pr_auto_merge` to the live-read `bopt` list.
5. PR + Issue Enrichment. Add a **Review and auto-merge** bullet: `#<n>` is green when approved, red when changes are requested, dim when a review is required, and underlined when auto-merge is queued; open PRs only; no decision keeps the glyph tint. Add a **Progress** bullet: the pending glyph becomes `nf-md-circle_slice_1…8` by `finished * 7 / total`; the glyphs live twice (`ENRICH_PIE_GLYPHS`, `enrichstate.PieSlices`) and are not user icon keys. In **Refresh**, note the 30s pending cadence. In **Drafts**, change "the rule lives twice" to name its three copies (`build_window_label`, `enrichstate`, the picker's `colorPRBadge`).

- [ ] **Step 2: Edit the HM description** — append to `prCheckRefreshSeconds`'s description: "A repo with pending checks re-polls every 30 seconds (never slower than this value) until they settle."

- [ ] **Step 3: Full gate**

Run each, one at a time:
- `nix build .#default -L`
- `nix flake check -L`
- `nix build .#lint -L`

Expected: all succeed.

- [ ] **Step 4: Commit, push, PR**

```bash
nix develop -c git add CLAUDE.md modules/home-manager.nix
nix develop -c git commit -m "docs: rich PR badge — progress, review, auto-merge, pending cadence (#633)"
```

Run the `deslop` skill (the push hook requires it) and commit its cleanup. Then:

```bash
git push -u origin feat/633-rich-pr-badge
gh pr create --assignee @me --title "feat(enrich): richer PR badge — check progress, review decision, auto-merge" --body "Closes #633. ..."
```
