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
AI_NAMING=@AI_NAMING@ # 1 when programs.lazytmux.aiNaming.enable, else 0

setup_claude_colors

# --- Single batched list-panes call: all data in one tmux IPC roundtrip ---
declare -A pane_to_win win_procs win_pane_path win_cur_branch win_active_pane win_cur_task
active_pane_proc=""
active_win_idx=""
# '|' delimiter, not tab: tab is IFS-whitespace, so an empty middle field (a
# window with no @branch yet) collapses and shifts every later field left,
# corrupting cur_branch/active flags. '@window_task' is free-form so it stays
# last — read drops any stray '|' it contains into that final field.
while IFS='|' read -r pane_id idx pane_path proc cur_branch pane_active window_active cur_task; do
	pane_to_win["${pane_id#%}"]="$idx"
	# First pane per window wins for path/branch/task — panes in a window share a
	# cwd, and @window_task/@branch are window options (same for every pane).
	if [[ -z ${win_pane_path[$idx]+x} ]]; then
		win_pane_path[$idx]="$pane_path"
		win_cur_branch[$idx]="$cur_branch"
		win_cur_task[$idx]="$cur_task"
	fi
	# The task file is keyed by the pane Claude runs in, so resolve the genuinely
	# active pane (list-panes orders by index, not active-first).
	[[ $pane_active == 1 ]] && win_active_pane[$idx]="${pane_id#%}"
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
done < <(tmux list-panes -s -t "$SESSION" -F '#{pane_id}|#{window_index}|#{pane_current_path}|#{pane_current_command}|#{@branch}|#{pane_active}|#{window_active}|#{@window_task}')

# --- Claude status: read pane files, bucket by window index ---
declare -A win_claude_state win_claude_fade win_claude_unseen
# Session-wide tally drives the status-bar session-name tint (@claude_session_fg)
sess_w=0 sess_k=0 sess_p=0 sess_d=0 sess_i=0 sess_e=0 sess_dn=0
sess_min_fade=100 sess_unseen=0
if [[ -d $CLAUDE_PANES_DIR ]]; then
	for pf in "$CLAUDE_PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		win_idx="${pane_to_win[$pane_file]:-}"
		[[ -n $win_idx ]] || continue
		read_pane_state "$pf" || continue
		state="$REPLY"
		fade=$REPLY_FADE
		unseen=$REPLY_UNSEEN
		# Session aggregate: count states, freshest pane wins the fade
		case "$state" in
		error) ((sess_e++)) ;;
		waiting) ((sess_w++)) ;;
		compacting) ((sess_k++)) ;;
		processing) ((sess_p++)) ;;
		done) ((sess_d++)) ;;
		idle) ((sess_i++)) ;;
		denied) ((sess_dn++)) ;;
		esac
		((fade < sess_min_fade)) && sess_min_fade=$fade
		[[ $unseen == 1 ]] && sess_unseen=1
		# Priority merge: error > waiting > compacting > processing > done > idle
		# Fade and unseen track the winning state's pane
		current="${win_claude_state[$win_idx]:-}"
		case "$state" in
		error) win_claude_state[$win_idx]="error" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		waiting) [[ $current != "error" ]] && win_claude_state[$win_idx]="waiting" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		compacting) [[ $current != "error" && $current != "waiting" ]] && win_claude_state[$win_idx]="compacting" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		processing) [[ $current != "error" && $current != "waiting" && $current != "compacting" ]] && win_claude_state[$win_idx]="processing" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		done) [[ -z $current || $current == "idle" ]] && win_claude_state[$win_idx]="done" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		idle) [[ -z $current ]] && win_claude_state[$win_idx]="idle" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		esac
	done
fi

# Session-name color: tint with the aggregate claude state, faded by the
# freshest pane's age. Empty when no claude panes — the format falls back to
# the theme's session color.
claude_priority_state "$sess_w" "$sess_k" "$sess_p" "$sess_d" "$sess_i" "$sess_e" "$sess_dn"
claude_faded_hex "$REPLY" "$sess_min_fade" "$sess_unseen"
session_fg=$REPLY

# --- Compute process icons + claude per window, measure display widths ---
declare -a all_idx=()
declare -A win_icons win_icon_dw win_display

# Collect all tmux set commands to batch via `tmux source -`
tmux_cmds=""
branch_changed=0
labels_changed=0

for idx in "${!win_pane_path[@]}"; do
	all_idx+=("$idx")
	pane_path="${win_pane_path[$idx]}"
	target="${SESSION}:${idx}"

	# Task label tracks the active pane's self-reported "what Claude is doing"
	# phrase (UserPromptSubmit hook). It can change in any window, so poll every
	# window each tick — a single small file read. Set directly (not batched via
	# `source -`): the phrase is free-form and would break the command parser.
	task=""
	[[ -f "$CLAUDE_TASKS_DIR/${win_active_pane[$idx]}" ]] &&
		IFS= read -r task <"$CLAUDE_TASKS_DIR/${win_active_pane[$idx]}"
	if [[ $task != "${win_cur_task[$idx]:-}" ]]; then
		tmux set -qw -t "$target" @window_task "$task"
		labels_changed=1
		# Fallback windows (no feature branch) get a Haiku-summarized title in
		# place of the raw prompt. The namer self-gates on @issue_id, debounces,
		# and caches; kicked only on task change so it isn't spawned every tick.
		cur_branch="${win_cur_branch[$idx]:-}"
		if ((AI_NAMING)) && [[ -z $cur_branch || $cur_branch == "main" || $cur_branch == "master" ]]; then
			tmux-ai-window-name "$target" >/dev/null 2>&1 &
		fi
	fi

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
	claude_colored_icon "$c_state" "${win_claude_fade[$idx]:-0}" "${win_claude_unseen[$idx]:-0}"
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
tmux_cmds+="set -q -t '$SESSION' @claude_session_fg '$session_fg'"$'\n'

# --- Second pass: set unpadded + padded icon variables ---
# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd font claude (2 cells)
TARGET_DW=$((MAX_ICONS * 3 + 2))
for idx in "${all_idx[@]}"; do
	target="${SESSION}:${idx}"

	# Unpadded (for window names — process icons + colored claude)
	tmux_cmds+="set -qw -t '$target' @window_icon_display '${win_display[$idx]}'"$'\n'

	# Re-assert automatic-rename: window names are derived (label + icon via
	# automatic-rename-format) and allow-rename is off, so it must stay on.
	# tmux-state restore creates windows with `new-window -n`, which flips it
	# off and freezes the name on a stale label; this self-heals that each tick.
	tmux_cmds+="set -qw -t '$target' automatic-rename on"$'\n'

	# Padded (for status bar — process icons + claude, fixed width)
	pad_to_width "${win_icons[$idx]}" "${win_icon_dw[$idx]}" "$TARGET_DW"
	tmux_cmds+="set -qw -t '$target' @window_icon_padded '$REPLY'"$'\n'
done

# Batch all tmux set commands in a single IPC call
printf '%s' "$tmux_cmds" | tmux source -

# A branch or task change means window labels (built by reflow from
# @branch/@issue_*/@window_task) are stale — no tmux hook fires on cd or a new
# prompt, so kick a forced reflow here. The wrapped tmux puts all lazytmux
# scripts on the server's PATH.
if ((branch_changed || labels_changed)); then
	tmux-reflow-windows "$SESSION" --force >/dev/null 2>&1 &
	disown
fi
