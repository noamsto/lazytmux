#!/usr/bin/env bash
# Toggle / inspect event-logging debug mode. Sentinel armed => logging on.
# Usage: lazytmux-debug {on|off|toggle|status|tail}
set -euo pipefail
# shellcheck source=/dev/null
source "@lib_log@"

cmd="${1:-toggle}"
msg=""

arm() {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	tmux set -g @lazytmux_debug 1 2>/dev/null || true
	msg="lazytmux debug: ON — $LAZYTMUX_LOG_FILE"
}
disarm() {
	rm -f "$LAZYTMUX_DEBUG_SENTINEL"
	tmux set -g @lazytmux_debug 0 2>/dev/null || true
	msg="lazytmux debug: OFF"
}

case "$cmd" in
on) arm ;;
off) disarm ;;
toggle) if [[ -f $LAZYTMUX_DEBUG_SENTINEL ]]; then disarm; else arm; fi ;;
status)
	if [[ -f $LAZYTMUX_DEBUG_SENTINEL ]]; then
		size=0
		[[ -f $LAZYTMUX_LOG_FILE ]] && size=$(file_size "$LAZYTMUX_LOG_FILE")
		msg="lazytmux debug: ON — $LAZYTMUX_LOG_FILE (${size} bytes)"
	else
		msg="lazytmux debug: OFF"
	fi
	;;
tail) exec tail -n +1 -f "$LAZYTMUX_LOG_FILE" ;;
*)
	echo "usage: lazytmux-debug {on|off|toggle|status|tail}" >&2
	exit 2
	;;
esac

printf '%s\n' "$msg"
# Surface the result in tmux when invoked from a keybinding.
[[ -n ${TMUX:-} ]] && tmux display-message -d 1500 "$msg" 2>/dev/null || true
