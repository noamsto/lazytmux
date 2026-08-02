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
