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
# first identityfile whose .pub actually exists, absolute. A host matching
# only `Host *` resolves to five-plus built-in defaults with id_rsa first, and
# id_rsa.pub usually doesn't exist — offering that path would silently never
# match `-f` downstream, so this walks the list instead of trusting the first
# entry. Empty REPLY when none of them exist — the caller must then skip the
# ssh-copy-id offer rather than guess, since this machine carries more than
# one key and a work key must not land on a personal host.
remote_auth_identity() {
	local key value candidate
	REPLY=""
	while read -r key value; do
		[[ $key == identityfile ]] || continue
		candidate="${value/#\~/$HOME}.pub"
		if [[ -f $candidate ]]; then
			REPLY="$candidate"
			return 0
		fi
	done <<<"$1"
	return 0
}

# Absolute, and free of whitespace and shell metacharacters: callers interpolate
# these values *unquoted* into remote command strings.
valid_remote_path() { [[ $1 =~ ^/[A-Za-z0-9._/@+:-]*$ ]]; }

# shell_quote's single-quoting is correct for every character except a literal
# backslash: fish treats `\` specially even inside single quotes, POSIX shells
# don't, and no quoted form satisfies both — so reject a backslash-bearing
# value at the boundary instead of trying to quote it. Screen $sess and
# LZTMUX_REMOTE_NEW_DIR through this before either ever reaches shell_quote.
shell_quotable() { [[ $1 != *\\* ]]; }
