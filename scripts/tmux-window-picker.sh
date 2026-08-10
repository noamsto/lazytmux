#!/usr/bin/env bash
# Window picker — launches the bubbletea TUI in a tmux popup.
set -euo pipefail

CLIENT=""
if [[ ${1:-} == --client ]]; then
	CLIENT=${2:-}
	shift 2 || shift
fi

ARGS="--tui --windows"
TITLE=" Windows "
if [[ ${1:-} == "--claude" ]]; then
	ARGS="$ARGS --claude"
	TITLE=" 🧠 Claude Windows "
fi

BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
# List-only wants a shorter popup so a full-height list isn't mostly blank;
# a popup can't be resized after creation, so the height is chosen here.
HEIGHT=85%
[[ $(tmux show -gv @picker_layout 2>/dev/null) == list ]] && HEIGHT=60%
# Pin the client: unpinned, tmux re-resolves to the session's most-recently-active
# client, which on a bridged host can be the tty-less control client (#346).
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
tmux display-popup "${POPUP_CLIENT[@]}" -E -w 90% -h "$HEIGHT" -b rounded -T "$TITLE" \
	-S "fg=$BORDER_FG" "@picker_generate@ $ARGS"
