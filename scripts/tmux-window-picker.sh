#!/usr/bin/env bash
# Window picker — launches the bubbletea TUI in a tmux popup.
set -euo pipefail

ARGS="--tui --windows"
TITLE=" Windows "
if [[ ${1:-} == "--agent" ]]; then
	ARGS="$ARGS --agent"
	TITLE=" Agent Windows "
fi

BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
# List-only wants a shorter popup so a full-height list isn't mostly blank;
# a popup can't be resized after creation, so the height is chosen here.
HEIGHT=85%
[[ $(tmux show -gv @picker_layout 2>/dev/null) == list ]] && HEIGHT=60%
tmux display-popup -E -w 90% -h "$HEIGHT" -b rounded -T "$TITLE" \
	-S "fg=$BORDER_FG" "@picker_generate@ $ARGS"
