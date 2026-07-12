#!/usr/bin/env bats
# Real tmux server + direct listener_promote call. Proves the choreography:
# an ssh pane is isolated into HOST-<session> as a single-pane window, and a
# second promote is idempotent (switch, not recreate). The pane-is-ssh and
# single-client checks are orthogonal to the choreography and are stubbed here
# (covered against a fake tmux in remote.bats); this test pins the live
# break-pane / move-window incantation.
#
# Isolation: a PATH shim injects `-L <socket> -f /dev/null` into every tmux
# call so the listener's bare `tmux` never touches the user's real server and
# never auto-loads the lztmux config (which creates sessions on startup).

setup() {
	command -v tmux >/dev/null || skip "tmux not on PATH"
	REAL_TMUX="$(command -v tmux)"
	SOCK="lztmux-it-$$-${BATS_TEST_NUMBER}"
	SHIM_BIN="$(mktemp -d)"
	# switch-client needs a live attached client, which a headless bats run has
	# none of; no-op it so the break-pane/move-window choreography still runs for
	# real. Everything else forwards to real tmux on the isolated socket.
	cat >"$SHIM_BIN/tmux" <<EOF
#!/bin/sh
[ "\$1" = switch-client ] && exit 0
exec "$REAL_TMUX" -L "$SOCK" -f /dev/null "\$@"
EOF
	chmod +x "$SHIM_BIN/tmux"
	PATH="$SHIM_BIN:$PATH"
	unset TMUX
	XDG_RUNTIME_DIR="$(mktemp -d)" # isolate the rate-limit file
	export XDG_RUNTIME_DIR

	tmux kill-server 2>/dev/null || true
	tmux new-session -d -s laptop -x 80 -y 24

	# shellcheck source=/dev/null
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
	# shellcheck source=/dev/null
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"

	# Stub the checks that need a real ssh pane / attached client — all invoked
	# indirectly by listener_promote, so shellcheck can't see the call sites.
	# shellcheck disable=SC2329
	listener_pane_is_ssh() { return 0; }
	# shellcheck disable=SC2329
	listener_resolve_client() {
		REPLY="itclient"
		return 0
	}
	# shellcheck disable=SC2329
	listener_rate_ok() { return 0; }
}

teardown() {
	tmux kill-server 2>/dev/null || true
}

@test "promote isolates the pane into HOST-session as a single-pane window" {
	pane="$(tmux display-message -p -t laptop '#{pane_id}')"
	listener_promote laptophost mono "$pane"
	# "ok promoted" is only set on the return-0 promote path; a refusal sets
	# "refused ...", so this assertion covers success too.
	[ "$REPLY" = "ok promoted" ]
	tmux has-session -t "=laptophost-mono"
	[ "$(tmux list-windows -t '=laptophost-mono' -F '#{window_id}' | grep -c .)" -eq 1 ]
	[ "$(tmux display-message -p -t "$pane" '#{session_name}')" = "laptophost-mono" ]
}

@test "second promote is idempotent (switch existing, not recreate)" {
	pane="$(tmux display-message -p -t laptop '#{pane_id}')"
	listener_promote laptophost mono "$pane"
	listener_promote laptophost mono "$pane"
	[ "$REPLY" = "ok existing" ]
}
