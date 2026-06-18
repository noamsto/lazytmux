#!/usr/bin/env bats

# Guards the UserPromptSubmit `task` hook in claude-plugin/scripts/status.sh:
# Claude Code re-injects non-user text (background-task notices, system
# reminders, command envelopes) wrapped in a hyphenated XML-ish tag. Those must
# NOT become the task label / window-name seed; real prompts must.

load helper

STATUS="claude-plugin/scripts/status.sh"

setup() {
	unset TMUX TMUX_PANE # no window-naming side effects; the task-set call is what we assert
	export CALLS="$BATS_TEST_TMPDIR/calls"
	: >"$CALLS"
	# Stub claude-status-update as an exported function: the child `bash
	# status.sh` inherits it, `command -v` finds it, and it needs no
	# executable/shebang — so it works in the pure nix-check sandbox where
	# /usr/bin/env doesn't exist.
	# shellcheck disable=SC2329 # invoked indirectly by the child `bash status.sh` via export -f
	claude-status-update() { printf '%s\n' "$*" >>"$CALLS"; }
	export -f claude-status-update
}

run_task() { # JSON prompt object on stdin
	printf '%s' "$1" | bash "$STATUS" task
}

@test "task: <task-notification> envelope is skipped" {
	run_task '{"prompt":"<task-notification> background task finished"}'
	[ ! -s "$CALLS" ]
}

@test "task: <system-reminder> envelope is skipped" {
	run_task '{"prompt":"<system-reminder>do the thing</system-reminder>"}'
	[ ! -s "$CALLS" ]
}

@test "task: <local-command-stdout> envelope is skipped" {
	run_task '{"prompt":"<local-command-stdout>output</local-command-stdout>"}'
	[ ! -s "$CALLS" ]
}

@test "task: leading whitespace before the envelope is still skipped" {
	run_task '{"prompt":"   <command-name>foo</command-name>"}'
	[ ! -s "$CALLS" ]
}

@test "task: a real prompt is forwarded to task set" {
	run_task '{"prompt":"fix the login bug"}'
	grep -qx 'task set fix the login bug' "$CALLS"
}

@test "task: a prompt opening with a hyphenated word but no brackets is NOT skipped" {
	run_task '{"prompt":"task-notification without brackets here"}'
	grep -q '^task set ' "$CALLS"
}

@test "task: an opening <tag> with no hyphen is NOT skipped" {
	run_task '{"prompt":"<div> render the component"}'
	grep -q '^task set ' "$CALLS"
}
