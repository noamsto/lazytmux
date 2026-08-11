#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031 # bats @test blocks run in subshells; export is intentional
# ssh and tmux are fakes on PATH for the script-level tests below; nothing
# here touches a real host or server (#357).

setup() {
	# shellcheck source=/dev/null
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"

	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	export SSH_LOG="$BATS_TEST_TMPDIR/ssh.log"
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	export COPYID_LOG="$BATS_TEST_TMPDIR/copyid.log"
	: >"$SSH_LOG"
	: >"$TMUX_LOG"
	: >"$COPYID_LOG"

	# Routes each ssh invocation by an option unique to its role rather than
	# position, so reordering flags elsewhere in the script can't misfile a
	# call under the wrong branch: ControlPersist= only appears on the master
	# command, ControlPath=none only on the pubkey probe, ` -G ` only on the
	# identity lookup.
	cat >"$FAKEBIN/ssh" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$SSH_LOG"
		case "$*" in
		*ControlPersist=*)
			exit "${FAKE_MASTER_EXIT:-0}"
			;;
		*ControlPath=none*)
			exit "${FAKE_PROBE_EXIT:-1}"
			;;
		*"-G --"*)
			printf 'identityfile %s\n' "${FAKE_IDENTITY:-~/.ssh/id_ed25519}"
			exit 0
			;;
		esac
		exit 1
	EOF

	# FAKE_COPYID_EXIT defaults to 0 so every existing accept-path test keeps
	# seeing success; the failing-install test below overrides it.
	cat >"$FAKEBIN/ssh-copy-id" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$COPYID_LOG"
		exit "${FAKE_COPYID_EXIT:-0}"
	EOF

	# PERSIST_EXIT defaults to 1: tmux exits non-zero for a session option that
	# was never set, which is what an absent @remote_auth_persist looks like.
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$TMUX_LOG"
		case "$*" in
		"show -gv @remote_auth_persist")
			printf '%s' "${PERSIST_OUTPUT:-}"
			exit "${PERSIST_EXIT:-1}"
			;;
		esac
		exit 0
	EOF

	chmod +x "$FAKEBIN"/*
	export PATH="$FAKEBIN:$PATH"

	# A real pub key file so the "identity found" path is reachable — the
	# no-tty test needs to fail past the key-file check to actually exercise
	# the tty gate, not stop earlier for an unrelated reason.
	export HOME="$BATS_TEST_TMPDIR/home"
	mkdir -p "$HOME/.ssh"
	touch "$HOME/.ssh/id_ed25519.pub"

	# Same @lib_remote@ substitution Nix does at build time.
	SCRIPT="$BATS_TEST_TMPDIR/lztmux-remote-auth"
	sed "s|@lib_remote@|$PWD/scripts/lib-remote.sh|g" \
		scripts/lztmux-remote-auth.sh >"$SCRIPT"
	export SCRIPT
}

# Every case runs `bash "$SCRIPT"` rather than executing it: the nix check
# sandbox has no /usr/bin/env, so the `#!/usr/bin/env bash` shebang cannot
# resolve there. writeShellScriptBin rewrites that shebang to a store path
# anyway, so an explicit interpreter is what the shipped script really gets.
# bats' own `run` gives the child a non-tty stdin, which is also what
# exercises the "no tty" branches below without any extra plumbing.

@test "remote_auth_identity: picks the host's own key and expands ~" {
	HOME=/home/tester remote_auth_identity "user noams
hostname mbp-m4-pro
identityfile ~/.ssh/id_ed25519
controlpath /home/tester/.ssh/master-noams@mbp:22"
	[ "$REPLY" = "/home/tester/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: first identityfile wins when ssh lists several" {
	HOME=/home/tester remote_auth_identity "identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/noam_factify_ed25519"
	[ "$REPLY" = "/home/tester/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: absolute paths pass through untouched" {
	HOME=/home/tester remote_auth_identity "identityfile /etc/keys/shared_ed25519"
	[ "$REPLY" = "/etc/keys/shared_ed25519.pub" ]
}

# No identityfile means ssh would fall back to its built-in defaults. Guessing
# one here could push a work key to a personal host, so the caller must skip the
# offer instead.
@test "remote_auth_identity: empty when the host declares no identity" {
	HOME=/home/tester remote_auth_identity "user noams
hostname lab"
	[ -z "$REPLY" ]
}

@test "persist: absent tmux option falls back to 14400" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	grep -q 'ControlPersist=14400' "$SSH_LOG"
}

@test "persist: empty tmux option falls back to 14400" {
	export PERSIST_EXIT=0 PERSIST_OUTPUT=""
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	grep -q 'ControlPersist=14400' "$SSH_LOG"
}

@test "persist: non-numeric tmux option falls back to 14400" {
	export PERSIST_EXIT=0 PERSIST_OUTPUT="lots"
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	grep -q 'ControlPersist=14400' "$SSH_LOG"
}

@test "persist: a plain integer tmux option is used verbatim" {
	export PERSIST_EXIT=0 PERSIST_OUTPUT="60"
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	grep -q 'ControlPersist=60' "$SSH_LOG"
	run grep -c 'ControlPersist=14400' "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "master command carries -M -f -o ServerAliveInterval=15 and no ControlPath, with -- before the host" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	master_line="$(head -n1 "$SSH_LOG")"
	[[ $master_line == *"-M"* ]]
	[[ $master_line == *"-f"* ]]
	[[ $master_line == *"-o ServerAliveInterval=15"* ]]
	[[ $master_line == *"-- tp-g6 true"* ]]
	[[ $master_line != *"ControlPath"* ]]
}

@test "a failing master connect exits non-zero and never reaches the probe or ssh-copy-id" {
	export FAKE_MASTER_EXIT=1

	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 1 ]

	[ "$(wc -l <"$SSH_LOG")" -eq 1 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "pubkey probe carries -o BatchMode=yes and -o ControlPath=none, with -- before the host" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	probe_line="$(sed -n '2p' "$SSH_LOG")"
	[[ $probe_line == *"-o BatchMode=yes"* ]]
	[[ $probe_line == *"-o ControlPath=none"* ]]
	[[ $probe_line == *"-- tp-g6 true"* ]]
}

@test "pubkey probe succeeding exits 0 without offering ssh-copy-id" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	# Only the master + probe ran; the identity lookup is never reached.
	[ "$(wc -l <"$SSH_LOG")" -eq 2 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "pubkey probe failing with no tty exits 0 without running ssh-copy-id" {
	export FAKE_PROBE_EXIT=1

	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	# Master, probe, and the identity lookup all ran, but there is no tty to
	# confirm the offer against, so ssh-copy-id must not run (and the script
	# must not hang on `read` waiting for one).
	[ "$(wc -l <"$SSH_LOG")" -eq 3 ]
	[[ $(tail -n1 "$SSH_LOG") == *"-G -- tp-g6"* ]]
	[ ! -s "$COPYID_LOG" ]
}

# The cases above all hit `[[ -t 0 ]] || exit 0` on line 62 — bats' `run` gives
# the child a non-tty stdin, so none of them ever reach the confirm prompt or
# ssh-copy-id. Mutation testing confirmed the gap: swapping the ssh-copy-id
# argument order left every test above green. These cases drive the script
# under a real pty via util-linux `script -qec CMD /dev/null`, piping the
# answer into script's own stdin so it lands on the child's pty stdin exactly
# like a typed reply. `script`'s stdout mirrors the pty (stdin+stdout+stderr
# all merge once a pty is in play, same as any real terminal), so `run`
# captures everything the user would see. FAKE_PROBE_EXIT is left unset
# (defaults to 1, i.e. no key installed yet) to reach the offer.
#
# The accept and ssh-copy-id-failure paths both end at pause_then_exit, which
# reads one more line ("Press Enter to return…") before returning — the
# second blank line in the piped input satisfies that, not a second answer.

@test "accepting the offer runs ssh-copy-id with -i <key> before the host, never swapped" {
	run bash -c "printf 'y\n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]

	# Argument order is the whole point: ssh-copy-id -i <key> <host>. Asserting
	# the exact recorded string (not just that ssh-copy-id ran at all) is what
	# makes this fail if the two arguments are ever swapped.
	[ "$(cat "$COPYID_LOG")" = "-i $HOME/.ssh/id_ed25519.pub tp-g6" ]
}

@test "answering n exits 0 without running ssh-copy-id" {
	run bash -c "printf 'n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "answering with empty input exits 0 without running ssh-copy-id" {
	run bash -c "printf '\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "a failing ssh-copy-id still exits 0, since the connection is authenticated regardless" {
	export FAKE_COPYID_EXIT=1
	run bash -c "printf 'y\n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[[ $output == *"Could not install the key; the connection is still authenticated."* ]]
}
