#!/usr/bin/env bash
# Local listener for remote session promotion. Run as `socat
# UNIX-LISTEN:<sock>,fork,mode=0600 EXEC:lztmux-listener`, so each connection
# gets this process with stdin/stdout wired to the socket. The ONLY component
# that mutates local tmux. Every field is validated before a tmux command runs.
set -uo pipefail

# @lib_remote@ is substituted at Nix build time; in bats the lib is pre-sourced.
# shellcheck source=/dev/null
[[ -f "@lib_remote@" ]] && source "@lib_remote@"

LZTMUX_RATE_FILE="${XDG_RUNTIME_DIR:-/tmp}/lztmux-last-promote"

listener_pane_is_ssh() {
	local cmd
	cmd="$(tmux display-message -p '#{pane_current_command}' -t "$1" 2>/dev/null)"
	[[ $cmd == ssh ]]
}

# Resolve the single client attached to the pane's origin session. Refuse on 0
# or >1 — the listener is not itself a client and cannot know which client
# issued the ssh when several are attached (multi-attach is ambiguous).
listener_resolve_client() {
	REPLY=""
	local sess clients count
	sess="$(tmux display-message -p '#{session_name}' -t "$1" 2>/dev/null)"
	[[ -n $sess ]] || return 1
	clients="$(tmux list-clients -t "$sess" -F '#{client_name}' 2>/dev/null)"
	count="$(grep -c . <<<"$clients")"
	[[ $count -eq 1 ]] || return 1
	REPLY="$clients"
	return 0
}

# Rate limit: refuse a second promote within 2s (blunts flooding).
listener_rate_ok() {
	local now last
	now="$(date +%s)"
	last="$(cat "$LZTMUX_RATE_FILE" 2>/dev/null || echo 0)"
	((now - last >= 2)) || return 1
	echo "$now" >"$LZTMUX_RATE_FILE"
	return 0
}

# Full promote (mutates tmux). Exercised in the integration test (Task 6).
listener_promote() {
	local host="$1" session="$2" pane="$3" target client
	remote_validate_field "$host" || {
		REPLY="refused bad-host"
		return 1
	}
	remote_validate_field "$session" || {
		REPLY="refused bad-session"
		return 1
	}
	remote_validate_pane "$pane" || {
		REPLY="refused bad-pane"
		return 1
	}
	listener_pane_is_ssh "$pane" || {
		REPLY="refused not-ssh-pane"
		return 1
	}
	listener_resolve_client "$pane" || {
		REPLY="refused client-ambiguous"
		return 1
	}
	client="$REPLY"
	listener_rate_ok || {
		REPLY="refused rate-limited"
		return 1
	}
	remote_session_name "$host" "$session"
	target="$REPLY"
	if tmux has-session -t "=$target" 2>/dev/null; then
		tmux switch-client -c "$client" -t "=$target"
		REPLY="ok existing"
		return 0
	fi
	# Isolate the pane into its own window, relocate that window into a fresh
	# session, drop the placeholder, follow the client. Exact incantation is
	# the one item pinned against a live server (Task 6).
	tmux break-pane -d -s "$pane" -n "$session" -t "=$target-tmp" 2>/dev/null || true
	tmux new-session -d -s "=$target" 2>/dev/null || true
	tmux move-window -s "=$target-tmp:" -t "=$target:" 2>/dev/null || true
	tmux switch-client -c "$client" -t "=$target"
	REPLY="ok promoted"
	return 0
}

# Parse one protocol line; set REPLY to the reply. Return non-zero on refusal.
listener_handle_line() {
	local line="$1"
	local -a f
	read -r -a f <<<"$line"
	case "${f[0]:-}" in
	hello)
		if [[ ${f[1]:-} == "$LZTMUX_PROTO_VERSION" ]]; then
			REPLY="ok $LZTMUX_PROTO_VERSION"
			return 0
		fi
		REPLY="incompatible"
		return 0
		;;
	promote)
		listener_promote "${f[1]:-}" "${f[2]:-}" "${f[3]:-}"
		return $?
		;;
	*)
		REPLY="refused unknown-verb"
		return 1
		;;
	esac
}

# socat EXEC main loop: read lines from the connection, reply per line. Skipped
# when sourced for tests.
if [[ -z ${LZTMUX_LISTENER_LIB:-} ]]; then
	while IFS= read -r line; do
		listener_handle_line "$line" || true
		printf '%s\n' "$REPLY"
		[[ $line == promote* ]] && break
	done
fi
