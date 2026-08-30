#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031 # bats @test blocks run in subshells; export is intentional
# Fanning a light/dark toggle out to the mirrors (#400). The toggle re-sources
# the config, so this script runs on every reload and must stay a no-op unless
# the flavor actually moved.
#
# tmux and the ctl are fakes on PATH; nothing here touches a server or a host.

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	export CTL_LOG="$BATS_TEST_TMPDIR/ctl.log"
	export STAMP="$BATS_TEST_TMPDIR/stamp"
	: >"$TMUX_LOG"
	: >"$CTL_LOG"
	: >"$STAMP"

	# FAKE_SESSIONS is the list-sessions reply: one "sock|name" per line.
	# FAKE_PANE is what a session's mirrored panes report.
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$TMUX_LOG"
		case "$*" in
		"show-options -gv @catppuccin_flavor") echo "${FAKE_FLAVOR:-}" ;;
		"show-options -gv @lztmux_theme_applied") cat "$STAMP" ;;
		"set-option -g @lztmux_theme_applied "*) printf '%s' "$4" >"$STAMP" ;;
		list-sessions*) printf '%s\n' "${FAKE_SESSIONS:-}" ;;
		list-panes*) printf '%s\n' "${FAKE_PANE:-}" ;;
		esac
		exit 0
	EOF

	cat >"$FAKEBIN/lztmux-remote-bridge-ctl" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$CTL_LOG"
		exit "${FAKE_CTL_RC:-0}"
	EOF

	chmod +x "$FAKEBIN"/*
	export PATH="$FAKEBIN:$PATH"

	# The unsubstituted @bridge_ctl@ placeholder falls back to PATH, which is
	# what puts the fake ctl in play; the shipped script takes a pinned store
	# path instead.
	FANOUT="scripts/lztmux-remote-theme.sh"
}

@test "mocha fans out dark to every mirrored session" {
	export FAKE_FLAVOR="mocha"
	FAKE_SESSIONS="$(printf '/run/sock-a|g6-work\n/run/sock-b|mbp-proj\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	grep -q -- "--sock /run/sock-a theme %7 dark" "$CTL_LOG"
	grep -q -- "--sock /run/sock-b theme %7 dark" "$CTL_LOG"
	[ "$(wc -l <"$CTL_LOG")" -eq 2 ]
}

@test "latte is the only flavor that means light" {
	export FAKE_FLAVOR="latte"
	FAKE_SESSIONS="$(printf '/run/sock-a|g6-work\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	grep -q -- "theme %7 light" "$CTL_LOG"
}

@test "an unchanged flavor fans out nothing" {
	export FAKE_FLAVOR="mocha"
	FAKE_SESSIONS="$(printf '/run/sock-a|g6-work\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	[ "$(wc -l <"$CTL_LOG")" -eq 1 ]

	# The reload a `prefix + r` causes, with the theme where it already was.
	: >"$CTL_LOG"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	[ ! -s "$CTL_LOG" ]
}

@test "a local-only session is skipped, mirrors beside it are not" {
	export FAKE_FLAVOR="latte"
	FAKE_SESSIONS="$(printf '|nix-config\n/run/sock-a|g6-work\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	[ "$(wc -l <"$CTL_LOG")" -eq 1 ]
	grep -q -- "--sock /run/sock-a" "$CTL_LOG"
}

@test "no sessions at all is a clean no-op" {
	export FAKE_FLAVOR="latte"
	export FAKE_SESSIONS=""
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	[ ! -s "$CTL_LOG" ]
}

@test "one unreachable daemon does not stop the next host" {
	export FAKE_FLAVOR="latte"
	export FAKE_CTL_RC=1
	FAKE_SESSIONS="$(printf '/run/sock-a|g6-work\n/run/sock-b|mbp-proj\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	[ "$(wc -l <"$CTL_LOG")" -eq 2 ]
}

@test "the stamp advances before the fan-out, so a failed host is not retried forever" {
	export FAKE_FLAVOR="latte"
	export FAKE_CTL_RC=1
	FAKE_SESSIONS="$(printf '/run/sock-a|g6-work\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	[ "$(cat "$STAMP")" = "light" ]
}

@test "a session name holding the delimiter does not corrupt the socket" {
	export FAKE_FLAVOR="latte"
	FAKE_SESSIONS="$(printf '/run/sock-a|weird|name\n')"
	export FAKE_SESSIONS
	export FAKE_PANE="%7"
	run bash "$FANOUT"
	[ "$status" -eq 0 ]
	grep -q -- "--sock /run/sock-a theme %7 light" "$CTL_LOG"
}
