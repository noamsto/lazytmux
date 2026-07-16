#!/usr/bin/env bash
# Create a local <host>-<sess> session with one window running the bridge for
# the remote window. M1: single window; resolve remote tmux path + TMUX_TMPDIR.
set -euo pipefail
host="$1"
sess="${2:-}"
win="${3:-0}"

remote_tmpdir="${LZTMUX_REMOTE_TMPDIR:-/run/user/$(ssh "$host" id -u)}"
remote_tmux="$(ssh "$host" "command -v tmux")"

if [[ -z $sess ]]; then
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	sess="$(ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-sessions -F '#{session_name}' | head -1")"
fi

local_sess="${host}-${sess}"
tmux new-session -d -s "$local_sess" -n "$sess" \
	"lztmux-remote-bridge --host '$host' --session '$sess' --window '$win' --tmux '$remote_tmux' --tmpdir '$remote_tmpdir'"
tmux switch-client -t "=$local_sess"
