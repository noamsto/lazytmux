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

# Fast-path: skip if window count + width unchanged since last reflow. Layout
# (detail mode + column width) depends only on the window set + width, not on
# which window is active — focus only changes the active tab's color, which tmux
# re-renders on its own without a reflow.
cache_key="$(tmux display-message -t "$SESSION" -p '#{session_windows}'):${WIDTH}"
if ((!FORCE)) && [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	exit 0
fi

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# --- Single pass: collect window data + pane processes ---
declare -a indices
declare -A win_procs # keyed by window_index, space-separated unique procs
total=0
has_zoom=0

FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}|#{@issue_provider}|#{@issue_id}|#{@issue_title}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}'
declare -A win_short win_short_dw win_long_dw
declare -A win_id win_id_dw win_rest_short win_rest_long win_pr win_pr_dw
pr_colw=0 # widest PR segment → shared PR column width (0 when no window has a PR)
while IFS='|' read -r idx branch pane_path zoomed iprov iid ititle prnum prstate prcheck prmerge; do
	indices+=("$idx")
	((zoomed)) && has_zoom=1

	build_window_label short "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path" "$prmerge"
	win_short[$idx]="$REPLY"
	win_id[$idx]="$REPLY_ID"
	win_rest_short[$idx]="$REPLY_REST"
	# shellcheck disable=SC2153 # REPLY_PR set by build_window_label (sourced lib)
	win_pr[$idx]="$REPLY_PR"
	measure_display_width "$REPLY"
	win_short_dw[$idx]=$REPLY_DW
	measure_display_width "$REPLY_ID"
	win_id_dw[$idx]=$REPLY_DW
	measure_display_width "${win_pr[$idx]}"
	win_pr_dw[$idx]=$REPLY_DW
	((win_pr_dw[$idx] > pr_colw)) && pr_colw=${win_pr_dw[$idx]}

	# Long mode only changes the remainder (title / full branch); the id and PR
	# segments are mode-independent.
	build_window_label long "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path" "$prmerge"
	win_rest_long[$idx]="$REPLY_REST"
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

# --- Layout: pick label detail (long/short) + column width, then pack ---
# Slot = idx_width + ": "(2) + name + pr + " "(1) + icon column.
# The PR segment carries its own leading space, so no PR adds nothing.
# The shared pr column (pr_colw) is only charged in the multi-line uniform
# slot; single-line entries are unpadded, so the one-row fit charges each
# window its own PR width — otherwise one window growing a PR inflates the
# fit test by pr_colw × window count and flips to compact despite free space.
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
slot_overhead=$((idx_width + 3 + max_icon_width)) # ": " + trailing space + icons
overhead=$((slot_overhead + pr_colw))             # + shared pr column (multi-line)

available=$((WIDTH - PREFIX_WIDTH))
zoom_extra=0
((has_zoom)) && zoom_extra=2
SEP_WIDTH=3 # " │ "
MAX_WIN_LINES=3

# Aggregate widths for the long and short label variants.
colw_long=0
colw_short=0
total_long=0
total_short=0
for idx in "${indices[@]}"; do
	((win_long_dw[$idx] > colw_long)) && colw_long=${win_long_dw[$idx]}
	((win_short_dw[$idx] > colw_short)) && colw_short=${win_short_dw[$idx]}
	((total_long += win_long_dw[$idx] + slot_overhead + win_pr_dw[$idx]))
	((total_short += win_short_dw[$idx] + slot_overhead + win_pr_dw[$idx]))
done
total_long=$((total_long + (total - 1) * SEP_WIDTH))
total_short=$((total_short + (total - 1) * SEP_WIDTH))

# lines_for_colw CW: rows needed to pack uniform CW-wide columns.
# Sets REPLY=rows, REPLY_PER=columns per row.
lines_for_colw() {
	local cw=$1 slot per
	slot=$((cw + overhead))
	per=$(((available + SEP_WIDTH) / (slot + SEP_WIDTH)))
	((per < 1)) && per=1
	REPLY_PER=$per
	REPLY=$(((total + per - 1) / per))
}

# Detail ladder: long on one row → long within MAX rows → short (compact id) on
# one row → short within MAX rows → short truncated to fit. Compact is preferred
# over truncating; truncation is the last resort.
labels_mode=long
colw=$colw_long
trunc=0
needs_multiline=1
if ((total_long + zoom_extra <= available)); then
	labels_mode=long
	colw=$colw_long
	needs_multiline=0
else
	lines_for_colw "$colw_long"
	if ((REPLY <= MAX_WIN_LINES)); then
		labels_mode=long
		colw=$colw_long
	elif ((total_short + zoom_extra <= available)); then
		labels_mode=short
		colw=$colw_short
		needs_multiline=0
	else
		labels_mode=short
		colw=$colw_short
		lines_for_colw "$colw_short"
		if ((REPLY > MAX_WIN_LINES)); then
			# even compact ids overflow the rows → truncate to fit
			trunc=1
			cols=$(((total + MAX_WIN_LINES - 1) / MAX_WIN_LINES))
			colw=$(((available - (cols - 1) * SEP_WIDTH) / cols - overhead))
			((colw < 6)) && colw=6
			((colw > colw_short)) && colw=$colw_short
		fi
	fi
fi

# Resolved display segments per window. The name column is rendered as
# bold(@window_label_id) + @window_label_disp, so the rest segment alone is
# truncated/padded against the column budget left after the (never-truncated)
# identity prefix: id + rest fills colw exactly. The PR segment is padded to
# its own shared column. Single-line mode uses the unpadded live-select
# options instead.
declare -A win_disp win_pr_disp
for idx in "${indices[@]}"; do
	if [[ $labels_mode == long ]]; then
		cur_rest="${win_rest_long[$idx]}"
	else
		cur_rest="${win_rest_short[$idx]}"
	fi
	rest_avail=$((colw - win_id_dw[$idx]))
	((rest_avail < 0)) && rest_avail=0
	if ((rest_avail == 0)); then
		cur_rest=""
	elif ((trunc)); then
		truncate_to_width "$cur_rest" "$rest_avail"
		cur_rest="$REPLY"
	fi
	measure_display_width "$cur_rest"
	pad_to_width "$cur_rest" "$REPLY_DW" "$rest_avail"
	win_disp[$idx]="$REPLY"

	pad_to_width "${win_pr[$idx]}" "${win_pr_dw[$idx]}" "$pr_colw"
	win_pr_disp[$idx]="$REPLY"
done

# Split points (uniform columns): break after every REPLY_PER windows.
current_line=0
split1=999
split2=999
if ((needs_multiline)); then
	lines_for_colw "$colw"
	per=$REPLY_PER
	if ((total > per)); then
		split1=${indices[$((per - 1))]}
		current_line=1
	fi
	if ((total > 2 * per)); then
		split2=${indices[$((2 * per - 1))]}
		current_line=2
	fi
fi

# --- Batch simple commands via tmux source, direct calls for complex formats ---
declare -a tmux_cmds=()

# Per-window vars set directly (not via `tmux source -`): labels carry free-form
# issue titles whose quotes/';'/'#' would break the batched command parser.
for idx in "${indices[@]}"; do
	target="${SESSION}:${idx}"
	tmux set -w -t "$target" @window_icon_padded "${win_icon_str[$idx]}"
	tmux set -w -t "$target" @window_label_short "${win_short[$idx]}"
	tmux set -w -t "$target" @window_label_id "${win_id[$idx]}"
	tmux set -w -t "$target" @window_label_rest_short "${win_rest_short[$idx]}"
	tmux set -w -t "$target" @window_label_rest_long "${win_rest_long[$idx]}"
	tmux set -w -t "$target" @window_label_disp "${win_disp[$idx]}"
	tmux set -w -t "$target" @window_pr_plain "${win_pr[$idx]}"
	tmux set -w -t "$target" @window_pr_disp "${win_pr_disp[$idx]}"
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
# Name column: bold identity prefix + column-padded remainder (id + disp fill
# colw exactly). Label content changes outside structural events (issue stamp,
# PR arrival) re-enter via the providers' forced reflow calls.
NAME="#[bold]#{@window_label_id}#[nobold]#{@window_label_disp}"
LABEL_Z="${NAME}#{?window_zoomed_flag, 󰁌,}"
IDX="#{p${idx_width}:window_index}"
# Base tab color: the active window gets lavender (Catppuccin's
# active/focused accent) on a raised surface_0 pill, the rest dim on the
# default bg. The pill bg spans the whole slot (name, icons, PR) and is reset
# at the end of ENTRY so it never leaks into the separator.
BASE="#{?window_active,#[fg=#{@thm_lavender}#,bg=#{@thm_surface_0}],#[fg=#{@thm_subtext_0}#,bg=#{@thm_bg}]}"
# PR segment colored by state on every tab (conflicting/failing=red,
# pending=peach, merged=mauve, success/open=green). No PR → no color directive,
# and @window_pr_disp is just column padding. Rendered last in the slot, so its
# state color only runs into the separator, which sets its own color.
PRCOLOR="#{?#{&&:#{@pr_number},#{!=:#{@pr_number},none}},#{?#{||:#{==:#{@pr_check_state},failure},#{==:#{@pr_mergeable},conflicting}},#[fg=#{@thm_red}],#{?#{==:#{@pr_check_state},pending},#[fg=#{@thm_peach}],#{?#{==:#{@pr_state},merged},#[fg=#{@thm_mauve}],#[fg=#{@thm_green}]}}},}"
ENTRY="#[range=window|#{window_index}]#[nobold]${BASE}${IDX}: ${LABEL_Z} ${ICON}${PRCOLOR}#{@window_pr_disp}#[bg=#{@thm_bg}]#[norange]"

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
