#!/usr/bin/env bats
# Tests claude-status-update.sh's `enrich [<ID>]` subcommand (#137): resolves
# the invoking pane's window/worktree/branch and re-fires tmux-issue-stamp,
# silently, outside a lazytmux tmux.

CSU="scripts/claude-status-update.sh"

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/claude-status"
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	# Fake tmux: only display-message -t T -p '#{pane_current_path}' is used by
	# the enrich path, answered from $FAKE_CWD regardless of target.
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		case "$1" in
		display-message) printf '%s' "${FAKE_CWD:-}" ;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"

	# Fake tmux-issue-stamp: records its argv so we can assert what fired.
	cat >"$FAKEBIN/tmux-issue-stamp" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$FAKE_STATE/stamplog"
	EOF
	chmod +x "$FAKEBIN/tmux-issue-stamp"

	export FAKE_STATE="$BATS_TEST_TMPDIR"
	export PATH="$FAKEBIN:$PATH"
	export HOME="$BATS_TEST_TMPDIR" # keep git off any real user config
	export TMUX="/tmp/fake-tmux-socket,1,0"
	unset TMUX_PANE

	REPO="$BATS_TEST_TMPDIR/repo"
	mkdir -p "$REPO"
	git -C "$REPO" init -q
	git -C "$REPO" config user.email t@t
	git -C "$REPO" config user.name t
	git -C "$REPO" config commit.gpgsign false
	git -C "$REPO" commit -q --allow-empty -m init
	git -C "$REPO" checkout -q -b feat/95-test
	export REPO
	TOP="$(git -C "$REPO" rev-parse --show-toplevel)"
	export TOP

	PLAIN="$BATS_TEST_TMPDIR/plain" # not a git repo
	mkdir -p "$PLAIN"
	export PLAIN
}

# Poll for a file the disowned background stamp writes.
wait_for() {
	for _ in $(seq 1 40); do
		[[ -f $1 ]] && return 0
		sleep 0.05
	done
	return 1
}

@test "enrich: outside tmux is a silent no-op" {
	unset TMUX
	FAKE_CWD="$REPO" run bash "$CSU" enrich --pane %7
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	sleep 0.2
	[ ! -f "$BATS_TEST_TMPDIR/stamplog" ]
}

@test "enrich: no pane id is a silent no-op" {
	FAKE_CWD="$REPO" run bash "$CSU" enrich
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	sleep 0.2
	[ ! -f "$BATS_TEST_TMPDIR/stamplog" ]
}

@test "enrich: tmux-issue-stamp missing from PATH is a silent no-op" {
	rm -f "$FAKEBIN/tmux-issue-stamp"
	FAKE_CWD="$REPO" run bash "$CSU" enrich --pane %7
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "enrich: non-git cwd is a no-op" {
	FAKE_CWD="$PLAIN" run bash "$CSU" enrich --pane %7
	[ "$status" -eq 0 ]
	sleep 0.2
	[ ! -f "$BATS_TEST_TMPDIR/stamplog" ]
}

@test "enrich: invalid id format errors even outside tmux" {
	unset TMUX
	run bash "$CSU" enrich 'bad id!' --pane %7
	[ "$status" -eq 1 ]
	[[ $output == *"Invalid issue id"* ]]
}

@test "enrich: no id resolves branch/worktree from the pane cwd" {
	FAKE_CWD="$REPO" run bash "$CSU" enrich --pane %7
	[ "$status" -eq 0 ]
	wait_for "$BATS_TEST_TMPDIR/stamplog"
	# Trailing space: the empty 4th (explicit-id) arg still occupies a "$*" slot.
	[ "$(cat "$BATS_TEST_TMPDIR/stamplog")" = "%7 $TOP feat/95-test " ]
}

@test "enrich: explicit id is forwarded as the 4th arg" {
	FAKE_CWD="$REPO" run bash "$CSU" enrich GH-42 --pane %7
	[ "$status" -eq 0 ]
	wait_for "$BATS_TEST_TMPDIR/stamplog"
	[ "$(cat "$BATS_TEST_TMPDIR/stamplog")" = "%7 $TOP feat/95-test GH-42" ]
}

@test "enrich: --pane normalizes a bare pane id (no leading %)" {
	FAKE_CWD="$REPO" run bash "$CSU" enrich --pane 7
	[ "$status" -eq 0 ]
	wait_for "$BATS_TEST_TMPDIR/stamplog"
	[ "$(cat "$BATS_TEST_TMPDIR/stamplog")" = "%7 $TOP feat/95-test " ]
}
