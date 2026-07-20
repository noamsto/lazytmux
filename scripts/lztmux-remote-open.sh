#!/usr/bin/env bash
# Create a local <host>-<sess> session and launch the M2 multi-window bridge
# daemon detached: it enumerates every remote window and mirrors each into its
# own local window (live add/close/rename/active-changed). Resolves remote
# tmux path + TMUX_TMPDIR for the ssh control connection.
set -euo pipefail
host="$1"
sess="${2:-}"
win="${3:-}"

if [[ -n $win && ! $win =~ ^[0-9]+$ ]]; then
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

if [[ -z $win ]]; then
	# base-index is non-zero under lazytmux (windows start at 1), so target the
	# session's active window rather than assuming index 0.
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	win="$(ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-windows -t '$sess' -F '#{window_index} #{window_active}' | awk '\$2==1{print \$1; exit}'")"
fi

local_sess="${host}-${sess}"
sock="${TMUX_TMPDIR:-/tmp}/lztmux-daemon-${local_sess}.sock"
# Absolute store path: pane PATH is stale until server restart, and the daemon
# respawns panes into this binary, so resolve it now on the (fresh) caller PATH.
renderer="$(command -v lztmux-remote-bridge-renderer)"

# Create the local session with a single initial window; the daemon reuses it
# for the first remote window and creates the rest.
tmux new-session -d -s "$local_sess" -n "$sess"

# Pass the (remote-derived, untrusted) params through the environment instead
# of interpolating them into a shell/command string tmux/ssh would re-parse,
# so a crafted remote session name can't break out into local shell execution.
export LZTMUX_BRIDGE_HOST="$host"
export LZTMUX_BRIDGE_SESSION="$sess"
export LZTMUX_BRIDGE_WINDOW="$win"
export LZTMUX_BRIDGE_TMUX="$remote_tmux"
export LZTMUX_BRIDGE_TMPDIR="$remote_tmpdir"
export LZTMUX_DAEMON_LOCAL_SESS="$local_sess"
export LZTMUX_DAEMON_SOCK="$sock"
export LZTMUX_DAEMON_RENDERER="$renderer"

# Launch the daemon DETACHED, outside the panes it manages (I4): it is not the
# window's command — it respawns the local panes into renderers. setsid is
# Linux-only (not on macOS base), so fall back to plain backgrounding + disown
# where it's unavailable; either way the daemon is fully detached from this shell.
if command -v setsid >/dev/null 2>&1; then            # portable-ok: guard, verified fallback below
	setsid lztmux-remote-bridge-daemon >/dev/null 2>&1 & # portable-ok: guarded above; else branch is the verified macOS fallback
else
	lztmux-remote-bridge-daemon >/dev/null 2>&1 &
	disown
fi

tmux switch-client -t "=$local_sess"
