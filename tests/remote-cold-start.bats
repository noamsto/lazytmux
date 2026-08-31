#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031,SC2016 # bats @test blocks run in subshells; export is intentional; '$(…)' in single quotes is the injection payload under test
# Cold-starting a serverless remote (#287). The launcher may only reach for
# tmux-startup.service when list-sessions came back empty, and must re-probe
# afterwards instead of assuming the unit produced the session it wanted —
# unit state is not server state, in either direction (#345).
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
	export RESTORE_MARKER="$BATS_TEST_TMPDIR/restored"
	# Presence of this file is the fake remote's "session 'proj' exists".
	export NEWDIR_MARKER="$BATS_TEST_TMPDIR/created"
	: >"$SSH_LOG"
	: >"$TMUX_LOG"
	: >"$CTL_LOG"

	# Skips the launcher's `ssh host id -u` round-trip.
	export LZTMUX_REMOTE_TMPDIR="/run/user/1000"
	# Keeps the daemon socket + log inside the test tmpdir.
	export TMUX_TMPDIR="$BATS_TEST_TMPDIR"

	cat >"$FAKEBIN/ssh" <<-'EOF'
		#!/bin/sh
		# One marker line per invocation (independent of how many lines the
		# command itself spans) so a test can count actual ssh round-trips, not
		# just substring hits.
		printf '===SSH-CALL===\n' >>"$SSH_LOG"
		echo "$*" >>"$SSH_LOG"
		# The launcher's combined probe is recognized by its fixed leading no-op,
		# without actually interpreting the shell it received. Session/window
		# resolve here only when the caller didn't already name them (embedded as
		# *_lit literals).
		case "$*" in
		*": lztmux-probe;"*)
			os="${FAKE_UNAME:-Linux}"
			uid=1000
			if [ "$os" = Darwin ]; then tmpdir="/tmp/tmux-$uid"; else tmpdir="/run/user/$uid"; fi
			case "$*" in
			*"tmpdir_lit="*) tmpdir=$(printf '%s\n' "$*" | sed -n "s/.*tmpdir_lit='\([^']*\)'.*/\1/p") ;;
			esac
			sess=""
			case "$*" in
			*"sess_lit="*) sess=$(printf '%s\n' "$*" | sed -n "s/.*sess_lit='\([^']*\)'.*/\1/p") ;;
			*) [ -f "$REMOTE_SERVER" ] && sess="$REMOTE_SESSION" ;;
			esac
			win=""
			case "$*" in
			*"win_lit="*) win=$(printf '%s\n' "$*" | sed -n "s/.*win_lit='\([^']*\)'.*/\1/p") ;;
			*) [ -n "$sess" ] && [ -z "${FAKE_NO_WINDOW:-}" ] && win=1 ;;
			esac
			printf 'os=%s\nuid=%s\ntmux=%s\ntmpdir=%s\nsess=%s\nwin=%s\n' "$os" "$uid" /usr/bin/tmux "$tmpdir" "$sess" "$win"
			exit 0
			;;
		esac
		case "$*" in
		*"command -v tmux-remux"*) echo /usr/bin/tmux-remux ;;
		*"tmux-remux restore"*)
			if [ -n "${FAKE_RESTORE_FAILS:-}" ]; then
				echo "restore: boom" >&2
				exit 1
			fi
			if [ -z "${RESTORE_TARGET_MISMATCH:-}" ]; then
				touch "$RESTORE_MARKER"
			fi
			;;
		*"has-session -t '=workstation'"*)
			[ -f "$REMOTE_SERVER" ] && exit 0
			exit 1
			;;
		*"has-session -t '=work'"*)
			[ -f "$RESTORE_MARKER" ] && exit 0
			exit 1
			;;
		*"new-session -d -s 'proj'"*)
			if [ -z "${NEWDIR_TARGET_MISMATCH:-}" ]; then
				touch "$NEWDIR_MARKER"
			fi
			;;
		*"has-session -t '=proj'"*)
			[ -f "$NEWDIR_MARKER" ] && exit 0
			exit 1
			;;
		*"systemctl --user restart"*)
			if [ -n "${FAKE_UNIT_MISSING:-}" ]; then
				echo "Failed to restart tmux-startup.service: Unit not found." >&2
				exit 1
			fi
			touch "$REMOTE_SERVER"
			;;
		# `start` against a dead server behind a RemainAfterExit=yes unit
		# systemd still calls `active`: exits 0, produces nothing (#345).
		*"systemctl --user start"*) ;;
		*"launchctl kickstart"*)
			if [ -n "${FAKE_AGENT_MISSING:-}" ]; then
				echo "Could not find service \"org.nix-community.home.tmux-startup\" in domain for gui" >&2
				exit 1
			fi
			touch "$REMOTE_SERVER"
			;;
		*list-sessions*) [ -f "$REMOTE_SERVER" ] && echo "$REMOTE_SESSION" ;;
		# A failed remote list-windows still exits 0 with empty stdout: the remote
		# command is a pipeline ending in awk, and carries none of our pipefail.
		*list-windows*) [ -n "${FAKE_NO_WINDOW:-}" ] || echo 1 ;;
		esac
		exit 0
	EOF

	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$TMUX_LOG"
		case "$*" in
		has-session*)
			if [ -n "${FAKE_LOCAL_SESSION:-}" ]; then
				case "$*" in
				*"=$FAKE_LOCAL_SESSION") exit 0 ;;
				*) exit 1 ;;
				esac
			fi
			if [ -n "${FAKE_DAEMON_SESSION:-}" ]; then
				case "$*" in
				*"=$FAKE_DAEMON_SESSION") exit 0 ;;
				*) exit 1 ;;
				esac
			fi
			[ -n "${FAKE_SESSION_GONE:-}" ] && exit 1
			exit 1
			;;
			display-message*)
			case "$*" in
			*"#{client_width} #{client_height} #{status}"*)
				[ -n "${FAKE_CLIENT_SIZE:-}" ] && printf '%s\n' "$FAKE_CLIENT_SIZE"
				;;
			esac
			;;
		show-options*)
			case "$*" in
			*"@bridge_host"*) [ -n "${FAKE_BRIDGE_HOST:-}" ] && printf '%s\n' "$FAKE_BRIDGE_HOST" ;;
			*"@bridge_session"*) [ -n "${FAKE_BRIDGE_SESSION:-}" ] && printf '%s\n' "$FAKE_BRIDGE_SESSION" ;;
			esac
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

	grep -q 'systemctl --user restart tmux-startup.service' "$SSH_LOG"
	# `start` would no-op against a unit systemd still calls active (#345).
	run grep -c 'systemctl --user start' "$SSH_LOG"
	[ "$status" -ne 0 ]

	# Two probes: the empty one that triggered the start, and the one after it.
	[ "$(grep -c list-sessions "$SSH_LOG")" -eq 2 ]

	# The session name came from the remote, never from the launcher.
	grep -q 'new-session -d -s tp-g6-workstation' "$TMUX_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "mirror session starts at the invoking client's content size" {
	export FAKE_CLIENT_SIZE='200 50 off'

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	grep -q 'new-session -d -s tp-g6-workstation -n workstation -x 200 -y 50' "$TMUX_LOG"
}

@test "an ordinary local session survives a colliding mirror name" {
	export REMOTE_SESSION=config
	export FAKE_LOCAL_SESSION=nix-config

	run bash "$LAUNCHER" nix
	[ "$status" -eq 0 ]

	grep -q 'new-session -d -s nix-config-remote -n config' "$TMUX_LOG"
	run ! grep -q 'kill-session -t =nix-config$' "$TMUX_LOG"
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
	export FAKE_DAEMON_SESSION=tp-g6-workstation FAKE_BRIDGE_HOST=tp-g6
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

@test "restore: requested session isn't live -> cold starts, restores, bridges" {
	export LZTMUX_REMOTE_RESTORE=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 0 ]

	grep -q 'systemctl --user restart tmux-startup.service' "$SSH_LOG"
	grep -q "has-session -t '=work'" "$SSH_LOG"
	grep -q 'tmux-remux restore' "$SSH_LOG"
	# Guards against dropping the PATH= that lets tmux-remux find the bare
	# `tmux` binary it execs — the fake `command -v tmux` above resolves to
	# /usr/bin/tmux, so the restore command must carry /usr/bin on PATH.
	grep -q 'PATH=/usr/bin:.*tmux-remux restore' "$SSH_LOG"
	grep -q 'new-session -d -s tp-g6-work' "$TMUX_LOG"
	grep -q 'switch-client -t =tp-g6-work' "$TMUX_LOG"
}

@test "restore: server already running but session missing -> restores without a cold start" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 0 ]

	run grep -c systemctl "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q "has-session -t '=work'" "$SSH_LOG"
	grep -q 'tmux-remux restore' "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-work' "$TMUX_LOG"
}

@test "restore: tmux-remux restore failing surfaces an error and bridges nothing" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1
	export FAKE_RESTORE_FAILS=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 1 ]
	[[ $output == *"restore failed"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "restore: session absent even after a successful restore fails loudly" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1
	export RESTORE_TARGET_MISMATCH=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 1 ]
	[[ $output == *"tmux-remux's restore filter may have skipped it"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "new dir: session absent and no server -> cold starts, then creates it" {
	export LZTMUX_REMOTE_NEW_DIR="/srv/my proj"

	run bash "$LAUNCHER" tp-g6 proj
	[ "$status" -eq 0 ]

	# Neither of the two -z $sess cold-start gates fires with a name in hand, so
	# without this branch's own gate the server would be an ssh session's.
	grep -q 'systemctl --user restart tmux-startup.service' "$SSH_LOG"
	grep -q "new-session -d -s 'proj' -c '/srv/my proj'" "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-proj' "$TMUX_LOG"
}

@test "new dir: server already running -> creates without a cold start" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_NEW_DIR=/srv/proj

	run bash "$LAUNCHER" tp-g6 proj
	[ "$status" -eq 0 ]

	run grep -c systemctl "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q "new-session -d -s 'proj' -c '/srv/proj'" "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-proj' "$TMUX_LOG"
}

@test "new dir: a session that already exists is bridged, not recreated" {
	touch "$REMOTE_SERVER" "$NEWDIR_MARKER"
	export LZTMUX_REMOTE_NEW_DIR=/srv/proj

	run bash "$LAUNCHER" tp-g6 proj
	[ "$status" -eq 0 ]

	run grep -c new-session "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =tp-g6-proj' "$TMUX_LOG"
}

@test "new dir: session absent even after a successful create fails loudly" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_NEW_DIR=/srv/proj
	export NEWDIR_TARGET_MISMATCH=1

	run bash "$LAUNCHER" tp-g6 proj
	[ "$status" -eq 1 ]
	[[ $output == *"was not created"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "new dir: combined with a restore is rejected before any round trip" {
	export LZTMUX_REMOTE_NEW_DIR=/srv/proj
	export LZTMUX_REMOTE_RESTORE=1

	run bash "$LAUNCHER" tp-g6 proj
	[ "$status" -eq 1 ]
	[[ $output == *"mutually exclusive"* ]]

	[ ! -s "$SSH_LOG" ]
	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "a session with no active window fails instead of opening a blank mirror" {
	touch "$REMOTE_SERVER"
	export FAKE_NO_WINDOW=1

	run bash "$LAUNCHER" tp-g6 ghost
	[ "$status" -eq 1 ]
	[[ $output == *"ghost"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "restore: a live session attach with the flag unset is unaffected" {
	touch "$REMOTE_SERVER"
	# LZTMUX_REMOTE_RESTORE intentionally unset.

	run bash "$LAUNCHER" tp-g6 workstation
	[ "$status" -eq 0 ]

	run grep -cE 'has-session|tmux-remux' "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "bad LZTMUX_REMOTE_TMPDIR is rejected before bridging" {
	export LZTMUX_REMOTE_TMPDIR="/run/user/1000 x"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 1 ]
	[[ $output == *"unusable remote tmpdir"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
	# Rejected before the value ever rides into the combined probe — not merely
	# before the daemon launches.
	[ ! -s "$SSH_LOG" ]

	export LZTMUX_REMOTE_TMPDIR='/run/user/$(id -u)'

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 1 ]
	[[ $output == *"unusable remote tmpdir"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
	[ ! -s "$SSH_LOG" ]
}

@test "LZTMUX_REMOTE_NEW_DIR containing a backslash is rejected before any round trip" {
	export LZTMUX_REMOTE_NEW_DIR='/srv/pro\ject'

	run bash "$LAUNCHER" tp-g6 proj
	[ "$status" -eq 1 ]
	[[ $output == *"backslash"* ]]

	[ ! -s "$SSH_LOG" ]
}

@test "a session name containing a backslash is rejected before list-windows ever runs" {
	# An explicit sess arg skips both cold-start gates regardless of
	# REMOTE_SERVER, so the only shell_quote("$sess") call this path reaches is
	# list-windows — assert it never gets built.
	run bash "$LAUNCHER" tp-g6 'wor\kstation'
	[ "$status" -eq 1 ]
	[[ $output == *"backslash"* ]]

	run grep -c list-windows "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "a session name containing a backslash is rejected before the restore path's has-session ever runs" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1

	run bash "$LAUNCHER" tp-g6 'wor\kstation'
	[ "$status" -eq 1 ]
	[[ $output == *"backslash"* ]]

	run grep -c has-session "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "shell_quote is plain POSIX single-quoting: correct for quotes, backslash-bearing input is unchanged (rejected upstream, not doubled)" {
	# shellcheck disable=SC1090
	eval "$(sed -n '/^shell_quote()/,/^}/p' "$LAUNCHER")"

	local input quoted result

	# Single-quote correctness, round-tripped under sh — the case shell_quote
	# still has to get right in every dialect.
	input="it's/a path"
	quoted="$(shell_quote "$input")"
	result="$(sh -c "printf '%s' $quoted")"
	[ "$result" = "$input" ]

	# A literal backslash now passes through inert (no longer doubled):
	# backslash-bearing values are rejected at the launcher's entry before they
	# ever reach shell_quote (see the rejection tests below), so this guards
	# only against reintroducing backslash-doubling here directly.
	quoted="$(shell_quote 'a\b')"
	[ "$quoted" = "'a\\b'" ]

	# fish round-trips the same single-quote case identically — `nix flake
	# check`'s sandbox has no fish (see flake.nix's remote-tests
	# nativeBuildInputs), so this leg only strengthens local runs; it must never
	# skip the sh assertions above, which are what the CI gate actually relies
	# on.
	if command -v fish >/dev/null 2>&1; then
		quoted="$(shell_quote "$input")"
		result="$(fish -c "echo $quoted")"
		[ "$result" = "$input" ]
	fi
}

# The launcher makes at most one combined ssh probe (resolving whichever of
# session/window the caller didn't already name) before ever reaching the
# daemon. These assert that, for each combination of what the caller knows.

@test "combined probe: neither session nor window given -> exactly one ssh call before the daemon" {
	touch "$REMOTE_SERVER"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	[ "$(grep -c '===SSH-CALL===' "$SSH_LOG")" -eq 1 ]
	grep -q ': lztmux-probe;' "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "combined probe: session given, window not -> exactly one ssh call before the daemon" {
	touch "$REMOTE_SERVER"

	run bash "$LAUNCHER" tp-g6 workstation
	[ "$status" -eq 0 ]

	[ "$(grep -c '===SSH-CALL===' "$SSH_LOG")" -eq 1 ]
	grep -q "sess_lit='workstation'" "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "combined probe: session and window both given -> exactly one ssh call before the daemon" {
	run bash "$LAUNCHER" tp-g6 workstation 3
	[ "$status" -eq 0 ]

	[ "$(grep -c '===SSH-CALL===' "$SSH_LOG")" -eq 1 ]
	grep -q "sess_lit='workstation'" "$SSH_LOG"
	grep -q "win_lit='3'" "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "combined probe: LZTMUX_REMOTE_TMPDIR unset still resolves in one ssh call" {
	unset LZTMUX_REMOTE_TMPDIR
	touch "$REMOTE_SERVER"

	run bash "$LAUNCHER" tp-g6
	[ "$status" -eq 0 ]

	[ "$(grep -c '===SSH-CALL===' "$SSH_LOG")" -eq 1 ]
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}

@test "a session name with spaces survives the combined probe end-to-end" {
	run bash "$LAUNCHER" tp-g6 'my session'
	[ "$status" -eq 0 ]

	# The whole name rides through shell_quote as one single-quoted literal,
	# never split on the embedded space.
	grep -q "sess_lit='my session'" "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-my session' "$TMUX_LOG"
}
