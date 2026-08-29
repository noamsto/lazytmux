#!/usr/bin/env bats
# shellcheck disable=SC2016 # '$(…)' in single quotes is the injection payload under test

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

@test "valid_remote_path: accepts normal absolute paths" {
	run valid_remote_path "/run/user/1000"
	[ "$status" -eq 0 ]

	run valid_remote_path "/tmp/tmux-1000"
	[ "$status" -eq 0 ]
}

@test "valid_remote_path: rejects relative, whitespace, and metacharacters" {
	run valid_remote_path "bin/x"
	[ "$status" -ne 0 ]

	run valid_remote_path "/run/user/1000 x"
	[ "$status" -ne 0 ]

	run valid_remote_path '/run/user/$(id -u)'
	[ "$status" -ne 0 ]
}

@test "shell_quotable: rejects a literal backslash, accepts everything else" {
	run shell_quotable 'a\b'
	[ "$status" -ne 0 ]

	run shell_quotable "/srv/my project (v2)"
	[ "$status" -eq 0 ]

	run shell_quotable "workstation"
	[ "$status" -eq 0 ]

	run shell_quotable "it's a test"
	[ "$status" -eq 0 ]
}
