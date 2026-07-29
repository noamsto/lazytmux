#!/usr/bin/env bash
# Notification event store + routing decision (#164). Sourced, not executed, by
# lztmux-notify (the writer) and lztmux-notify-center (the reader).
#
# This library does NOT source lib-log.sh. notify_prune needs acquire_lock and
# file_mtime, so a consumer that calls it must source lib-log.sh first — the
# router does; the center never prunes and needs nothing from lib-log.
# shellcheck disable=SC2034  # constants are used by the sourcing scripts

# Derived at source time so a test can relocate the whole store with one
# exported variable (same shape as CLAUDE_STATUS_DIR in lib-claude.sh).
LZTMUX_NOTIFY_DIR="${LZTMUX_NOTIFY_DIR:-/tmp/lazytmux-notify}"
NOTIFY_EVENTS_DIR="$LZTMUX_NOTIFY_DIR/events"
NOTIFY_MARKER="$LZTMUX_NOTIFY_DIR/.server_start"
NOTIFY_PRUNE_LOCK="$LZTMUX_NOTIFY_DIR/.prune.lock"

# Cap for title/body. They become a tmux message line and a popup row.
NOTIFY_VALUE_MAX=200

# Per-source glyphs. One definition, read by the router's message line and by
# the history center — the two are built in parallel and would otherwise drift.
# Icon overrides are P2. Codepoints verified with fontTools, not guessed.
NOTIFY_ICON_CLAUDE="󰚩"   # nerd: nf-md-robot (U+F06A9)
NOTIFY_ICON_PR="󰘭"       # nerd: nf-md-source-merge (U+F062D)
NOTIFY_ICON_BELL="󰂞"     # nerd: nf-md-bell-ring (U+F009E)
NOTIFY_ICON_ACTIVITY="󱅫" # nerd: nf-md-bell-badge (U+F116B)

# notify_route WINDOW_ACTIVE SESSION_ATTACHED -> REPLY=message|history
# Pure: no tmux, no forks, no stdout. `message` only when the event's window is
# the current one AND someone is attached to watch it; everything else — empty,
# non-numeric, missing — is history. The router is the only caller that feeds
# this live tmux values.
notify_route() {
	REPLY=history
	[[ ${1:-} == 1 ]] || return 0
	[[ ${2:-} =~ ^[0-9]+$ ]] || return 0
	# 10# so a hypothetical leading zero is decimal, not an octal error.
	((10#${2} > 0)) && REPLY=message
	return 0
}

# notify_icon SOURCE -> REPLY (empty for an unknown source)
notify_icon() {
	case "${1:-}" in
	claude) REPLY="$NOTIFY_ICON_CLAUDE" ;;
	pr) REPLY="$NOTIFY_ICON_PR" ;;
	bell) REPLY="$NOTIFY_ICON_BELL" ;;
	activity) REPLY="$NOTIFY_ICON_ACTIVITY" ;;
	*) REPLY="" ;;
	esac
}

# notify_locator SESSION WINDOW -> REPLY, e.g. "lazytmux:@7". The only locator
# format: the renderer and the center call this on the same two stored fields,
# so they agree by construction.
notify_locator() {
	REPLY="${1:-}:${2:-}"
}

# notify_ago SECONDS -> REPLY. Same compact vocabulary as claude_ago in
# lib-claude.sh and relAgo in the picker, so an age reads the same everywhere.
notify_ago() {
	local s="${1:-0}"
	((s < 0)) && s=0
	if ((s < 60)); then
		REPLY="${s}s"
	elif ((s < 3600)); then
		REPLY="$((s / 60))m"
	elif ((s < 86400)); then
		REPLY="$((s / 3600))h"
	else
		REPLY="$((s / 86400))d"
	fi
}

# notify_event_name -> REPLY="<epoch>-<ms>-<pid>". Sorts chronologically under a
# plain glob, so no consumer needs a stat to order events.
# EPOCHREALTIME's fraction is always 6 digits, so its first three ARE the
# zero-padded milliseconds — the same read log_event does in lib-log.sh.
# The [.,] class matters: a non-C LC_NUMERIC uses a comma radix.
notify_event_name() {
	local epoch us=${EPOCHREALTIME#*[.,]}
	printf -v epoch '%(%s)T' -1
	REPLY="$epoch-${us:0:3}-$$"
}

# notify_sanitize VALUE -> REPLY. Squeeze to one line, drop control chars, trim,
# cap. Deletes cntrl rather than keeping [:print:]: tr is byte-oriented, so a
# whitelist strips every non-ASCII byte and mangles UTF-8 (emoji, accents, RTL)
# — the same reasoning as the sanitizers in claude-status-update.sh.
notify_sanitize() {
	local clean
	clean=$(printf '%s' "${1:-}" | tr '\n\r\t' '   ' | tr -d '[:cntrl:]' | tr -s ' ')
	clean="${clean# }"
	clean="${clean:0:NOTIFY_VALUE_MAX}"
	clean="${clean% }"
	REPLY="$clean"
}

# notify_valid_source / notify_valid_level — constrained vocabularies, validated
# rather than sanitized. Status only, no REPLY.
notify_valid_source() {
	case "${1:-}" in
	claude | pr | bell | activity) return 0 ;;
	esac
	return 1
}

notify_valid_level() {
	case "${1:-}" in
	info | warn | error) return 0 ;;
	esac
	return 1
}

# notify_prune SERVER_START
# Drops events written by a previous tmux server. Window and pane ids restart on
# server (re)start, so an event naming @7 from a dead server points at an
# unrelated window — actively misleading, not merely stale. A marker holding the
# current start_time gates the directory scan to once per server generation, so
# the emit path never globs the events dir outside that gate. Structurally
# mirrors claude_prune_stale_state in lib-claude.sh.
#
# Requires acquire_lock + file_mtime from lib-log.sh. Failing to acquire the
# lock is not an error: another emit is already pruning, so skip and continue.
notify_prune() {
	local server_start="${1:-}"
	[[ -z $server_start ]] && return 0
	[[ -r $NOTIFY_MARKER && $(<"$NOTIFY_MARKER") == "$server_start" ]] && return 0
	# The lock is a mkdir inside this dir, so the dir must exist first or every
	# acquire fails and the prune never runs.
	mkdir -p "$LZTMUX_NOTIFY_DIR" 2>/dev/null || return 0
	(
		# Called inside the subshell whose exit releases it: acquire_lock arms an
		# EXIT trap that rmdir's the lock.
		acquire_lock "$NOTIFY_PRUNE_LOCK" || exit 0
		local f mt
		for f in "$NOTIFY_EVENTS_DIR"/*; do
			[[ -f $f ]] || continue
			mt=$(file_mtime "$f")
			((mt < server_start)) && rm -f "$f"
		done
		printf '%s\n' "$server_start" >"$NOTIFY_MARKER"
	)
	return 0
}
