#!/usr/bin/env bash
# Window picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.

set -euo pipefail

# shellcheck source=/dev/null  # Nix store paths substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_claude@

MAX_ICONS=@MAX_ICONS@
MAX_ICONS_PICKER=@MAX_ICONS_PICKER@

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")

# --- Claude status: read all pane files once, bucket by window and session ---
# Build pane_id -> window target mapping from a single tmux call
declare -A pane_to_window  # pane_id (no %) -> "session:window_index"
declare -A pane_to_session # pane_id (no %) -> session_name
while IFS=$'\t' read -r pane_id sess win_idx; do
	pane_to_window["${pane_id#%}"]="${sess}:${win_idx}"
	pane_to_session["${pane_id#%}"]="$sess"
done < <(tmux list-panes -a -F '#{pane_id}	#{session_name}	#{window_index}')

# Per-window and per-session claude state tallies
declare -A win_waiting win_compacting win_processing win_done win_idle
declare -A sess_waiting sess_compacting sess_processing sess_done sess_idle

if [[ -d $CLAUDE_PANES_DIR ]]; then
	for pf in "$CLAUDE_PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		target="${pane_to_window[$pane_file]:-}"
		sess="${pane_to_session[$pane_file]:-}"
		[[ -n $target ]] || continue
		read_pane_state "$pf" || continue
		state="$REPLY"
		# Tally per window
		case "$state" in
		waiting) win_waiting[$target]=$((${win_waiting[$target]:-0} + 1)) ;;
		compacting) win_compacting[$target]=$((${win_compacting[$target]:-0} + 1)) ;;
		processing) win_processing[$target]=$((${win_processing[$target]:-0} + 1)) ;;
		done) win_done[$target]=$((${win_done[$target]:-0} + 1)) ;;
		idle) win_idle[$target]=$((${win_idle[$target]:-0} + 1)) ;;
		esac
		# Tally per session
		case "$state" in
		waiting) sess_waiting[$sess]=$((${sess_waiting[$sess]:-0} + 1)) ;;
		compacting) sess_compacting[$sess]=$((${sess_compacting[$sess]:-0} + 1)) ;;
		processing) sess_processing[$sess]=$((${sess_processing[$sess]:-0} + 1)) ;;
		done) sess_done[$sess]=$((${sess_done[$sess]:-0} + 1)) ;;
		idle) sess_idle[$sess]=$((${sess_idle[$sess]:-0} + 1)) ;;
		esac
	done
fi

setup_claude_colors

# --- Collect process icons with single list-panes -a call ---
declare -A win_seen_procs sess_seen_procs
declare -A win_proc_list sess_proc_list

while IFS=$'\t' read -r sess win_idx proc; do
	[[ -n $proc ]] || continue
	target="${sess}:${win_idx}"
	if [[ -z ${win_seen_procs["${target}:${proc}"]+x} ]]; then
		win_seen_procs["${target}:${proc}"]=1
		win_proc_list[$target]+="${win_proc_list[$target]:+ }$proc"
	fi
	if [[ -z ${sess_seen_procs["${sess}:${proc}"]+x} ]]; then
		sess_seen_procs["${sess}:${proc}"]=1
		sess_proc_list[$sess]+="${sess_proc_list[$sess]:+ }$proc"
	fi
done < <(tmux list-panes -a -F '#{session_name}	#{window_index}	#{pane_current_command}')
unset win_seen_procs sess_seen_procs

# --- First pass: build icon strings and measure display widths ---
declare -A win_icons_str win_icons_dw
declare -A sess_icons_str sess_icons_dw

# Per-window icons (capped at MAX_ICONS)
while IFS=$'\t' read -r sess sess_id win_idx sess_path; do
	[[ -n $win_idx ]] || continue
	target="${sess}:${win_idx}"

	build_proc_icons "${win_proc_list[$target]:-}" "$MAX_ICONS"
	icons="$REPLY"
	icons_dw=$REPLY_DW

	claude_priority_state \
		"${win_waiting[$target]:-0}" "${win_compacting[$target]:-0}" \
		"${win_processing[$target]:-0}" "${win_done[$target]:-0}" "${win_idle[$target]:-0}"
	if [[ -n $REPLY ]]; then
		claude_colored_icon "$REPLY"
		icons+="$REPLY"
		((icons_dw += 3)) # 2-cell icon + 1 space
	fi

	win_icons_str[$target]="$icons"
	win_icons_dw[$target]=$icons_dw
done < <(tmux list-windows -a -F '#{session_name}	#{session_id}	#{window_index}	#{session_path}')

# Per-session icons (capped at MAX_ICONS_PICKER for more coverage)
while IFS=$'\t' read -r sess sess_id; do
	[[ -n $sess ]] || continue

	build_proc_icons "${sess_proc_list[$sess]:-}" "$MAX_ICONS_PICKER"
	icons="$REPLY"
	icons_dw=$REPLY_DW

	claude_priority_state \
		"${sess_waiting[$sess]:-0}" "${sess_compacting[$sess]:-0}" \
		"${sess_processing[$sess]:-0}" "${sess_done[$sess]:-0}" "${sess_idle[$sess]:-0}"
	if [[ -n $REPLY ]]; then
		claude_colored_icon "$REPLY"
		icons+="$REPLY"
		((icons_dw += 3)) # 2-cell icon + 1 space
	fi

	sess_icons_str[$sess]="$icons"
	sess_icons_dw[$sess]=$icons_dw
done < <(tmux list-sessions -F '#{session_name}	#{session_id}')

# --- Compute dynamic column widths ---
max_win_dw=0
for target in "${!win_icons_dw[@]}"; do
	((win_icons_dw[$target] > max_win_dw)) && max_win_dw=${win_icons_dw[$target]}
done
WIN_COL=$((max_win_dw > 0 ? max_win_dw + 1 : 0))

max_sess_dw=0
for sess in "${!sess_icons_dw[@]}"; do
	((sess_icons_dw[$sess] > max_sess_dw)) && max_sess_dw=${sess_icons_dw[$sess]}
done
SESS_COL=$((max_sess_dw > 0 ? max_sess_dw + 1 : 0))

# --- Second pass: pad and build tmux commands ---
declare -a tmux_cmds=()

tmux_cmds+=("set -g @picker_icon_branch '#[fg=${thm_green}]${icon_branch}#[fg=default]'")
tmux_cmds+=("set -g @picker_icon_dir '#[fg=${thm_blue}]${icon_dir}#[fg=default]'")

# Per-window
while IFS=$'\t' read -r sess sess_id win_idx sess_path; do
	[[ -n $win_idx ]] || continue
	target="${sess}:${win_idx}"
	id_target="${sess_id}:${win_idx}"

	pad_to_width "${win_icons_str[$target]}" "${win_icons_dw[$target]}" "$WIN_COL"
	tmux_cmds+=("set -w -t '${id_target}' @picker_win_icons '${REPLY}'")

	short_path="${sess_path/#$HOME/\~}"
	tmux_cmds+=("set -t '${sess_id}' @picker_path '$short_path'")
done < <(tmux list-windows -a -F '#{session_name}	#{session_id}	#{window_index}	#{session_path}')

# Per-session
while IFS=$'\t' read -r sess sess_id; do
	[[ -n $sess ]] || continue
	pad_to_width "${sess_icons_str[$sess]:-}" "${sess_icons_dw[$sess]:-0}" "$SESS_COL"
	tmux_cmds+=("set -t '${sess_id}' @picker_icons '${REPLY}'")
done < <(tmux list-sessions -F '#{session_name}	#{session_id}')

# Execute all tmux set commands in a single invocation
printf '%s\n' "${tmux_cmds[@]}" | tmux source -

# Session rows: [icons + claude] [dir icon] path
# Window rows:  [icons + claude] name [zoomed] [branch icon] branch
tmux choose-tree -Zw -O name \
	-F '#{?window_format,#{@picker_win_icons}#{window_name}#{?window_zoomed_flag, 󰁌,}#{?#{@branch}, #{@picker_icon_branch} #{=30:@branch},},#{@picker_icons}#{@picker_icon_dir} #{=30:@picker_path}}' \
	'switch-client -t "%1"'
