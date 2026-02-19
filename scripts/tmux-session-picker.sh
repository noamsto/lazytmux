#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty

set -euo pipefail

# Read icon variables from tmux
icon_session=$(tmux show -gv @icon_session 2>/dev/null || echo "")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Pre-compute claude status for each session into @claude_status user variable
while IFS= read -r sess; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  tmux set -t "$sess" @claude_status "$status"
done < <(tmux list-sessions -F '#{session_name}')

# Store icons as tmux vars so they're available in format strings
tmux set -g @picker_icon_session "$icon_session"
tmux set -g @picker_icon_dir "$icon_dir"
tmux set -g @picker_icon_branch "$icon_branch"

# Open choose-tree using #{?window_format,...,session_format} to distinguish rows
# Session rows: [session icon] name  [dir icon] path  [claude status]
# Window rows:  index: [app icon] name  [branch icon] branch (when @branch is set)
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_index}: #{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_icon_session} #{session_name}  #{@picker_icon_dir} #{=30:session_path}  #{@claude_status}}' \
  'switch-client -t "%1"'
