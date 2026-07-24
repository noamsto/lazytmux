#!/usr/bin/env bats
# Real tmux server. Pins the two assumptions the fake-tmux suite cannot: window
# options (@worktree, @bridge_win) are readable from a `list-panes -a` format and
# are reported on EVERY pane row of the window, and \t in a format arrives as a
# real tab. Both failures would be silent — the matcher would degrade to
# path-only (R1) or stop excluding bridge windows (R9).
#
# TMUX_TMPDIR is a short, fixed /tmp dir: `tmux -L` resolves to
# "$TMUX_TMPDIR/tmux-<uid>/<name>" and a long bats tmpdir path blows the unix
# socket 108-char limit. -f /dev/null keeps the user's config out of it.

setup() {
	command -v tmux >/dev/null || skip "tmux not on PATH"
	export TMUX_TMPDIR="/tmp/lztmux-wtm-bats-$$"
	rm -rf "$TMUX_TMPDIR"
	mkdir -p "$TMUX_TMPDIR"
	unset TMUX
	TM="tmux -L wtm -f /dev/null"
	$TM kill-server 2>/dev/null || true
	$TM new-session -d -s s1 -x 80 -y 24
	$TM split-window -t s1:
	$TM set-option -t s1: -w @worktree /tmp/wt-a
	$TM set-option -t s1: -w @bridge_win 1
}

teardown() {
	tmux -L wtm kill-server 2>/dev/null || true
	rm -rf "$TMUX_TMPDIR"
}

@test "window options are reported on every pane row of list-panes -a" {
	# $'...' — tmux emits \t literally; see the matcher's comment on the same string.
	run $TM list-panes -a -F $'#{session_name}\t#{window_index}\t#{window_id}\t#{@worktree}\t#{@bridge_win}\t#{pane_active}\t#{pane_current_path}'
	[ "$status" -eq 0 ]
	[ "$(printf '%s\n' "$output" | grep -c .)" -eq 2 ]
	while IFS=$'\t' read -r _ _ wid tag bridge active _; do
		[ -n "$wid" ]
		[ "$tag" = "/tmp/wt-a" ]
		[ "$bridge" = "1" ]
		[ -n "$active" ]
	done <<<"$output"
}
