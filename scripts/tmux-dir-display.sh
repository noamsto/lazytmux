#!/usr/bin/env bash
# Display directory for tmux status bar as relative path from git root
# Shows "./" when at git/worktree root, "./subdir" when in subdirectory
# Args: $1 = @branch value (unused), $2 = pane_current_path

pane_path="$2"

# Get git root — cached in @git_root by tmux-update-icons, fallback to git call
git_root="$3"
if [[ -z $git_root ]] && [[ -n $pane_path ]] && [[ -d $pane_path ]]; then
	git_root=$(git -C "$pane_path" rev-parse --show-toplevel 2>/dev/null)
fi

# Show relative path from git root
if [[ -n $git_root && $pane_path == "$git_root"* ]]; then
	if [[ $pane_path == "$git_root" ]]; then
		echo "./"
	else
		echo "./${pane_path#"$git_root"/}"
	fi
else
	# Not in git repo or stale cache — show with ~ for $HOME
	echo "${pane_path/#"$HOME"/\~}"
fi
