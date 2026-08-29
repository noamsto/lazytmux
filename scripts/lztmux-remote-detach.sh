#!/usr/bin/env bash
# Detach the remote-bridge mirror for a session: SIGTERM its daemon and let the
# daemon's own teardown drop the mirror session. Non-destructive by design — the
# remote session only loses a control-mode client, so everything running on it
# keeps running and prefix+s reopens the bridge. The shell twin of the picker's
# stopBridgeDaemon (picker/remote.go), for the keybind path.
set -euo pipefail

sess="${1:-$(tmux display-message -p '#{session_name}')}"

# Trailing colon, not an =-prefix: display-message takes a target-PANE, where
# "=name" is not a session shorthand and resolves to nothing — silently, rc=0
# with an empty format. The colon form resolves the session unambiguously even
# when its name is numeric (CLAUDE.md, "Session Targeting Gotcha").
sock="$(tmux display-message -p -t "$sess:" '#{@bridge_sock}')"
if [[ -z $sock ]]; then
	tmux display-message "lztmux-remote-detach: $sess is not a bridged session"
	exit 0
fi

pid=""
if [[ -r "${sock}.pid" ]]; then
	pid="$(<"${sock}.pid")"
fi
if [[ $pid =~ ^[0-9]+$ ]]; then
	kill -TERM -- "$pid" 2>/dev/null || true
	# The daemon's teardown kills the mirror session itself; wait for it so the
	# fallback below only fires for a daemon that never will.
	for _ in {1..20}; do
		tmux has-session -t "=$sess" 2>/dev/null || exit 0
		sleep 0.1
	done
fi

# No live daemon (already exited, or wedged past the wait): its teardown will
# never run, so the mirror would linger with a dead renderer in every pane.
# Killing it here costs nothing — the remote is untouched on either path.
tmux kill-session -t "=$sess" 2>/dev/null || true
