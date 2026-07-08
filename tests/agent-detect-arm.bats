#!/usr/bin/env bats
# Tests arm_agent_detect in tmux-update-icons.sh: arms pipe-pane for panes
# running a known agent command with no live pipe, skips everything else.

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	# Fake tmux: list-panes reports a codex pane (%3, no pipe) and a fish pane
	# (%5, no pipe); pipe-pane calls are recorded so we can assert on them.
	cat >"$FAKEBIN/tmux" <<-EOF
		#!/bin/sh
		case "\$*" in
		*"list-panes"*) printf '%%3\tcodex\t0\n%%5\tfish\t0\n' ;;
		*"pipe-pane"*) echo "\$@" >>"$BATS_TEST_TMPDIR/pipe.log" ;;
		esac
	EOF
	chmod +x "$FAKEBIN/tmux"
	export PATH="$FAKEBIN:$PATH"
	export AGENT_DETECT_BIN="agent-detect"
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

@test "raw unsubstituted AGENT_DETECT_BIN placeholder is a safe no-op" {
	unset AGENT_DETECT_BIN
	run bash -c 'unset AGENT_DETECT_BIN; source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	[ ! -s "$BATS_TEST_TMPDIR/pipe.log" ]
}

@test "throttle: sweep is skipped on a non-multiple-of-5 second" {
	run bash -c 'export CLAUDE_NOW=101; source scripts/tmux-update-icons.sh; arm_agent_detect'
	[ "$status" -eq 0 ]
	[ ! -s "$BATS_TEST_TMPDIR/pipe.log" ]
}
