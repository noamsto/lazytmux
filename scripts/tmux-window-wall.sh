#!/usr/bin/env bash
# Window wall — launches the bubbletea TUI in wall mode, in a tmux popup.
set -euo pipefail

CLIENT=""
if [[ ${1:-} == --client ]]; then
	CLIENT=${2:-}
	shift 2 || shift
fi

BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
# Fixed geometry: the wall is its own shape (a tiled grid of live previews),
# not the list/preview split @picker_layout governs, and a popup can't be
# resized after creation.
# Pin the client: unpinned, tmux re-resolves to the session's most-recently-active
# client, which on a bridged host can be the tty-less control client (#346,
# reported upstream as tmux/tmux#5551 — drop the pin once that ships).
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
tmux display-popup "${POPUP_CLIENT[@]}" -E -w 95% -h 90% -b rounded -T " Wall " \
	-S "fg=$BORDER_FG" "@picker_generate@ --tui --windows --wall"
