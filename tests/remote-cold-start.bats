#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031 # bats @test blocks run in subshells; export is intentional
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
	export CTL_LOG="$BATS_TEST_TMPDIR/ctl.log"
	# Presence of this file is the fake remote's "a tmux server is running".
	export REMOTE_SERVER="$BATS_TEST_TMPDIR/remote-server"
	export REMOTE_SESSION="workstation"
	: >"$SSH_LOG"
	: >"$TMUX_LOG"
	: >"$CTL_LOG"

	# Skips the launcher's `ssh host id -u` round-trip.
	export LZTMUX_REMOTE_TMPDIR="/run/user/1000"
	# Keeps the daemon socket + log inside the test tmpdir.
	export TMUX_TMPDIR="$BATS_TEST_TMPDIR"

	cat >"$FAKEBIN/ssh" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$SSH_LOG"
		case "$*" in
		*"command -v tmux"*) echo /usr/bin/tmux ;;
		*"uname -s; id -u"*) printf '%s\n%s\n' "${FAKE_UNAME:-Linux}" 1000 ;;
		*"systemctl --user start"*)
			if [ -n "${FAKE_UNIT_MISSING:-}" ]; then
				echo "Failed to start tmux-startup.service: Unit not found." >&2
				exit 1
			fi
			touch "$REMOTE_SERVER"
			;;
		*"launchctl kickstart"*)
			if [ -n "${FAKE_AGENT_MISSING:-}" ]; then
				echo "Could not find service \"org.nix-community.home.tmux-startup\" in domain for gui" >&2
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
		case "$*" in
		has-session*)
			[ -n "${FAKE_SESSION_GONE:-}" ] && exit 1
			;;
		esac
		exit 0
	EOF

	# The launcher's PATH fallback for an unsubstituted placeholder. The shipped
	# script takes the pinned store paths instead — see the pinning case below.
	for stub in lztmux-remote-bridge-renderer tmux-reflow-windows lztmux-remote-bridge-daemon; do
		printf '#!/bin/sh\nexit 0\n' >"$FAKEBIN/$stub"
	done
	cat >"$FAKEBIN/lztmux-remote-bridge-ctl" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$CTL_LOG"
		if [ -n "${FAKE_CTL_ERROR:-}" ]; then
			printf '%s\n' "$FAKE_CTL_ERROR" >&2
			exit 1
		fi
	EOF

	chmod +x "$FAKEBIN"/*
	export PATH="$FAKEBIN:$PATH"

	# Same @lib_remote@ substitution Nix does at build time.
	LAUNCHER="$BATS_TEST_TMPDIR/lztmux-remote-open"
	# Both placeholders sit on one line (`[[ -f … ]] && source …`), so /g matters.
	sed "s|@lib_remote@|$PWD/scripts/lib-remote.sh|g" \
		scripts/lztmux-remote-open.sh >"$LAUNCHER"
	export LAUNCHER
}

teardown() {
	if [[ -n ${DAEMON_PID:-} ]]; then
		kill "$DAEMON_PID" 2>/dev/null || true
	fi
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

@test "live compatible daemon is reused only after its ping succeeds" {
	touch "$REMOTE_SERVER"
	sleep 30 &
	DAEMON_PID=$!
	local sock="$TMUX_TMPDIR/lztmux-daemon-tp-g6-workstation.sock"
	printf '%s\n' "$DAEMON_PID" >"${sock}.pid"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	grep -q -- "--sock $sock ping _" "$CTL_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
	kill -0 "$DAEMON_PID"
}

@test "live daemon is reaped and the mirror recreated when its session is gone" {
	touch "$REMOTE_SERVER"
	export FAKE_SESSION_GONE=1
	sleep 30 &
	DAEMON_PID=$!
	local sock="$TMUX_TMPDIR/lztmux-daemon-tp-g6-workstation.sock"
	printf '%s\n' "$DAEMON_PID" >"${sock}.pid"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	grep -q -- "--sock $sock ping _" "$CTL_LOG"
	run kill -0 "$DAEMON_PID"
	[ "$status" -ne 0 ]
	grep -q 'new-session -d -s tp-g6-workstation' "$TMUX_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "live incompatible daemon is terminated and replaced" {
	touch "$REMOTE_SERVER"
	export FAKE_CTL_ERROR='lztmux-remote-bridge-ctl: ctl protocol version "2", this daemon speaks "1" — reopen the bridge'
	sleep 30 &
	DAEMON_PID=$!
	local sock="$TMUX_TMPDIR/lztmux-daemon-tp-g6-workstation.sock"
	printf '%s\n' "$DAEMON_PID" >"${sock}.pid"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	grep -q -- "--sock $sock ping _" "$CTL_LOG"
	grep -q 'new-session -d -s tp-g6-workstation' "$TMUX_LOG"
	run kill -0 "$DAEMON_PID"
	[ "$status" -ne 0 ]
}

@test "substituted bridge binaries win over the ones on PATH" {
	touch "$REMOTE_SERVER"

	# Pinned copies stand in for the store paths Nix substitutes; setup()'s PATH
	# stubs stay in place as what a stale tmux server would reach instead.
	local pinned="$BATS_TEST_TMPDIR/pinned"
	mkdir -p "$pinned"
	export PINNED_CTL_LOG="$BATS_TEST_TMPDIR/pinned-ctl.log"
	export PINNED_DAEMON_ENV="$BATS_TEST_TMPDIR/pinned-daemon.env"
	cat >"$pinned/ctl" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$PINNED_CTL_LOG"
		exit 1
	EOF
	cat >"$pinned/daemon" <<-'EOF'
		#!/bin/sh
		printf '%s\n%s\n' "$LZTMUX_DAEMON_RENDERER" "$LZTMUX_DAEMON_REFLOW" >"$PINNED_DAEMON_ENV"
	EOF
	printf '#!/bin/sh\nexit 0\n' >"$pinned/renderer"
	printf '#!/bin/sh\nexit 0\n' >"$pinned/reflow"
	chmod +x "$pinned"/*

	# Same substitution Nix does at build time, for all five placeholders.
	local launcher="$BATS_TEST_TMPDIR/lztmux-remote-open-pinned"
	sed -e "s|@lib_remote@|$PWD/scripts/lib-remote.sh|g" \
		-e "s|@bridge_ctl@|$pinned/ctl|g" \
		-e "s|@bridge_daemon@|$pinned/daemon|g" \
		-e "s|@bridge_renderer@|$pinned/renderer|g" \
		-e "s|@reflow@|$pinned/reflow|g" \
		scripts/lztmux-remote-open.sh >"$launcher"

	# A live pid forces the probe; the pinned ctl fails it, so the launcher also
	# has to reach the recreate path with the pinned daemon.
	sleep 30 &
	DAEMON_PID=$!
	local sock="$TMUX_TMPDIR/lztmux-daemon-tp-g6-workstation.sock"
	printf '%s\n' "$DAEMON_PID" >"${sock}.pid"

	run bash "$launcher" tp-g6
	[ "$status" -eq 0 ]

	grep -q -- "--sock $sock ping _" "$PINNED_CTL_LOG"
	[ ! -s "$CTL_LOG" ] # the PATH ctl was never consulted

	# The renderer/reflow the spawned daemon was handed are pinned too — those
	# are what mirror panes respawn into.
	local waited=0
	while [[ ! -s $PINNED_DAEMON_ENV && $waited -lt 50 ]]; do
		sleep 0.1
		waited=$((waited + 1))
	done
	run cat "$PINNED_DAEMON_ENV"
	[ "${lines[0]}" = "$pinned/renderer" ]
	[ "${lines[1]}" = "$pinned/reflow" ]
}

@test "unreachable bridge socket never signals a recycled live pid" {
	touch "$REMOTE_SERVER"
	export FAKE_CTL_ERROR='lztmux-remote-bridge-ctl: bridge daemon unreachable: connect: connection refused'
	sleep 30 &
	DAEMON_PID=$!
	local sock="$TMUX_TMPDIR/lztmux-daemon-tp-g6-workstation.sock"
	printf '%s\n' "$DAEMON_PID" >"${sock}.pid"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	grep -q 'new-session -d -s tp-g6-workstation' "$TMUX_LOG"
	kill -0 "$DAEMON_PID"
}

@test "cold start: an explicit session argument skips both the probe and the unit" {
	run bash "$LAUNCHER" tp-g6 scratch
	[ "$status" -eq 0 ]

	run grep -cE 'list-sessions|systemctl' "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =tp-g6-scratch' "$TMUX_LOG"
}

@test "darwin cold start: kickstarts the launchd agent, re-probes, bridges" {
	export FAKE_UNAME=Darwin
	unset LZTMUX_REMOTE_TMPDIR

	run bash "$LAUNCHER" mbp
	[ "$status" -eq 0 ]

	grep -q 'launchctl kickstart gui/1000/org.nix-community.home.tmux-startup' "$SSH_LOG"
	# macOS socket dir, never the Linux one.
	grep -q 'TMUX_TMPDIR=/tmp/tmux-1000' "$SSH_LOG"
	run grep -c 'TMUX_TMPDIR=/run/user' "$SSH_LOG"
	[ "$status" -ne 0 ]
	# Two probes: the empty one that triggered the kickstart, and the one after.
	[ "$(grep -c list-sessions "$SSH_LOG")" -eq 2 ]

	grep -q 'new-session -d -s mbp-workstation' "$TMUX_LOG"
	grep -q 'switch-client -t =mbp-workstation' "$TMUX_LOG"
}

@test "darwin cold start: a missing launchd agent fails by name and bridges nothing" {
	export FAKE_UNAME=Darwin FAKE_AGENT_MISSING=1
	unset LZTMUX_REMOTE_TMPDIR

	run bash "$LAUNCHER" mbp
	[ "$status" -eq 1 ]
	[[ $output == *"no tmux-startup launchd agent"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "darwin host with a live server is never cold-started" {
	export FAKE_UNAME=Darwin
	touch "$REMOTE_SERVER"

	run bash "$LAUNCHER" mbp
	[ "$status" -eq 0 ]

	run grep -cE 'launchctl|systemctl' "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =mbp-workstation' "$TMUX_LOG"
}
