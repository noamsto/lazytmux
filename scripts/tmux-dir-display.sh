#!/usr/bin/env bash
# Display directory for tmux status bar as relative path from git root
# Shows "./" when at git/worktree root, "./subdir" when in subdirectory
# Args: $1 = @branch value (unused), $2 = pane_current_path

pane_path="$2"

# Get git root
git_root=""
if [[ -n $pane_path ]] && [[ -d $pane_path ]]; then
	git_root=$(git -C "$pane_path" rev-parse --show-toplevel 2>/dev/null)
fi

# Show relative path from git root
if [[ -n $git_root ]]; then
	if [[ $pane_path == "$git_root" ]]; then
		echo "./"
	else
		# Get relative path and prefix with ./
		rel_path="${pane_path#"$git_root"/}"
		echo "./$rel_path"
	fi
else
	# Not in git repo, show basename
	basename "$pane_path" 2>/dev/null
fi
