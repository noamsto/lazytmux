#!/usr/bin/env bash
# Window picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.

set -euo pipefail

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")

# Icon map (Nix-generated)
# shellcheck disable=SC2190  # icon map entries are Nix-generated placeholders
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK="@FALLBACK_ICON@"
MAX_ICONS=@MAX_ICONS@

# Pre-compute colored icons
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"

# Pre-compute claude status and process icons for each window,
# accumulating session-level unique procs from per-window data.
declare -A sess_all_seen   # keyed by "sess:proc" to track unique procs per session
declare -A sess_procs_list # keyed by sess, value is space-separated ordered proc list

while IFS=$'\t' read -r sess_name win_idx sess_path; do
	[[ -n $win_idx ]] || continue
	target="${sess_name}:${win_idx}"
	status=$(claude-status --window "$target" 2>/dev/null || true)
	tmux set -w -t "$target" @claude_win_status "$status"

	# Collect unique process icons for this window
	declare -A win_seen=()
	declare -a win_procs=()
	while IFS= read -r proc; do
		[[ -z $proc ]] && continue
		if [[ -z ${win_seen[$proc]+x} ]]; then
			win_seen[$proc]=1
			win_procs+=("$proc")
		fi
		# Also track at session level
		if [[ -z ${sess_all_seen["${sess_name}:${proc}"]+x} ]]; then
			sess_all_seen["${sess_name}:${proc}"]=1
			sess_procs_list[$sess_name]+="${sess_procs_list[$sess_name]:+ }$proc"
		fi
	done < <(tmux list-panes -t "$target" -F '#{pane_current_command}' 2>/dev/null)

	win_icons=""
	icon_count=0
	for proc in "${win_procs[@]}"; do
		((icon_count >= MAX_ICONS)) && break
		win_icons+="${ICON_MAP[$proc]:-$FALLBACK}"
		((icon_count++))
	done
	unset win_seen win_procs

	tmux set -w -t "$target" @picker_win_icons "$win_icons"

	# Collapse $HOME to ~ (set once per session, harmless to repeat)
	short_path="${sess_path/#$HOME/\~}"
	tmux set -t "$sess_name" @picker_path "$short_path"
done < <(tmux list-windows -a -F '#{session_name}	#{window_index}	#{session_path}')

# Set session-level icons from accumulated per-window data
for sess_name in "${!sess_procs_list[@]}"; do
	s_icons=""
	s_count=0
	# shellcheck disable=SC2086  # intentional word splitting on space-separated proc list
	for proc in ${sess_procs_list[$sess_name]}; do
		((s_count >= MAX_ICONS)) && break
		s_icons+="${ICON_MAP[$proc]:-$FALLBACK}"
		((s_count++))
	done
	tmux set -t "$sess_name" @picker_icons "$s_icons"
done

# Session rows: [process icons] [dir icon] path
# Window rows:  [process icons] name [zoomed] [branch icon] branch [claude status]
tmux choose-tree -Zw -O name \
	-F '#{?window_format,#{@picker_win_icons} #{window_name}#{?window_zoomed_flag, 󰁌,}#{?#{@branch}, #{@picker_icon_branch} #{=30:@branch},} #{@claude_win_status},#{@picker_icons} #{@picker_icon_dir} #{=30:@picker_path}}' \
	'switch-client -t "%1"'
