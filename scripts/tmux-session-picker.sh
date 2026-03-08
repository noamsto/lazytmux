#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.
#
# choose-tree shows "session_name: FORMAT" — so we don't include name in format.
# To align the dir icon column, we pad with spaces to compensate for name length.

set -euo pipefail

# shellcheck source=/dev/null  # Nix store paths substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_claude@

MAX_ICONS=@MAX_ICONS_PICKER@

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# --- Claude status: read all pane files once, bucket by session ---
declare -A sess_claude_waiting sess_claude_compacting sess_claude_processing sess_claude_done sess_claude_idle

if [[ -d $CLAUDE_PANES_DIR ]]; then
	for pf in "$CLAUDE_PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		state="" pane_session=""
		while IFS='=' read -r key val; do
			case "$key" in
			session) pane_session="$val" ;;
			esac
		done <"$pf"
		[[ -n $pane_session ]] || continue
		read_pane_state "$pf" || continue
		state="$REPLY"
		# Tally into per-session counters
		case "$state" in
		waiting) sess_claude_waiting[$pane_session]=$((${sess_claude_waiting[$pane_session]:-0} + 1)) ;;
		compacting) sess_claude_compacting[$pane_session]=$((${sess_claude_compacting[$pane_session]:-0} + 1)) ;;
		processing) sess_claude_processing[$pane_session]=$((${sess_claude_processing[$pane_session]:-0} + 1)) ;;
		done) sess_claude_done[$pane_session]=$((${sess_claude_done[$pane_session]:-0} + 1)) ;;
		idle) sess_claude_idle[$pane_session]=$((${sess_claude_idle[$pane_session]:-0} + 1)) ;;
		esac
	done
fi

setup_claude_colors

# --- Collect process icons per session with a single tmux list-panes -a ---
declare -A sess_seen_procs
declare -A sess_proc_list

while IFS=$'\t' read -r sess proc; do
	[[ -n $sess && -n $proc ]] || continue
	if [[ -z ${sess_seen_procs["${sess}:${proc}"]+x} ]]; then
		sess_seen_procs["${sess}:${proc}"]=1
		sess_proc_list[$sess]+="${sess_proc_list[$sess]:+ }$proc"
	fi
done < <(tmux list-panes -a -F '#{session_name}	#{pane_current_command}')
unset sess_seen_procs

# Build icon strings per session (dynamic-width column: process icons + claude status)
declare -A sess_icons_map sess_icons_dw

while IFS=$'\t' read -r sess _; do
	[[ -n $sess ]] || continue

	build_proc_icons "${sess_proc_list[$sess]:-}" "$MAX_ICONS"
	icons="$REPLY"
	icons_dw=$REPLY_DW

	# Append claude status icon
	claude_priority_state \
		"${sess_claude_waiting[$sess]:-0}" "${sess_claude_compacting[$sess]:-0}" \
		"${sess_claude_processing[$sess]:-0}" "${sess_claude_done[$sess]:-0}" "${sess_claude_idle[$sess]:-0}"
	if [[ -n $REPLY ]]; then
		claude_colored_icon "$REPLY"
		icons+="$REPLY"
		((icons_dw += 2)) # 1-cell nerd font icon + 1 space
	fi

	sess_icons_map[$sess]="$icons"
	sess_icons_dw[$sess]=$icons_dw
done < <(tmux list-sessions -F '#{session_name}	#{session_id}')
unset sess_proc_list

# Second pass: find max width and pad all to max + 1
max_icon_dw=0
for sess in "${!sess_icons_dw[@]}"; do
	((sess_icons_dw[$sess] > max_icon_dw)) && max_icon_dw=${sess_icons_dw[$sess]}
done
ICON_COL_WIDTH=$((max_icon_dw + 1))

for sess in "${!sess_icons_map[@]}"; do
	pad_to_width "${sess_icons_map[$sess]}" "${sess_icons_dw[$sess]}" "$ICON_COL_WIDTH"
	sess_icons_map[$sess]="$REPLY"
done

# --- Collect session data and build tmux commands in one batch ---
declare -a sessions=() tmux_cmds=()
max_name=0

# Pre-compute colored icons (2 commands)
tmux_cmds+=("set -g @picker_icon_dir '#[fg=${thm_blue}]${icon_dir}#[fg=default]'")
tmux_cmds+=("set -g @picker_icon_branch '#[fg=${thm_green}]${icon_branch}#[fg=default]'")

while IFS=$'\t' read -r sess _; do
	[[ -n $sess ]] || continue
	sessions+=("$sess")
	((${#sess} > max_name)) && max_name=${#sess}
done < <(tmux list-sessions -F '#{session_name}	#{session_id}')

# Build all tmux set commands (use $id to avoid numeric name ambiguity)
while IFS=$'\t' read -r sess sess_id sess_path; do
	[[ -n $sess ]] || continue
	pad_len=$((max_name - ${#sess}))
	padding=$(printf '%*s' "$pad_len" '')
	short_path="${sess_path/#$HOME/\~}"

	printf -v empty_icons '%*s' "$ICON_COL_WIDTH" ''
	tmux_cmds+=("set -t '${sess_id}' @picker_pad '$padding'")
	tmux_cmds+=("set -t '${sess_id}' @picker_path '$short_path'")
	tmux_cmds+=("set -t '${sess_id}' @picker_icons '${sess_icons_map[$sess]:-$empty_icons}'")
done < <(tmux list-sessions -F '#{session_name}	#{session_id}	#{session_path}')

# Execute all tmux set commands in a single invocation
printf '%s\n' "${tmux_cmds[@]}" | tmux source -

# Format: [padding] [icons + claude] [dir icon] path
# tmux's tree prefix shows "session_name:" before this, padding aligns the icon column
tmux choose-tree -Zs -O name \
	-F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=30:@branch},},#{@picker_pad}#{@picker_icons}#{@picker_icon_dir} #{@picker_path}}' \
	'switch-client -t "%1"'
