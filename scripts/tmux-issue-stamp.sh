#!/usr/bin/env bash
# One-shot issue-identity dispatcher. Runs from the worktrunk post-switch hook
# tail. Iterates configured providers in priority order; first complete
# (id) wins. Writes @issue_* window options on the target. Always exits 0.
#
# Usage: tmux-issue-stamp <target> <worktree-path> <branch>
#   <target> is a tmux target (e.g. "$session:$window" or "$N" session id form).
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

target="${1:-}"
worktree="${2:-}"
branch="${3:-}"

[[ -z $target || -z $branch ]] && exit 0

provider_priority_list
read -r -a providers <<<"$REPLY"

run_provider() {
	case "$1" in
	linear) @issue_stamp_linear@ "$worktree" "$branch" ;;
	github) @issue_stamp_github@ "$worktree" "$branch" ;;
	*) printf '\n\n\n' ;;
	esac
}

chosen_provider="" id="" title="" url=""
for p in "${providers[@]}"; do
	mapfile -t out < <(run_provider "$p")
	if [[ -n ${out[0]:-} ]]; then
		chosen_provider="$p"
		id="${out[0]:-}"
		title="${out[1]:-}"
		url="${out[2]:-}"
		break
	fi
done

if [[ -z $id ]]; then
	# No provider matched: clear any stale stamp left from a previous branch on
	# this window (a stamp is never overwritten by absence, only by presence),
	# then recompute labels so the display falls back to the branch.
	tmux set-option -t "$target" -wu @issue_provider 2>/dev/null
	tmux set-option -t "$target" -wu @issue_id 2>/dev/null
	tmux set-option -t "$target" -wu @issue_title 2>/dev/null
	tmux set-option -t "$target" -wu @issue_url 2>/dev/null
	@reflow@ "$(tmux display-message -t "$target" -p '#{session_name}')" --force >/dev/null 2>&1 &
	disown -a
	exit 0
fi

tmux set-option -t "$target" -w @issue_provider "$chosen_provider"
tmux set-option -t "$target" -w @issue_id "$id"
tmux set-option -t "$target" -w @issue_title "$title"
tmux set-option -t "$target" -w @issue_url "$url"

# Recompute window labels now that the issue id/title exist (cache bypass).
@reflow@ "$(tmux display-message -t "$target" -p '#{session_name}')" --force >/dev/null 2>&1 &

# Kick an immediate PR fetch for this branch (likely "none" for a fresh branch).
@pr_enrich@ --target "$target" --branch "$branch" --force >/dev/null 2>&1 &
disown -a

exit 0
