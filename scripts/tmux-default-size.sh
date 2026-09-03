#!/usr/bin/env bash
# Keep default-size aligned with the largest real terminal, so detached
# sessions aren't born at tmux's built-in 80x24 (#494).
set -euo pipefail

best_w=0
best_h=0
best_area=0

while IFS='|' read -r control width height; do
	[ "$control" = 1 ] && continue
	[[ $width =~ ^[0-9]+$ ]] || continue
	[[ $height =~ ^[0-9]+$ ]] || continue
	area=$((width * height))
	if [ "$area" -gt "$best_area" ] || { [ "$area" -eq "$best_area" ] && [ "$width" -gt "$best_w" ]; }; then
		best_w=$width
		best_h=$height
		best_area=$area
	fi
done < <(tmux list-clients -F '#{client_control_mode}|#{client_width}|#{client_height}' 2>/dev/null)

[ "$best_w" -gt 0 ] && [ "$best_h" -gt 0 ] || exit 0

[ "$best_w" -lt 80 ] && best_w=80
[ "$best_h" -lt 24 ] && best_h=24

want="${best_w}x${best_h}"
current="$(tmux show-option -gv default-size 2>/dev/null || true)"
[ "$current" = "$want" ] && exit 0

tmux set -g default-size "$want"
