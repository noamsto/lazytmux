#!/usr/bin/env bash
# Remote `tmux` wrapper installed by the home-manager module as a shell function
# (`tmux() { lztmux-remote-shim "$@"; }`). Detects nested-over-ssh, optionally
# promotes into a local session on the initiating laptop, then execs the real
# tmux. Inert unless the env gate passes and the listener handshake succeeds.
set -uo pipefail

# @lib_remote@ is substituted at Nix build time; in bats the lib is pre-sourced.
# shellcheck source=/dev/null
[[ -f "@lib_remote@" ]] && source "@lib_remote@"

LZTMUX_STATE="${LZTMUX_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/lztmux}"
LZTMUX_SOCK="/tmp/lztmux-outer-${USER}.sock"

shim_memory() { # host -> echoes always|never|"" from the memory file
	local host="$1"
	[[ -f "$LZTMUX_STATE/remote-hosts" ]] || return 0
	sed -n "s/^${host}=//p" "$LZTMUX_STATE/remote-hosts" | head -n1
}

shim_remember() { # host verdict
	mkdir -p "$LZTMUX_STATE"
	sed -i "/^$1=/d" "$LZTMUX_STATE/remote-hosts" 2>/dev/null || true
	printf '%s=%s\n' "$1" "$2" >>"$LZTMUX_STATE/remote-hosts"
}

# Set REPLY to promote|plain. Does NOT do the network handshake — that is the
# main flow's job so shim_decide stays unit-testable.
shim_decide() {
	REPLY="plain"
	remote_env_gate || return 0
	local host="${LZTMUX_HOST:-$(hostname -s)}" mem ans
	mem="$(shim_memory "$host")"
	case "$mem" in
	always)
		REPLY="promote"
		return 0
		;;
	never)
		REPLY="plain"
		return 0
		;;
	esac
	if [[ -n ${LZTMUX_SHIM_ANSWER:-} ]]; then
		ans="$LZTMUX_SHIM_ANSWER"
	else
		printf '[%s] Nested lztmux over ssh. Promote into a local session? [Y/n] (a=always, x=never) ' "$host" >/dev/tty
		read -r ans </dev/tty || ans="n"
	fi
	case "$ans" in
	"" | y | Y) REPLY="promote" ;;
	a | A)
		shim_remember "$host" always
		REPLY="promote"
		;;
	x | X)
		shim_remember "$host" never
		REPLY="plain"
		;;
	*) REPLY="plain" ;;
	esac
	return 0
}

shim_handshake() { # returns 0 iff listener alive and version matches
	command -v socat >/dev/null || return 1
	local resp
	resp="$(printf 'hello %s\n' "$LZTMUX_PROTO_VERSION" | timeout 2 socat - "UNIX-CONNECT:$LZTMUX_SOCK" 2>/dev/null | head -n1)"
	[[ $resp == "ok $LZTMUX_PROTO_VERSION" ]]
}

if [[ -z ${LZTMUX_SHIM_LIB:-} ]]; then
	shim_decide
	if [[ $REPLY == promote ]] && shim_handshake; then
		# Only bare `tmux` / `tmux a[ttach]` promote; anything else runs plain.
		session="default"
		case "${1:-}" in
		"" | a | at | att | atta | attac | attach | attach-session)
			session="${2:-default}"
			printf 'promote %s %s %s\n' "$(hostname -s)" "$session" "$TMUX_PANE" |
				timeout 3 socat - "UNIX-CONNECT:$LZTMUX_SOCK" >/dev/null 2>&1 || true
			;;
		esac
	fi
	exec tmux "$@"
fi
