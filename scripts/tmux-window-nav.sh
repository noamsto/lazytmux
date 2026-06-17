#!/usr/bin/env bash
# tmux-window-nav: move the selection up or down a row in the reflowed window grid.
#
# tmux-reflow-windows packs windows into uniform `per`-column rows (per is stamped
# as the session option @window_per), so the window directly below the current one
# sits `per` positions later in window-index order. Down clamps to the last window
# when the column below it is missing; moving past the top or bottom edge is a no-op.
#
# Args (expanded by the key binding): <up|down> <session> <current-index> <per>

dir=$1
session=$2
cur=$3
per=$4

[[ $per =~ ^[0-9]+$ ]] && ((per > 0)) || exit 0

mapfile -t indices < <(tmux list-windows -t "$session" -F '#{window_index}')
total=${#indices[@]}

rank=-1
for i in "${!indices[@]}"; do
	if [[ ${indices[$i]} == "$cur" ]]; then
		rank=$i
		break
	fi
done
((rank < 0)) && exit 0

case "$dir" in
down)
	((rank / per >= (total - 1) / per)) && exit 0 # already on the last row
	target=$((rank + per))
	((target > total - 1)) && target=$((total - 1))
	;;
up)
	target=$((rank - per))
	((target < 0)) && exit 0 # already on the first row
	;;
*)
	exit 0
	;;
esac

tmux select-window -t "$session:${indices[$target]}"
