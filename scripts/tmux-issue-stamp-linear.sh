#!/usr/bin/env bash
# Linear issue provider for tmux-issue-stamp.
# Usage: tmux-issue-stamp-linear <worktree-path> <branch>
# Output: three lines on stdout — id, title, url (empty lines for unset fields).
# Errors → empty output, exit 0.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

worktree="${1:-}"
branch="${2:-}"
id="" title="" url=""

# Resolve the Linear key from the branch first; bail early if none.
branch_to_linear_key "$branch"
key="$REPLY"
if [[ -z $key ]]; then
	printf '\n\n\n'
	exit 0
fi
id="$key"

# If the linear CLI is available, enrich title + url from within the worktree.
if command -v linear >/dev/null 2>&1 && [[ -d $worktree ]]; then
	title="$(cd "$worktree" && linear issue title 2>/dev/null)" || title=""
	url="$(cd "$worktree" && linear issue url 2>/dev/null)" || url=""
	# Prefer the CLI's canonical id when present.
	cli_id="$(cd "$worktree" && linear issue id 2>/dev/null)" || cli_id=""
	[[ -n $cli_id ]] && id="$cli_id"
fi

if [[ -n $title ]]; then
	sanitize_title "$title"
	title="$REPLY"
fi

printf '%s\n%s\n%s\n' "$id" "$title" "$url"
exit 0
