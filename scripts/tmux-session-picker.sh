#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty

set -euo pipefail

# Read icon variables from tmux
icon_session=$(tmux show -gv @icon_session 2>/dev/null || echo "")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Pre-compute claude status and shortened path for each session
while IFS=$'\t' read -r sess sess_path; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  tmux set -t "$sess" @claude_status "$status"
  # Collapse $HOME to ~
  short_path="${sess_path/#$HOME/\~}"
  tmux set -t "$sess" @picker_path "$short_path"
done < <(tmux list-sessions -F '#{session_name}	#{session_path}')

# Store icons as tmux vars so they're available in format strings
tmux set -g @picker_icon_session "$icon_session"
tmux set -g @picker_icon_dir "$icon_dir"
tmux set -g @picker_icon_branch "$icon_branch"

# Open choose-tree using #{?window_format,...,session_format} to distinguish rows
# Note: choose-tree -F does NOT support #[style] sequences — only plain text + #{} vars
# Session rows: [dir icon] path  [claude status]
# Window rows:  [app icon] name  [branch icon] branch (when @branch is set)
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_icon_dir} #{=30:@picker_path}  #{@claude_status}}' \
  'switch-client -t "%1"'
