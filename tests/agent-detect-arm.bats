#!/usr/bin/env bats
# Tests arm_agent_detect in tmux-update-icons.sh: arms pipe-pane for panes
# running a known agent command with no live pipe, skips everything else.

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	# Fake tmux: list-panes reports a codex pane (%3, no pipe), a fish pane
	# (%5, no pipe), a nix-wrapped claude pane (%7, no pipe, as makeWrapper
	# names it on a nix host), and a nix-wrapped non-agent pane (%9, no
	# pipe); pipe-pane calls are recorded so we can assert on them.
	cat >"$FAKEBIN/tmux" <<-EOF
		#!/bin/sh
		case "\$*" in
		*"list-panes"*) printf '%%3|codex|0\n%%5|fish|0\n%%7|.claude-wrapped|0\n%%9|.nvim-wrapped|0\n' ;;
		*"pipe-pane"*) echo "\$@" >>"$BATS_TEST_TMPDIR/pipe.log" ;;
		esac
	EOF
	chmod +x "$FAKEBIN/tmux"
	export PATH="$FAKEBIN:$PATH"
	export AGENT_DETECT_BIN="agent-detect"
	export AGENT_COMMANDS="claude codex"
	# Multiple of 5 so the every-5th-tick throttle lets the sweep run (the lib
	# that normally sets CLAUDE_NOW is a build-time placeholder here).
	export CLAUDE_NOW=100
	: >"$BATS_TEST_TMPDIR/pipe.log"
}

@test "arms pipe-pane for an agent pane with no live pipe" {
	run bash -c 'source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	grep -q 'pipe-pane.*%3.*agent-detect 3' "$BATS_TEST_TMPDIR/pipe.log"
}

@test "does not arm a non-agent pane" {
	run bash -c 'source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	run grep -q '%5' "$BATS_TEST_TMPDIR/pipe.log"
	[ "$status" -ne 0 ]
}

@test "arms pipe-pane for a nix-wrapped agent pane (.foo-wrapped)" {
	run bash -c 'source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	grep -q 'pipe-pane.*%7.*agent-detect 7' "$BATS_TEST_TMPDIR/pipe.log"
}

@test "does not arm a nix-wrapped non-agent pane" {
	run bash -c 'source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	run grep -q '%9' "$BATS_TEST_TMPDIR/pipe.log"
	[ "$status" -ne 0 ]
}

@test "raw unsubstituted AGENT_DETECT_BIN placeholder is a safe no-op" {
	unset AGENT_DETECT_BIN
	run bash -c 'unset AGENT_DETECT_BIN; source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	[ ! -s "$BATS_TEST_TMPDIR/pipe.log" ]
}

@test "raw unsubstituted AGENT_COMMANDS placeholder matches no command" {
	run bash -c 'unset AGENT_COMMANDS; source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	[ ! -s "$BATS_TEST_TMPDIR/pipe.log" ]
}

@test "throttle: sweep is skipped on a non-multiple-of-5 second" {
	run bash -c 'export CLAUDE_NOW=101; source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	[ ! -s "$BATS_TEST_TMPDIR/pipe.log" ]
}

@test "throttle: a non-empty first argument bypasses the non-multiple-of-5 gate" {
	# The @lztmux-sweep-tick monitor hook drives this on its own 5s cadence
	# (main passes "force"), so the modulo must not also gate it -- it would
	# silently stop arming whenever the hook's clock drifts off a multiple of
	# 5. Same CLAUDE_NOW as the throttled case above; only the argument differs.
	run bash -c 'export CLAUDE_NOW=101; source scripts/tmux-update-icons.sh; arm_agent_detect force'
	[ "$status" -eq 0 ]
	grep -q 'pipe-pane.*%3.*agent-detect 3' "$BATS_TEST_TMPDIR/pipe.log"
}

@test "sweep dispatch: fires on LZTMUX_TICK_SWEEP with no arguments (the real hook shape)" {
	# main's sweep branch calls arm_agent_detect force, which bypasses the
	# throttle above -- so CLAUDE_NOW=101 (non-multiple-of-5) still arms here,
	# proving the env var (not argv) drove the dispatch. No arguments at all:
	# with no claude_prune_stale_state on this path, there is no server start
	# time to pass, so the hook command (config/tmux.conf.nix) carries none.
	run bash -c 'export CLAUDE_NOW=101 LZTMUX_TICK_SWEEP=1; source scripts/tmux-update-icons.sh; main'
	[ "$status" -eq 0 ]
	grep -q 'pipe-pane.*%3.*agent-detect 3' "$BATS_TEST_TMPDIR/pipe.log"
}

@test "sweep dispatch: a session literally named --sweep still takes the normal rendering path" {
	# Regression case for the exact bug this dispatch was rewritten to avoid:
	# #{qs:session_name} quotes a session name for the shell but does not
	# change its VALUE, so a session named "--sweep" makes $1 that literal
	# string on every ordinary invocation. With no LZTMUX_TICK_SWEEP set, main
	# must treat "--sweep" as $SESSION and call the NORMAL unguarded
	# arm_agent_detect (no argument) -- which, at a non-multiple-of-5
	# CLAUDE_NOW, is throttled and arms nothing. A misrouted dispatch would
	# instead call arm_agent_detect force (bypassing the throttle, as the test
	# above shows) and return early, so pipe.log would be populated here too.
	# main crashes further on past this point (the raw script's unsubstituted
	# @MAX_ICONS@ placeholder), which is irrelevant to the dispatch decision
	# and happens well after arm_agent_detect has already run -- hence the
	# output redirect rather than asserting on $status.
	run bash -c 'export CLAUDE_NOW=101; unset LZTMUX_TICK_SWEEP; source scripts/tmux-update-icons.sh; main --sweep >/dev/null 2>&1'
	[ ! -s "$BATS_TEST_TMPDIR/pipe.log" ]
}
