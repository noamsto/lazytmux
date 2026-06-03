#!/usr/bin/env bash
# Shared issue/PR enrichment utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.
# Functions use the REPLY convention (set REPLY instead of echoing) to avoid
# subshell forks, matching lib-icons.sh / lib-claude.sh.

# shellcheck disable=SC2034  # used by scripts that source this library

ENRICH_CACHE_DIR="/tmp/lazytmux-pr" # PR-state cache dir; consumed by the PR enrichment poller

# branch_to_linear_key BRANCH
# Extracts a Linear issue key (TEAM-123) from a branch name.
# Requires letters before the dash (pure-numeric prefixes are GitHub issues).
# Assumes the repo's type/id-desc branch convention (CLAUDE.md): a slashless
# branch like 'fix-208-x' is intentionally treated as Linear key FIX-208.
# Sets REPLY to the uppercased key, or empty if no match.
branch_to_linear_key() {
	local branch="$1"
	REPLY=""
	# Take the last path segment, then match <letters>-<digits> at its start.
	local slug="${branch##*/}"
	if [[ $slug =~ ^([A-Za-z]+-[0-9]+) ]]; then
		REPLY="${BASH_REMATCH[1]^^}"
	fi
}

# branch_to_gh_issue_number BRANCH
# Extracts a GitHub issue number from a branch name. Matches:
#   ^<digits>-         247-fix-bug
#   /<digits>-         feature/247-foo
#   ^gh-<digits>       gh-247
#   ^issue-<digits>    issue-247
# Sets REPLY to the number, or empty if no match.
branch_to_gh_issue_number() {
	local branch="$1"
	REPLY=""
	local slug="${branch##*/}"
	if [[ $slug =~ ^(gh|issue)-([0-9]+) ]]; then
		REPLY="${BASH_REMATCH[2]}"
	elif [[ $slug =~ ^([0-9]+)- ]]; then
		REPLY="${BASH_REMATCH[1]}"
	fi
}

# sanitize_title RAW
# Strips CR, LF, and ESC control chars, then hard-truncates to 50 chars.
# Sets REPLY to the cleaned title.
sanitize_title() {
	local clean="${1//$'\r'/}"
	clean="${clean//$'\n'/}"
	clean="${clean//$'\033'/}"
	REPLY="${clean:0:50}"
}

# truncate_ellipsis STR MAX
# If STR exceeds MAX display chars, truncate to MAX-1 and append "…".
# Sets REPLY to the (possibly shortened) string.
truncate_ellipsis() {
	local str="$1" max="$2"
	if ((${#str} > max)); then
		REPLY="${str:0:max-1}…"
	else
		REPLY="$str"
	fi
}

# branch_sha1 BRANCH
# Computes a stable cache key (sha1 hex) for a branch name.
# Sets REPLY to the 40-char hex digest.
branch_sha1() {
	local out
	out="$(printf '%s' "$1" | sha1sum)"
	REPLY="${out%% *}"
}

# collapse_check_rollup ROLLUP_JSON
# Maps a gh `statusCheckRollup` array (a CheckRun | StatusContext union) to a
# single state. CheckRun entries carry .status + .conclusion; StatusContext
# entries (Travis/CircleCI-v1/commit-status API) carry .state instead.
# Priority: empty → none;
#   any FAILURE/ERROR/CANCELLED/TIMED_OUT/ACTION_REQUIRED/STALE → failure;
#   else any IN_PROGRESS/QUEUED/PENDING/EXPECTED, or an unfinished CheckRun
#     (empty conclusion) → pending;
#   else success.
# Sets REPLY to one of: failure | pending | success | none.
collapse_check_rollup() {
	local json="$1"
	REPLY="$(jq -r '
		if (. | length) == 0 then "none"
		elif any(.[]; (.conclusion // .state // "") | ascii_upcase
			| . == "FAILURE" or . == "ERROR" or . == "CANCELLED"
			or . == "TIMED_OUT" or . == "ACTION_REQUIRED" or . == "STALE") then "failure"
		elif any(.[];
			((.status // "") | ascii_upcase | (. == "IN_PROGRESS" or . == "QUEUED" or . == "PENDING"))
			or ((.state // "") | ascii_upcase | (. == "EXPECTED" or . == "PENDING"))
			or (.__typename == "CheckRun" and ((.conclusion // "") == ""))) then "pending"
		else "success"
		end
	' <<<"$json" 2>/dev/null)" || REPLY="none"
	if [[ -z $REPLY ]]; then REPLY="none"; fi
}

# provider_priority_list
# Returns the configured issue-tracker providers in priority order.
# The @providers@ placeholder is substituted at Nix build time from
# programs.lazytmux.enrich.providers. Sets REPLY to a space-separated list.
provider_priority_list() {
	REPLY="@providers@"
}
