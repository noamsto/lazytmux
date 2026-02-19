#!/usr/bin/env bash
# Window picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty

set -euo pipefail

# Read icon variables from tmux
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_session=$(tmux show -gv @icon_session 2>/dev/null || echo "")

# Pre-compute claude status for each window into @claude_win_status user variable
while IFS=$'\t' read -r sess_name win_idx; do
  [[ -n $win_idx ]] || continue
  target="${sess_name}:${win_idx}"
  status=$(claude-status --window "$target" 2>/dev/null || true)
  tmux set -w -t "$target" @claude_win_status "$status"
done < <(tmux list-windows -a -F '#{session_name}	#{window_index}')

# Store icons as tmux vars for format strings
tmux set -g @picker_icon_branch "$icon_branch"
tmux set -g @picker_icon_dir "$icon_dir"
tmux set -g @picker_icon_session "$icon_session"

# Open choose-tree with windows expanded
# Colors match status bar: mauve=session, blue=branch, subtext_0=dir/index, overlay_1=claude
# Session rows: [session icon] name  [dir icon] path
# Window rows:  index: [app icon] name [zoomed] [branch icon] branch [claude status]
tmux choose-tree -Zw -O name \
  -F '#{?window_format,#[fg=#{@thm_fg}]#{window_name}#{?window_zoomed_flag, 󰁌,}#{?#{@branch}, #[fg=#{@thm_blue}]#{@picker_icon_branch} #{=30:@branch},} #{@claude_win_status},#[fg=#{@thm_subtext_0}]#{@picker_icon_dir} #{=30:session_path}}' \
  'switch-client -t "%1"'
