#!/usr/bin/env bash
# Shared Claude status utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.

# shellcheck disable=SC2034  # used by scripts that source this library
CLAUDE_STATUS_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
CLAUDE_PANES_DIR="$CLAUDE_STATUS_DIR/panes"
CLAUDE_ISSUES_DIR="$CLAUDE_STATUS_DIR/issues"
CLAUDE_TASKS_DIR="$CLAUDE_STATUS_DIR/tasks"
CLAUDE_SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
CLAUDE_ICON_WAITING="󰔟"
CLAUDE_ICON_COMPACTING="󰡍"
CLAUDE_ICON_DONE="󰸞"
CLAUDE_ICON_IDLE="󰒲"
CLAUDE_ICON_ERROR="󰅚"  # nerd: nf-md-close_circle_outline
CLAUDE_ICON_DENIED="󰔟" # same clock as waiting, different color

# Timestamp cache (set once per script invocation)
printf -v CLAUDE_NOW '%(%s)T' -1

# Staleness fade — the color stays the state's bright hue until its threshold,
# then eases toward dim grey over CLAUDE_FADE_DURATION seconds (not a hard snap).
CLAUDE_STALE_WAITING=30
CLAUDE_STALE_COMPACTING=60
CLAUDE_STALE_PROCESSING=300
CLAUDE_STALE_DONE=60
CLAUDE_STALE_ERROR=120
CLAUDE_STALE_DENIED=60
CLAUDE_FADE_DURATION=45

# read_pane_state PANE_FILE_PATH
# Reads a pane state file and computes its staleness fade.
# Sets REPLY to the state string, REPLY_FADE to 0..100 (0 = fresh/full color,
# 100 = fully dim), REPLY_UNSEEN to 0 or 1.
# Unseen means the agent reached a terminal state while the user was in another
# window. It pins the fade to 0 — the icon stays bright until the user focuses
# that window.
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

	REPLY_FADE=0
	if [[ -n $timestamp ]]; then
		local age=$((CLAUDE_NOW - timestamp)) start=0
		case "$state" in
		waiting) start=$CLAUDE_STALE_WAITING ;;
		compacting) start=$CLAUDE_STALE_COMPACTING ;;
		processing) start=$CLAUDE_STALE_PROCESSING ;;
		done) start=$CLAUDE_STALE_DONE ;;
		error) start=$CLAUDE_STALE_ERROR ;;
		denied) start=$CLAUDE_STALE_DENIED ;;
		esac
		if ((start > 0 && age > start)); then
			if ((age >= start + CLAUDE_FADE_DURATION)); then
				REPLY_FADE=100
			else
				REPLY_FADE=$(((age - start) * 100 / CLAUDE_FADE_DURATION))
			fi
		fi
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
	denied) REPLY="$CLAUDE_ICON_DENIED" ;;
	*) REPLY="" ;;
	esac
}

# setup_claude_colors
# Detects light/dark theme and sets per-state raw hex (H_*) plus the formatted
# C_* tmux color strings built from them. Must be called before any of the
# color/icon helpers below.
setup_claude_colors() {
	local theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
	local theme="dark"
	if [[ -f $theme_file ]]; then
		theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4) || true
	fi

	if [[ $theme == "light" ]]; then
		H_W="#fe640b" H_K="#04a5e5" H_P="#179299" H_D="#40a02b" H_I="#6c6f85" H_E="#d20f39" H_DN="#df8e1d"
	else
		H_W="#fab387" H_K="#89dceb" H_P="#94e2d5" H_D="#a6e3a1" H_I="#6c7086" H_E="#f38ba8" H_DN="#f9e2af"
	fi
	C_W="#[fg=$H_W]" C_K="#[fg=$H_K]" C_P="#[fg=$H_P]" C_D="#[fg=$H_D]" C_I="#[fg=$H_I]" C_E="#[fg=$H_E]" C_DN="#[fg=$H_DN]"
	C_R="#[fg=default]"
}

# fade_hex FROM_HEX TO_HEX PCT
# Linearly interpolates between two #rrggbb colors; PCT 0 = FROM, 100 = TO.
# Sets REPLY to the resulting #rrggbb. Pure arithmetic — safe in hot paths.
fade_hex() {
	local from=$1 to=$2 pct=$3
	local fr=$((16#${from:1:2})) fg=$((16#${from:3:2})) fb=$((16#${from:5:2}))
	local tr=$((16#${to:1:2})) tg=$((16#${to:3:2})) tb=$((16#${to:5:2}))
	printf -v REPLY '#%02x%02x%02x' \
		$((fr + (tr - fr) * pct / 100)) \
		$((fg + (tg - fg) * pct / 100)) \
		$((fb + (tb - fb) * pct / 100))
}

# claude_faded_hex STATE [FADE] [UNSEEN]
# Sets REPLY to the state's hex, eased toward dim grey (H_I) by FADE (0..100).
# UNSEEN=1 pins to full color. Empty for an unknown state.
# Must call setup_claude_colors first.
claude_faded_hex() {
	case "$1" in
	waiting) REPLY=$H_W ;;
	compacting) REPLY=$H_K ;;
	processing) REPLY=$H_P ;;
	done) REPLY=$H_D ;;
	idle) REPLY=$H_I ;;
	error) REPLY=$H_E ;;
	denied) REPLY=$H_DN ;;
	*)
		REPLY=""
		return
		;;
	esac
	local fade=${2:-0}
	[[ ${3:-0} == 1 ]] && fade=0
	# `if`, not `&&` — a trailing false `((...))` would make this the function's
	# non-zero exit status and trip `set -e` in callers (claude-status).
	if ((fade > 0)); then
		fade_hex "$REPLY" "$H_I" "$fade"
	fi
}

# claude_colored_icon STATE [FADE] [UNSEEN]
# Returns tmux-colored icon string for a state.
# Sets REPLY to "#[fg=...]ICON#[fg=default] " or empty if unknown.
# FADE 0..100 eases the color toward dim grey; UNSEEN=1 keeps it bright.
# Must call setup_claude_colors first.
claude_colored_icon() {
	claude_state_icon "$1"
	local icon=$REPLY
	[[ -n $icon ]] || {
		REPLY=""
		return
	}
	claude_faded_hex "$1" "${2:-0}" "${3:-0}"
	REPLY="#[fg=${REPLY}]${icon}${C_R} "
}

# claude_priority_state WAITING COMPACTING PROCESSING DONE IDLE ERROR DENIED
# Given counts per state, returns the highest-priority non-zero state.
# Sets REPLY to state string, empty if all zero.
claude_priority_state() {
	local w=$1 k=$2 p=$3 d=$4 i=$5 e=${6:-0} dn=${7:-0}
	if ((e > 0)); then
		REPLY="error"
	elif ((w > 0)); then
		REPLY="waiting"
	elif ((dn > 0)); then
		REPLY="denied"
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

# format_issue_list MAX [ID...]
# Joins issue ids with spaces, capped at MAX ids followed by "+N" overflow.
# Sets REPLY to e.g. "ENG-1 ENG-2 ENG-3 +2", empty if no ids.
format_issue_list() {
	local max="$1"
	shift
	if (($# == 0)); then
		REPLY=""
		return
	fi
	if (($# <= max)); then
		REPLY="$*"
		return
	fi
	local overflow=$(($# - max))
	REPLY="${*:1:max} +$overflow"
}
