#!/usr/bin/env bats
# Tests tmux-worktree-match against a fake tmux: `list-panes` prints the
# fixture rows, `set-option` is recorded so the R15 tag-clearing can be
# asserted in both directions. Every case here is a row of SPEC.md's test
# table; the requirement id is in the test name.

setup() {
	STATE="$BATS_TEST_TMPDIR/state"
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$STATE" "$FAKEBIN"
	export FAKE_TMUX_STATE="$STATE"

	# Fake tmux. Arg shapes match the matcher's exact calls:
	#   list-panes -a -F FMT          -> prints $FAKE_ROWS, logs the call
	#   set-option -t ID -w -u @opt   -> logged verbatim (minus the subcommand)
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		st="$FAKE_TMUX_STATE"
		case "$1" in
		list-panes)
			echo "list-panes" >>"$st/calls"
			[ -n "${FAKE_TMUX_FAIL:-}" ] && exit 1
			printf '%s' "${FAKE_ROWS:-}"
			;;
		set-option)
			shift
			echo "$*" >>"$st/setlog"
			;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"
	export PATH="$FAKEBIN:$PATH"

	MATCH="$BATS_TEST_DIRNAME/../scripts/tmux-worktree-match.sh"

	# W must exist on disk: the matcher probes it with `cd -P` (R14).
	W="$BATS_TEST_TMPDIR/wt"
	V="$BATS_TEST_TMPDIR/other"
	mkdir -p "$W" "$V"
}

# sess widx wid tag bridge active path
row() { printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$@"; }

@test "R1: tag alone never matches — tagged window whose panes are elsewhere" {
	FAKE_ROWS="$(row s1 1 @1 "$W" "" 1 "$V")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R1: falls through to a real, untagged window in the worktree" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$V"
		row s1 2 @2 "" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ "$output" = "$(printf 's1\t2\t@2')" ]
}

@test "R2: tagged window validated by a background pane" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$V"
		row s1 1 @1 "$W" "" 0 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R2: untagged window with only a background pane in W is NOT a candidate" {
	rows="$(
		row s1 1 @1 "" "" 1 "$V"
		row s1 1 @1 "" "" 0 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R2: untagged window whose active pane is in W matches (rank 2)" {
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$W")" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R2: untagged window with an empty pane path never validates" {
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R2a: tagged window whose only pane path is unreadable matches (rank 4)" {
	FAKE_ROWS="$(row s1 1 @1 "$W" "" 1 "")" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R2a: rank 2 beats rank 4" {
	rows="$(
		row s1 1 @1 "$W" "" 1 ""
		row s1 2 @2 "" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t2\t@2')" ]
}

@test "R3: a sibling path sharing a string prefix does not match" {
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$W-old")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R3: a pane deep under W/ matches" {
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$W/src/deep")" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R4: a pane under W/.worktrees/ does not match" {
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$W/.worktrees/other")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R5: window rank is the MINIMUM over its panes, not the first validating row" {
	# @2 (rank 1) is listed first; @1's background validating pane precedes its
	# active one. A first-match-then-exit ranker scores @1 as rank 1 and picks @2.
	rows="$(
		row s1 2 @2 "$W" "" 1 "$V"
		row s1 2 @2 "$W" "" 0 "$W"
		row s1 1 @1 "$W" "" 0 "$W"
		row s1 1 @1 "$W" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R5: rank 0 beats rank 1" {
	rows="$(
		row s1 1 @1 "$W" "" 0 "$W"
		row s1 1 @1 "$W" "" 1 "$V"
		row s1 2 @2 "$W" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t2\t@2')" ]
}

@test "R5: rank 1 beats rank 2" {
	rows="$(
		row s1 1 @1 "" "" 1 "$W"
		row s1 2 @2 "$W" "" 1 "$V"
		row s1 2 @2 "$W" "" 0 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t2\t@2')" ]
}

@test "R6: the current window wins its rank even when listed second" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$W"
		row s1 2 @2 "$W" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 2
	[ "$output" = "$(printf 's1\t2\t@2')" ]
}

@test "R6: with no current-window candidate, row order wins" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$W"
		row s1 2 @2 "$W" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R8: a match in another session is reported with that session's name" {
	FAKE_ROWS="$(row s2 3 @9 "$W" "" 1 "$W")" run bash "$MATCH" "$W" s1 1
	[ "$output" = "$(printf 's2\t3\t@9')" ]
}

@test "R9: a tagged bridge window is neither a candidate nor cleared" {
	FAKE_ROWS="$(row s1 1 @1 "$W" 1 1 "$V")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	[ ! -f "$STATE/setlog" ]
}

@test "R9: an untagged bridge window sitting in W is not a rank-2 candidate" {
	FAKE_ROWS="$(row s1 1 @1 "" 1 1 "$W")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R13: a match prints three tab-separated fields ending in the window id" {
	FAKE_ROWS="$(row s1 4 @7 "$W" "" 1 "$W")" run bash "$MATCH" "$W" s1 9
	[ "$(printf '%s' "$output" | awk -F'\t' '{print NF}')" = "3" ]
	[ "$(printf '%s' "$output" | cut -f3)" = "@7" ]
}

@test "R13: a failing tmux yields empty output and status 0" {
	FAKE_TMUX_FAIL=1 FAKE_ROWS="$(row s1 1 @1 "$W" "" 1 "$W")" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R13: zero rows yields empty output and status 0" {
	FAKE_ROWS="" run bash "$MATCH" "$W" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "R13: an empty target exits 0 without querying tmux" {
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$W")" run bash "$MATCH" "" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	[ ! -f "$STATE/calls" ]
}

@test "R14: a symlinked target matches a pane reporting the resolved path" {
	mkdir -p "$BATS_TEST_TMPDIR/real"
	# Resolve through any symlink in the tmpdir itself (macOS /var -> /private/var),
	# so the fixture path is spelled exactly as `cd -P` will report it.
	real="$(cd -P "$BATS_TEST_TMPDIR/real" && pwd -P)"
	link="$BATS_TEST_TMPDIR/link"
	ln -s "$real" "$link"
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$real/src")" run bash "$MATCH" "$link" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R14: a target that does not exist still matches its raw spelling" {
	gone="$BATS_TEST_TMPDIR/gone"
	FAKE_ROWS="$(row s1 1 @1 "" "" 1 "$gone")" run bash "$MATCH" "$gone" s1 9
	[ "$output" = "$(printf 's1\t1\t@1')" ]
}

@test "R15: a falsified tag is cleared" {
	FAKE_ROWS="$(row s1 1 @1 "$W" "" 1 "$V")" run bash "$MATCH" "$W" s1 9
	[ "$(cat "$STATE/setlog")" = "-t @1 -w -u @worktree" ]
}

@test "R15: a corroborated tag is left alone" {
	FAKE_ROWS="$(row s1 1 @1 "$W" "" 1 "$W")" run bash "$MATCH" "$W" s1 9
	[ ! -f "$STATE/setlog" ]
}

@test "R15: a tag naming a DIFFERENT worktree is never cleared" {
	# Non-goal: repairing tags in general. Only a @worktree == W tag proven
	# false while resolving a switch to W may be unset (#100 owns the rest).
	FAKE_ROWS="$(row s1 1 @1 "$V" "" 1 "$V")" run bash "$MATCH" "$W" s1 9
	[ -z "$output" ]
	[ ! -f "$STATE/setlog" ]
}

@test "R15: a tag is not cleared on evidence of an unreadable pane path" {
	FAKE_ROWS="$(row s1 1 @1 "$W" "" 1 "")" run bash "$MATCH" "$W" s1 9
	[ ! -f "$STATE/setlog" ]
}

@test "R15: clearing happens even when another window matched" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$V"
		row s1 2 @2 "" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$output" = "$(printf 's1\t2\t@2')" ]
	[ "$(cat "$STATE/setlog")" = "-t @1 -w -u @worktree" ]
}

@test "R15: clearing is suppressed when the target directory cannot be resolved" {
	gone="$BATS_TEST_TMPDIR/gone"
	FAKE_ROWS="$(row s1 1 @1 "$gone" "" 1 "$V")" run bash "$MATCH" "$gone" s1 9
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	[ ! -f "$STATE/setlog" ]
}

@test "R12: exactly one list-panes invocation" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$V"
		row s1 2 @2 "" "" 1 "$W"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$(grep -c list-panes "$STATE/calls")" -eq 1 ]
}

@test "R16: clearing writes nothing but -u @worktree" {
	rows="$(
		row s1 1 @1 "$W" "" 1 "$V"
		row s1 2 @2 "$W" "" 1 "$V"
	)"
	FAKE_ROWS="$rows" run bash "$MATCH" "$W" s1 9
	[ "$(grep -c . "$STATE/setlog")" -eq 2 ]
	[ "$(grep -cv -- '-w -u @worktree$' "$STATE/setlog")" -eq 0 ]
}
