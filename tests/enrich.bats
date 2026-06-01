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

@test "provider_priority_list: default order from substituted placeholder" {
	provider_priority_list
	[ "$REPLY" = "linear github" ]
}
