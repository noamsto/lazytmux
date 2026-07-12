#!/usr/bin/env bats

setup() {
	# shellcheck source=/dev/null
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
}

@test "env gate passes when ssh + not in remote tmux + valid pane" {
	SSH_CONNECTION="1.2.3.4 5 6.7.8.9 22" TMUX="" TMUX_PANE="%5" run remote_env_gate
	[ "$status" -eq 0 ]
}

@test "env gate fails when not over ssh" {
	SSH_CONNECTION="" TMUX="" TMUX_PANE="%5" run remote_env_gate
	[ "$status" -ne 0 ]
}

@test "env gate fails when already inside remote tmux" {
	SSH_CONNECTION="x" TMUX="/tmp/tmux-1000/default,7,0" TMUX_PANE="%5" run remote_env_gate
	[ "$status" -ne 0 ]
}

@test "env gate fails when pane absent (ssh'd from outside local tmux)" {
	SSH_CONNECTION="x" TMUX="" TMUX_PANE="" run remote_env_gate
	[ "$status" -ne 0 ]
}

@test "field validation accepts good names, rejects metacharacters" {
	run remote_validate_field "my-host_1.2"
	[ "$status" -eq 0 ]
	run remote_validate_field 'a;rm -rf'
	[ "$status" -ne 0 ]
	run remote_validate_field ""
	[ "$status" -ne 0 ]
}

@test "pane validation accepts %N only" {
	run remote_validate_pane "%12"
	[ "$status" -eq 0 ]
	run remote_validate_pane "12"
	[ "$status" -ne 0 ]
	run remote_validate_pane '%1;x'
	[ "$status" -ne 0 ]
}

@test "session name composes host-session" {
	remote_session_name "web01" "mono"
	[ "$REPLY" = "web01-mono" ]
}
