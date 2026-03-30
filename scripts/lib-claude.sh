#!/usr/bin/env bash
# Shared Claude status utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.

# shellcheck disable=SC2034  # used by scripts that source this library
CLAUDE_PANES_DIR="/tmp/claude-status/panes"
CLAUDE_SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
CLAUDE_ICON_WAITING="󰔟"
CLAUDE_ICON_COMPACTING="󰡍"
CLAUDE_ICON_DONE="󰸞"
CLAUDE_ICON_IDLE="󰒲"
CLAUDE_ICON_ERROR="󰅚" # nerd: nf-md-close_circle_outline

# Timestamp cache (set once per script invocation)
printf -v CLAUDE_NOW '%(%s)T' -1

# Staleness thresholds (seconds) — icon stays, color dims past these
CLAUDE_STALE_WAITING=30
CLAUDE_STALE_COMPACTING=60
CLAUDE_STALE_PROCESSING=300
CLAUDE_STALE_DONE=60
CLAUDE_STALE_ERROR=120

# read_pane_state PANE_FILE_PATH
# Reads a pane state file and checks staleness.
# Sets REPLY to the state string, REPLY_STALE to 0 or 1, REPLY_UNSEEN to 0 or 1.
# Unseen means the agent reached a terminal state while the user was in another
# window. It persists through staleness — the icon stays bright until the user
# focuses that window.
# Returns 1 if file doesn't exist or has no state.
read_pane_state() {
	local pane_file="$1"
	[[ -f $pane_file ]] || return 1

	local state="" timestamp="" unseen="" key val
	while IFS='=' read -r key val; do
		case "$key" in
		state) state="$val" ;;
		timestamp) timestamp="$val" ;;
		unseen) unseen="$val" ;;
		esac
	done <"$pane_file"

	[[ -n $state ]] || return 1

	REPLY_STALE=0
	if [[ -n $timestamp ]]; then
		local age=$((CLAUDE_NOW - timestamp))
		case "$state" in
		waiting) ((age > CLAUDE_STALE_WAITING)) && REPLY_STALE=1 ;;
		compacting) ((age > CLAUDE_STALE_COMPACTING)) && REPLY_STALE=1 ;;
		processing) ((age > CLAUDE_STALE_PROCESSING)) && REPLY_STALE=1 ;;
		done) ((age > CLAUDE_STALE_DONE)) && REPLY_STALE=1 ;;
		error) ((age > CLAUDE_STALE_ERROR)) && REPLY_STALE=1 ;;
		esac
	fi

	REPLY_UNSEEN=0
	[[ $unseen == "1" ]] && REPLY_UNSEEN=1

	REPLY="$state"
}

# claude_state_icon STATE
# Maps state to its plain icon glyph (no color codes).
# Sets REPLY to the icon string, empty if unknown state.
claude_state_icon() {
	case "$1" in
	processing) REPLY="${CLAUDE_SPINNER_FRAMES[$((CLAUDE_NOW % ${#CLAUDE_SPINNER_FRAMES[@]}))]}" ;;
	waiting) REPLY="$CLAUDE_ICON_WAITING" ;;
	compacting) REPLY="$CLAUDE_ICON_COMPACTING" ;;
	done) REPLY="$CLAUDE_ICON_DONE" ;;
	idle) REPLY="$CLAUDE_ICON_IDLE" ;;
	error) REPLY="$CLAUDE_ICON_ERROR" ;;
	*) REPLY="" ;;
	esac
}

# setup_claude_colors
# Detects light/dark theme and sets C_W, C_K, C_P, C_D, C_I, C_R variables.
# Must be called before claude_colored_icon.
setup_claude_colors() {
	local theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
	local theme="dark"
	if [[ -f $theme_file ]]; then
		theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4) || true
	fi

	if [[ $theme == "light" ]]; then
		C_W="#[fg=#fe640b]" C_K="#[fg=#04a5e5]" C_P="#[fg=#179299]" C_D="#[fg=#40a02b]" C_I="#[fg=#6c6f85]" C_E="#[fg=#d20f39]"
	else
		C_W="#[fg=#fab387]" C_K="#[fg=#89dceb]" C_P="#[fg=#94e2d5]" C_D="#[fg=#a6e3a1]" C_I="#[fg=#6c7086]" C_E="#[fg=#f38ba8]"
	fi
	C_R="#[fg=default]"
}

# claude_colored_icon STATE [STALE] [UNSEEN]
# Returns tmux-colored icon string for a state.
# Sets REPLY to "#[fg=...]ICON#[fg=default] " or empty if unknown.
# If STALE=1 and UNSEEN=0, uses dim color (C_I) instead of the state's color.
# If UNSEEN=1, keeps bright color regardless of staleness.
# Must call setup_claude_colors first.
claude_colored_icon() {
	local icon color
	case "$1" in
	waiting)
		icon="$CLAUDE_ICON_WAITING"
		color=$C_W
		;;
	compacting)
		icon="$CLAUDE_ICON_COMPACTING"
		color=$C_K
		;;
	processing)
		icon="${CLAUDE_SPINNER_FRAMES[$((CLAUDE_NOW % ${#CLAUDE_SPINNER_FRAMES[@]}))]}"
		color=$C_P
		;;
	done)
		icon="$CLAUDE_ICON_DONE"
		color=$C_D
		;;
	idle)
		icon="$CLAUDE_ICON_IDLE"
		color=$C_I
		;;
	error)
		icon="$CLAUDE_ICON_ERROR"
		color=$C_E
		;;
	*) REPLY="" && return ;;
	esac
	# Dim stale icons, but unseen overrides — stay bright until user looks
	[[ ${2:-0} == 1 && ${3:-0} != 1 ]] && color=$C_I
	REPLY="${color}${icon}${C_R} "
}

# claude_priority_state WAITING COMPACTING PROCESSING DONE IDLE ERROR
# Given counts per state, returns the highest-priority non-zero state.
# Sets REPLY to state string, empty if all zero.
claude_priority_state() {
	local w=$1 k=$2 p=$3 d=$4 i=$5 e=${6:-0}
	if ((e > 0)); then
		REPLY="error"
	elif ((w > 0)); then
		REPLY="waiting"
	elif ((k > 0)); then
		REPLY="compacting"
	elif ((p > 0)); then
		REPLY="processing"
	elif ((d > 0)); then
		REPLY="done"
	elif ((i > 0)); then
		REPLY="idle"
	else
		REPLY=""
	fi
}

# tally_claude_state STATE ARRAY_PREFIX
# Increments the associative array entry ${ARRAY_PREFIX}[$STATE] by 1.
# Usage: tally_claude_state "processing" "win_claude"
#   → increments win_claude_processing
# This is a convenience for the common tally pattern.
tally_claude_state() {
	local state="$1" prefix="$2"
	local varname="${prefix}_${state}"
	# Use nameref for clean indirect access
	declare -n _ref="$varname" 2>/dev/null || return 0
	_ref=$((_ref + 1))
}
