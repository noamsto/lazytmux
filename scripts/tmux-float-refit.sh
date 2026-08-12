#!/usr/bin/env bash
# Reassert each floating pane's declared geometry after its window resized.
#
# tmux resolves new-pane's -x/-y/-X/-Y percentages once, at creation, into
# absolute cells: a floating cell keeps only {sx,sy,xoff,yoff}, and
# layout_resize() skips floating cells outright. So a float sized on one client
# overflows a smaller one, stays small on a larger one, and is never clamped
# back on screen either. Upstream calls the behaviour undecided (tmux/tmux#5135
# wants "resize hints" as part of a wider layout rework), so the creation
# percentages are stamped into @float_geom and reapplied here.
#   args: <target-window>   (the window-resized hook passes #{window_id})
# Both commands re-derive their percentages against the window's current size,
# and each no-ops when the value is unchanged, so this never churns a redraw.
set -uo pipefail

target=${1:-}
[[ -z $target ]] && exit 0

while IFS=$'\t' read -r pane geom; do
	read -r width height xoff yoff <<<"$geom"
	# Floats created outside the binds (a mouse Ctrl-drag) carry no stamp:
	# their geometry is the user's, not ours, so leave them alone.
	[[ -n $yoff ]] || continue
	tmux resize-pane -t "$pane" -x "$width" -y "$height"
	tmux move-pane -t "$pane" -X "$xoff" -Y "$yoff"
done < <(tmux list-panes -t "$target" -f '#{pane_floating_flag}' \
	-F $'#{pane_id}\t#{@float_geom}' 2>/dev/null)
