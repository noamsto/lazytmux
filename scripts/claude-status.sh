#!/usr/bin/env bash
# Unified Claude status indicator for tmux
# Merges claude-tmux-indicator and claude-session-status into one script.
#
# Usage:
#   claude-status --pane <pane_id>                     [--format icon|icon-color]
#   claude-status --window <session:index>             [--format icon|icon-color]
#   claude-status --session <name>                     [--format icon|icon-color|short|long|gum]
#
# Formats:
#   icon        Plain icon, count if >1 (no tmux color codes)
#   icon-color  Icon with tmux #[fg=...] color codes, count if >1 (default)
#   short       Icon + total count always (e.g. "icon2")
#   long        Text breakdown ("2 processing, 1 waiting")
#   gum         Gum-styled output for sesh picker
#
# Pane/window modes add a leading space (for tmux status bar positioning).
# Session mode has no leading space.

set -euo pipefail

# shellcheck source=/dev/null  # Nix store path substituted at build time
source @lib_claude@

# --- Counting ---

count_processing=0 count_waiting=0 count_compacting=0 count_done=0 count_idle=0 total=0

tally_state() {
	((total++)) || true
	case "$1" in
	processing) ((count_processing++)) || true ;;
	waiting) ((count_waiting++)) || true ;;
	compacting) ((count_compacting++)) || true ;;
	done) ((count_done++)) || true ;;
	idle) ((count_idle++)) || true ;;
	esac
}

count_for_window() {
	while IFS= read -r pane; do
		[[ -n $pane ]] || continue
		read_pane_state "$CLAUDE_PANES_DIR/${pane#%}" || continue
		tally_state "$REPLY"
	done < <(tmux list-panes -t "$1" -F '#{pane_id}' 2>/dev/null)
}

count_for_session() {
	for pf in "$CLAUDE_PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		local pane_session="" key val
		while IFS='=' read -r key val; do
			[[ $key == "session" ]] && {
				pane_session="$val"
				break
			}
		done <"$pf"
		[[ $pane_session == "$1" ]] || continue
		read_pane_state "$pf" || continue
		tally_state "$REPLY"
	done
}

get_priority_state() {
	claude_priority_state "$count_waiting" "$count_compacting" "$count_processing" "$count_done" "$count_idle"
}

# --- Output Formatting ---

format_output() {
	local state="$1" count="$2" format="$3" leading_space="$4"
	[[ -n $state ]] || return 0

	local prefix=""
	[[ $leading_space == "true" ]] && prefix=" "

	case "$format" in
	icon)
		claude_state_icon "$state"
		local count_prefix=""
		[[ $count -gt 1 ]] && count_prefix="${count} "
		echo "${prefix}${count_prefix}${REPLY} "
		;;
	icon-color)
		setup_claude_colors
		claude_colored_icon "$state"
		echo "${prefix}${REPLY}"
		;;
	short)
		claude_state_icon "$state"
		echo "${prefix}${count} ${REPLY}"
		;;
	long)
		local parts=()
		[[ $count_processing -eq 0 ]] || parts+=("$count_processing processing")
		[[ $count_waiting -eq 0 ]] || parts+=("$count_waiting waiting")
		[[ $count_compacting -eq 0 ]] || parts+=("$count_compacting compacting")
		[[ $count_done -eq 0 ]] || parts+=("$count_done done")
		[[ $count_idle -eq 0 ]] || parts+=("$count_idle idle")
		local IFS=", "
		echo "${parts[*]}"
		;;
	gum)
		claude_state_icon "$state"
		local icon="$REPLY"
		local gum_color label
		case "$state" in
		waiting) gum_color=216 label="$count_waiting waiting" ;;
		compacting) gum_color=117 label="$count_compacting compacting" ;;
		processing) gum_color=183 label="$count_processing working" ;;
		done) gum_color=151 label="$count_done done" ;;
		idle) gum_color=245 label="$count_idle idle" ;;
		esac
		gum style --foreground "$gum_color" "$icon $label" 2>/dev/null || echo "$icon $label"
		;;
	esac
}

# --- Main ---

mode="" target="" format=""

while [[ $# -gt 0 ]]; do
	case "$1" in
	--pane)
		mode="pane"
		target="$2"
		shift 2
		;;
	--session)
		mode="session"
		target="$2"
		shift 2
		;;
	--window)
		mode="window"
		target="$2"
		shift 2
		;;
	--format)
		format="$2"
		shift 2
		;;
	--no-color)
		format="icon"
		shift
		;;
	*) shift ;;
	esac
done

[[ -n $mode && -n $target ]] || exit 0
[[ -n $format ]] || format="icon-color"

case "$mode" in
pane)
	read_pane_state "$CLAUDE_PANES_DIR/${target#%}" || exit 0
	tally_state "$REPLY"
	get_priority_state
	format_output "$REPLY" 1 "$format" "true"
	;;
window)
	count_for_window "$target"
	if [[ $total -eq 0 ]]; then
		# Fixed-width blank: match visible width of " icon X " (5 display cols)
		echo "     "
		exit 0
	fi
	get_priority_state
	format_output "$REPLY" "$total" "$format" "true"
	;;
session)
	count_for_session "$target"
	[[ $total -gt 0 ]] || exit 0
	get_priority_state
	format_output "$REPLY" "$total" "$format" "false"
	;;
esac
