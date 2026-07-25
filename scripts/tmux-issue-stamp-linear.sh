#!/usr/bin/env bash
# Linear issue provider for tmux-issue-stamp.
# Usage: tmux-issue-stamp-linear <worktree-path> <branch> [<explicit-key>]
# <explicit-key> (optional): skip branch-regex derivation and resolve this
# Linear key directly (explicit-id mode — see tmux-issue-stamp). The CLI's
# branch-derived commands (`linear issue title`/`url` with no argument) only
# work from the worktree's own branch, so explicit mode passes the key as an
# argument instead of relying on cwd.
# Output: three lines on stdout — id, title, url (empty lines for unset fields).
# Errors → empty output, exit 0.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

worktree="${1:-}"
branch="${2:-}"
explicit_key="${3:-}"
id="" title="" url=""

# Bounded (timeout) so a network stall can't hold tmux-issue-stamp's per-window
# lock long enough for a concurrent trigger to see it as stale and steal it.
if [[ -n $explicit_key ]]; then
	id="$explicit_key"
	if command -v linear >/dev/null 2>&1; then
		title="$(timeout 15 linear issue title "$id" 2>/dev/null)" || title=""
		url="$(timeout 15 linear issue url "$id" 2>/dev/null)" || url=""
	fi
else
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
		(cd "$worktree" && timeout 15 linear issue title 2>/dev/null) >"$tmpd/title" &
		(cd "$worktree" && timeout 15 linear issue url 2>/dev/null) >"$tmpd/url" &
		(cd "$worktree" && timeout 15 linear issue id 2>/dev/null) >"$tmpd/id" &
		wait
		title="$(<"$tmpd/title")"
		url="$(<"$tmpd/url")"
		cli_id="$(<"$tmpd/id")"
		rm -rf "$tmpd"
		# Prefer the CLI's canonical id when present.
		[[ -n $cli_id ]] && id="$cli_id"
	fi
fi

if [[ -n $title ]]; then
	sanitize_title "$title"
	title="$REPLY"
fi

printf '%s\n%s\n%s\n' "$id" "$title" "$url"
exit 0
