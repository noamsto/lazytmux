#!/usr/bin/env bash
# Shared issue/PR enrichment utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.
# Functions use the REPLY convention (set REPLY instead of echoing) to avoid
# subshell forks, matching lib-icons.sh / lib-claude.sh.

# shellcheck disable=SC2034  # used by scripts that source this library

ENRICH_CACHE_DIR="/tmp/lazytmux-pr" # PR-state cache dir; consumed by the PR enrichment poller

# Enrich glyphs (substituted at Nix build time from enrichIconSetRaw). Text-only;
# the status template applies color and appends process/claude icons separately.
ENRICH_ICON_LINEAR="@enrich_icon_linear@"
ENRICH_ICON_GITHUB="@enrich_icon_github@"
ENRICH_ICON_PENDING="@enrich_icon_pending@"
ENRICH_ICON_SUCCESS="@enrich_icon_success@"
ENRICH_ICON_FAILURE="@enrich_icon_failure@"
ENRICH_ICON_MERGED="@enrich_icon_merged@"

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

# build_window_label MODE PROVIDER ISSUE_ID ISSUE_TITLE PR_NUMBER PR_STATE \
#                    PR_CHECK_STATE BRANCH PANE_PATH
# MODE is "short" or "long". Composes the text-only window label (no color, no
# process/claude icons — the status template adds those). The issue id is taken
# from a stamped @issue_id or, if absent, derived from the branch (provider
# priority); the branch remainder after the id is the fallback title. Issue
# windows show "<provider> <id>[ <title>]"; branches with no issue show the
# branch (long=full, short=basename) or the directory basename.
#
# The PR indicator is NOT folded into the name. It is returned as its own
# segment so the status template can color just the PR by check state and keep
# window_name / pickers free of color codes. The name itself is also split into
# a bold-able identity prefix and a remainder.
#
# Sets:
#   REPLY      full plain name (no PR); always REPLY_ID + REPLY_REST
#   REPLY_ID   bold-able identity prefix ("<provider> <id>") or "" (plain branch)
#   REPLY_REST remainder after the id (title / branch / dir basename)
#   REPLY_PR   plain PR segment " <glyph> #<num>" or "" when there is no PR
build_window_label() {
	local mode="$1" provider="$2" issue_id="$3" issue_title="$4"
	local pr_number="$5" pr_state="$6" pr_check="$7" branch="$8" pane_path="$9"
	local provider_icon pr_glyph=""
	REPLY=""
	REPLY_ID=""
	REPLY_REST=""
	REPLY_PR=""

	# Resolve issue identity: a stamped @issue_id wins; otherwise derive it from
	# the branch (so issue windows get the compact id + special format even before
	# or without issue-stamp). The branch remainder after the id serves as the
	# title when no stamped title exists.
	local rid="$issue_id" rprov="$provider" rtitle="$issue_title"
	if [[ -z $rid && -n $branch ]]; then
		local provs p
		provider_priority_list
		provs="$REPLY"
		for p in $provs; do
			if [[ $p == "linear" ]]; then
				branch_to_linear_key "$branch"
				if [[ -n $REPLY ]]; then
					rid="$REPLY"
					rprov="linear"
					break
				fi
			elif [[ $p == "github" ]]; then
				branch_to_gh_issue_number "$branch"
				if [[ -n $REPLY ]]; then
					rid="$REPLY"
					rprov="github"
					break
				fi
			fi
		done
		if [[ -n $rid && -z $rtitle ]]; then
			local slug="${branch##*/}"
			if [[ $slug =~ ^([A-Za-z]+-[0-9]+|gh-[0-9]+|issue-[0-9]+|[0-9]+)-(.+)$ ]]; then
				rtitle="${BASH_REMATCH[2]}"
			fi
		fi
	fi

	# PR indicator (glyph + number) is independent of issue detection — a branch
	# can have a PR with no tracked issue. The number lets you locate a window's
	# PR at a glance. Returned as its own segment (REPLY_PR), never fused into
	# the name, so the template can color it by check state in isolation.
	if [[ -n $pr_number && $pr_number != "none" ]]; then
		case "$pr_check" in
		failure) pr_glyph="$ENRICH_ICON_FAILURE" ;;
		pending) pr_glyph="$ENRICH_ICON_PENDING" ;;
		*)
			if [[ $pr_state == "merged" ]]; then
				pr_glyph="$ENRICH_ICON_MERGED"
			else
				pr_glyph="$ENRICH_ICON_SUCCESS"
			fi
			;;
		esac
		REPLY_PR=" ${pr_glyph} #${pr_number}"
	fi

	if [[ -n $rid ]]; then
		if [[ $rprov == "linear" ]]; then
			provider_icon="$ENRICH_ICON_LINEAR"
		else
			provider_icon="$ENRICH_ICON_GITHUB"
		fi
		REPLY_ID="${provider_icon} ${rid}"
		if [[ $mode == "long" && -n $rtitle ]]; then
			REPLY_REST=" ${rtitle}"
		fi
	elif [[ -n $branch ]]; then
		if [[ $mode == "long" ]]; then
			REPLY_REST="${branch}"
		else
			REPLY_REST="${branch##*/}"
		fi
	else
		REPLY_REST="${pane_path##*/}"
	fi

	REPLY="${REPLY_ID}${REPLY_REST}"
}
