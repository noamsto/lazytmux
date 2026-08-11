#!/usr/bin/env bats
# The picker popup can't be resized after creation, so its height is chosen at
# launch from @picker_layout: list-only opens shorter (#286). tmux is faked on
# PATH; @picker_generate@ (a Nix build placeholder) is stubbed to a marker.

bats_require_minimum_version 1.5.0

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"
	export ARGS_LOG="$BATS_TEST_TMPDIR/popup-args"

	# Fake tmux: `show -gv @picker_layout` returns $FAKE_LAYOUT; display-popup
	# records its argv so the test can read back the -h value. /bin/sh, not
	# /usr/bin/env bash — the nix check sandbox has no /usr/bin/env.
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		case "$1" in
		show) [ "$3" = "@picker_layout" ] && printf '%s\n' "${FAKE_LAYOUT:-}"; exit 0 ;;
		display-popup) printf '%s\n' "$*" >"$ARGS_LOG"; exit 0 ;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"
	export PATH="$FAKEBIN:$PATH"
}

# @picker_generate@ is a Nix build placeholder; stub it so the raw script runs.
mk_launcher() {
	local out="$BATS_TEST_TMPDIR/$1"
	sed 's|@picker_generate@|picker-gen|g' "scripts/$1" >"$out"
	chmod +x "$out"
	printf '%s' "$out"
}

height_of() { sed -n 's/.*-h \([0-9]*%\).*/\1/p' "$ARGS_LOG"; }
width_of() { sed -n 's/.*-w \([0-9]*%\).*/\1/p' "$ARGS_LOG"; }

@test "session picker: list layout opens the short popup" {
	launcher="$(mk_launcher tmux-session-picker.sh)"
	FAKE_LAYOUT=list bash "$launcher"
	[ "$(height_of)" = "60%" ]
}

@test "session picker: preview layout keeps the tall popup" {
	launcher="$(mk_launcher tmux-session-picker.sh)"
	FAKE_LAYOUT=preview bash "$launcher"
	[ "$(height_of)" = "85%" ]
}

@test "session picker: unset layout defaults tall" {
	launcher="$(mk_launcher tmux-session-picker.sh)"
	bash "$launcher"
	[ "$(height_of)" = "85%" ]
}

@test "window picker: list layout opens the short popup" {
	launcher="$(mk_launcher tmux-window-picker.sh)"
	FAKE_LAYOUT=list bash "$launcher"
	[ "$(height_of)" = "60%" ]
}

@test "window picker: preview layout keeps the tall popup" {
	launcher="$(mk_launcher tmux-window-picker.sh)"
	FAKE_LAYOUT=preview bash "$launcher"
	[ "$(height_of)" = "85%" ]
}

@test "window wall: ignores list layout, opens fixed geometry" {
	launcher="$(mk_launcher tmux-window-wall.sh)"
	FAKE_LAYOUT=list bash "$launcher"
	[ "$(width_of)" = "95%" ]
	[ "$(height_of)" = "90%" ]
}

@test "window wall: ignores preview layout, opens fixed geometry" {
	launcher="$(mk_launcher tmux-window-wall.sh)"
	FAKE_LAYOUT=preview bash "$launcher"
	[ "$(width_of)" = "95%" ]
	[ "$(height_of)" = "90%" ]
}

@test "session picker: --client foo pins the popup's client" {
	launcher="$(mk_launcher tmux-session-picker.sh)"
	bash "$launcher" --client foo
	grep -Eq -- '(^| )-c foo( |$)' "$ARGS_LOG"
}

@test "session picker: no --client logs no -c" {
	launcher="$(mk_launcher tmux-session-picker.sh)"
	bash "$launcher"
	run ! grep -Eq -- '(^| )-c ' "$ARGS_LOG"
}

@test "window picker: --client foo --agent pins the client and still reaches --agent" {
	launcher="$(mk_launcher tmux-window-picker.sh)"
	bash "$launcher" --client foo --agent
	grep -Eq -- '(^| )-c foo( |$)' "$ARGS_LOG"
	grep -q -- '--agent' "$ARGS_LOG"
}

@test "scratchpad: --client foo pins the client and still treats sess as the session" {
	launcher="$(mk_launcher tmux-scratchpad.sh)"
	bash "$launcher" --client foo sess
	grep -Eq -- '(^| )-c foo( |$)' "$ARGS_LOG"
	grep -F -- 'scratch: sess' "$ARGS_LOG"
	grep -Eq -- "--attach 'sess'" "$ARGS_LOG"
}
