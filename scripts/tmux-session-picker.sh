#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty

set -euo pipefail

# Pre-compute claude status for each session into @claude_status user variable
while IFS= read -r sess; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  tmux set -t "$sess" @claude_status "$status"
done < <(tmux list-sessions -F '#{session_name}')

# Open choose-tree using #{?window_format,...,session_format} to distinguish rows
# window_format is true for window rows, false for session rows
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_index}: #{window_name}#{?#{@branch}, #{=20:@branch},},#{session_name} (#{session_windows}w) #{@claude_status}}' \
  'switch-client -t "%1"'
