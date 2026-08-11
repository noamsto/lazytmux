#!/usr/bin/env bash
# Interactive ssh handshake for one remote-bridge host (#357).
#
# Creates a ControlMaster, which is the whole point: `ControlMaster no` (the
# Host * default) still REUSES an existing master, so once this succeeds the
# picker probe, lztmux-remote-open's 3-8 ssh calls, the bridge daemon's `ssh
# -CC` and the graphics fetcher all ride it with no prompt and no code changes.
#
# Runs with a real tty — the picker hands its popup over via tea.ExecProcess —
# because ssh must own the terminal while a password is typed. Nothing here
# reads, stores or forwards the secret.
set -euo pipefail

# shellcheck source=/dev/null
source "@lib_remote@"

host="${1:?usage: lztmux-remote-auth <host>}"

persist="$(tmux show -gv @remote_auth_persist 2>/dev/null || true)"
[[ $persist =~ ^[0-9]+$ ]] || persist=14400

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

# -M overrides the config's `ControlMaster no`. ControlPath is deliberately NOT
# passed: the user's config owns it, and every other call site looks there.
# -f backgrounds only after authentication, so the host-key question and the
# password prompt both happen here and a clean return means auth succeeded.
# `true` rather than -N lets ControlPersist reap the master (verified: process
# and socket both gone once it expires); -N would live until an explicit -O exit
# and leave a stale socket across a suspend.
# ServerAliveInterval is explicit because Host * sets it to 0, which would let a
# half-open master hang every later call with nothing to time it out.
if ! ssh -M -f -o ControlPersist="$persist" -o ServerAliveInterval=15 "$host" true; then
	printf '\nCould not authenticate to %s.\n' "$host" >&2
	pause_then_exit 1
fi

printf '\nConnected. %s stays authenticated for %ss of idle time.\n' "$host" "$persist"

# Would pubkey auth alone have worked? ControlPath=none forces a fresh
# connection rather than riding the master just created, and BatchMode makes it
# fail rather than prompt — so a non-zero exit means no key is installed. This
# tests the condition directly instead of parsing `ssh -v` for its auth method.
if ssh -o BatchMode=yes -o ControlPath=none -o ConnectTimeout=5 "$host" true 2>/dev/null; then
	exit 0
fi

remote_auth_identity "$(ssh -G "$host" 2>/dev/null || true)"
key="$REPLY"
[[ -n $key && -f $key ]] || exit 0
[[ -t 0 ]] || exit 0

printf '\nInstall %s on %s so this is the last time? [y/N] ' "$key" "$host"
read -r reply
[[ $reply == [Yy]* ]] || exit 0

# Rides the master created above, so this needs no second password.
if ! ssh-copy-id -i "$key" "$host"; then
	printf '\nCould not install the key; the connection is still authenticated.\n' >&2
	pause_then_exit 0
fi
printf '\n%s will not ask again.\n' "$host"
pause_then_exit 0
