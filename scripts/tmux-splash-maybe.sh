#!/usr/bin/env bash
# Show the welcome splash once, only on a fresh, empty session.
# Fired (backgrounded) from client-attached / client-session-changed hooks.
# $1 = target session name (#{hook_session_name}); falls back to the current
# one. $2 = invoking client name (#{hook_client}).
set -euo pipefail

session="${1:-}"
[ -n "$session" ] || session="$(tmux display-message -p '#{session_name}')"

# Once per tmux server — a global flag, so only the first fresh session after
# server start shows it (not every new session, nor on session switch).
if [ "$(tmux show-option -gqv @splash_shown)" = "1" ]; then exit 0; fi

# The remote bridge attaches a -CC control-mode client. It has no tty, so a
# second popup on it dereferences NULL inside tmux and takes the whole server
# down (#346, upstream tmux/tmux#5551) — and a mirror has no business showing
# a splash. Deliberately without setting @splash_shown: a later real attach
# must still get it.
client="${2:-}"
control=""
while read -r mode name; do
	[ "$name" = "$client" ] && control="$mode" || true
done < <(tmux list-clients -t "$session" -F '#{client_control_mode} #{client_name}' 2>/dev/null)
[ -n "$control" ] || exit 1
[ "$control" = 1 ] && exit 0

# Only a brand-new, single-pane session.
[ "$(tmux display-message -t "$session" -p '#{session_windows}')" = "1" ] || exit 0
[ "$(tmux display-message -t "$session" -p '#{window_panes}')" = "1" ] || exit 0

# Only when the pane is sitting at an interactive shell — never cover a
# tmux-remux–restored program/editor.
case "$(tmux display-message -t "$session" -p '#{pane_current_command}')" in
fish | bash | zsh | sh | dash | nu) ;;
*) exit 0 ;;
esac

# SSH_CONNECTION is attach-scoped: tmux's update-environment copies it from
# the attaching client into the SESSION's environment table at attach time
# (this repo only ever appends to that default list, never replaces it — see
# `set -ga update-environment` in config/tmux.conf.nix), so it's readable here
# even though this hook itself runs server-side, not in the client's shell.
# A client-session-changed firing reflects whichever client most recently
# attached, not necessarily the one that triggered this particular call.
# show-environment exits 0 both when the var is set (`NAME=value`) and when
# it was never set by the attaching client (tmux prints a `-NAME` removal
# marker) — so the exit code alone can't distinguish remote from local; the
# value must be inspected.
is_remote_attach() {
	local v
	v="$(tmux show-environment -t "$session" SSH_CONNECTION 2>/dev/null)" || return 1
	case "$v" in
	SSH_CONNECTION=?*) return 0 ;;
	*) return 1 ;;
	esac
}

# Baked in at build time from programs.lazytmux.splash.remote (skip|static|full).
if is_remote_attach; then
	# shellcheck disable=SC2194 # @splash_remote@ is a Nix build-time placeholder
	case "@splash_remote@" in
	skip)
		# Don't set @splash_shown: a later local attach on this same server
		# should still get the splash it never got over ssh.
		exit 0
		;;
	static)
		tmux set-option -g @splash_shown 1
		tmux display-popup -E -B -w 100% -h 100% -t "$session" -c "$client" @tmux_splash@ --static
		exit 0
		;;
	esac
fi

tmux set-option -g @splash_shown 1
tmux display-popup -E -B -w 100% -h 100% -t "$session" -c "$client" @tmux_splash@
