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

shim_memory() { # host -> sets REPLY to always|never|"" from the memory file
	REPLY=""
	local host="$1" h v
	[[ -f "$LZTMUX_STATE/remote-hosts" ]] || return 0
	while IFS='=' read -r h v; do
		[[ $h == "$host" ]] && {
			REPLY="$v"
			return 0
		}
	done <"$LZTMUX_STATE/remote-hosts"
	return 0
}

shim_remember() { # host verdict
	local host="$1" verdict="$2" h v
	mkdir -p "$LZTMUX_STATE"
	local tmp="$LZTMUX_STATE/remote-hosts.tmp"
	: >"$tmp"
	if [[ -f "$LZTMUX_STATE/remote-hosts" ]]; then
		while IFS='=' read -r h v; do
			[[ $h == "$host" ]] && continue
			printf '%s=%s\n' "$h" "$v" >>"$tmp"
		done <"$LZTMUX_STATE/remote-hosts"
	fi
	printf '%s=%s\n' "$host" "$verdict" >>"$tmp"
	mv "$tmp" "$LZTMUX_STATE/remote-hosts"
}

# Pure argv classifier. Set REPLY to the target session name and return 0 when
# the argv is a promotable form (bare `tmux`, or an attach verb optionally with
# `-t <name>`); return non-zero for anything else (new-session, ls, -V, ...).
shim_target_session() {
	REPLY="default"
	case "${1:-}" in
	"") return 0 ;;
	a | at | att | atta | attac | attach | attach-session) ;;
	*) return 1 ;;
	esac
	shift
	while [[ $# -gt 0 ]]; do
		if [[ $1 == -t ]]; then
			REPLY="${2:-default}"
			return 0
		fi
		shift
	done
	return 0
}

# Set REPLY to promote|plain. Does NOT do the network handshake — that is the
# main flow's job so shim_decide stays unit-testable.
shim_decide() {
	REPLY="plain"
	remote_env_gate || return 0
	local host="${LZTMUX_HOST:-$(hostname -s)}" mem ans
	shim_memory "$host"
	mem="$REPLY"
	REPLY="plain"
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
	# Only bare `tmux` / `tmux a[ttach]` promote. shim_decide (and its blocking
	# prompt) must not run for `tmux ls`, `tmux -V`, `tmux new-session`, etc.
	if shim_target_session "$@"; then
		session="$REPLY"
		host="${LZTMUX_HOST:-$(hostname -s)}"
		shim_decide
		if [[ $REPLY == promote ]] && shim_handshake; then
			printf 'promote %s %s %s\n' "$host" "$session" "$TMUX_PANE" |
				timeout 3 socat - "UNIX-CONNECT:$LZTMUX_SOCK" >/dev/null 2>&1 || true
		fi
	fi
	exec tmux "$@"
fi
