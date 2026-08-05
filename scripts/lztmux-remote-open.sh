#!/usr/bin/env bash
# Create a local <host>-<sess> session and launch the M2 multi-window bridge
# daemon detached: it enumerates every remote window and mirrors each into its
# own local window (live add/close/rename/active-changed). Resolves remote
# tmux path + TMUX_TMPDIR for the ssh control connection.
set -euo pipefail

# @lib_remote@ is substituted at Nix build time; in bats the lib is pre-sourced.
# shellcheck source=/dev/null
[[ -f "@lib_remote@" ]] && source "@lib_remote@"

# shell_quote single-quotes $1 for a POSIX shell (escaping embedded quotes),
# mirroring shellQuote in the daemon — remote-derived names must not break out.
shell_quote() { printf "'%s'" "${1//\'/\'\\\'\'}"; }

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

# Prints the host's most-recent session name, or nothing when the remote has no
# tmux server: list-sessions fails into `head`, so the remote pipeline still
# exits 0 with empty output.
first_remote_session() {
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-sessions -F '#{session_name}' | head -1"
}

if [[ -z $sess ]]; then
	sess="$(first_remote_session)"
fi

# Nothing to bridge: start the host's OWN startup session rather than inventing
# a name here — tmux-startup.service carries the remote's configured session
# name and directory. Starting it blind is safe: the unit is Type=forking with
# RemainAfterExit, and its script exact-matches `has-session` before creating
# anything. Then re-probe, because unit state is not server state — a live
# server can sit behind an `inactive` unit (#287).
if [[ -z $sess ]]; then
	if ! ssh "$host" -- systemctl --user start tmux-startup.service; then
		echo "lztmux-remote-open: $host has no tmux server, and no tmux-startup.service to start one" >&2
		exit 1
	fi
	sess="$(first_remote_session)"
	if [[ -z $sess ]]; then
		echo "lztmux-remote-open: started tmux-startup.service on $host but no session appeared" >&2
		exit 1
	fi
fi

if [[ -z $win ]]; then
	# base-index is non-zero under lazytmux (windows start at 1), so target the
	# session's active window rather than assuming index 0.
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	win="$(ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-windows -t $(shell_quote "$sess") -F '#{window_index} #{window_active}' | awk '\$2==1{print \$1; exit}'")"
fi

local_sess="${host}-${sess}"
sock_dir="${TMUX_TMPDIR:-${XDG_RUNTIME_DIR:-/tmp}}"
sock_name="${local_sess//[^A-Za-z0-9._-]/_}"
sock="${sock_dir}/lztmux-daemon-${sock_name}.sock"
# Absolute store path: pane PATH is stale until server restart, and the daemon
# respawns panes into this binary, so resolve it now on the (fresh) caller PATH.
renderer="$(command -v lztmux-remote-bridge-renderer)"
reflow="$(command -v tmux-reflow-windows)"

# Dedup: if a daemon for this host+session is already running (pidfile present
# and live), just attach to the existing mirror session — no new SSH connection.
if remote_daemon_alive "${sock}.pid"; then
	tmux switch-client -t "=$local_sess"
	exit 0
fi
# Stale cleanup: a prior daemon was killed (SIGTERM/SIGKILL) without running
# teardown, leaving socket + pidfile behind. Remove both so the new daemon can
# bind cleanly; the session below is also replaced.
rm -f "$sock" "${sock}.pid"

# The <host>-<sess> session is an ephemeral mirror (the remote is the source of
# truth). Discard any pre-existing one — a stale bridge from a prior run, or a
# ghost resurrected by tmux-remux on restore — so it can't collide with
# new-session ("duplicate session"); =-prefix is exact-match (numeric names).
tmux kill-session -t "=$local_sess" 2>/dev/null || true

# Create the local session with a single initial window; the daemon reuses it
# for the first remote window and creates the rest.
tmux new-session -d -s "$local_sess" -n "$sess"

# Read by tmux-statusline to name the machine on line 0. Session-scoped, so it
# survives the daemon replacing every window under it.
tmux set-option -t "$local_sess" @bridge_host "$host"

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
export LZTMUX_DAEMON_REFLOW="$reflow"

# Launch the daemon DETACHED, outside the panes it manages (I4): it is not the
# window's command — it respawns the local panes into renderers. setsid is
# Linux-only (not on macOS base), so fall back to plain backgrounding + disown
# where it's unavailable; either way the daemon is fully detached from this shell.
if command -v setsid >/dev/null 2>&1; then                       # portable-ok: guard, verified fallback below
	setsid lztmux-remote-bridge-daemon >/dev/null 2>"${sock}.log" & # portable-ok: guarded above; else branch is the verified macOS fallback
else
	nohup lztmux-remote-bridge-daemon >/dev/null 2>"${sock}.log" &
	disown
fi

tmux switch-client -t "=$local_sess"
