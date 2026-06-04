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

# Accept --force (cache bypass, used by enrich scripts after writing vars).
FORCE=0
pos=()
for a in "$@"; do
	if [[ $a == --force ]]; then
		FORCE=1
	else
		pos+=("$a")
	fi
done
set -- "${pos[@]}"

# Accept session/width as args (from hooks) or fall back to display-message
SESSION=${1:-$(tmux display-message -p '#{session_name}')}
WIDTH=${2:-$(tmux display-message -p '#{client_width}')}
MAX_ICONS=@MAX_ICONS@

# Scratch sessions manage their own status bar (hints bar); skip reflow.
case "$SESSION" in
scratch-*) exit 0 ;;
esac

# Fast-path: skip if window count + width + active window unchanged since last
# reflow. Active window is in the key so focus changes re-pack multi-line active
# mode (where the active tab's long width shifts split points).
active_win=$(tmux display-message -t "$SESSION" -p '#{window_index}')
cache_key="$(tmux display-message -t "$SESSION" -p '#{session_windows}'):${WIDTH}:${active_win}"
if ((!FORCE)) && [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	exit 0
fi

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# --- Single pass: collect window data + pane processes ---
declare -a indices
declare -A win_procs # keyed by window_index, space-separated unique procs
total=0
has_zoom=0

FMT='#{window_index}|#{window_active}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}|#{@issue_provider}|#{@issue_id}|#{@issue_title}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}'
declare -A win_short win_long win_short_dw win_long_dw
active_idx=""
while IFS='|' read -r idx wactive branch pane_path zoomed iprov iid ititle prnum prstate prcheck; do
	indices+=("$idx")
	((zoomed)) && has_zoom=1
	((wactive)) && active_idx="$idx"

	build_window_label short "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path"
	win_short[$idx]="$REPLY"
	measure_display_width "$REPLY"
	win_short_dw[$idx]=$REPLY_DW

	build_window_label long "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path"
	win_long[$idx]="$REPLY"
	measure_display_width "$REPLY"
	win_long_dw[$idx]=$REPLY_DW

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

# --- Build icon strings with fixed-width padding (stable across icon changes) ---
max_icon_width=$((MAX_ICONS * 3 + 2))
declare -A win_icon_str
for idx in "${indices[@]}"; do
	build_proc_icons "${win_procs[$idx]:-}" "$MAX_ICONS"
	pad_to_width "$REPLY" "$REPLY_DW" "$max_icon_width"
	win_icon_str[$idx]="$REPLY"
done

# --- Per-window slot widths ---
# Slot = idx_width + ": "(2) + label + " "(1) + icon column.
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
overhead=$((idx_width + 3 + max_icon_width)) # ": " + trailing space + icons
declare -A short_slot long_slot
for idx in "${indices[@]}"; do
	short_slot[$idx]=$((win_short_dw[$idx] + overhead))
	long_slot[$idx]=$((win_long_dw[$idx] + overhead))
done

available=$((WIDTH - PREFIX_WIDTH))
zoom_extra=0
((has_zoom)) && zoom_extra=2
SEP_WIDTH=3 # " │ "
MAX_WIN_LINES=3

# pack_count NAME-of-slot-array  → sets REPLY to the line count needed (1..N).
# Greedy first-fit; SEP between items on a line.
pack_count() {
	# shellcheck disable=SC2178
	local -n _slot=$1
	local lines=1 cur=0 first=1
	local idx w
	for idx in "${indices[@]}"; do
		w=${_slot[$idx]}
		if ((first)); then
			cur=$w
			first=0
		elif ((cur + SEP_WIDTH + w > available)); then
			((lines++))
			cur=$w
		else
			cur=$((cur + SEP_WIDTH + w))
		fi
	done
	REPLY=$lines
}

# Mode decision: all-long if it fits the allowed lines, else active.
pack_count long_slot
long_lines=$REPLY
if ((long_lines <= MAX_WIN_LINES)); then
	labels_mode=long
else
	labels_mode=active
fi

# Chosen slot per window for the actual packing.
declare -A chosen_slot
for idx in "${indices[@]}"; do
	if [[ $labels_mode == long ]] || [[ $idx == "$active_idx" ]]; then
		chosen_slot[$idx]=${long_slot[$idx]}
	else
		chosen_slot[$idx]=${short_slot[$idx]}
	fi
done

# Single-line check using chosen widths.
total_single=0
for idx in "${indices[@]}"; do
	((total_single += chosen_slot[$idx]))
done
total_single=$((total_single + (total - 1) * SEP_WIDTH))

needs_multiline=0
((total_single + zoom_extra > available)) && needs_multiline=1

# Greedy split points using chosen widths.
current_line=0
split1=999
split2=999
if ((needs_multiline)); then
	cumulative=0
	prev_idx=
	for ((j = 0; j < total; j++)); do
		idx=${indices[$j]}
		w=${chosen_slot[$idx]}
		if ((cumulative == 0)); then
			cumulative=$w
		elif ((cumulative + SEP_WIDTH + w > available)); then
			((current_line++))
			if ((current_line == 1)); then
				split1=$prev_idx
			elif ((current_line == 2)); then
				split2=$prev_idx
				break
			fi
			cumulative=$w
		else
			cumulative=$((cumulative + SEP_WIDTH + w))
		fi
		prev_idx=$idx
	done
fi

# --- Batch simple commands via tmux source, direct calls for complex formats ---
declare -a tmux_cmds=()

# Per-window vars set directly (not via `tmux source -`): labels carry free-form
# issue titles whose quotes/';'/'#' would break the batched command parser.
for idx in "${indices[@]}"; do
	target="${SESSION}:${idx}"
	tmux set -w -t "$target" @window_icon_padded "${win_icon_str[$idx]}"
	tmux set -w -t "$target" @window_label_short "${win_short[$idx]}"
	tmux set -w -t "$target" @window_label_long "${win_long[$idx]}"
done

# Split points and status line count
tmux_cmds+=("set -t '$SESSION' @window_split '$split1'")
tmux_cmds+=("set -t '$SESSION' @window_split2 '$split2'")
tmux_cmds+=("set -t '$SESSION' @reflow_key '$cache_key'")
tmux_cmds+=("set -t '$SESSION' @labels_mode '${labels_mode}'")

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
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "
ICON='#{@window_icon_padded}'
# Live label selection: long mode → long; active mode → long iff active else short.
LABEL='#{?#{==:#{@labels_mode},long},#{@window_label_long},#{?window_active,#{@window_label_long},#{@window_label_short}}}'
LABEL_Z="${LABEL}#{?window_zoomed_flag, 󰁌,}"
IDX="#{p${idx_width}:window_index}"
ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: ${LABEL_Z} ${ICON},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]${LABEL_Z} ${ICON}}#[norange]"

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
