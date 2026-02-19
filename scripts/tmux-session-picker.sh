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
thm_mauve=$(tmux show -gv @thm_mauve 2>/dev/null || echo "magenta")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Pre-compute colored icons into global vars (choose-tree reads them via #{@var})
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"

# First pass: collect session data and find max widths for column alignment
declare -a sessions=() paths=() statuses=()
max_name=0
max_path=0

while IFS=$'\t' read -r sess sess_path; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  short_path="${sess_path/#$HOME/\~}"
  sessions+=("$sess")
  paths+=("$short_path")
  statuses+=("$status")
  (( ${#sess} > max_name )) && max_name=${#sess}
  (( ${#short_path} > max_path )) && max_path=${#short_path}
done < <(tmux list-sessions -F '#{session_name}	#{session_path}')

# Second pass: set padded name, padded path, and status per session
for i in "${!sessions[@]}"; do
  padded_name=$(printf "%-${max_name}s" "${sessions[$i]}")
  padded_path=$(printf "%-${max_path}s" "${paths[$i]}")
  # Bake colored session name into variable (choose-tree renders #[style] from vars)
  tmux set -t "${sessions[$i]}" @picker_name "#[fg=${thm_mauve},bold]${padded_name}#[fg=default,nobold]"
  tmux set -t "${sessions[$i]}" @picker_path "$padded_path"
  tmux set -t "${sessions[$i]}" @claude_status "${statuses[$i]}"
done

# Session rows: name (padded)  [dir icon] path (padded)  [claude status]
# Window rows:  [app icon] name  [branch icon] branch (when @branch is set)
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_name}  #{@picker_icon_dir} #{@picker_path}  #{@claude_status}}' \
  'switch-client -t "%1"'
