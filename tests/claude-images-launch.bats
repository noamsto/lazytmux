#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031  # bats test bodies run in subshells; export is intentional

# Unit-tests resolve_target via the script's --resolve seam, which prints
# "<MODE>\t<KEY>\t<MANIFEST>" and exits before any tmux/kitty call.

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/state"
	SCRIPT="scripts/tmux-claude-images.sh"
	unset TMUX TMUX_PANE KITTY_LISTEN_ON CLAUDE_CODE_SESSION_ID
}

@test "tmux mode: keyed by TMUX_PANE" {
	export TMUX="/tmp/sock,1,0" TMUX_PANE="%7"
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "$output" = "tmux	%7	$CLAUDE_STATUS_DIR/images/7.jsonl" ]
}

@test "kitty mode: keyed by CLAUDE_CODE_SESSION_ID when not in tmux" {
	export KITTY_LISTEN_ON="unix:/tmp/kitty-1" CLAUDE_CODE_SESSION_ID="sess-abc"
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "$output" = "kitty	sess-abc	$CLAUDE_STATUS_DIR/images/sess-abc.jsonl" ]
}

@test "tmux wins over kitty when both present" {
	export TMUX="/tmp/sock,1,0" TMUX_PANE="%3" KITTY_LISTEN_ON="unix:/tmp/kitty-1"
	run bash "$SCRIPT" --resolve
	[ "${output%%	*}" = "tmux" ]
}

@test "none mode: neither tmux nor kitty" {
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "${output%%	*}" = "none" ]
}

@test "kitty mode with empty session id resolves with empty key" {
	export KITTY_LISTEN_ON="unix:/tmp/kitty-1"
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "$output" = "kitty		$CLAUDE_STATUS_DIR/images/.jsonl" ]
}
