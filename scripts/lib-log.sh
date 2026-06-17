#!/usr/bin/env bash
# Event logging for debugging tmux/claude/reflow/enrich/picker behavior.
# Sourced, not executed. Off unless the sentinel exists; the gate is a
# fork-free [[ -f ]] test, so hot paths pay nothing when debug is off.
# See docs/superpowers/specs/2026-06-09-event-logging-design.md
# shellcheck disable=SC2034  # exported names are used by sourcing scripts

LAZYTMUX_LOG_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/lazytmux"
LAZYTMUX_LOG_FILE="$LAZYTMUX_LOG_DIR/events.log"
# Sentinel lives in /tmp: dies on reboot, survives config reload (tmux sources
# the conf on every prefix+r, so a conf-load clear would disarm debug mid-bug).
LAZYTMUX_DEBUG_SENTINEL="${LAZYTMUX_DEBUG_SENTINEL:-/tmp/lazytmux-debug.on}"

# log_enabled: true when debug is armed. Fork-free builtin test — the hot-path gate.
log_enabled() { [[ -f $LAZYTMUX_DEBUG_SENTINEL ]]; }

# file_size / file_mtime FILE -> bytes / mtime-epoch on stdout (0 if absent).
# Portable: GNU `stat -c` first, BSD/macOS `stat -f` fallback. Home is lib-log
# because every stat-using script already sources it.
file_size() { stat -c %s "$1" 2>/dev/null || stat -f %z "$1" 2>/dev/null || echo 0; }
file_mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null || echo 0; }

# _json_escape STR -> REPLY  (JSON-safe inner string, no surrounding quotes).
# Backslash first, then quote/tab/cr; newlines stripped; remaining C0 controls stripped.
_json_escape() {
	local s=$1
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\t'/\\t}
	s=${s//$'\r'/\\r}
	s=${s//$'\n'/}
	s=${s//[$'\x01'-$'\x08'$'\x0b'$'\x0c'$'\x0e'-$'\x1f']/}
	REPLY=$s
}

# _log_rotate: flock-guarded size rotation, keeps events.log.1. Cap is read live
# from LAZYTMUX_LOG_MAX_BYTES (default 5 MiB) so tests can shrink it.
_log_rotate() {
	[[ -f $LAZYTMUX_LOG_FILE ]] || return 0
	local cap="${LAZYTMUX_LOG_MAX_BYTES:-5242880}"
	local size
	size=$(file_size "$LAZYTMUX_LOG_FILE")
	((size < cap)) && return 0
	(
		flock -n 9 || exit 0
		local s
		s=$(file_size "$LAZYTMUX_LOG_FILE")
		((s >= cap)) && mv -f "$LAZYTMUX_LOG_FILE" "$LAZYTMUX_LOG_FILE.1"
	) 9>"$LAZYTMUX_LOG_DIR/.rotate.lock"
}

# log_event CATEGORY [KEY VALUE]...  No-op unless debug armed. One JSON line.
log_event() {
	log_enabled || return 0
	local cat=$1
	shift
	mkdir -p "$LAZYTMUX_LOG_DIR"
	# Millisecond ISO-8601, fork-free: bash strftime + EPOCHREALTIME. Avoids
	# date's GNU-only %N (BSD date has no sub-second). [.,] tolerates a comma
	# radix under a non-C LC_NUMERIC.
	local ts us=${EPOCHREALTIME#*[.,]}
	printf -v ts '%(%FT%T)T.%s' -1 "${us:0:3}"
	_json_escape "$cat"
	local line="{\"ts\":\"$ts\",\"cat\":\"$REPLY\""
	local k v ek
	while (($# >= 2)); do
		k=$1
		v=$2
		shift 2
		_json_escape "$k"
		ek=$REPLY
		_json_escape "$v"
		line+=",\"$ek\":\"$REPLY\""
	done
	line+="}"
	_log_rotate
	printf '%s\n' "$line" >>"$LAZYTMUX_LOG_FILE"
}
