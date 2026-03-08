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

# Accept session/width as args (from hooks) or fall back to display-message
SESSION=${1:-$(tmux display-message -p '#{session_name}')}
WIDTH=${2:-$(tmux display-message -p '#{client_width}')}
MAX_ICONS=@MAX_ICONS@

# Fast-path: skip if window count + width unchanged since last reflow
cache_key="$(tmux display-message -t "$SESSION" -p '#{session_windows}'):${WIDTH}"
if [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	exit 0
fi

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# --- Single pass: collect window data + pane processes ---
declare -a indices
declare -A win_procs # keyed by window_index, space-separated unique procs
max_text_len=0       # capped at 20, for multi-line padded column width
max_text_len_raw=0   # uncapped, for split-point calculation
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
	((text_len > max_text_len_raw)) && max_text_len_raw=$text_len
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

for idx in "${indices[@]}"; do
	build_proc_icons "${win_procs[$idx]:-}" "$MAX_ICONS"
	icon_str="$REPLY"
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
# Slot base: IDX(idx_width) + ": "(2) + TEXT(P) + " "(1) + ICON(max_icon_width)
# P = max_text_len + 2(zoom) + ellipsis_extra
SEP_WIDTH=3 # " │ "
slot_base_raw=$((max_text_len_raw + 2 + idx_width + 3 + max_icon_width))
slot_base=$((max_text_len + 2 + ellipsis_extra + idx_width + 3 + max_icon_width))

# Single-line check: N items + (N-1) separators
if ((total * slot_base_raw + (total - 1) * SEP_WIDTH + zoom_extra <= available)); then
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

# Execute batched simple commands + early redraw so layout appears immediately
{
	printf '%s\n' "${tmux_cmds[@]}"
	echo "refresh-client -S"
} | tmux source -

# Common format fragments
ICON='#{@window_icon_padded}'
TEXT='#{?#{@branch},#{=30:@branch}#{?#{==:#{=30:@branch},#{@branch}},,…},#{=30:#{b:pane_current_path}}#{?#{==:#{=30:#{b:pane_current_path}},#{b:pane_current_path}},,…}}'
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "

if ((!needs_multiline && current_line == 0)); then
	# Single line: clear ALL session-level format overrides to fall back to global.
	# tmux treats session-level status-format as all-or-nothing: setting any index
	# at session level overrides ALL indices. So we must unset all of them.
	tmux set -u -t "$SESSION" status-format[0] 2>/dev/null || true
	tmux set -u -t "$SESSION" status-format[1] 2>/dev/null || true
	tmux set -u -t "$SESSION" status-format[2] 2>/dev/null || true
	tmux set -u -t "$SESSION" status-format[3] 2>/dev/null || true
elif ((current_line == 0)); then
	# Preserve status-format[0] at session level (all-or-nothing override)
	FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
	[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"
	P=$((max_text_len + 2 + ellipsis_extra))
	TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
	IDX="#{p${idx_width}:window_index}"
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: #{p${P}:${TEXT_Z}} ${ICON},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]#{p${P}:${TEXT_Z}} ${ICON}}#[norange]"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
	tmux set -t "$SESSION" status-format[2] ""
	tmux set -t "$SESSION" status-format[3] ""
else
	# Preserve status-format[0] at session level (all-or-nothing override)
	FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
	[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"

	P=$((max_text_len + 2 + ellipsis_extra))
	TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
	IDX="#{p${idx_width}:window_index}"
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: #{p${P}:${TEXT_Z}} ${ICON},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]#{p${P}:${TEXT_Z}} ${ICON}}#[norange]"

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
