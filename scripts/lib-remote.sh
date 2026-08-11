#!/usr/bin/env bash
# Pure helpers for the remote control-mode bridge launcher.
# Hot-path helpers set REPLY; no side effects on source.

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

# remote_auth_identity <ssh -G output>: set REPLY to the public half of the
# first identity the host resolves to, absolute. Empty REPLY when the host
# declares none — the caller must then skip the ssh-copy-id offer rather than
# guess, since this machine carries more than one key and a work key must not
# land on a personal host.
remote_auth_identity() {
	local key value
	REPLY=""
	while read -r key value; do
		[[ $key == identityfile ]] || continue
		REPLY="${value/#\~/$HOME}.pub"
		return 0
	done <<<"$1"
	return 0
}
