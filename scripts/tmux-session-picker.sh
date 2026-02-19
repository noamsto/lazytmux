#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.
#
# choose-tree shows "session_name: FORMAT" — so we don't include name in format.
# To align the dir icon column, we pad with spaces to compensate for name length.

set -euo pipefail

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Pre-compute colored icons into global vars (choose-tree reads them via #{@var})
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"

# First pass: collect session data and find max name width for icon alignment
declare -a sessions=() paths=() statuses=()
max_name=0

while IFS=$'\t' read -r sess sess_path; do
  [[ -n $sess ]] || continue
  status=$(claude-status --session "$sess" --format icon-color 2>/dev/null || true)
  short_path="${sess_path/#$HOME/\~}"
  sessions+=("$sess")
  paths+=("$short_path")
  statuses+=("$status")
  (( ${#sess} > max_name )) && max_name=${#sess}
done < <(tmux list-sessions -F '#{session_name}	#{session_path}')

# Second pass: set alignment padding, path, and status per session
for i in "${!sessions[@]}"; do
  name="${sessions[$i]}"
  pad_len=$(( max_name - ${#name} ))
  padding=$(printf '%*s' "$pad_len" '')
  tmux set -t "$name" @picker_pad "$padding"
  tmux set -t "$name" @picker_path "${paths[$i]}"
  tmux set -t "$name" @claude_status "${statuses[$i]}"
done

# Format: [padding] [dir icon] path  [claude status]
# tmux's tree prefix shows "session_name:" before this, padding aligns the icon column
tmux choose-tree -Zs -O name \
  -F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_pad}#{@picker_icon_dir} #{@picker_path} #{@claude_status}}' \
  'switch-client -t "%1"'
