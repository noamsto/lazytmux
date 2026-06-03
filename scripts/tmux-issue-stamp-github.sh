#!/usr/bin/env bash
# GitHub Issues provider for tmux-issue-stamp.
# Usage: tmux-issue-stamp-github <worktree-path> <branch>
# Output: three lines on stdout — id, title, url (empty for unset fields).
# Errors → empty output, exit 0.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

worktree="${1:-}"
branch="${2:-}"
id="" title="" url=""

# Only a github.com origin qualifies.
if [[ -d $worktree ]]; then
	origin="$(git -C "$worktree" remote get-url origin 2>/dev/null)" || origin=""
else
	origin=""
fi
if [[ $origin != *github.com* ]]; then
	printf '\n\n\n'
	exit 0
fi

branch_to_gh_issue_number "$branch"
num="$REPLY"
if [[ -z $num ]]; then
	printf '\n\n\n'
	exit 0
fi
id="#$num"

# Fetch title + url via gh from within the worktree (repo context).
if command -v gh >/dev/null 2>&1; then
	json="$(cd "$worktree" && gh issue view "$num" --json number,title,url 2>/dev/null)" || json=""
	if [[ -n $json ]]; then
		title="$(jq -r '.title // ""' <<<"$json" 2>/dev/null)" || title=""
		url="$(jq -r '.url // ""' <<<"$json" 2>/dev/null)" || url=""
	fi
fi

if [[ -n $title ]]; then
	sanitize_title "$title"
	title="$REPLY"
fi

printf '%s\n%s\n%s\n' "$id" "$title" "$url"
exit 0
