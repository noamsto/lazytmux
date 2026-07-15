#!/usr/bin/env bash
# Tag a window as a worktree window — @worktree/@branch/@git_root, then @issue_*
# via the existing stamp. Idempotent, navigation-free, CLAUDECODE-agnostic. The
# single place that defines HOW a window gets tagged: fired at window creation
# (after-new-window/after-new-session) so any window — whoever created it (a raw
# `tmux new-window`, a dispatcher, a tmux-remux restore) — becomes a first-class
# worktree window, and reused by the worktrunk post-switch hook for navigation.
#
# Usage:
#   tmux-reconcile-window <target>                      # cwd-derived (creation hooks)
#   tmux-reconcile-window <target> <worktree> <branch>  # explicit (post-switch nav)
# <target> is any tmux target; the creation hooks pass #{window_id} (globally
# unique), which sidesteps both numeric-session ambiguity and $-reexpansion.
set -uo pipefail

target="${1:-}"
[[ -z $target ]] && exit 0

# Empty when enrich is disabled (Nix build-time substitution).
issue_stamp="@issue_stamp@"

if [[ -n ${2:-} && -n ${3:-} ]]; then
	# Explicit mode: the caller (worktrunk) knows the worktree/branch
	# authoritatively. Avoids reading pane_current_path, which lags behind the
	# async send-keys `cd` the post-switch take-over/match branches issue.
	top="$2"
	br="$3"
	root="$2"
else
	# cwd mode: derive from the target window's active pane. Read the path PINNED
	# to the target — a bare #{pane_current_path} in a hook resolves against the
	# attached client's active window, not the just-created one.
	cwd=$(tmux display-message -t "$target" -p '#{pane_current_path}' 2>/dev/null) || exit 0
	[[ -z $cwd ]] && exit 0
	git -C "$cwd" rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
	top=$(git -C "$cwd" rev-parse --show-toplevel 2>/dev/null) || exit 0
	[[ -z $top ]] && exit 0
	# Real branch name (matches tmux-update-icons / tmux-branch-display); empty on
	# a detached HEAD, which is fine — tags still set, stamp self-bails below.
	br=$(git -C "$cwd" branch --show-current 2>/dev/null) || br=""
	root="$top"
fi

# Idempotent: skip the writes (and the redundant issue-stamp) when nothing changed.
cur_wt=$(tmux show-options -t "$target" -wqv @worktree 2>/dev/null)
cur_br=$(tmux show-options -t "$target" -wqv @branch 2>/dev/null)
[[ $cur_wt == "$top" && $cur_br == "$br" ]] && exit 0

tmux set-option -t "$target" -w @worktree "$top"
tmux set-option -t "$target" -w @git_root "$root"
[[ -n $br ]] && tmux set-option -t "$target" -w @branch "$br"

# Derive @issue_* from the branch (enrich only; placeholder empty when disabled).
if [[ -n $issue_stamp && -n $br ]]; then
	"$issue_stamp" "$target" "$top" "$br" >/dev/null 2>&1 &
	disown
fi

exit 0
