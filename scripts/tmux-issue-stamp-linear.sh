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
# NOTE: three separate `linear` calls assume the CLI resolves the same issue
# (from the worktree's branch) across all three. A single `linear issue json`
# call would remove this seam if/when the CLI supports it. The three are
# independent network round-trips, so run them concurrently — the post-switch
# hook waits on the slowest call, not their sum.
if command -v linear >/dev/null 2>&1 && [[ -d $worktree ]]; then
	tmpd="$(mktemp -d)"
	(cd "$worktree" && linear issue title 2>/dev/null) >"$tmpd/title" &
	(cd "$worktree" && linear issue url 2>/dev/null) >"$tmpd/url" &
	(cd "$worktree" && linear issue id 2>/dev/null) >"$tmpd/id" &
	wait
	title="$(<"$tmpd/title")"
	url="$(<"$tmpd/url")"
	cli_id="$(<"$tmpd/id")"
	rm -rf "$tmpd"
	# Prefer the CLI's canonical id when present.
	[[ -n $cli_id ]] && id="$cli_id"
fi

if [[ -n $title ]]; then
	sanitize_title "$title"
	title="$REPLY"
fi

printf '%s\n%s\n%s\n' "$id" "$title" "$url"
exit 0
