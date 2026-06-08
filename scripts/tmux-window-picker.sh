#!/usr/bin/env bash
# Window picker — launches the bubbletea TUI in a tmux popup.
set -euo pipefail

ARGS="--tui --windows"
TITLE=" Windows "
if [[ ${1:-} == "--claude" ]]; then
	ARGS="$ARGS --claude"
	TITLE=" 🧠 Claude Windows "
fi

BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
tmux display-popup -E -w 90% -h 85% -b rounded -T "$TITLE" \
	-S "fg=$BORDER_FG" "@picker_generate@ $ARGS"
