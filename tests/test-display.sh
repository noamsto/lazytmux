#!/usr/bin/env bash
# Mock-mode window-list regression test.
# Spawns a throwaway tmux server using the built wrapper, sets @issue_*/@pr_*
# options for the documented display states, and diffs window names against a
# golden file. Run from repo root after `nix build .#default`.
#
# Why no -n on new-session/new-window: an explicit window name disables
# automatic-rename, so the window would keep its literal name instead of
# rendering automatic-rename-format. We let auto-rename (on by default in the
# baked config) drive the name so the test captures the RENDERED enrichment.
#
# Window indices: the config sets `base-index 1`, so the initial window is
# s:1 and each new-window fills the lowest free index (s:2..s:5).
set -euo pipefail

TMUX_BIN="${TMUX_BIN:-./result/bin/tmux}"
SOCKET="enrichtest-$$"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPECTED="$SCRIPT_DIR/fixtures/window-list.expected"

cleanup() { "$TMUX_BIN" -L "$SOCKET" kill-server 2>/dev/null || true; }
trap cleanup EXIT

t() { "$TMUX_BIN" -L "$SOCKET" "$@"; }

t new-session -d -s s # state: no provider (branch fallback)
t set-option -t s:1 -w @branch "feat/plain-branch"

t new-window -t s # Linear + open PR pending
t set-option -t s:2 -w @issue_provider linear
t set-option -t s:2 -w @issue_id NOA-123
t set-option -t s:2 -w @pr_number 247
t set-option -t s:2 -w @pr_state open
t set-option -t s:2 -w @pr_check_state pending

t new-window -t s # Linear + open PR passing
t set-option -t s:3 -w @issue_provider linear
t set-option -t s:3 -w @issue_id NOA-124
t set-option -t s:3 -w @pr_number 248
t set-option -t s:3 -w @pr_state open
t set-option -t s:3 -w @pr_check_state success

t new-window -t s # GitHub + open PR passing
t set-option -t s:4 -w @issue_provider github
t set-option -t s:4 -w @issue_id "#142"
t set-option -t s:4 -w @pr_number 249
t set-option -t s:4 -w @pr_state open
t set-option -t s:4 -w @pr_check_state success

t new-window -t s # Linear + no PR yet
t set-option -t s:5 -w @issue_provider linear
t set-option -t s:5 -w @issue_id NOA-125
t set-option -t s:5 -w @pr_number none

# Trailing whitespace is stripped per line: an unset @window_icon_display (no
# process icon on a mock pane) renders as a trailing space, which the
# trim-trailing-whitespace pre-commit hook would strip from the golden anyway.
# Normalizing here keeps the golden trim-clean and the comparison stable. This
# only strips the structurally-empty icon slot; a real icon is non-whitespace
# content and would still be captured and compared.
#
# Wait for auto-rename to settle. Renames fire window-by-window over ~300ms
# (status-interval is 1s), and a partially-renamed read can look stable for two
# consecutive polls. Require three consecutive identical reads to skip past that
# transient before locking in; cap ~3s.
prev=""
got=""
stable=0
for _ in $(seq 1 30); do
	got="$(t list-windows -t s -F '#{window_name}' | sed 's/[[:space:]]*$//')"
	if [[ -n $got && $got == "$prev" ]]; then
		stable=$((stable + 1))
		[[ $stable -ge 2 ]] && break
	else
		stable=0
	fi
	prev="$got"
	sleep 0.1
done

if [[ ${UPDATE_GOLDEN:-0} == "1" ]]; then
	printf '%s\n' "$got" >"$EXPECTED"
	echo "golden updated"
	exit 0
fi

diff <(printf '%s\n' "$got") "$EXPECTED"
echo "display test passed"
