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
	# ssh -G lookup. real ssh -G always emits a controlpath line (default
	# "none") alongside identityfile, so the fake does too.
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
			printf 'controlpath %s\n' "${FAKE_CONTROLPATH:-none}"
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
	remote_auth_identity "user noams
hostname mbp-m4-pro
identityfile ~/.ssh/id_ed25519
controlpath $HOME/.ssh/master-noams@mbp:22"
	[ "$REPLY" = "$HOME/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: walks multiple identityfiles and picks the first whose .pub exists" {
	# The realistic Host * shape: five-plus built-in defaults, id_rsa first,
	# and only id_ed25519.pub (touched in setup) actually on disk.
	remote_auth_identity "identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_dsa
identityfile ~/.ssh/id_ecdsa
identityfile ~/.ssh/id_ecdsa_sk
identityfile ~/.ssh/id_ed25519"
	[ "$REPLY" = "$HOME/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: first identityfile wins when both exist" {
	touch "$HOME/.ssh/noam_factify_ed25519.pub"
	remote_auth_identity "identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/noam_factify_ed25519"
	[ "$REPLY" = "$HOME/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: absolute paths pass through untouched" {
	touch "$BATS_TEST_TMPDIR/shared_ed25519.pub"
	remote_auth_identity "identityfile $BATS_TEST_TMPDIR/shared_ed25519"
	[ "$REPLY" = "$BATS_TEST_TMPDIR/shared_ed25519.pub" ]
}

# Guessing here could push a work key to a personal host, so when none of the
# resolved identityfiles has a .pub on disk the caller must skip the offer
# instead of falling back to the first (nonexistent) one.
@test "remote_auth_identity: empty when none of the resolved identityfiles' .pub exists" {
	remote_auth_identity "identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_dsa
identityfile ~/.ssh/id_ecdsa
identityfile ~/.ssh/id_ecdsa_sk
identityfile ~/.ssh/id_xmss"
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

# ControlPersist=0 means "persist forever" to ssh, not "off" — the
# home-manager type clamps 60-86400, but a hand-set tmux option isn't bound by
# that, so 0 must still be rejected here.
@test "persist: a zero tmux option falls back to 14400" {
	export PERSIST_EXIT=0 PERSIST_OUTPUT="0"
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	grep -q 'ControlPersist=14400' "$SSH_LOG"
	run grep -c -- '-o ControlPersist=0 ' "$SSH_LOG"
	[ "$status" -ne 0 ]
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

@test "ssh -G runs before the master command, so the ControlPath check has an answer either way" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	first_line="$(head -n1 "$SSH_LOG")"
	[[ $first_line == *"-G --"* ]]
}

@test "master command carries -M -f -o ServerAliveInterval=15 and no ControlPath, with -- before the host" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	master_line="$(sed -n '2p' "$SSH_LOG")"
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

	# ssh -G (for the ControlPath check) ran ahead of the master command, which
	# then failed, so exactly those two calls are logged.
	[ "$(wc -l <"$SSH_LOG")" -eq 2 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "pubkey probe carries -o BatchMode=yes and -o ControlPath=none, with -- before the host" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	probe_line="$(sed -n '3p' "$SSH_LOG")"
	[[ $probe_line == *"-o BatchMode=yes"* ]]
	[[ $probe_line == *"-o ControlPath=none"* ]]
	[[ $probe_line == *"-- tp-g6 true"* ]]
}

@test "pubkey probe succeeding exits 0 without offering ssh-copy-id" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	# ssh -G, the master, and the probe ran; the identity lookup is never
	# reached and reuses ssh -G's output anyway, so no fourth call appears.
	[ "$(wc -l <"$SSH_LOG")" -eq 3 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "pubkey probe failing with no tty exits 0 without running ssh-copy-id" {
	export FAKE_PROBE_EXIT=1

	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]

	# ssh -G, the master, and the probe all ran, but there is no tty to confirm
	# the offer against, so ssh-copy-id must not run (and the script must not
	# hang on `read` waiting for one). The identity lookup reuses ssh -G's
	# earlier output rather than invoking ssh a fourth time.
	[ "$(wc -l <"$SSH_LOG")" -eq 3 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "no ControlPath in ssh config: warns sharing is off instead of claiming persistence" {
	export FAKE_PROBE_EXIT=0
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	[[ $output == *"Connection sharing is off"* ]]
	[[ $output != *"stays authenticated"* ]]
}

@test "a configured ControlPath: claims the persistence window" {
	export FAKE_PROBE_EXIT=0 FAKE_CONTROLPATH="$HOME/.ssh/master-%r@%n:%p"
	run bash "$SCRIPT" tp-g6
	[ "$status" -eq 0 ]
	[[ $output == *"stays authenticated for 14400s"* ]]
	[[ $output != *"Connection sharing is off"* ]]
}

# The confirm prompt sits behind `[[ -t 0 ]]` and bats' `run` gives the child a
# non-tty stdin, so reaching it needs a real pty: util-linux `script -qec CMD
# /dev/null`. Piping the answer into script's own stdin lands it on the child's
# pty stdin like a typed reply, and script's stdout mirrors the pty
# (stdin/stdout/stderr all merge once a pty is in play), so `run` captures what
# the user would see. FAKE_PROBE_EXIT is left unset (defaults to 1, no key
# installed) to reach the offer at all.
#
# The accept and ssh-copy-id-failure paths end at pause_then_exit, which reads
# one more line — that is what the second blank line in the piped input
# satisfies, not a second answer. The decline path exits from the confirm
# branch with no pause, so it needs only the one line.

# nixpkgs' util-linux claims darwin support but its darwin build ships no
# `script`, so these four exited 127 on the aarch64-darwin CI leg. Probe the
# GNU calling convention rather than testing `uname`: BSD `script` takes the
# command after the file and would silently misread `-qec`, so "a script(1)
# exists" is the wrong question. Skipping is visible in the bats output; the
# linux leg still runs all four.
require_gnu_script() {
	script -qec true /dev/null >/dev/null 2>&1 || skip "no GNU-style script(1) for a pty"
}

@test "accepting the offer runs ssh-copy-id with -i <key> before the host, never swapped" {
	require_gnu_script
	run bash -c "printf 'y\n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]

	# Argument order is the whole point: ssh-copy-id -i <key> <host>. Asserting
	# the exact recorded string (not just that ssh-copy-id ran at all) is what
	# makes this fail if the two arguments are ever swapped.
	[ "$(cat "$COPYID_LOG")" = "-i $HOME/.ssh/id_ed25519.pub tp-g6" ]
}

@test "answering n exits 0 without running ssh-copy-id" {
	require_gnu_script
	run bash -c "printf 'n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "declining the offer warns the daemon cannot authenticate unattended" {
	require_gnu_script
	run bash -c "printf 'n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[[ $output == *"cannot answer a password prompt"* ]]
	[[ $output == *"bridging a session to tp-g6 will fail"* ]]
}

@test "answering with empty input exits 0 without running ssh-copy-id" {
	require_gnu_script
	run bash -c "printf '\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[ ! -s "$COPYID_LOG" ]
}

@test "a failing ssh-copy-id still exits 0, since the connection is authenticated regardless" {
	require_gnu_script
	export FAKE_COPYID_EXIT=1
	run bash -c "printf 'y\n\n' | timeout 5 script -qec 'bash $SCRIPT tp-g6' /dev/null"
	[ "$status" -eq 0 ]
	[[ $output == *"Could not install the key; the connection is still authenticated."* ]]
}
