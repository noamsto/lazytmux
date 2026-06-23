#!/usr/bin/env bats
# Tests tmux-reconcile-window against a real git repo, with tmux + the issue
# stamp faked so we can assert which options get set and whether the stamp fires.

setup() {
	export HOME="$BATS_TEST_TMPDIR" # keep git off any real user config
	STATE="$BATS_TEST_TMPDIR/state"
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$STATE" "$FAKEBIN"
	export FAKE_TMUX_STATE="$STATE"

	# Fake tmux: answers display-message/show-options from env+state, records
	# every set-option to $STATE/setlog and mirrors the value into $STATE/opt_*.
	# Arg shapes match reconcile's exact calls:
	#   display-message -t T -p FMT      (we only need pane_current_path -> FAKE_CWD)
	#   show-options    -t T -wqv OPT    -> OPT is $5
	#   set-option      -t T -w OPT VAL  -> OPT is $5, VAL is $6
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		st="$FAKE_TMUX_STATE"
		case "$1" in
		display-message) printf '%s' "${FAKE_CWD:-}" ;;
		show-options)
			[ -f "$st/opt_$5" ] && cat "$st/opt_$5"
			;;
		set-option)
			printf '%s' "$6" >"$st/opt_$5"
			echo "$5=$6" >>"$st/setlog"
			;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"

	# Fake issue stamp: records its argv so we can assert it ran (and with what).
	cat >"$FAKEBIN/issue-stamp" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$FAKE_TMUX_STATE/stamplog"
	EOF
	chmod +x "$FAKEBIN/issue-stamp"

	# Build a runnable reconcile with the @issue_stamp@ placeholder resolved.
	RECONCILE="$BATS_TEST_TMPDIR/reconcile.sh"
	sed "s|@issue_stamp@|$FAKEBIN/issue-stamp|" scripts/tmux-reconcile-window.sh >"$RECONCILE"

	# A real git worktree to derive from, on a known branch.
	REPO="$BATS_TEST_TMPDIR/repo"
	mkdir -p "$REPO"
	git -C "$REPO" init -q
	git -C "$REPO" config user.email t@t
	git -C "$REPO" config user.name t
	git -C "$REPO" config commit.gpgsign false
	git -C "$REPO" commit -q --allow-empty -m init
	git -C "$REPO" checkout -q -b feat/95-test
	TOP="$(git -C "$REPO" rev-parse --show-toplevel)"

	PLAIN="$BATS_TEST_TMPDIR/plain" # not a git repo
	mkdir -p "$PLAIN"

	export PATH="$FAKEBIN:$PATH"
}

# Poll for a file the disowned background stamp writes.
wait_for() {
	for _ in $(seq 1 40); do
		[[ -f $1 ]] && return 0
		sleep 0.05
	done
	return 1
}

@test "cwd mode: tags a worktree window and kicks the stamp" {
	FAKE_CWD="$REPO" run bash "$RECONCILE" @1
	[ "$status" -eq 0 ]
	[ "$(cat "$STATE/opt_@worktree")" = "$TOP" ]
	[ "$(cat "$STATE/opt_@git_root")" = "$TOP" ]
	[ "$(cat "$STATE/opt_@branch")" = "feat/95-test" ]
	wait_for "$STATE/stamplog"
	[ "$(cat "$STATE/stamplog")" = "@1 $TOP feat/95-test" ]
}

@test "non-worktree cwd: clean no-op, no tags, no stamp" {
	FAKE_CWD="$PLAIN" run bash "$RECONCILE" @1
	[ "$status" -eq 0 ]
	[ ! -f "$STATE/setlog" ]
	[ ! -f "$STATE/stamplog" ]
}

@test "idempotent: matching @worktree/@branch skips writes and stamp" {
	printf '%s' "$TOP" >"$STATE/opt_@worktree"
	printf '%s' "feat/95-test" >"$STATE/opt_@branch"
	FAKE_CWD="$REPO" run bash "$RECONCILE" @1
	[ "$status" -eq 0 ]
	[ ! -f "$STATE/setlog" ]
	[ ! -f "$STATE/stamplog" ]
}

@test "explicit mode: tags from args, ignores cwd" {
	# cwd points at a non-git dir to prove it isn't consulted in explicit mode.
	FAKE_CWD="$PLAIN" run bash "$RECONCILE" @1 "/some/worktree" "feat/95-explicit"
	[ "$status" -eq 0 ]
	[ "$(cat "$STATE/opt_@worktree")" = "/some/worktree" ]
	[ "$(cat "$STATE/opt_@git_root")" = "/some/worktree" ]
	[ "$(cat "$STATE/opt_@branch")" = "feat/95-explicit" ]
	wait_for "$STATE/stamplog"
	[ "$(cat "$STATE/stamplog")" = "@1 /some/worktree feat/95-explicit" ]
}
