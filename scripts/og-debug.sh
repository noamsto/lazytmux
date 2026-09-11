#!/usr/bin/env bash
# Toggle / inspect event-logging debug mode. Sentinel armed => logging on.
# Usage: og-debug {on|off|toggle|status|tail}
set -euo pipefail
# shellcheck source=/dev/null
source "@lib_log@"

cmd="${1:-toggle}"
msg=""

arm() {
	: >"$OG_DEBUG_SENTINEL"
	tmux set -g @og_debug 1 2>/dev/null || true
	msg="og debug: ON — $OG_LOG_FILE"
}
disarm() {
	rm -f "$OG_DEBUG_SENTINEL"
	tmux set -g @og_debug 0 2>/dev/null || true
	msg="og debug: OFF"
}

case "$cmd" in
on) arm ;;
off) disarm ;;
toggle) if [[ -f $OG_DEBUG_SENTINEL ]]; then disarm; else arm; fi ;;
status)
	if [[ -f $OG_DEBUG_SENTINEL ]]; then
		size=0
		[[ -f $OG_LOG_FILE ]] && size=$(file_size "$OG_LOG_FILE")
		msg="og debug: ON — $OG_LOG_FILE (${size} bytes)"
	else
		msg="og debug: OFF"
	fi
	;;
tail) exec tail -n +1 -f "$OG_LOG_FILE" ;;
*)
	echo "usage: og-debug {on|off|toggle|status|tail}" >&2
	exit 2
	;;
esac

printf '%s\n' "$msg"
# Surface the result in tmux when invoked from a keybinding.
[[ -n ${TMUX:-} ]] && tmux display-message -d 1500 "$msg" 2>/dev/null || true
