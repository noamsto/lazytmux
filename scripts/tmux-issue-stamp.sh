#!/usr/bin/env bash
# One-shot issue-identity dispatcher. Runs from the worktrunk post-switch hook
# tail. Iterates configured providers in priority order; first complete
# (id) wins. Writes @issue_* window options on the target. Always exits 0.
#
# Usage: tmux-issue-stamp <target> <worktree-path> <branch> [<explicit-id>]
#   <target> is a tmux target (e.g. "$session:$window" or "$N" session id form).
#   <explicit-id> (optional) skips branch-regex derivation and resolves this id
#   directly — used by `claude-status-update enrich <ID>` right after an issue
#   or PR is created mid-session, before/without a branch encoding it.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@
# shellcheck source=/dev/null
source @lib_log@

target="${1:-}"
worktree="${2:-}"
branch="${3:-}"
explicit_id="${4:-}"

[[ -z $target || -z $branch ]] && exit 0

# Remote-bridge mirror window (#167 @bridge_win opt-out): its identity is the
# remote content it mirrors, not this repo/branch — never stamp it.
[[ $(tmux show-options -t "$target" -wqv @bridge_win 2>/dev/null) == 1 ]] && exit 0

# Serialize every stamp trigger (post-switch, the auto branch-transition
# trigger in tmux-update-icons, and the explicit `enrich` subcommand) through a
# per-window lock: they can all fire around the same moment and would
# otherwise interleave writes from stale/duplicate provider calls. Keyed by
# window_id (stable across the "$session:$idx" / pane-id / explicit-mode
# target shapes callers pass). Non-blocking — a losing fire is redundant, the
# winner's write already covers it, so the 1s poller never blocks.
win_id="$(tmux display-message -t "$target" -p '#{window_id}' 2>/dev/null)" || exit 0
[[ -z $win_id ]] && exit 0
mkdir -p "$ENRICH_STAMP_LOCK_DIR" 2>/dev/null
if ! acquire_lock "$ENRICH_STAMP_LOCK_DIR/${win_id#@}.lock"; then
	# Logged (not just a silent exit): a stuck holder or an unwritable lock dir
	# would otherwise make every future stamp on this window vanish with zero
	# trace — indistinguishable from "nothing changed".
	log_enabled && log_event enrich event stamp_skip_locked win_id "$win_id"
	exit 0
fi

# The pane just cd'd into the worktree but update-icons' 1s tick hasn't
# refreshed @branch/@git_root yet — stamp them now so dir-display doesn't
# briefly render the worktree path relative to the parent repo's root.
if [[ -n $worktree ]]; then
	tmux set-option -t "$target" -w @branch "$branch"
	tmux set-option -t "$target" -w @git_root "$worktree"
fi

run_provider() {
	case "$1" in
	linear) @issue_stamp_linear@ "$worktree" "$branch" "${2:-}" ;;
	github) @issue_stamp_github@ "$worktree" "$branch" "${2:-}" ;;
	*) printf '\n\n\n' ;;
	esac
}

chosen_provider="" id="" title="" url=""
if [[ -n $explicit_id ]]; then
	parse_explicit_issue_id "$explicit_id"
	# A malformed id (REPLY_LOCAL empty) must stay a hard "no id" here — calling
	# run_provider with an empty local id would make the provider script's own
	# `[[ -n $explicit_num ]]` guard read "no explicit id" and silently fall
	# back to branch derivation, breaking explicit-id mode's contract of never
	# deriving from the branch.
	if [[ -n $REPLY_LOCAL ]]; then
		chosen_provider="$REPLY_PROVIDER"
		mapfile -t out < <(run_provider "$chosen_provider" "$REPLY_LOCAL")
		id="${out[0]:-}"
		title="${out[1]:-}"
		url="${out[2]:-}"
	fi
else
	provider_priority_list
	read -r -a providers <<<"$REPLY"
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
fi

if [[ -z $id ]]; then
	# No provider matched: clear any stale stamp left from a previous branch on
	# this window (a stamp is never overwritten by absence, only by presence),
	# then recompute labels so the display falls back to the branch.
	tmux set-option -t "$target" -wu @issue_provider 2>/dev/null
	tmux set-option -t "$target" -wu @issue_id 2>/dev/null
	tmux set-option -t "$target" -wu @issue_title 2>/dev/null
	tmux set-option -t "$target" -wu @issue_url 2>/dev/null
	tmux set-option -t "$target" -wu @issue_branch 2>/dev/null
	@reflow@ "$(tmux display-message -t "$target" -p '#{session_name}')" --force >/dev/null 2>&1 &
	log_enabled && log_event enrich event stamp_clear win_id "$win_id" sess "$(tmux display-message -t "$target" -p '#{session_name}' 2>/dev/null || true)"
	disown -a
	exit 0
fi

tmux set-option -t "$target" -w @issue_provider "$chosen_provider"
tmux set-option -t "$target" -w @issue_id "$id"
tmux set-option -t "$target" -w @issue_title "$title"
tmux set-option -t "$target" -w @issue_url "$url"
tmux set-option -t "$target" -w @issue_branch "$branch"
log_enabled && log_event enrich event stamp provider "$chosen_provider" id "$id" title "$title" url "$url" win_id "$win_id" sess "$(tmux display-message -t "$target" -p '#{session_name}' 2>/dev/null || true)"

# Recompute window labels now that the issue id/title exist (cache bypass).
@reflow@ "$(tmux display-message -t "$target" -p '#{session_name}')" --force >/dev/null 2>&1 &

# Kick an immediate PR fetch for this branch (likely "none" for a fresh branch).
@pr_enrich@ --target "$target" --branch "$branch" --dir "$worktree" --force >/dev/null 2>&1 &
disown -a

exit 0
