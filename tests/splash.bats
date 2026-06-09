#!/usr/bin/env bats

# A fake `tmux` driven by env vars. It logs `display-popup` invocations to
# $POPUP_LOG so the test can assert whether the splash would have opened.
setup() {
	STUBDIR="$(mktemp -d)"
	POPUP_LOG="$STUBDIR/popup.log"
	export POPUP_LOG
	cat >"$STUBDIR/tmux" <<-'EOF'
		#!/bin/sh
		# /bin/sh, not /usr/bin/env: the Nix flake-check sandbox has no /usr/bin.
		# Get the last argument (the format string passed to -p).
		last_arg() { eval "echo \"\${$#}\""; }
		case "$1" in
		show-option) echo "${FAKE_SHOWN:-}";;
		display-message)
			fmt="$(last_arg "$@")"
			case "$fmt" in
			'#{session_name}') echo "s";;
			'#{session_windows}') echo "${FAKE_WINDOWS:-1}";;
			'#{window_panes}') echo "${FAKE_PANES:-1}";;
			'#{pane_current_command}') echo "${FAKE_CMD:-fish}";;
			esac;;
		set-option) ;;
		display-popup) echo "called" >>"$POPUP_LOG";;
		esac
	EOF
	chmod +x "$STUBDIR/tmux"
	PATH="$STUBDIR:$PATH"
	GATE="$STUBDIR/gate.sh"
	sed 's#@tmux_splash@#/bin/true#' scripts/tmux-splash-maybe.sh >"$GATE"
	chmod +x "$GATE"
}

teardown() { rm -rf "$STUBDIR"; }

@test "fresh single-shell session opens the popup" {
	FAKE_SHOWN="" FAKE_WINDOWS=1 FAKE_PANES=1 FAKE_CMD=fish run bash "$GATE" s
	[ "$status" -eq 0 ]
	[ -s "$POPUP_LOG" ]
}

@test "already-shown session does not open the popup" {
	FAKE_SHOWN=1 run bash "$GATE" s
	[ "$status" -eq 0 ]
	[ ! -s "$POPUP_LOG" ]
}

@test "multi-pane session does not open the popup" {
	FAKE_SHOWN="" FAKE_WINDOWS=1 FAKE_PANES=2 run bash "$GATE" s
	[ "$status" -eq 0 ]
	[ ! -s "$POPUP_LOG" ]
}

@test "session running a program (not a shell) does not open the popup" {
	FAKE_SHOWN="" FAKE_WINDOWS=1 FAKE_PANES=1 FAKE_CMD=nvim run bash "$GATE" s
	[ "$status" -eq 0 ]
	[ ! -s "$POPUP_LOG" ]
}
