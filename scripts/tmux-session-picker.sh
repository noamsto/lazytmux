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
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Pre-compute colored icons into global vars (choose-tree reads them via #{@var})
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"

# First pass: collect session data and find max path width for column alignment
declare -a sessions=() paths=() statuses=()
max_path=0

while IFS=$'\t' read -r sess sess_path; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  short_path="${sess_path/#$HOME/\~}"
  sessions+=("$sess")
  paths+=("$short_path")
  statuses+=("$status")
  (( ${#short_path} > max_path )) && max_path=${#short_path}
done < <(tmux list-sessions -F '#{session_name}	#{session_path}')

# Second pass: set padded paths and status
for i in "${!sessions[@]}"; do
  padded=$(printf "%-${max_path}s" "${paths[$i]}")
  tmux set -t "${sessions[$i]}" @picker_path "$padded"
  tmux set -t "${sessions[$i]}" @claude_status "${statuses[$i]}"
done

# Session rows: [dir icon] path (padded)  [claude status]
# Window rows:  [app icon] name  [branch icon] branch (when @branch is set)
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_icon_dir} #{@picker_path}  #{@claude_status}}' \
  'switch-client -t "%1"'
