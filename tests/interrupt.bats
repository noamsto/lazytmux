#!/usr/bin/env bats

load helper

setup() {
	setup_lib_claude
	PANE_DIR="$BATS_TEST_TMPDIR/panes"
	mkdir -p "$PANE_DIR"
}

# write_pane FILE STATE AGE_SECONDS [TRANSCRIPT_PATH]
write_pane() {
	local ts=$((CLAUDE_NOW - $3))
	{
		echo "state=$2"
		echo "timestamp=$ts"
		echo "session=work"
		[[ -n ${4:-} ]] && echo "transcript=$4"
	} >"$1"
	return 0
}

@test "read_pane_state: stale processing with marker at tail → interrupted" {
	local tr="$BATS_TEST_TMPDIR/t.jsonl"
	printf '%s\n' \
		'{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}' \
		'{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}' >"$tr"
	write_pane "$PANE_DIR/p1" processing 60 "$tr"
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "interrupted" ]
	[ "$REPLY_FADE" -eq 0 ]
	[ "$REPLY_UNSEEN" -eq 1 ]
}

@test "read_pane_state: fresh processing is not checked → processing" {
	local tr="$BATS_TEST_TMPDIR/t.jsonl"
	printf '%s\n' '{"text":"[Request interrupted by user]"}' >"$tr"
	write_pane "$PANE_DIR/p1" processing 2 "$tr"
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "processing" ]
}

@test "read_pane_state: stale processing without marker (long tool) → processing" {
	local tr="$BATS_TEST_TMPDIR/t.jsonl"
	printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}' >"$tr"
	write_pane "$PANE_DIR/p1" processing 120 "$tr"
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "processing" ]
}

@test "read_pane_state: marker present but not near tail (resumed) → processing" {
	local tr="$BATS_TEST_TMPDIR/t.jsonl"
	printf '%s\n' \
		'{"text":"[Request interrupted by user]"}' \
		'{"type":"user","message":"resumed"}' \
		'{"type":"assistant","message":"back to work"}' >"$tr"
	write_pane "$PANE_DIR/p1" processing 120 "$tr"
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "processing" ]
}

@test "read_pane_state: stale processing with no transcript path → processing" {
	write_pane "$PANE_DIR/p1" processing 120 ""
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "processing" ]
}

@test "read_pane_state: transcript file missing → processing" {
	write_pane "$PANE_DIR/p1" processing 120 "$BATS_TEST_TMPDIR/absent.jsonl"
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "processing" ]
}

@test "read_pane_state: terminal states are never reclassified" {
	local tr="$BATS_TEST_TMPDIR/t.jsonl"
	printf '%s\n' '{"text":"[Request interrupted by user]"}' >"$tr"
	write_pane "$PANE_DIR/p1" "done" 120 "$tr"
	read_pane_state "$PANE_DIR/p1"
	[ "$REPLY" = "done" ]
}

@test "claude_priority_state: interrupted outranks processing/done/idle" {
	claude_priority_state 0 0 1 1 1 0 0 1
	[ "$REPLY" = "interrupted" ]
}

@test "claude_priority_state: error/waiting/compacting outrank interrupted" {
	claude_priority_state 0 0 0 0 0 1 0 1
	[ "$REPLY" = "error" ]
	claude_priority_state 1 0 0 0 0 0 0 1
	[ "$REPLY" = "waiting" ]
	claude_priority_state 0 1 0 0 0 0 0 1
	[ "$REPLY" = "compacting" ]
}

@test "interrupted resolves to a glyph and a color" {
	claude_state_icon interrupted
	[ -n "$REPLY" ]
	setup_claude_colors
	claude_faded_hex interrupted
	[ -n "$REPLY" ]
}
