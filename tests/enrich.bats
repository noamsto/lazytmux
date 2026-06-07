#!/usr/bin/env bats

load helper

setup() {
	setup_lib_enrich
}

@test "branch_to_linear_key: lowercase team-num" {
	branch_to_linear_key "noa-123-foo"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: already uppercase" {
	branch_to_linear_key "NOA-123-foo"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: with slash prefix" {
	branch_to_linear_key "feature/noa-123-foo"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: bare key no suffix" {
	branch_to_linear_key "noa-123"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: pure-numeric prefix is not a linear key" {
	branch_to_linear_key "123-foo"
	[ -z "$REPLY" ]
}

@test "branch_to_linear_key: plain branch name yields empty" {
	branch_to_linear_key "main"
	[ -z "$REPLY" ]
}

@test "branch_to_gh_issue_number: leading number" {
	branch_to_gh_issue_number "247-fix-bug"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: gh- prefix" {
	branch_to_gh_issue_number "gh-247-fix"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: issue- prefix" {
	branch_to_gh_issue_number "issue-247"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: slash then number" {
	branch_to_gh_issue_number "feature/247-foo"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: linear-style branch is not a gh issue" {
	branch_to_gh_issue_number "noa-123-foo"
	[ -z "$REPLY" ]
}

@test "branch_to_gh_issue_number: plain branch yields empty" {
	branch_to_gh_issue_number "main"
	[ -z "$REPLY" ]
}

@test "sanitize_title: strips CR/LF and truncates to 50" {
	sanitize_title "$(printf 'Add foo\r\nbar baz')"
	[ "$REPLY" = "Add foobar baz" ]
}

@test "sanitize_title: hard-truncates long titles to 50 chars" {
	local long="123456789012345678901234567890123456789012345678901234567890"
	sanitize_title "$long"
	[ "${#REPLY}" -eq 50 ]
}

@test "sanitize_title: strips ESC control char" {
	sanitize_title "$(printf 'title\033[31mcolored')"
	[ "$REPLY" = "title[31mcolored" ]
}

@test "truncate_ellipsis: short string is unchanged" {
	truncate_ellipsis "short" 25
	[ "$REPLY" = "short" ]
}

@test "truncate_ellipsis: long string gets ellipsis at limit" {
	truncate_ellipsis "this title is definitely longer than twenty-five" 25
	[ "${#REPLY}" -eq 25 ]
	[ "${REPLY: -1}" = "…" ]
}

@test "branch_sha1: stable 40-char hex for a branch" {
	branch_sha1 "feat/2-pr-window-enrichment"
	[ "${#REPLY}" -eq 40 ]
	[[ $REPLY =~ ^[0-9a-f]{40}$ ]]
}

@test "branch_sha1: same branch yields same key" {
	branch_sha1 "main"
	local first="$REPLY"
	branch_sha1 "main"
	[ "$REPLY" = "$first" ]
}

@test "collapse_check_rollup: all success/neutral → success" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-success.json)"
	[ "$REPLY" = "success" ]
}

@test "collapse_check_rollup: any failure → failure" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-failure.json)"
	[ "$REPLY" = "failure" ]
}

@test "collapse_check_rollup: any pending → pending" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-pending.json)"
	[ "$REPLY" = "pending" ]
}

@test "collapse_check_rollup: empty array → none" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-empty.json)"
	[ "$REPLY" = "none" ]
}

@test "collapse_check_rollup: pending + neutral → pending" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-mixed.json)"
	[ "$REPLY" = "pending" ]
}

@test "collapse_check_rollup: StatusContext failure → failure" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-statuscontext-failure.json)"
	[ "$REPLY" = "failure" ]
}

@test "collapse_check_rollup: StatusContext success → success" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-statuscontext-success.json)"
	[ "$REPLY" = "success" ]
}

@test "collapse_check_rollup: cancelled conclusion → failure" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-cancelled.json)"
	[ "$REPLY" = "failure" ]
}

@test "provider_priority_list: default order from substituted placeholder" {
	provider_priority_list
	[ "$REPLY" = "linear github" ]
}

@test "build_window_label: enriched short = provider id" {
	build_window_label short linear ENG-1957 "refactor services" "" "" "" feat/eng-1957 /x
	[ "$REPLY" = "L ENG-1957" ]
	[ "$REPLY_ID" = "L ENG-1957" ]
	[ "$REPLY_REST" = "" ]
	[ "$REPLY_PR" = "" ]
}

@test "build_window_label: enriched long = provider id title" {
	build_window_label long linear ENG-1957 "refactor services" "" "" "" feat/eng-1957 /x
	[ "$REPLY" = "L ENG-1957 refactor services" ]
	[ "$REPLY_ID" = "L ENG-1957" ]
	[ "$REPLY_REST" = " refactor services" ]
}

@test "build_window_label: failing PR is a separate REPLY_PR segment in both modes" {
	build_window_label short github 247 "fix bug" 247 OPEN failure gh-247 /x
	[ "$REPLY" = "G 247" ]
	[ "$REPLY_PR" = " F #247" ]
	build_window_label long github 247 "fix bug" 247 OPEN failure gh-247 /x
	[ "$REPLY" = "G 247 fix bug" ]
	[ "$REPLY_PR" = " F #247" ]
}

@test "build_window_label: open PR with passing checks uses success glyph" {
	build_window_label short linear ENG-1 "" 9 open success br /x
	[ "$REPLY" = "L ENG-1" ]
	[ "$REPLY_PR" = " S #9" ]
}

@test "build_window_label: merged PR uses merged glyph" {
	build_window_label short linear ENG-1 "t" 9 merged success br /x
	[ "$REPLY" = "L ENG-1" ]
	[ "$REPLY_PR" = " M #9" ]
}

@test "build_window_label: conflicting PR uses conflict glyph" {
	build_window_label short linear ENG-1 "t" 9 open success br /x conflicting
	[ "$REPLY_PR" = " C #9" ]
}

@test "build_window_label: conflict glyph wins over failing checks" {
	build_window_label short linear ENG-1 "t" 9 open failure br /x conflicting
	[ "$REPLY_PR" = " C #9" ]
}

@test "build_window_label: mergeable PR keeps check-state glyph" {
	build_window_label short linear ENG-1 "t" 9 open pending br /x mergeable
	[ "$REPLY_PR" = " P #9" ]
}

@test "build_window_label: pr_number=none is treated as no PR" {
	build_window_label short linear ENG-1 "t" none "" "" br /x
	[ "$REPLY" = "L ENG-1" ]
	[ "$REPLY_PR" = "" ]
}

@test "build_window_label: long with empty title falls back to short form" {
	build_window_label long linear ENG-1 "" "" "" "" br /x
	[ "$REPLY" = "L ENG-1" ]
}

@test "build_window_label: stamped id with empty title uses branch remainder (long)" {
	build_window_label long linear ENG-6011 "" "" "" "" eng-6011-fixservices-dedup-key /x
	[ "$REPLY" = "L ENG-6011 fixservices-dedup-key" ]
	[ "$REPLY_ID" = "L ENG-6011" ]
	[ "$REPLY_REST" = " fixservices-dedup-key" ]
}

@test "build_window_label: plain short = branch basename" {
	build_window_label short "" "" "" "" "" "" feature/fix-login /x
	[ "$REPLY" = "fix-login" ]
	[ "$REPLY_ID" = "" ]
	[ "$REPLY_REST" = "fix-login" ]
}

@test "build_window_label: plain long = full branch" {
	build_window_label long "" "" "" "" "" "" feature/fix-login /x
	[ "$REPLY" = "feature/fix-login" ]
}

@test "build_window_label: no branch falls back to dir basename" {
	build_window_label long "" "" "" "" "" "" "" /home/noams/proj
	[ "$REPLY" = "proj" ]
}

@test "build_window_label: plain branch with merged PR keeps name plain, PR separate (long)" {
	build_window_label long "" "" "" 1921 merged success chore/nango-coding-agent-skill /x
	[ "$REPLY" = "chore/nango-coding-agent-skill" ]
	[ "$REPLY_PR" = " M #1921" ]
}

@test "build_window_label: plain branch with pending PR keeps name plain, PR separate (short)" {
	build_window_label short "" "" "" 1958 open pending feature/fix-login /x
	[ "$REPLY" = "fix-login" ]
	[ "$REPLY_PR" = " P #1958" ]
}

@test "build_window_label: plain branch with no PR is unchanged" {
	build_window_label long "" "" "" none "" "" feature/fix-login /x
	[ "$REPLY" = "feature/fix-login" ]
	[ "$REPLY_PR" = "" ]
}

@test "build_window_label: derives linear key from branch (short)" {
	build_window_label short "" "" "" "" "" "" eng-6017-featservices-gmail /x
	[ "$REPLY" = "L ENG-6017" ]
}

@test "build_window_label: derived linear long uses branch remainder as title" {
	build_window_label long "" "" "" "" "" "" eng-6017-featservices-gmail /x
	[ "$REPLY" = "L ENG-6017 featservices-gmail" ]
	[ "$REPLY_ID" = "L ENG-6017" ]
	[ "$REPLY_REST" = " featservices-gmail" ]
}

@test "build_window_label: derived issue + open PR keeps id name, PR separate" {
	build_window_label short "" "" "" 1958 open pending eng-6017-foo /x
	[ "$REPLY" = "L ENG-6017" ]
	[ "$REPLY_PR" = " P #1958" ]
}

@test "build_window_label: derives github number from numeric branch" {
	build_window_label short "" "" "" "" "" "" 247-fix-bug /x
	[ "$REPLY" = "G 247" ]
}

@test "build_window_label: stamped issue id takes precedence over branch" {
	build_window_label short linear ABC-1 "" "" "" "" eng-6017-foo /x
	[ "$REPLY" = "L ABC-1" ]
}

@test "build_window_label: non-issue branch stays bare" {
	build_window_label long "" "" "" "" "" "" chore/nango-coding-agent-skill /x
	[ "$REPLY" = "chore/nango-coding-agent-skill" ]
	[ "$REPLY_ID" = "" ]
}
