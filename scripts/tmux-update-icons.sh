#!/usr/bin/env bash
# Lightweight icon updater called via #() every status-interval.
# Updates @window_icon_display (unpadded, for window names / top-right)
# and @window_icon_padded (fixed-width, for status bar alignment).
# Includes claude status icon in the padded column.
# Outputs nothing (side-effect only).

# shellcheck source=/dev/null  # Nix store paths substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_claude@

SESSION=${1:-$(tmux display-message -p '#{session_name}')}
MAX_ICONS=@MAX_ICONS@

# --- Claude status: read pane files, bucket by window index ---
declare -A pane_to_win
while IFS=$'\t' read -r pane_id win_idx; do
	pane_to_win["${pane_id#%}"]="$win_idx"
done < <(tmux list-panes -s -t "$SESSION" -F '#{pane_id}	#{window_index}')

declare -A win_claude_state
if [[ -d $CLAUDE_PANES_DIR ]]; then
	for pf in "$CLAUDE_PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		win_idx="${pane_to_win[$pane_file]:-}"
		[[ -n $win_idx ]] || continue
		read_pane_state "$pf" || continue
		state="$REPLY"
		# Priority merge: waiting > compacting > processing > done > idle
		current="${win_claude_state[$win_idx]:-}"
		case "$state" in
		waiting) win_claude_state[$win_idx]="waiting" ;;
		compacting) [[ $current != "waiting" ]] && win_claude_state[$win_idx]="compacting" ;;
		processing) [[ $current != "waiting" && $current != "compacting" ]] && win_claude_state[$win_idx]="processing" ;;
		done) [[ -z $current || $current == "idle" ]] && win_claude_state[$win_idx]="done" ;;
		idle) [[ -z $current ]] && win_claude_state[$win_idx]="idle" ;;
		esac
	done
fi

# --- First pass: compute process icons + claude per window, measure display widths ---
declare -a all_idx=()
declare -A win_icons win_icon_dw win_proc_icons

while IFS='|' read -r idx _; do
	target="${SESSION}:${idx}"
	all_idx+=("$idx")

	# Collect unique processes across all panes in this window
	declare -A seen=()
	procs=""
	while IFS= read -r proc; do
		[[ -z $proc ]] && continue
		if [[ -z ${seen[$proc]+x} ]]; then
			seen[$proc]=1
			procs+="${procs:+ }$proc"
		fi
	done < <(tmux list-panes -t "$target" -F '#{pane_current_command}' 2>/dev/null)
	unset seen

	# Build process icons
	build_proc_icons "$procs" "$MAX_ICONS"
	proc_icon_str="${REPLY% }"
	icon="$REPLY"
	icon_dw=$REPLY_DW

	# Append claude status icon (shares the icon column)
	c_state="${win_claude_state[$idx]:-}"
	claude_state_icon "$c_state"
	if [[ -n $REPLY ]]; then
		icon+="$REPLY "
		((icon_dw += 2)) # 1-cell nerd font icon + 1 space
	fi

	win_icons[$idx]="$icon"
	win_icon_dw[$idx]=$icon_dw
	win_proc_icons[$idx]="$proc_icon_str"
done < <(tmux list-windows -t "$SESSION" -F '#{window_index}|')

# Set active pane icon for top-right display
active_proc=$(tmux display-message -t "$SESSION" -p '#{pane_current_command}' 2>/dev/null) || true
tmux set -q -t "$SESSION" @active_pane_icon "${ICON_MAP[$active_proc]:-}"

# --- Second pass: set unpadded + padded icon variables ---
# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd font claude (2 cells)
TARGET_DW=$((MAX_ICONS * 3 + 2))
for idx in "${all_idx[@]}"; do
	target="${SESSION}:${idx}"

	# Unpadded (for window names — process icons only, no claude)
	tmux set -qw -t "$target" @window_icon_display "${win_proc_icons[$idx]}"

	# Padded (for status bar — process icons + claude, fixed width)
	pad_to_width "${win_icons[$idx]}" "${win_icon_dw[$idx]}" "$TARGET_DW"
	tmux set -qw -t "$target" @window_icon_padded "$REPLY"
done
