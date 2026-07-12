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
