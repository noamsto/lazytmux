#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.

set -euo pipefail

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
thm_subtext_0=$(tmux show -gv @thm_subtext_0 2>/dev/null || echo "white")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Pre-compute colored icons into global vars (choose-tree reads them via #{@var})
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"

# Pre-compute claude status and shortened path for each session
while IFS=$'\t' read -r sess sess_path; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  tmux set -t "$sess" @claude_status "$status"
  # Collapse $HOME to ~
  short_path="${sess_path/#$HOME/\~}"
  tmux set -t "$sess" @picker_path "$short_path"
done < <(tmux list-sessions -F '#{session_name}	#{session_path}')

# Session rows: [dir icon] path  [claude status]
# Window rows:  [app icon] name  [branch icon] branch (when @branch is set)
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_icon_dir} #{=30:@picker_path}  #{@claude_status}}' \
  'switch-client -t "%1"'
