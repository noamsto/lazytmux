#!/usr/bin/env bash
# Pure logic for the remote nested-session feature. Sourced by the shim, the
# listener, and bats. No side effects; hot-path helpers set REPLY.

export LZTMUX_PROTO_VERSION=1

# remote_env_gate: is this a fresh remote shell that was launched from inside a
# local tmux over ssh? SSH_CONNECTION proves ssh; empty TMUX proves we are not
# already inside the remote server; a %N TMUX_PANE is the forwarded local pane
# id (only present when SendEnv had something to send, i.e. we were in local
# tmux). This is the real nested signal — do not gate on a marker constant.
remote_env_gate() {
	[[ -n ${SSH_CONNECTION:-} ]] || return 1
	[[ -z ${TMUX:-} ]] || return 1
	[[ ${TMUX_PANE:-} =~ ^%[0-9]+$ ]] || return 1
	return 0
}

remote_validate_field() {
	[[ $1 =~ ^[A-Za-z0-9._-]{1,64}$ ]]
}

remote_validate_pane() {
	[[ $1 =~ ^%[0-9]+$ ]]
}

remote_session_name() {
	REPLY="$1-$2"
}

# remote_daemon_alive <pidfile>: return 0 if the file exists and the PID in it
# owns a live process; return 1 otherwise (file missing, empty, or dead PID).
# Used by the launcher to reuse an already-running bridge instead of stacking
# a rival daemon for the same host+session.
remote_daemon_alive() {
	local pidfile="$1" pid
	[[ -f $pidfile ]] || return 1
	pid="$(<"$pidfile")"
	[[ -n $pid ]] || return 1
	kill -0 "$pid" 2>/dev/null
}
