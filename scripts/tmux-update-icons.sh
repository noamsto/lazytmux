#!/usr/bin/env bash
# Lightweight icon updater called via #() every status-interval.
# Updates @window_icon_display (unpadded, for window names / top-right)
# and @window_icon_padded (fixed-width, for status bar alignment).
# Includes claude status icon in the padded column.
# Outputs nothing (side-effect only).

SESSION=${1:-$(tmux display-message -p '#{session_name}')}

# Icon map (Nix-generated)
# shellcheck disable=SC2190  # icon map entries are Nix-generated placeholders
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK="@FALLBACK_ICON@"
MAX_ICONS=@MAX_ICONS@

# Claude status: read pane files once, bucket by window index
PANES_DIR="/tmp/claude-status/panes"
SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
printf -v _now '%(%s)T' -1

# Map pane IDs to window indices for this session
declare -A pane_to_win
while IFS=$'\t' read -r pane_id win_idx; do
	pane_to_win["${pane_id#%}"]="$win_idx"
done < <(tmux list-panes -s -t "$SESSION" -F '#{pane_id}	#{window_index}')

# Read claude status files, determine per-window state
declare -A win_claude_state
if [[ -d $PANES_DIR ]]; then
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		win_idx="${pane_to_win[$pane_file]:-}"
		[[ -n $win_idx ]] || continue
		state="" timestamp=""
		while IFS='=' read -r key val; do
			case "$key" in
			state) state="$val" ;;
			timestamp) timestamp="$val" ;;
			esac
		done <"$pf"
		[[ -n $state ]] || continue
		# Staleness checks
		if [[ -n $timestamp ]]; then
			age=$((_now - timestamp))
			[[ $state == "waiting" && $age -gt 30 ]] && state="processing"
			[[ $state == "processing" && $age -gt 15 ]] && state="done"
		fi
		# Priority: waiting > compacting > processing > done > idle
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

claude_icon_for() {
	case "$1" in
	waiting) echo "󰔟" ;;
	compacting) echo "󰡍" ;;
	processing) echo "${SPINNER_FRAMES[$((_now % ${#SPINNER_FRAMES[@]}))]}" ;;
	done) echo "󰸞" ;;
	idle) echo "󰒲" ;;
	*) ;;
	esac
}

# First pass: compute process icons + claude per window, measure display widths
declare -a all_idx=()
declare -A win_icons win_icon_dw win_proc_icons

while IFS='|' read -r idx _; do
	target="${SESSION}:${idx}"
	all_idx+=("$idx")

	# Collect unique processes across all panes in this window
	declare -A seen=()
	declare -a unique_procs=()
	while IFS= read -r proc; do
		[[ -z $proc ]] && continue
		if [[ -z ${seen[$proc]+x} ]]; then
			seen[$proc]=1
			unique_procs+=("$proc")
		fi
	done < <(tmux list-panes -t "$target" -F '#{pane_current_command}' 2>/dev/null)

	# Map to icons, cap at MAX_ICONS
	icon=""
	count=0
	for proc in "${unique_procs[@]}"; do
		((count >= MAX_ICONS)) && break
		proc_icon="${ICON_MAP[$proc]:-$FALLBACK}"
		[[ -z $proc_icon ]] && continue
		icon+="$proc_icon "
		((count++)) || true
	done
	unset seen unique_procs

	# Save process-only icons (unpadded, for window names)
	proc_icon_str="${icon% }"

	# Append claude status icon (shares the icon column)
	c_state="${win_claude_state[$idx]:-}"
	c_icon=$(claude_icon_for "$c_state")
	[[ -n $c_icon ]] && icon+="$c_icon "

	# Measure display width
	dw=$(printf '%s' "$icon" | wc -L)

	win_icons[$idx]="$icon"
	win_icon_dw[$idx]=$dw
	win_proc_icons[$idx]="$proc_icon_str"
done < <(tmux list-windows -t "$SESSION" -F '#{window_index}|')

# Set active pane icon for top-right display
active_proc=$(tmux display-message -t "$SESSION" -p '#{pane_current_command}' 2>/dev/null) || true
tmux set -q -t "$SESSION" @active_pane_icon "${ICON_MAP[$active_proc]:-}"

# Second pass: set unpadded + padded icon variables
# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd claude (2 cells)
TARGET_DW=$((MAX_ICONS * 3 + 2))
for idx in "${all_idx[@]}"; do
	target="${SESSION}:${idx}"
	icon="${win_icons[$idx]}"

	# Unpadded (for window names — process icons only, no claude)
	tmux set -qw -t "$target" @window_icon_display "${win_proc_icons[$idx]}"

	# Padded (for status bar — process icons + claude, fixed width)
	pad_needed=$((TARGET_DW - win_icon_dw[$idx]))
	((pad_needed < 0)) && pad_needed=0
	printf -v pad '%*s' "$pad_needed" ''
	tmux set -qw -t "$target" @window_icon_padded "${icon}${pad}"
done
