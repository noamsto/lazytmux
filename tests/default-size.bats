#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
	STUBDIR="$(mktemp -d)"
	SET_LOG="$STUBDIR/set.log"
	export SET_LOG
	cat >"$STUBDIR/tmux" <<-'EOF'
		#!/bin/sh
		case "$1" in
		list-clients)
			printf '%s\n' "$FAKE_CLIENTS"
			;;
		show-option)
			echo "${FAKE_DEFAULT_SIZE:-}"
			;;
		set)
			echo "$*" >>"$SET_LOG"
			;;
		esac
	EOF
	chmod +x "$STUBDIR/tmux"
	PATH="$STUBDIR:$PATH"
	SCRIPT="$STUBDIR/tmux-default-size.sh"
	cp scripts/tmux-default-size.sh "$SCRIPT"
	chmod +x "$SCRIPT"
}

teardown() { rm -rf "$STUBDIR"; }

@test "picks largest non-control client and floors at 80x24" {
	FAKE_CLIENTS=$'0|200|55\n1|80|24\n0|40|20' \
		FAKE_DEFAULT_SIZE='' run bash "$SCRIPT"
	[ "$status" -eq 0 ]
	grep -qx 'set -g default-size 200x55' "$SET_LOG"
}

@test "floors undersized real client at 80x24" {
	FAKE_CLIENTS='0|40|20' FAKE_DEFAULT_SIZE='' run bash "$SCRIPT"
	[ "$status" -eq 0 ]
	grep -qx 'set -g default-size 80x24' "$SET_LOG"
}

@test "skips when only control-mode clients exist" {
	FAKE_CLIENTS='1|250|65' FAKE_DEFAULT_SIZE='' run bash "$SCRIPT"
	[ "$status" -eq 0 ]
	[ ! -s "$SET_LOG" ]
}

@test "no-ops when default-size already matches" {
	FAKE_CLIENTS='0|200|55' FAKE_DEFAULT_SIZE='200x55' run bash "$SCRIPT"
	[ "$status" -eq 0 ]
	[ ! -s "$SET_LOG" ]
}
