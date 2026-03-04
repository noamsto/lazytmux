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

# Timestamp cache (set once per script invocation)
printf -v CLAUDE_NOW '%(%s)T' -1

# read_pane_state PANE_FILE_PATH
# Reads a pane state file with staleness checks.
# Sets REPLY to the (possibly adjusted) state string.
# Returns 1 if file doesn't exist or has no state.
read_pane_state() {
	local pane_file="$1"
	[[ -f $pane_file ]] || return 1

	local state="" timestamp="" key val
	while IFS='=' read -r key val; do
		case "$key" in
		state) state="$val" ;;
		timestamp) timestamp="$val" ;;
		esac
	done <"$pane_file"

	[[ -n $state ]] || return 1

	if [[ -n $timestamp ]]; then
		local age=$((CLAUDE_NOW - timestamp))
		[[ $state == "waiting" && $age -gt 30 ]] && state="processing"
		[[ $state == "compacting" && $age -gt 60 ]] && state="done"
		[[ $state == "processing" && $age -gt 15 ]] && state="done"
	fi

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
		C_W="#[fg=#fe640b]" C_K="#[fg=#04a5e5]" C_P="#[fg=#179299]" C_D="#[fg=#40a02b]" C_I="#[fg=#6c6f85]"
	else
		C_W="#[fg=#fab387]" C_K="#[fg=#89dceb]" C_P="#[fg=#94e2d5]" C_D="#[fg=#a6e3a1]" C_I="#[fg=#6c7086]"
	fi
	C_R="#[fg=default]"
}

# claude_colored_icon STATE
# Returns tmux-colored icon string for a state.
# Sets REPLY to "#[fg=...]ICON#[fg=default] " or empty if unknown.
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
	*) REPLY="" && return ;;
	esac
	REPLY="${color}${icon}${C_R} "
}

# claude_priority_state WAITING COMPACTING PROCESSING DONE IDLE
# Given counts per state, returns the highest-priority non-zero state.
# Sets REPLY to state string, empty if all zero.
claude_priority_state() {
	local w=$1 k=$2 p=$3 d=$4 i=$5
	if ((w > 0)); then
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
