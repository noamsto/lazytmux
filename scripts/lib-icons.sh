#!/usr/bin/env bash
# Shared icon utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.
# Nix build time substitution provides ICON_MAP and FALLBACK_ICON.

# shellcheck disable=SC2034,SC2190  # REPLY_DW used by callers; icon map entries are Nix-generated
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK_ICON="@FALLBACK_ICON@"

# _icon_cell_width CHAR
# Determines display width of a single icon character from its Unicode codepoint.
# Nerd Font PUA (U+E000-F8FF, U+F0000+): 1 cell. Emoji/other: 2 cells.
# Sets _ICW to integer width (avoids clobbering REPLY used by callers).
_icon_cell_width() {
	local -i cp
	printf -v cp '%d' "'${1:0:1}"
	if ((cp == 0)); then
		_ICW=2 # unrecognized → assume wide
	elif ((cp >= 0xE000 && cp <= 0xF8FF)) || ((cp >= 0xF0000)); then
		_ICW=1
	else
		_ICW=2
	fi
}

# build_proc_icons "proc1 proc2 ..." MAX_COUNT
# Builds space-separated icon string from process names, capped at MAX_COUNT.
# Sets REPLY to the icon string (trailing space per icon).
# Sets REPLY_DW to display width (per-icon width varies: nerd font=1, emoji=2, +1 space each).
build_proc_icons() {
	local procs="$1" max="$2"
	REPLY=""
	REPLY_DW=0
	local count=0
	# shellcheck disable=SC2086  # intentional word splitting
	for proc in $procs; do
		((count >= max)) && break
		local icon="${ICON_MAP[$proc]:-$FALLBACK_ICON}"
		[[ -z $icon ]] && continue
		REPLY+="$icon "
		_icon_cell_width "$icon"
		((REPLY_DW += _ICW + 1)) # icon width + 1 space
		((count++)) || true
	done
}

# pad_to_width STRING CURRENT_WIDTH TARGET_WIDTH
# Pads STRING with spaces to reach TARGET_WIDTH.
# Sets REPLY to padded string.
pad_to_width() {
	local str="$1" current="$2" target="$3"
	local pad_needed=$((target - current))
	((pad_needed < 0)) && pad_needed=0
	local pad=""
	printf -v pad '%*s' "$pad_needed" ''
	REPLY="${str}${pad}"
}
