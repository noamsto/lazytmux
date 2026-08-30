#!/usr/bin/env bash
# Session picker — launches the bubbletea TUI in a tmux popup.
set -euo pipefail

CLIENT=""
if [[ ${1:-} == --client ]]; then
	CLIENT=${2:-}
	shift 2 || shift
fi

# Both options in one round-trip: the popup's open latency is dominated by
# forks queued behind the (single-threaded) tmux server, and these run before
# anything paints. Both are only ever `set -g`, so resolving them through the
# option chain rather than `show -gv` picks the same values.
OPTS=$(tmux display -p '#{@thm_overlay_1}|#{@picker_layout}' 2>/dev/null || true)
BORDER_FG=${OPTS%%|*}
[[ -n $BORDER_FG ]] || BORDER_FG="#7f849c"
# List-only wants a shorter popup so a full-height list isn't mostly blank;
# a popup can't be resized after creation, so the height is chosen here.
HEIGHT=85%
[[ ${OPTS#*|} == list ]] && HEIGHT=60%
# Pin the client: unpinned, tmux re-resolves to the session's most-recently-active
# client, which on a bridged host can be the tty-less control client (#346).
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
tmux display-popup "${POPUP_CLIENT[@]}" -E -w 90% -h "$HEIGHT" -b rounded -T " Sessions " \
	-S "fg=$BORDER_FG" "@picker_generate@ --tui"
