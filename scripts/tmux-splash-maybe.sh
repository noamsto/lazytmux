#!/usr/bin/env bash
# Show the welcome splash once, only on a fresh, empty session.
# Fired (backgrounded) from client-attached / client-session-changed hooks.
# $1 = target session name (#{hook_session}); falls back to the current one.
set -euo pipefail

session="${1:-}"
[ -n "$session" ] || session="$(tmux display-message -p '#{session_name}')"

# Once per session — use explicit if to avoid set -e ambiguity with && exit 0.
if [ "$(tmux show-option -t "$session" -qv @splash_shown)" = "1" ]; then exit 0; fi

# Only a brand-new, single-pane session.
[ "$(tmux display-message -t "$session" -p '#{session_windows}')" = "1" ] || exit 0
[ "$(tmux display-message -t "$session" -p '#{window_panes}')" = "1" ] || exit 0

# Only when the pane is sitting at an interactive shell — never cover a
# tmux-state–restored program/editor.
case "$(tmux display-message -t "$session" -p '#{pane_current_command}')" in
fish | bash | zsh | sh | dash | nu) ;;
*) exit 0 ;;
esac

tmux set-option -t "$session" @splash_shown 1
tmux display-popup -E -B -w 100% -h 100% -t "$session" @tmux_splash@
