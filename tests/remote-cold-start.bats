#!/usr/bin/env bats
# Cold-starting a serverless remote (#287). The launcher may only reach for
# tmux-startup.service when list-sessions came back empty, and must re-probe
# afterwards instead of assuming the unit produced the session it wanted —
# unit state is not server state.
#
# ssh and tmux are fakes on PATH; nothing here touches a real host or server.

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	export SSH_LOG="$BATS_TEST_TMPDIR/ssh.log"
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	# Presence of this file is the fake remote's "a tmux server is running".
	export REMOTE_SERVER="$BATS_TEST_TMPDIR/remote-server"
	export REMOTE_SESSION="workstation"
	: >"$SSH_LOG"
	: >"$TMUX_LOG"

	# Skips the launcher's `ssh host id -u` round-trip.
	export LZTMUX_REMOTE_TMPDIR="/run/user/1000"
	# Keeps the daemon socket + log inside the test tmpdir.
	export TMUX_TMPDIR="$BATS_TEST_TMPDIR"

	cat >"$FAKEBIN/ssh" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$SSH_LOG"
		case "$*" in
		*"command -v tmux"*) echo /usr/bin/tmux ;;
		*"systemctl --user start"*)
			if [ -n "${FAKE_UNIT_MISSING:-}" ]; then
				echo "Failed to start tmux-startup.service: Unit not found." >&2
				exit 1
			fi
			touch "$REMOTE_SERVER"
			;;
		*list-sessions*) [ -f "$REMOTE_SERVER" ] && echo "$REMOTE_SESSION" ;;
		*list-windows*) echo 1 ;;
		esac
		exit 0
	EOF

	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$TMUX_LOG"
		exit 0
	EOF

	# The launcher resolves these with `command -v` and hands them to the daemon.
	for stub in lztmux-remote-bridge-renderer tmux-reflow-windows lztmux-remote-bridge-daemon; do
		printf '#!/bin/sh\nexit 0\n' >"$FAKEBIN/$stub"
	done

	chmod +x "$FAKEBIN"/*
	export PATH="$FAKEBIN:$PATH"

	# Same @lib_remote@ substitution Nix does at build time.
	LAUNCHER="$BATS_TEST_TMPDIR/lztmux-remote-open"
	# Both placeholders sit on one line (`[[ -f … ]] && source …`), so /g matters.
	sed "s|@lib_remote@|$PWD/scripts/lib-remote.sh|g" \
		scripts/lztmux-remote-open.sh >"$LAUNCHER"
	export LAUNCHER
}

# Every case runs `bash "$LAUNCHER"` rather than executing it: the nix check
# sandbox has no /usr/bin/env, so the `#!/usr/bin/env bash` shebang cannot
# resolve there. writeShellScriptBin rewrites that shebang to a store path
# anyway, so an explicit interpreter is what the shipped script really gets.

@test "cold start: no server -> starts the unit, re-probes, bridges what it finds" {
	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	grep -q 'systemctl --user start tmux-startup.service' "$SSH_LOG"
	# Two probes: the empty one that triggered the start, and the one after it.
	[ "$(grep -c list-sessions "$SSH_LOG")" -eq 2 ]

	# The session name came from the remote, never from the launcher.
	grep -q 'new-session -d -s tp-g6-workstation' "$TMUX_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "cold start: a host with no startup unit fails by name and bridges nothing" {
	export FAKE_UNIT_MISSING=1

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 1 ]
	[[ $output == *"no tmux-startup.service"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "cold start: a host that already has a session is never cold-started" {
	touch "$REMOTE_SERVER"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	run grep -c systemctl "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "cold start: an explicit session argument skips both the probe and the unit" {
	run bash "$LAUNCHER" tp-g6 scratch
	[ "$status" -eq 0 ]

	run grep -cE 'list-sessions|systemctl' "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =tp-g6-scratch' "$TMUX_LOG"
}
