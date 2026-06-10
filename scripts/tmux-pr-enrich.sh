#!/usr/bin/env bash
# Background PR enrichment poller. Three entry modes:
#   --tick                  cheap gate; daemonize a full pass if .last-tick stale
#   --target T --branch B [--dir D]
#                           enrich one window's branch (with --force to bypass
#                           TTL); D is a checkout dir giving gh its repo context
#   --mock-* ...            write mock @pr_* options directly (no gh), for tests
# Always exits 0. Writes @pr_number @pr_title @pr_state @pr_check_state @pr_url.
#
# gh resolves the repo from its cwd, and this poller's own cwd is the tmux
# server's (usually not a repo at all) — so every gh call must run inside a
# checkout of the branch's repo: D in single-target mode, the window's
# @worktree/@git_root in the full pass.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@
# shellcheck source=/dev/null
source @lib_log@

REFRESH_SECONDS="@pr_refresh_seconds@"
TTL=60
# A cached "no PR" expires faster: none→PR is the transition a user actively
# waits on right after `gh pr create`; PR-state changes are less urgent.
TTL_NONE=15
# Merged/closed PRs are terminal: re-polling them every TTL wastes two serial
# gh calls per branch. The long TTL (not infinity) still catches a reopened PR
# eventually; --force (prefix+i r) checks immediately.
TTL_TERMINAL=3600

mkdir -p "$ENRICH_CACHE_DIR" 2>/dev/null

# --- arg parse ---
mode="tick"
target="" branch="" dir="" force=0
mock_number="" mock_state="" mock_check="" mock_title="" mock_url="" mock_mergeable=""
while (($#)); do
	case "$1" in
	--tick) mode="tick" ;;
	--tick-run) mode="tickrun" ;;
	--target)
		target="$2"
		shift
		;;
	--branch)
		branch="$2"
		shift
		;;
	--dir)
		dir="$2"
		shift
		;;
	--force) force=1 ;;
	--mock-pr-number)
		mock_number="$2"
		mode="mock"
		shift
		;;
	--mock-pr-state)
		mock_state="$2"
		shift
		;;
	--mock-check-state)
		mock_check="$2"
		shift
		;;
	--mock-pr-title)
		mock_title="$2"
		shift
		;;
	--mock-pr-url)
		mock_url="$2"
		shift
		;;
	--mock-mergeable)
		mock_mergeable="$2"
		shift
		;;
	*) ;;
	esac
	shift
done

# --- helper definitions (defined before any code path calls them) ---

# write_pr_options TARGET NUMBER TITLE STATE CHECK URL MERGEABLE
write_pr_options() {
	# Only the glyph-driving options (@pr_number/@pr_state/@pr_check_state/
	# @pr_mergeable) are captured before writing so we can skip the
	# (cache-bypassing) reflow when unchanged.
	local prev
	prev=$(tmux display-message -t "$1" -p '#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}')
	tmux set-option -t "$1" -w @pr_number "$2"
	tmux set-option -t "$1" -w @pr_title "$3"
	tmux set-option -t "$1" -w @pr_state "$4"
	tmux set-option -t "$1" -w @pr_check_state "$5"
	tmux set-option -t "$1" -w @pr_url "$6"
	tmux set-option -t "$1" -w @pr_mergeable "${7:-}"
	log_enabled && log_event enrich event pr target "$1" number "$2" state "$4" check "$5" mergeable "${7:-}"
	if [[ $prev != "$2|$4|$5|${7:-}" ]]; then
		@reflow@ "$(tmux display-message -t "$1" -p '#{session_name}')" --force >/dev/null 2>&1 &
	fi
}

# branch_cache_key DIR BRANCH — sets REPLY to the cache key (sha1). Scoped by
# the repo's git common dir so identical branch names in different repos don't
# share a cache slot; worktrees of one repo resolve to the same key.
branch_cache_key() {
	local d="$1" repo=""
	[[ -n $d ]] && repo="$(git -C "$d" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
	branch_sha1 "${repo}|$2"
}

# fetch_branch_pr DIR BRANCH  → echoes cache JSON path, refreshing via gh if
# stale. DIR is a checkout of the branch's repo (empty: gh falls back to the
# inherited cwd, useful only when the caller already runs inside the repo).
fetch_branch_pr() {
	local d="$1" b="$2"
	branch_cache_key "$d" "$b"
	local cache="$ENRICH_CACHE_DIR/$REPLY.json"
	local lock="$ENRICH_CACHE_DIR/$REPLY.lock"

	# Serve the cache when the decision says so (fresh + not forced).
	local exists=0 content="" age=0 ttl=$TTL state
	if [[ -f $cache ]]; then
		exists=1
		content="$(<"$cache")"
		age=$(($(date +%s) - $(stat -c %Y "$cache" 2>/dev/null || echo 0)))
		state="$(jq -r '(.[0].state // "") | ascii_upcase' <<<"$content" 2>/dev/null)"
		[[ $state == "MERGED" || $state == "CLOSED" ]] && ttl=$TTL_TERMINAL
	fi
	pr_cache_decision "$force" "$exists" "$content" "$age" "$ttl" "$TTL_NONE"
	if [[ $REPLY == "serve" ]]; then
		printf '%s' "$cache"
		return
	fi

	command -v gh >/dev/null 2>&1 || {
		printf '%s' "$cache"
		return
	}

	# Locked fetch in a subshell: flock on fd 9 releases when the subshell
	# exits, scoping the lock to THIS branch's fetch (not the whole pass).
	# If another process holds the lock, skip the fetch and serve the cache.
	# A failed gh call (offline, auth, rate limit) leaves the cache untouched
	# so the last-known PR state keeps showing instead of wiping to "none".
	(
		flock -n 9 || exit 0
		if [[ -n $d ]]; then cd "$d" 2>/dev/null || exit 0; fi
		local json
		json="$(gh pr list --head "$b" --state open --limit 1 \
			--json number,title,url,state,statusCheckRollup,mergeable 2>/dev/null)" || exit 0
		if [[ $json == "[]" || -z $json ]]; then
			json="$(gh pr list --head "$b" --state all --limit 1 \
				--json number,title,url,state,statusCheckRollup,mergeable 2>/dev/null)" || exit 0
		fi
		printf '%s' "$json" >"$cache.tmp.$$" && mv -f "$cache.tmp.$$" "$cache"
	) 9>"$lock"
	printf '%s' "$cache"
}

# apply_cache_to_target TARGET CACHE_PATH
apply_cache_to_target() {
	local tgt="$1" cache="$2"
	local json="[]"
	[[ -f $cache ]] && json="$(cat "$cache")"
	if [[ $json == "[]" || -z $json ]]; then
		write_pr_options "$tgt" "none" "" "" "" "" ""
		return
	fi
	local number title url state rollup mergeable
	number="$(jq -r '.[0].number // ""' <<<"$json")"
	title="$(jq -r '.[0].title // ""' <<<"$json")"
	url="$(jq -r '.[0].url // ""' <<<"$json")"
	state="$(jq -r '(.[0].state // "") | ascii_downcase' <<<"$json")"
	mergeable="$(jq -r '(.[0].mergeable // "") | ascii_downcase' <<<"$json")"
	rollup="$(jq -c '.[0].statusCheckRollup // []' <<<"$json")"
	collapse_check_rollup "$rollup"
	local check="$REPLY"
	sanitize_title "$title"
	write_pr_options "$tgt" "$number" "$REPLY" "$state" "$check" "$url" "$mergeable"
}

# enrich_repo_group DIR BRANCHES WINDOWS — one repo's slice of the full pass.
# BRANCHES is newline-separated; WINDOWS is newline-separated "target|branch"
# lines. One gh call indexes the repo's open PRs by head branch (headRefName;
# each value is a single-element array matching the per-branch cache format).
# Only heads with no open PR fall back to a per-branch lookup (which catches
# merged/closed via --state all). So the common case — each worktree has an
# open PR — costs a single API round-trip per repo.
enrich_repo_group() {
	local d="$1"
	local branches=() wlines=()
	mapfile -t branches <<<"$2"
	mapfile -t wlines <<<"$3"

	declare -A open_pr
	local all_json head obj
	if command -v gh >/dev/null 2>&1 && [[ -n $d ]] &&
		all_json="$(cd "$d" 2>/dev/null && gh pr list --state open --limit 100 \
			--json number,title,url,state,statusCheckRollup,mergeable,headRefName 2>/dev/null)" &&
		[[ -n $all_json ]]; then
		while IFS=$'\t' read -r head obj; do
			[[ -n $head ]] && open_pr[$head]="$obj"
		done < <(jq -r '.[] | "\(.headRefName)\t\([.])"' <<<"$all_json")
	fi

	local br cache line tgt b2
	for br in "${branches[@]}"; do
		[[ -z $br ]] && continue
		branch_cache_key "$d" "$br"
		cache="$ENRICH_CACHE_DIR/$REPLY.json"
		if [[ -n ${open_pr[$br]+x} ]]; then
			printf '%s' "${open_pr[$br]}" >"$cache.tmp.$$" && mv -f "$cache.tmp.$$" "$cache"
		else
			# No open PR for this head (or gh/batch unavailable): the per-branch
			# lookup resolves merged/closed and serves the cache on failure.
			cache="$(fetch_branch_pr "$d" "$br")"
		fi
		for line in "${wlines[@]}"; do
			IFS="|" read -r tgt b2 <<<"$line"
			[[ $b2 == "$br" ]] && apply_cache_to_target "$tgt" "$cache"
		done
	done
}

# run_full_pass — enrich every window that carries a @branch. Windows are
# grouped by repo (git common dir, derived from @worktree/@git_root); each
# group runs concurrently as one enrich_repo_group. Multi-repo setups pay one
# round-trip per repo, all in flight at once.
run_full_pass() {
	local windows
	mapfile -t windows < <(tmux list-windows -a -F '#{session_id}:#{window_id}|#{@worktree}|#{@git_root}|#{@branch}' | awk -F'|' 'NF>=4')

	# Unique branches (capped — matches the prior head -n 30 bound) and window
	# lines, grouped by repo. "-" collects windows with no usable dir: their
	# fetch keeps the legacy inherited-cwd behavior.
	declare -A seen grp_dir grp_branches grp_windows
	local total=0 line tgt wt gr br d key sk
	for line in "${windows[@]}"; do
		IFS="|" read -r tgt wt gr br <<<"$line"
		[[ -z $br ]] && continue
		d="${wt:-$gr}"
		key=""
		if [[ -n $d ]]; then
			key="$(git -C "$d" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
		fi
		# No dir, or not a repo (stale worktree): legacy inherited-cwd fetch.
		if [[ -z $key ]]; then
			key="-"
			d=""
		fi
		grp_windows[$key]+="$tgt|$br"$'\n'
		sk="$key|$br"
		[[ -n ${seen[$sk]:-} ]] && continue
		((total >= 30)) && continue
		seen[$sk]=1
		grp_dir[$key]="$d"
		grp_branches[$key]+="$br"$'\n'
		((++total))
	done
	((total)) || return

	local k
	for k in "${!grp_branches[@]}"; do
		enrich_repo_group "${grp_dir[$k]}" "${grp_branches[$k]}" "${grp_windows[$k]}" &
	done
	wait
}

# --- mock mode: write the provided values directly, no gh ---
if [[ $mode == "mock" ]]; then
	[[ -z $target ]] && exit 0
	sanitize_title "$mock_title"
	write_pr_options "$target" "$mock_number" "$REPLY" "$mock_state" "$mock_check" "$mock_url" "$mock_mergeable"
	exit 0
fi

# --- tickrun: the detached child re-invoked itself; run exactly one pass ---
if [[ $mode == "tickrun" ]]; then
	run_full_pass
	exit 0
fi

# --- single-target mode (from dispatcher / force refresh) ---
if [[ -n $target && -n $branch ]]; then
	cache="$(fetch_branch_pr "$dir" "$branch")"
	apply_cache_to_target "$target" "$cache"
	exit 0
fi

# --- tick mode: cheap gate, then daemonize a full pass ---
last_tick="$ENRICH_CACHE_DIR/.last-tick"
if ((force == 0)) && [[ -f $last_tick ]]; then
	tick_age=$(($(date +%s) - $(stat -c %Y "$last_tick" 2>/dev/null || echo 0)))
	((tick_age < REFRESH_SECONDS)) && exit 0
fi
# Mark the tick fresh BEFORE daemonizing: best-effort — if the detached pass
# crashes we wait one cycle; --force / the prefix+i r keybind force a retry.
touch "$last_tick"

# Detach so the status refresh returns immediately. The child re-invokes with
# --tick-run, which runs run_full_pass once and exits (no re-daemonize).
setsid "${BASH_SOURCE[0]}" --tick-run >/dev/null 2>&1 &
disown
exit 0
