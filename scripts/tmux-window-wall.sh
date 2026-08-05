#!/usr/bin/env bash
# Window wall — launches the bubbletea TUI in wall mode, in a tmux popup.
set -euo pipefail

BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
# Fixed geometry: the wall is its own shape (a tiled grid of live previews),
# not the list/preview split @picker_layout governs, and a popup can't be
# resized after creation.
tmux display-popup -E -w 95% -h 90% -b rounded -T " Wall " \
	-S "fg=$BORDER_FG" "@picker_generate@ --tui --windows --wall"
