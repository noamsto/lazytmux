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

setup_tmux_fake() {
	FAKE_BIN="$(mktemp -d)"
	# Resolve bash at write time: the nix check sandbox has no /usr/bin/env, and
	# the fake uses bash-isms (arrays, ((...))) so /bin/sh will not do.
	echo "#!$(command -v bash)" >"$FAKE_BIN/tmux"
	cat >>"$FAKE_BIN/tmux" <<'EOF'
# Fake tmux driven by files the test writes:
#   $FAKE_STATE/cmd_<pane>   -> pane_current_command
#   $FAKE_STATE/sess_<pane>  -> session_name
#   $FAKE_STATE/clients_<session> -> newline list of client names
case "$1" in
display-message)
	pane=""; for ((i=1;i<=$#;i++)); do [[ ${!i} == -t ]] && { j=$((i+1)); pane="${!j}"; }; done
	fmt=""
	for a in "$@"; do case "$a" in "#{pane_current_command}") fmt=cmd ;; "#{session_name}") fmt=sess ;; esac; done
	cat "$FAKE_STATE/${fmt}_${pane}" 2>/dev/null
	;;
list-clients)
	# args: list-clients -t <session> -F <fmt>
	sess=""; for ((i=1;i<=$#;i++)); do [[ ${!i} == -t ]] && { j=$((i+1)); sess="${!j}"; }; done
	cat "$FAKE_STATE/clients_${sess}" 2>/dev/null
	;;
esac
EOF
	chmod +x "$FAKE_BIN/tmux"
	FAKE_STATE="$(mktemp -d)"
	export FAKE_STATE
	PATH="$FAKE_BIN:$PATH"
}

@test "pane_is_ssh true only for ssh command" {
	setup_tmux_fake
	# shellcheck source=/dev/null
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	echo ssh >"$FAKE_STATE/cmd_%5"
	run listener_pane_is_ssh "%5"
	[ "$status" -eq 0 ]
	echo bash >"$FAKE_STATE/cmd_%6"
	run listener_pane_is_ssh "%6"
	[ "$status" -ne 0 ]
}

@test "resolve_client returns the single client, refuses on 0 or 2" {
	setup_tmux_fake
	# shellcheck source=/dev/null
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	echo laptop >"$FAKE_STATE/sess_%5"
	printf 'client0\n' >"$FAKE_STATE/clients_laptop"
	run listener_resolve_client "%5"
	[ "$status" -eq 0 ]
	[ "$output" = "client0" ] || {
		listener_resolve_client "%5"
		[ "$REPLY" = "client0" ]
	}
	printf 'client0\nclient1\n' >"$FAKE_STATE/clients_laptop"
	run listener_resolve_client "%5"
	[ "$status" -ne 0 ]
	: >"$FAKE_STATE/clients_laptop"
	run listener_resolve_client "%5"
	[ "$status" -ne 0 ]
}

@test "handle_line: hello matches version, rejects mismatch" {
	# shellcheck source=/dev/null
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	listener_handle_line "hello 1"
	[ "$REPLY" = "ok 1" ]
	listener_handle_line "hello 999"
	[ "$REPLY" = "incompatible" ]
}

@test "handle_line: unknown verb refused" {
	# shellcheck source=/dev/null
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	run listener_handle_line "danger rm -rf /"
	[ "$status" -ne 0 ]
}

@test "handle_line: promote with bad charset refused before any tmux call" {
	# shellcheck source=/dev/null
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	run listener_handle_line 'promote host sess;rm %5'
	[ "$status" -ne 0 ]
	run listener_handle_line 'promote host sess notapane'
	[ "$status" -ne 0 ]
}

@test "shim_decide: never-listed host -> plain, no prompt" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"
	echo "web01=never" >"$LZTMUX_STATE/remote-hosts"
	SSH_CONNECTION=x TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 shim_decide
	[ "$REPLY" = "plain" ]
}

@test "shim_decide: always-listed host -> promote, no prompt" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"
	echo "web01=always" >"$LZTMUX_STATE/remote-hosts"
	SSH_CONNECTION=x TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 shim_decide
	[ "$REPLY" = "promote" ]
}

@test "shim_decide: env gate fails -> plain" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"
	SSH_CONNECTION="" TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 shim_decide
	[ "$REPLY" = "plain" ]
}

@test "shim_decide: undecided host uses seeded answer y -> promote" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"
	SSH_CONNECTION=x TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 LZTMUX_SHIM_ANSWER=y shim_decide
	[ "$REPLY" = "promote" ]
}

@test "shim_target_session: no args -> default, promotable" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	shim_target_session
	[ "$REPLY" = "default" ]
}

@test "shim_target_session: attach -t work -> work" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	shim_target_session attach -t work
	[ "$REPLY" = "work" ]
}

@test "shim_target_session: a -t work -> work" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	shim_target_session a -t work
	[ "$REPLY" = "work" ]
}

@test "shim_target_session: bare attach -> default" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	shim_target_session attach
	[ "$REPLY" = "default" ]
}

@test "shim_target_session: new-session -s foo -> not promotable" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	run shim_target_session new-session -s foo
	[ "$status" -ne 0 ]
}

@test "shim_target_session: ls -> not promotable" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	run shim_target_session ls
	[ "$status" -ne 0 ]
}

@test "shim_target_session: -V -> not promotable" {
	# shellcheck source=/dev/null
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	run shim_target_session -V
	[ "$status" -ne 0 ]
}
