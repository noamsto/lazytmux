#!/usr/bin/env bash
# tmux-reflow-windows: Compute window layout and set status-format lines
# Handles split points, dynamic padding, icon caching, and status line count (2-4).
# Called by hooks on window add/remove/resize, NOT every status-interval.
#
# Key design: icon and text are separated for alignment.
# Icons have variable char-to-display-width ratios,
# so padding only the text portion (ASCII branch/dir names) gives
# consistent column alignment regardless of icon encoding.

# shellcheck source=/dev/null  # Nix store path substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_enrich@

# Accept session/width as args (from hooks) or fall back to display-message
SESSION=${1:-$(tmux display-message -p '#{session_name}')}
WIDTH=${2:-$(tmux display-message -p '#{client_width}')}
MAX_ICONS=@MAX_ICONS@

# Scratch sessions manage their own status bar (hints bar); skip reflow.
case "$SESSION" in
scratch-*) exit 0 ;;
esac

# Fast-path: skip if window count + width unchanged since last reflow
cache_key="$(tmux display-message -t "$SESSION" -p '#{session_windows}'):${WIDTH}"
if [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	exit 0
fi

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# --- Single pass: collect window data + pane processes ---
declare -a indices
declare -A win_procs    # keyed by window_index, space-separated unique procs
declare -A win_text_len # per-window text length (for single-line total)
max_text_len=0          # capped at 30, for multi-line padded column width
total=0
has_zoom=0
has_truncated=0

FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}'
while IFS='|' read -r idx branch pane_path zoomed; do
	indices+=("$idx")
	((zoomed)) && has_zoom=1

	if [[ -n $branch ]]; then
		text_len=${#branch}
	else
		dirname=${pane_path##*/}
		text_len=${#dirname}
	fi
	win_text_len[$idx]=$text_len
	capped=$text_len
	((capped > 30)) && {
		has_truncated=1
		capped=30
	}
	((capped > max_text_len)) && max_text_len=$capped
	((total++))
done < <(tmux list-windows -t "$SESSION" -F "$FMT")

[[ $total -eq 0 ]] && exit 0

# Collect all pane processes in one call, bucket by window index
declare -A win_seen # keyed by "idx:proc"
while IFS=$'\t' read -r win_idx proc; do
	[[ -n $proc ]] || continue
	if [[ -z ${win_seen["${win_idx}:${proc}"]+x} ]]; then
		win_seen["${win_idx}:${proc}"]=1
		win_procs[$win_idx]+="${win_procs[$win_idx]:+ }$proc"
	fi
done < <(tmux list-panes -s -t "$SESSION" -F '#{window_index}	#{pane_current_command}')
unset win_seen

# --- Build icon strings with fixed-width padding (stable layout across icon changes) ---
# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd font claude (2 cells)
max_icon_width=$((MAX_ICONS * 3 + 2))
declare -A win_icon_str
# Sum actual single-line width: per-window text + icon + separators
# Single-line format: IDX(idx_width) + ": "(2) + text + " "(1) + unpadded_icon
# No column padding — each window takes only its natural width.
total_single_width=0

for idx in "${indices[@]}"; do
	build_proc_icons "${win_procs[$idx]:-}" "$MAX_ICONS"
	icon_str="$REPLY"
	((total_single_width += ${win_text_len[$idx]} + REPLY_DW))
	pad_to_width "$icon_str" "$REPLY_DW" "$max_icon_width"
	win_icon_str[$idx]="$REPLY"
done

# --- Compute split points ---
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
available=$((WIDTH - PREFIX_WIDTH))

zoom_extra=0
((has_zoom)) && zoom_extra=2
ellipsis_extra=0
((has_truncated)) && ellipsis_extra=1
SEP_WIDTH=3 # " │ "
# Single-line total: sum of per-window (text + icon) + fixed overhead per window + separators
# Fixed per window: idx_width + ": "(2) + " "(1) = idx_width + 3
total_single_width=$((total_single_width + total * (idx_width + 3) + (total - 1) * SEP_WIDTH))
# Multi-line uses padded columns for alignment
# P = max_text_len + 2(zoom) + ellipsis_extra
slot_base=$((max_text_len + 2 + ellipsis_extra + idx_width + 3 + max_icon_width))

# Single-line check: actual total width + zoom
if ((total_single_width + zoom_extra <= available)); then
	needs_multiline=0
else
	needs_multiline=1
fi

current_line=0
split1=999
split2=999

if ((needs_multiline)); then
	# Greedy fill: slot_base per item + SEP_WIDTH between items
	cumulative=0
	prev_idx=
	for ((j = 0; j < total; j++)); do
		if ((cumulative == 0)); then
			# First item on line
			cumulative=$slot_base
		elif ((cumulative + SEP_WIDTH + slot_base > available)); then
			# Would overflow: start new line
			((current_line++))
			if ((current_line == 1)); then
				split1=$prev_idx
			elif ((current_line == 2)); then
				split2=$prev_idx
				break
			fi
			cumulative=$slot_base
		else
			cumulative=$((cumulative + SEP_WIDTH + slot_base))
		fi
		prev_idx=${indices[$j]}
	done
fi

# --- Batch simple commands via tmux source, direct calls for complex formats ---
declare -a tmux_cmds=()

# Set padded icon display per window (separate from @window_icon_display which is unpadded)
for idx in "${indices[@]}"; do
	tmux_cmds+=("set -w -t '${SESSION}:${idx}' @window_icon_padded '${win_icon_str[$idx]}'")
done

# Split points and status line count
tmux_cmds+=("set -t '$SESSION' @window_split '$split1'")
tmux_cmds+=("set -t '$SESSION' @window_split2 '$split2'")
tmux_cmds+=("set -t '$SESSION' @reflow_key '$cache_key'")

if ((current_line >= 2)); then
	tmux_cmds+=("set -t '$SESSION' status 4")
elif ((current_line >= 1)); then
	tmux_cmds+=("set -t '$SESSION' status 3")
else
	tmux_cmds+=("set -t '$SESSION' status 2")
fi

# Single-line branch collapses into the same batch as an atomic unset of the
# whole session-level status-format array. Doing this as one command matters:
# tmux treats session-level status-format as all-or-nothing (any set index
# suppresses global for ALL indices), so unsetting [0], [1], [2], [3] in
# separate tmux calls creates a visible intermediate where [0] is unset but
# [1..3] are still set — line 0 renders blank and the session name flashes
# away. The per-index unsets that used to live here are redundant with the
# bare `status-format` unset below.
if ((!needs_multiline && current_line == 0)); then
	tmux_cmds+=("set -u -t '$SESSION' status-format")
fi

# Execute batched simple commands + early redraw so layout appears immediately
{
	printf '%s\n' "${tmux_cmds[@]}"
	echo "refresh-client -S"
} | tmux source -

# Common format fragments
ICON='#{@window_icon_padded}'
TEXT='#{?#{@branch},#{=30:@branch}#{?#{==:#{=30:@branch},#{@branch}},,…},#{=30:#{b:pane_current_path}}#{?#{==:#{=30:#{b:pane_current_path}},#{b:pane_current_path}},,…}}'
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "

# Multi-line format fragments
P=$((max_text_len + 2 + ellipsis_extra))
TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
IDX="#{p${idx_width}:window_index}"
ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: #{p${P}:${TEXT_Z}} ${ICON},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]#{p${P}:${TEXT_Z}} ${ICON}}#[norange]"

# Multi-line branches stay on direct `tmux set` calls: FMT0 contains embedded
# single quotes (e.g. '#{session_name}') that break outer-single-quoted
# batched commands. See commit 60421e7.
if ((!needs_multiline && current_line == 0)); then
	: # handled via batched unset above
elif ((current_line == 0)); then
	FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
	[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
	tmux set -t "$SESSION" status-format[2] ""
	tmux set -t "$SESSION" status-format[3] ""
else
	FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
	[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"
	PREFIX1="#{?#{e|>|:#{session_windows},#{@window_split}},├,╰}─"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ${PREFIX1} #{W:#{?#{e|<=|:#{window_index},#{@window_split}},${ENTRY}#{?window_end_flag,,#{?#{e|==|:#{window_index},#{@window_split}},,${SEP}}},}}"

	PREFIX2="#{?#{e|>|:#{session_windows},#{@window_split2}},├,╰}─"
	tmux set -t "$SESSION" status-format[2] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ${PREFIX2} #{W:#{?#{e|>|:#{window_index},#{@window_split}},#{?#{e|<=|:#{window_index},#{@window_split2}},${ENTRY}#{?window_end_flag,,#{?#{e|==|:#{window_index},#{@window_split2}},,${SEP}}},},}}"

	tmux set -t "$SESSION" status-format[3] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:#{?#{e|>|:#{window_index},#{@window_split2}},${ENTRY}#{?window_end_flag,,${SEP}},}}"
fi

# Force immediate status bar redraw
tmux refresh-client -S 2>/dev/null || true
