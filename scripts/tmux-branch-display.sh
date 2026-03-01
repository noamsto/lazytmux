#!/usr/bin/env bash
# Display branch name for tmux status bar
# Args: $1 = @branch value (may be empty), $2 = pane_current_path

branch="$1"
pane_path="$2"

# If @branch is set, use it
if [[ -n $branch ]]; then
	echo "$branch"
	exit 0
fi

# Otherwise get from git
if [[ -n $pane_path ]] && [[ -d $pane_path ]]; then
	git -C "$pane_path" branch --show-current 2>/dev/null
fi
