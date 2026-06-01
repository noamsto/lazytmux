#!/usr/bin/env bash
# Background PR enrichment poller. Three entry modes:
#   --tick                  cheap gate; daemonize a full pass if .last-tick stale
#   --target T --branch B   enrich one window's branch (with --force to bypass TTL)
#   --mock-* ...            write mock @pr_* options directly (no gh), for tests
# Always exits 0. Writes @pr_number @pr_title @pr_state @pr_check_state @pr_url.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

REFRESH_SECONDS="@pr_refresh_seconds@"
TTL=60

mkdir -p "$ENRICH_CACHE_DIR" 2>/dev/null

# --- arg parse ---
mode="tick"
target="" branch="" force=0
mock_number="" mock_state="" mock_check="" mock_title="" mock_url=""
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
	*) ;;
	esac
	shift
done

# --- helper definitions (defined before any code path calls them) ---

# write_pr_options TARGET NUMBER TITLE STATE CHECK URL
write_pr_options() {
	tmux set-option -t "$1" -w @pr_number "$2"
	tmux set-option -t "$1" -w @pr_title "$3"
	tmux set-option -t "$1" -w @pr_state "$4"
	tmux set-option -t "$1" -w @pr_check_state "$5"
	tmux set-option -t "$1" -w @pr_url "$6"
}

# fetch_branch_pr BRANCH  → echoes cache JSON path, refreshing via gh if stale.
fetch_branch_pr() {
	local b="$1"
	branch_sha1 "$b"
	local cache="$ENRICH_CACHE_DIR/$REPLY.json"
	local lock="$ENRICH_CACHE_DIR/$REPLY.lock"

	# Skip if fresh and not forced.
	if ((force == 0)) && [[ -f $cache ]]; then
		local age=$(($(date +%s) - $(stat -c %Y "$cache" 2>/dev/null || echo 0)))
		((age < TTL)) && {
			printf '%s' "$cache"
			return
		}
	fi

	command -v gh >/dev/null 2>&1 || {
		printf '%s' "$cache"
		return
	}

	# Locked fetch in a subshell: flock on fd 9 releases when the subshell
	# exits, scoping the lock to THIS branch's fetch (not the whole pass).
	# If another process holds the lock, skip the fetch and serve the cache.
	(
		flock -n 9 || exit 0
		local json
		json="$(gh pr list --head "$b" --state open --limit 1 \
			--json number,title,url,state,statusCheckRollup 2>/dev/null)" || json="[]"
		if [[ $json == "[]" || -z $json ]]; then
			json="$(gh pr list --head "$b" --state all --limit 1 \
				--json number,title,url,state,statusCheckRollup 2>/dev/null)" || json="[]"
		fi
		printf '%s' "$json" >"$cache"
	) 9>"$lock"
	printf '%s' "$cache"
}

# apply_cache_to_target TARGET CACHE_PATH
apply_cache_to_target() {
	local tgt="$1" cache="$2"
	local json="[]"
	[[ -f $cache ]] && json="$(cat "$cache")"
	if [[ $json == "[]" || -z $json ]]; then
		write_pr_options "$tgt" "none" "" "" "" ""
		return
	fi
	local number title url state rollup
	number="$(jq -r '.[0].number // ""' <<<"$json")"
	title="$(jq -r '.[0].title // ""' <<<"$json")"
	url="$(jq -r '.[0].url // ""' <<<"$json")"
	state="$(jq -r '(.[0].state // "") | ascii_downcase' <<<"$json")"
	rollup="$(jq -c '.[0].statusCheckRollup // []' <<<"$json")"
	collapse_check_rollup "$rollup"
	local check="$REPLY"
	sanitize_title "$title"
	write_pr_options "$tgt" "$number" "$REPLY" "$state" "$check" "$url"
}

# run_full_pass — enrich every window that carries a @branch, fetching once per
# unique branch and applying the cached result to all windows on that branch.
run_full_pass() {
	declare -A seen
	local tgt _wt br
	while IFS="|" read -r tgt _wt br; do
		[[ -z $br ]] && continue
		[[ -n ${seen[$br]:-} ]] && continue
		seen[$br]=1
		local cache
		cache="$(fetch_branch_pr "$br")"
		# Apply to every window on this branch.
		local t _w b2
		while IFS="|" read -r t _w b2; do
			[[ $b2 == "$br" ]] && apply_cache_to_target "$t" "$cache"
		done < <(tmux list-windows -a -F '#{session_id}:#{window_id}|#{@worktree}|#{@branch}' | awk -F'|' 'NF>=3')
	done < <(tmux list-windows -a -F '#{session_id}:#{window_id}|#{@worktree}|#{@branch}' | awk -F'|' 'NF>=3 && $3!=""' | head -n 30)
}

# --- mock mode: write the provided values directly, no gh ---
if [[ $mode == "mock" ]]; then
	[[ -z $target ]] && exit 0
	sanitize_title "$mock_title"
	write_pr_options "$target" "$mock_number" "$REPLY" "$mock_state" "$mock_check" "$mock_url"
	exit 0
fi

# --- tickrun: the detached child re-invoked itself; run exactly one pass ---
if [[ $mode == "tickrun" ]]; then
	run_full_pass
	exit 0
fi

# --- single-target mode (from dispatcher / force refresh) ---
if [[ -n $target && -n $branch ]]; then
	cache="$(fetch_branch_pr "$branch")"
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
setsid env LAZYTMUX_PR_CHILD=1 "${BASH_SOURCE[0]}" --tick-run >/dev/null 2>&1 &
disown
exit 0
