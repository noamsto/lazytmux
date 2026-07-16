#!/usr/bin/env bash
# Create a local <host>-<sess> session with one window running the bridge for
# the remote window. M1: single window; resolve remote tmux path + TMUX_TMPDIR.
set -euo pipefail
host="$1"
sess="${2:-}"
win="${3:-0}"

if [[ ! $win =~ ^[0-9]+$ ]]; then
	echo "lztmux-remote-open: window index must be numeric, got: $win" >&2
	exit 1
fi

remote_tmpdir="${LZTMUX_REMOTE_TMPDIR:-/run/user/$(ssh "$host" id -u)}"
# single-quoted: $(id -un) expands on the remote side (NixOS profile fallback)
remote_tmux="$(ssh "$host" 'command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux')"

if [[ -z $sess ]]; then
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	sess="$(ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-sessions -F '#{session_name}' | head -1")"
fi

# Pass the (remote-derived, untrusted) params through tmux's environment
# instead of interpolating them into the /bin/sh command string tmux runs,
# so a crafted remote session name can't break out into local shell
# execution. The bridge reads LZTMUX_BRIDGE_* from its inherited env.
local_sess="${host}-${sess}"
tmux new-session -d -s "$local_sess" -n "$sess" \
	-e "LZTMUX_BRIDGE_HOST=$host" \
	-e "LZTMUX_BRIDGE_SESSION=$sess" \
	-e "LZTMUX_BRIDGE_WINDOW=$win" \
	-e "LZTMUX_BRIDGE_TMUX=$remote_tmux" \
	-e "LZTMUX_BRIDGE_TMPDIR=$remote_tmpdir" \
	lztmux-remote-bridge
tmux switch-client -t "=$local_sess"
