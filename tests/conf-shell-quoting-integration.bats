#!/usr/bin/env bats
# shellcheck disable=SC2016,SC2088 # the literal $ and ~ format fixtures must not expand in this test shell
bats_require_minimum_version 1.5.0 # run !
# Behavioural guard for tmux's two shell-quoting format modifiers.  `q:` never
# wraps, whereas next-3.8's `qs:` emits a POSIX single-quoted shell word.

setup() {
	TMUX_BIN="${TMUX_BIN:?set TMUX_BIN to the built wrapper}"
	SOCKET="lztmux-shell-quoting-${BATS_TEST_NUMBER}-$$"
	SESSION="shell-quoting"
	TEST_HOME="$BATS_TEST_TMPDIR/home"
	mkdir -p "$TEST_HOME"
	export HOME="$TEST_HOME"
	export XDG_CACHE_HOME="$TEST_HOME/.cache"
	export XDG_CONFIG_HOME="$TEST_HOME/.config"
	export XDG_STATE_HOME="$TEST_HOME/.local/state"
	export TERM=xterm-256color

	t new-session -d -s "$SESSION"

	# tmux 3.7 accepts qs: but silently returns the raw value. This must fail
	# before any behavioural assertion can report a hollow green result.
	t set-option -g @qs_probe lztmux
	[ "$(t display-message -p -t "$SESSION" '#{qs:@qs_probe}')" = "'lztmux'" ]
}

teardown() {
	t kill-server 2>/dev/null || true
}

t() {
	timeout --foreground 30s "$TMUX_BIN" -L "$SOCKET" "$@"
}

make_argv_recorder() {
	ARGV_RECORDER="$BATS_TEST_TMPDIR/record-argv"
	cat >"$ARGV_RECORDER" <<-'EOF'
		out=$1
		shift
		printf 'ARGC=%d\n' "$#" >"$out"
		for arg; do
			printf '<%s>\n' "$arg" >>"$out"
		done
	EOF
}

run_shell_to() { # target output format
	local target="$1" output="$2" format="$3" command
	printf -v command 'bash %q %q %s' "$ARGV_RECORDER" "$output" "$format"
	t run-shell -t "$target" "$command"
}

@test "qs preserves a leading tilde as one run-shell argv word" {
	local session='~/src'
	make_argv_recorder
	t new-session -d -s "$session"

	run_shell_to "$session" "$BATS_TEST_TMPDIR/tilde" '#{qs:session_name}'
	[ "$(cat "$BATS_TEST_TMPDIR/tilde")" = $'ARGC=1\n<~/src>' ]
}

@test "qs preserves an empty expansion as one run-shell argv word" {
	make_argv_recorder
	t set-option -g @empty_probe ''

	run_shell_to "$SESSION" "$BATS_TEST_TMPDIR/empty" '#{qs:@empty_probe}'
	[ "$(cat "$BATS_TEST_TMPDIR/empty")" = $'ARGC=1\n<>' ]
}

@test "q leaves a leading tilde unescaped at format level" {
	# If upstream starts wrapping or escaping ~ in q:, revisit the conversion:
	# this sentinel describes why qs: is required by the shipped tmux today.
	t set-option -g @q_probe '~/src'
	[ "$(t display-message -p -t "$SESSION" '#{q:@q_probe}')" = '~/src' ]
}
