#!/usr/bin/env bash
# tmux-reflow-windows: Compute window layout and set status-format lines
# Handles split points, dynamic padding, icon caching, and status line count (2-4).
# Called by hooks on window add/remove/resize, NOT every status-interval.
#
# Key design: icon and text are separated for alignment.
# Icons have variable char-to-display-width ratios,
# so padding only the text portion (ASCII branch/dir names) gives
# consistent column alignment regardless of icon encoding.

# Accept session/width as args (from hooks) or fall back to display-message
SESSION=${1:-$(tmux display-message -p '#{session_name}')}
WIDTH=${2:-$(tmux display-message -p '#{client_width}')}

# Fast-path: skip if window count + width unchanged since last reflow
cache_key="$(tmux display-message -t "$SESSION" -p '#{session_windows}'):${WIDTH}"
if [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	exit 0
fi

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# Icon map (Nix-generated)
# shellcheck disable=SC2190  # icon map entries are Nix-generated placeholders
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK="@FALLBACK_ICON@"
MAX_ICONS=@MAX_ICONS@

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
	((capped > 20)) && {
		has_truncated=1
		capped=20
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

# --- Build icon strings and track max width ---
max_icon_width=0
declare -A win_icon_str

for idx in "${indices[@]}"; do
	icon=""
	count=0
	# shellcheck disable=SC2086  # intentional word splitting
	for proc in ${win_procs[$idx]:-}; do
		((count >= MAX_ICONS)) && break
		[[ -n $icon ]] && icon+=" "
		icon+="${ICON_MAP[$proc]:-$FALLBACK}"
		((count++)) || true
	done

	# Estimate display width: each icon ~2 cols + spaces between
	icon_width=$((count * 2 + (count > 1 ? count - 1 : 0)))
	((icon_width > max_icon_width)) && max_icon_width=$icon_width

	win_icon_str[$idx]="$icon"
done

# --- Compute split points ---
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
available=$((WIDTH - PREFIX_WIDTH))

zoom_extra=0
((has_zoom)) && zoom_extra=2
ellipsis_extra=0
((has_truncated)) && ellipsis_extra=1
slot_width_raw=$((max_text_len_raw + idx_width + 11 + max_icon_width))
slot_width_capped=$((max_text_len + 2 + ellipsis_extra + idx_width + 11 + max_icon_width))

if ((slot_width_raw * total + zoom_extra <= available)); then
	needs_multiline=0
else
	needs_multiline=1
fi

cumulative=0
current_line=0
split1=999
split2=999
prev_idx=

if ((needs_multiline)); then
	for ((j = 0; j < total; j++)); do
		if ((cumulative + slot_width_capped > available && cumulative > 0)); then
			((current_line++))
			if ((current_line == 1)); then
				split1=$prev_idx
			elif ((current_line == 2)); then
				split2=$prev_idx
				break
			fi
			cumulative=$slot_width_capped
		else
			cumulative=$((cumulative + slot_width_capped))
		fi
		prev_idx=${indices[$j]}
	done
fi

# --- Batch simple commands via tmux source, direct calls for complex formats ---
declare -a tmux_cmds=()

# Set icon display per window
for idx in "${indices[@]}"; do
	tmux_cmds+=("set -w -t '${SESSION}:${idx}' @window_icon_display '${win_icon_str[$idx]}'")
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

# Preserve status-format[0] at session level (contains single quotes, can't batch)
FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"

# Common format fragments
ICON='#{@window_icon_display}'
TEXT='#{?#{@branch},#{=20:@branch}#{?#{==:#{=20:@branch},#{@branch}},,…},#{=20:#{b:pane_current_path}}#{?#{==:#{=20:#{b:pane_current_path}},#{b:pane_current_path}},,…}}'
CLAUDE='#(@claude_status_bin@ --window '"'"'#{session_name}:#{window_index}'"'"')'
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "

if ((!needs_multiline && current_line == 0)); then
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]#{window_index}: #{window_name},#[fg=#{@thm_subtext_0}#,nobold]#{window_index}: #[fg=#{@thm_fg}]#{window_name}}#{?window_zoomed_flag, 󰁌,}${CLAUDE}#[norange]"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
	tmux set -t "$SESSION" status-format[2] ""
	tmux set -t "$SESSION" status-format[3] ""
elif ((current_line == 0)); then
	P=$((max_text_len + 2 + ellipsis_extra))
	TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
	IDX="#{p${idx_width}:window_index}"
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: ${ICON} #{p${P}:${TEXT_Z}},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]${ICON} #{p${P}:${TEXT_Z}}}${CLAUDE}#[norange]"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
	tmux set -t "$SESSION" status-format[2] ""
	tmux set -t "$SESSION" status-format[3] ""
else
	P=$((max_text_len + 2 + ellipsis_extra))
	TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
	IDX="#{p${idx_width}:window_index}"
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: ${ICON} #{p${P}:${TEXT_Z}},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]${ICON} #{p${P}:${TEXT_Z}}}${CLAUDE}#[norange]"

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
