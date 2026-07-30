#!/usr/bin/env bats
# shellcheck disable=SC2016 # the $-prefixed tmux session id is literal, not an expansion
# API-budget shape of a full enrichment pass. Unusually, these assert on the gh
# calls a pass makes rather than the tmux options it writes: the poller shares a
# 5000/hr GraphQL bucket with every other tool on the machine, so how often it
# asks is the behaviour worth pinning.
#
# Fakes: gh logs its argv and answers from env; tmux answers list-windows from
# $FAKE_WINDOWS and swallows set-option.

load helper

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"
	export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
	export LAZYTMUX_ENRICH_CACHE_DIR="$BATS_TEST_TMPDIR/cache"
	unset TMUX TMUX_PANE

	cat >"$FAKEBIN/gh" <<-'EOF'
		#!/bin/sh
		printf '%s\n' "$*" >>"$GH_LOG"
		case "$*" in
		*"--state open --limit 100"*)
			[ -n "${GH_BATCH_FAIL:-}" ] && exit 1
			printf '%s' "$GH_BATCH_JSON"
			;;
		*) printf '[]' ;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/gh"

	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		case "$1" in
		list-windows) printf '%s\n' "$FAKE_WINDOWS" ;;
		display-message) printf '\n' ;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"

	export PATH="$FAKEBIN:$PATH"
	export HOME="$BATS_TEST_TMPDIR" # keep git off any real user config

	REPO="$BATS_TEST_TMPDIR/repo"
	mkdir -p "$REPO"
	git -C "$REPO" init -q
	git -C "$REPO" config user.email t@t
	git -C "$REPO" config user.name t
	git -C "$REPO" config commit.gpgsign false
	git -C "$REPO" commit -q --allow-empty -m init

	# One window per branch, same repo: one has an open PR, one has none.
	export FAKE_WINDOWS='$1:@1|'"$REPO"'||feat/has-pr|
$1:@2|'"$REPO"'||feat/no-pr|'
	export GH_BATCH_JSON='[{"number":7,"title":"t","url":"u","state":"OPEN","statusCheckRollup":[],"mergeable":"MERGEABLE","isDraft":false,"headRefName":"feat/has-pr"}]'

	make_pr_enrich
}

# Counts logged gh calls matching a fixed string.
gh_calls() {
	grep -cF -- "$1" "$GH_LOG" || true
}

@test "pass: a branch with an open PR costs only the repo batch" {
	run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	[ "$(gh_calls '--head feat/has-pr')" -eq 0 ]
	[ "$(gh_calls '--state open --limit 100')" -eq 1 ]
}

@test "pass: a PR-less branch is settled with one --state all call, never --state open" {
	run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	[ "$(gh_calls '--head feat/no-pr --state all --limit 1')" -eq 1 ]
	[ "$(gh_calls '--head feat/no-pr --state open --limit 1')" -eq 0 ]
}

@test "pass: a second pass re-asks nothing for the PR-less branch" {
	run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	# Two batches (they carry the open PR's live check state), but the terminal
	# answer for feat/no-pr is cached for TTL_TERMINAL.
	[ "$(gh_calls '--state open --limit 100')" -eq 2 ]
	[ "$(gh_calls '--head feat/no-pr')" -eq 1 ]
}

@test "pass: a failed batch falls back to the full open-then-all lookup" {
	GH_BATCH_FAIL=1 run bash "$PR_ENRICH_SCRIPT" --tick-run
	[ "$status" -eq 0 ]
	# Nothing was ruled out, so both heads get the two-call lookup rather than
	# inheriting the batch's authority.
	[ "$(gh_calls '--head feat/has-pr --state open --limit 1')" -eq 1 ]
	[ "$(gh_calls '--head feat/has-pr --state all --limit 1')" -eq 1 ]
	[ "$(gh_calls '--head feat/no-pr --state open --limit 1')" -eq 1 ]
}
