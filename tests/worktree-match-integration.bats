#!/usr/bin/env bats
# Real tmux server. Pins the two assumptions the fake-tmux suite cannot: window
# options (@worktree, @bridge_win) are readable from a `list-panes -a` format and
# are reported on EVERY pane row of the window, and $'\t' in a format arrives as
# a real tab. Both failures would be silent — the matcher would degrade to
# path-only (R1) or stop excluding bridge windows (R9).
#
# TMUX_TMPDIR is a short, per-run /tmp dir: `tmux -L` resolves to
# "$TMUX_TMPDIR/tmux-<uid>/<name>" and a long bats tmpdir path blows the unix
# socket 108-char limit. -f /dev/null keeps the user's config out of it. The
# socket name is per-run too, so this never adopts (or races the teardown of) a
# server left by another run — `kill-server` is asynchronous, and reusing a
# fixed name made this test flaky in the nix sandbox.

setup() {
	command -v tmux >/dev/null || skip "tmux not on PATH"
	export TMUX_TMPDIR="/tmp/lztmux-wtm-$$-${BATS_TEST_NUMBER}"
	rm -rf "$TMUX_TMPDIR"
	mkdir -p "$TMUX_TMPDIR"
	unset TMUX
	SOCK="wtm-$$-${BATS_TEST_NUMBER}"
	TM=(tmux -L "$SOCK" -f /dev/null)

	# Assert the fixture built, so a half-set-up server surfaces here instead of
	# as an unreadable parse failure downstream.
	"${TM[@]}" new-session -d -s s1 -x 80 -y 24
	"${TM[@]}" split-window -t s1:
	"${TM[@]}" set-option -t s1: -w @worktree /tmp/wt-a
	"${TM[@]}" set-option -t s1: -w @bridge_win 1
}

teardown() {
	[ -n "${SOCK:-}" ] && tmux -L "$SOCK" kill-server 2>/dev/null
	[ -n "${TMUX_TMPDIR:-}" ] && rm -rf "$TMUX_TMPDIR"
	return 0
}

@test "window options are reported on every pane row of list-panes -a" {
	# $'...' is load-bearing: tmux emits \t literally, it does not expand it.
	run "${TM[@]}" list-panes -a -F $'#{session_name}\t#{window_index}\t#{window_id}\t#{@worktree}\t#{@bridge_win}\t#{pane_active}\t#{pane_current_path}'
	[ "$status" -eq 0 ]

	# awk, not `IFS=$'\t' read`: tab is an IFS *whitespace* character, so a run
	# of tabs collapses and an empty field would silently shift every later
	# column left — turning a missing option into a confusing parse error.
	# Dump the raw bytes on failure so a future flake is diagnosable.
	printf '%s\n' "$output" | cat -A >&2
	printf '%s\n' "$output" | awk -F'\t' '
		NF != 7 { print "row " NR " has " NF " fields, want 7"; bad = 1; next }
		$3 == "" { print "row " NR ": empty window_id"; bad = 1 }
		$4 != "/tmp/wt-a" { print "row " NR ": @worktree=[" $4 "], want /tmp/wt-a"; bad = 1 }
		$5 != "1" { print "row " NR ": @bridge_win=[" $5 "], want 1"; bad = 1 }
		$6 !~ /^[01]$/ { print "row " NR ": pane_active=[" $6 "]"; bad = 1 }
		END {
			if (NR != 2) { print "got " NR " rows, want 2 (one per pane)"; bad = 1 }
			exit bad
		}
	'
}
