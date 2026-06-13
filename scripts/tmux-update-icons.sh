#!/usr/bin/env bash
# Lightweight icon updater called via #() every status-interval.
# Updates @window_icon_display (unpadded, for window names / top-right)
# and @window_icon_padded (fixed-width, for status bar alignment).
# Includes colored claude status icon in both variables.
# Outputs nothing (side-effect only).

# shellcheck source=/dev/null  # Nix store paths substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_claude@

SESSION=${1:-$(tmux display-message -p '#{session_name}')}
MAX_ICONS=@MAX_ICONS@

setup_claude_colors

# --- Single batched list-panes call: all data in one tmux IPC roundtrip ---
declare -A pane_to_win win_procs win_pane_path win_cur_branch
active_pane_proc=""
active_win_idx=""
while IFS=$'\t' read -r pane_id idx pane_path proc cur_branch pane_active window_active; do
	pane_to_win["${pane_id#%}"]="$idx"
	# First pane_path per window wins (active pane comes first from list-panes)
	if [[ -z ${win_pane_path[$idx]+x} ]]; then
		win_pane_path[$idx]="$pane_path"
		win_cur_branch[$idx]="$cur_branch"
	fi
	[[ $window_active == 1 ]] && active_win_idx="$idx"
	# Track the session's active pane command (active pane in active window)
	[[ $pane_active == 1 && $window_active == 1 ]] && active_pane_proc="$proc"
	# Collect unique processes per window
	[[ -z $proc ]] && continue
	existing="${win_procs[$idx]:-}"
	case " $existing " in
	*" $proc "*) ;;
	*) win_procs[$idx]="${existing:+$existing }$proc" ;;
	esac
done < <(tmux list-panes -s -t "$SESSION" -F '#{pane_id}	#{window_index}	#{pane_current_path}	#{pane_current_command}	#{@branch}	#{pane_active}	#{window_active}')

# --- Claude status: read pane files, bucket by window index ---
declare -A win_claude_state win_claude_stale win_claude_unseen
if [[ -d $CLAUDE_PANES_DIR ]]; then
	for pf in "$CLAUDE_PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		win_idx="${pane_to_win[$pane_file]:-}"
		[[ -n $win_idx ]] || continue
		read_pane_state "$pf" || continue
		state="$REPLY"
		stale=$REPLY_STALE
		unseen=$REPLY_UNSEEN
		# Priority merge: error > waiting > compacting > processing > done > idle
		# Staleness and unseen track the winning state's pane
		current="${win_claude_state[$win_idx]:-}"
		case "$state" in
		error) win_claude_state[$win_idx]="error" win_claude_stale[$win_idx]=$stale win_claude_unseen[$win_idx]=$unseen ;;
		waiting) [[ $current != "error" ]] && win_claude_state[$win_idx]="waiting" win_claude_stale[$win_idx]=$stale win_claude_unseen[$win_idx]=$unseen ;;
		compacting) [[ $current != "error" && $current != "waiting" ]] && win_claude_state[$win_idx]="compacting" win_claude_stale[$win_idx]=$stale win_claude_unseen[$win_idx]=$unseen ;;
		processing) [[ $current != "error" && $current != "waiting" && $current != "compacting" ]] && win_claude_state[$win_idx]="processing" win_claude_stale[$win_idx]=$stale win_claude_unseen[$win_idx]=$unseen ;;
		done) [[ -z $current || $current == "idle" ]] && win_claude_state[$win_idx]="done" win_claude_stale[$win_idx]=$stale win_claude_unseen[$win_idx]=$unseen ;;
		idle) [[ -z $current ]] && win_claude_state[$win_idx]="idle" win_claude_stale[$win_idx]=$stale win_claude_unseen[$win_idx]=$unseen ;;
		esac
	done
fi

# --- Compute process icons + claude per window, measure display widths ---
declare -a all_idx=()
declare -A win_icons win_icon_dw win_display

# Collect all tmux set commands to batch via `tmux source -`
tmux_cmds=""
branch_changed=0

for idx in "${!win_pane_path[@]}"; do
	all_idx+=("$idx")
	pane_path="${win_pane_path[$idx]}"
	target="${SESSION}:${idx}"

	# Branch detection forks git per window. A branch only changes in the window
	# where a checkout/cd happens, so poll only the active window each tick;
	# inactive windows trust their cached @branch (worktrunk stamps it on switch).
	# A window with no @branch yet (manual new-window, restore) is polled once to
	# seed it, then trusted — this caps the steady git fork rate at ~1/tick.
	if [[ $idx == "$active_win_idx" || -z ${win_cur_branch[$idx]:-} ]]; then
		branch=$(git -C "$pane_path" branch --show-current 2>/dev/null) || branch=""
		if [[ $branch != "${win_cur_branch[$idx]:-}" ]]; then
			tmux_cmds+="set -qw -t '$target' @branch '$branch'"$'\n'
			# Re-derive git root when branch changes (different repo or worktree)
			git_root=$(git -C "$pane_path" rev-parse --show-toplevel 2>/dev/null) || git_root=""
			tmux_cmds+="set -qw -t '$target' @git_root '$git_root'"$'\n'
			branch_changed=1
		fi
	fi

	# Build process icons from batched data
	build_proc_icons "${win_procs[$idx]:-}" "$MAX_ICONS"
	proc_icon_str="${REPLY% }"
	icon="$REPLY"
	icon_dw=$REPLY_DW

	# Append colored claude status icon (shares the icon column)
	c_state="${win_claude_state[$idx]:-}"
	display="${proc_icon_str}"
	claude_colored_icon "$c_state" "${win_claude_stale[$idx]:-0}" "${win_claude_unseen[$idx]:-0}"
	if [[ -n $REPLY ]]; then
		icon+="$REPLY"
		((icon_dw += 2)) # 1-cell nerd font icon + 1 space
		# Add to display with space separator if process icons exist
		[[ -n $display ]] && display+=" "
		display+="${REPLY% }" # strip trailing space for display
	fi

	win_icons[$idx]="$icon"
	win_icon_dw[$idx]=$icon_dw
	win_display[$idx]="$display"
done

# Set active pane icon for top-right display (from batched data)
active_icon=""
# Normalize nix makeWrapper's `.foo-wrapped` to `foo` (see lib-icons).
[[ $active_pane_proc == .*-wrapped ]] && active_pane_proc="${active_pane_proc#.}" && active_pane_proc="${active_pane_proc%-wrapped}"
[[ -n $active_pane_proc ]] && active_icon="${ICON_MAP[$active_pane_proc]:-}"
tmux_cmds+="set -q -t '$SESSION' @active_pane_icon '$active_icon'"$'\n'

# --- Second pass: set unpadded + padded icon variables ---
# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd font claude (2 cells)
TARGET_DW=$((MAX_ICONS * 3 + 2))
for idx in "${all_idx[@]}"; do
	target="${SESSION}:${idx}"

	# Unpadded (for window names — process icons + colored claude)
	tmux_cmds+="set -qw -t '$target' @window_icon_display '${win_display[$idx]}'"$'\n'

	# Padded (for status bar — process icons + claude, fixed width)
	pad_to_width "${win_icons[$idx]}" "${win_icon_dw[$idx]}" "$TARGET_DW"
	tmux_cmds+="set -qw -t '$target' @window_icon_padded '$REPLY'"$'\n'
done

# Batch all tmux set commands in a single IPC call
printf '%s' "$tmux_cmds" | tmux source -

# A branch change means window labels (built by reflow from @branch/@issue_*)
# are stale — no tmux hook fires on cd, so kick a forced reflow here. The
# wrapped tmux puts all lazytmux scripts on the server's PATH.
if ((branch_changed)); then
	tmux-reflow-windows "$SESSION" --force >/dev/null 2>&1 &
	disown
fi
