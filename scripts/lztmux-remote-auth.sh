#!/usr/bin/env bash
# Interactive ssh handshake for one remote-bridge host (#357).
#
# Creates a ControlMaster, which is the whole point: `ControlMaster no` (the
# Host * default) still REUSES an existing master, so once this succeeds the
# picker probe and lztmux-remote-open's ssh calls ride it with no prompt and no
# code changes. The bridge daemon and the graphics fetcher do NOT: the daemon
# passes its own `-o ControlPath` on the ssh command line, which overrides the
# config and builds a master of its own, so those two still authenticate
# independently.
#
# Runs with a real tty — the picker hands its popup over via tea.ExecProcess —
# because ssh must own the terminal while a password is typed. Nothing here
# reads, stores or forwards the secret.
set -euo pipefail

# @lib_remote@ is substituted at Nix build time; in bats the lib is pre-sourced.
# shellcheck source=/dev/null
[[ -f "@lib_remote@" ]] && source "@lib_remote@"

host="${1:?usage: lztmux-remote-auth <host>}"

persist="$(tmux show -gv @remote_auth_persist 2>/dev/null || true)"
# 0 tells ssh to persist forever, not "off" — a hand-set tmux option can carry
# it even though the home-manager type clamps 60-86400, so reject it like any
# other non-positive value rather than passing it through.
[[ $persist =~ ^[1-9][0-9]*$ ]] || persist=14400

# Pause before handing the terminal back, so a failure is readable instead of
# being wiped by the picker's next full-screen paint.
pause_then_exit() {
	if [[ -t 0 ]]; then
		printf '\nPress Enter to return… '
		read -r _
	fi
	exit "$1"
}

printf 'Authenticating to %s\n\n' "$host"

# Read once, before the master command, so the ControlPath check below has an
# answer regardless of whether auth succeeds; reused for the identity lookup
# near the end instead of asking ssh again.
sshg="$(ssh -G -- "$host" 2>/dev/null || true)"
controlpath="$(awk '$1 == "controlpath" { print $2; exit }' <<<"$sshg")"

# -M overrides the config's `ControlMaster no`. ControlPath is deliberately NOT
# passed: the user's config owns it, and every other call site looks there.
# -f backgrounds only after authentication, so the host-key question and the
# password prompt both happen here and a clean return means auth succeeded.
# `true` rather than -N lets ControlPersist reap the master (verified: process
# and socket both gone once it expires); -N would live until an explicit -O exit
# and leave a stale socket across a suspend.
# ServerAliveInterval is explicit because Host * sets it to 0, which would let a
# half-open master hang every later call with nothing to time it out.
if ! ssh -M -f -o ControlPersist="$persist" -o ServerAliveInterval=15 -- "$host" true; then
	printf '\nCould not authenticate to %s.\n' "$host" >&2
	pause_then_exit 1
fi

# OpenSSH's own default ControlPath is "none": connection sharing is off
# unless the user's config sets a path, in which case -M above created nothing
# for anything to reuse. Claiming a persistent connection in that case would
# be false — lazytmux is a public flake, so a stock config is the common case,
# not the exception.
if [[ -z $controlpath || $controlpath == none ]]; then
	printf '\nConnected to %s. Connection sharing is off (no ControlPath set), so ssh will prompt again on the next connection — set a ControlPath in your ssh config to avoid that.\n' "$host"
else
	printf '\nConnected. %s stays authenticated for %ss of idle time.\n' "$host" "$persist"
fi

# Would pubkey auth alone have worked? ControlPath=none forces a fresh
# connection rather than riding the master just created, and BatchMode makes it
# fail rather than prompt — so a non-zero exit means no key is installed. This
# tests the condition directly instead of parsing `ssh -v` for its auth method.
if ssh -o BatchMode=yes -o ControlPath=none -o ConnectTimeout=5 -- "$host" true 2>/dev/null; then
	exit 0
fi

remote_auth_identity "$sshg"
key="$REPLY"
[[ -n $key ]] || exit 0
[[ -t 0 ]] || exit 0

printf '\nInstall %s on %s so this is the last time? [y/N] ' "$key" "$host"
read -r reply
if [[ $reply != [Yy]* ]]; then
	printf '\nNo key installed — the remote-bridge daemon runs detached with no terminal and cannot answer a password prompt, so bridging a session to %s will fail until a key is installed.\n' "$host"
	exit 0
fi

# Rides the master created above, so this needs no second password.
if ! ssh-copy-id -i "$key" "$host"; then
	printf '\nCould not install the key; the connection is still authenticated.\n' >&2
	pause_then_exit 0
fi
printf '\n%s will not ask again.\n' "$host"
pause_then_exit 0
