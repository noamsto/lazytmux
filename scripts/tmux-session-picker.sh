#!/usr/bin/env bash
# Session picker — launches the bubbletea TUI in a tmux popup.
set -euo pipefail

BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
tmux display-popup -E -w 90% -h 85% -b rounded -T " Sessions " \
	-S "fg=$BORDER_FG" "@picker_generate@ --tui"
