#!/usr/bin/env bash
# Smart tmux↔kitty navigation. Called from the C-hjkl bindings (vim-tmux-navigator
# style). At a tmux edge inside kitty, hand focus to the neighbouring kitty window
# (e.g. the aeye carousel); otherwise move within tmux. Self-gates on
# KITTY_LISTEN_ON so non-kitty / tmux-split users get plain select-pane.
#   args: <select-pane-flag L|D|U|R> <kitty-dir left|down|up|right> <zoomed 0|1> <at_edge 0|1>
set -u
flag=$1 dir=$2 zoomed=$3 edge=$4
[ "$zoomed" = 1 ] && exit 0
if [ "$edge" = 1 ] && [ -n "${KITTY_LISTEN_ON:-}" ] && command -v kitty >/dev/null 2>&1; then
	kitty @ action neighboring_window "$dir" 2>/dev/null && exit 0
fi
tmux select-pane -"$flag"
