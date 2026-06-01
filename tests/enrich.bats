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
