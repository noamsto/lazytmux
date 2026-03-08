#!/usr/bin/env bash
# Shared icon utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.
# Nix build time substitution provides ICON_MAP and FALLBACK_ICON.

# shellcheck disable=SC2034,SC2190  # REPLY_DW used by callers; icon map entries are Nix-generated
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK_ICON="@FALLBACK_ICON@"

# build_proc_icons "proc1 proc2 ..." MAX_COUNT
# Builds space-separated icon string from process names, capped at MAX_COUNT.
# Sets REPLY to the icon string (trailing space per icon).
# Sets REPLY_DW to display width (each icon = 2 cells + 1 space = 3 per icon).
build_proc_icons() {
	local procs="$1" max="$2"
	REPLY=""
	local count=0
	# shellcheck disable=SC2086  # intentional word splitting
	for proc in $procs; do
		((count >= max)) && break
		local icon="${ICON_MAP[$proc]:-$FALLBACK_ICON}"
		[[ -z $icon ]] && continue
		REPLY+="$icon "
		((count++)) || true
	done
	REPLY_DW=$((count * 3))
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
