#!/usr/bin/env bats

setup() {
	# shellcheck source=/dev/null
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
}

@test "remote_daemon_alive: true for a live pid, false for dead/missing pidfile" {
	local pidfile
	pidfile="$(mktemp)"

	# Live process: write our own shell's PID.
	echo $$ >"$pidfile"
	run remote_daemon_alive "$pidfile"
	[ "$status" -eq 0 ]

	# Dead process: use a pid that doesn't exist (max pid + 1 wraps, so use a
	# large fixed value instead of relying on /proc absent pids).
	echo 4194304 >"$pidfile"
	run remote_daemon_alive "$pidfile"
	[ "$status" -ne 0 ]

	# Missing pidfile entirely.
	rm -f "$pidfile"
	run remote_daemon_alive "$pidfile"
	[ "$status" -ne 0 ]
}
