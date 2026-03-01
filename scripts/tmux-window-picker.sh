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

# Pre-compute colored icons
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"

# Pre-compute claude status for each window and shortened session path
while IFS=$'\t' read -r sess_name win_idx sess_path; do
	[[ -n $win_idx ]] || continue
	target="${sess_name}:${win_idx}"
	status=$(claude-status --window "$target" 2>/dev/null || true)
	tmux set -w -t "$target" @claude_win_status "$status"
	# Collapse $HOME to ~ (set once per session, harmless to repeat)
	short_path="${sess_path/#$HOME/\~}"
	tmux set -t "$sess_name" @picker_path "$short_path"
done < <(tmux list-windows -a -F '#{session_name}	#{window_index}	#{session_path}')

# Session rows: [dir icon] path
# Window rows:  [app icon] name [zoomed] [branch icon] branch [claude status]
tmux choose-tree -Zw -O name \
	-F '#{?window_format,#{window_name}#{?window_zoomed_flag, 󰁌,}#{?#{@branch}, #{@picker_icon_branch} #{=30:@branch},} #{@claude_win_status},#{@picker_icon_dir} #{=30:@picker_path}}' \
	'switch-client -t "%1"'
