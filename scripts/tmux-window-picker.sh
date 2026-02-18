#!/usr/bin/env bash
# Window picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty

set -euo pipefail

# Pre-compute claude status for each window into @claude_win_status user variable
while IFS=$'\t' read -r sess_name win_idx; do
  [[ -n $win_idx ]] || continue
  target="${sess_name}:${win_idx}"
  status=$(claude-status --window "$target" 2>/dev/null || true)
  tmux set -w -t "$target" @claude_win_status "$status"
done < <(tmux list-windows -a -F '#{session_name}	#{window_index}')

# Open choose-tree with windows expanded
# window_format rows: "index: icon+dirname [zoomed] [branch] [claude]"
# session_format rows: "session_name (Nw)"
tmux choose-tree -Zw -O name \
  -F '#{?window_format,#{window_index}: #{window_name}#{?window_zoomed_flag, 󰁌,}#{?#{@branch}, #{=30:@branch},} #{@claude_win_status},#{session_name} (#{session_windows}w)}' \
  'switch-client -t "%1"'
