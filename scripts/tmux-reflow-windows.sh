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
# shellcheck source=/dev/null
source @lib_log@

# Accept --force (cache bypass, used by enrich scripts after writing vars) and
# --debounce (coalesce a resize burst, see below).
FORCE=0
DEBOUNCE=0
pos=()
for a in "$@"; do
	case "$a" in
	--force) FORCE=1 ;;
	--debounce) DEBOUNCE=1 ;;
	*) pos+=("$a") ;;
	esac
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

# Debounce a resize burst: client-resized fires once per drag step, and every
# distinct width misses the cache below → a full O(N) recompute each time. The
# hook backgrounds this (-b), so the sleep is off the server's command queue.
# Each invocation stamps a token and waits out the burst; only the last one to
# stamp (the final width) survives the token check and reflows — the rest bail.
if ((DEBOUNCE)); then
	stamp="/tmp/lazytmux-reflow-debounce.${SESSION//\//_}"
	token=$EPOCHREALTIME
	printf '%s' "$token" >"$stamp" 2>/dev/null
	sleep 0.12
	last=""
	[[ -f $stamp ]] && last=$(<"$stamp")
	[[ $last == "$token" ]] || exit 0
fi

# Fast-path: skip if window count + width unchanged since last reflow. Layout
# (detail mode + column width) depends only on the window set + width, not on
# which window is active — focus only changes the active tab's color, which tmux
# re-renders on its own without a reflow.
# One display-message fetches both the window count and the stored key (the
# fast path runs on every reflow, so halving its forks matters).
{
	IFS= read -r win_count
	IFS= read -r prev_key
} < <(tmux display-message -t "$SESSION" -p $'#{session_windows}\n#{@reflow_key}' 2>/dev/null)
cache_key="${win_count}:${WIDTH}"
if ((!FORCE)) && [[ $cache_key == "$prev_key" ]]; then
	log_enabled && log_event reflow event cache_hit wins "$win_count" width "$WIDTH" sess "$SESSION"
	exit 0
fi

# Serialize compute+write across concurrent reflows (issue #150). A dispatcher
# fan-out fires a burst of reflows — sync window hooks racing backgrounded
# --force reflows from the enrich/icon scripts — with no ordering guarantee, so
# a reflow that read partial state could finish last and clobber the correct
# layout (then freeze it, since the win_count:WIDTH cache key can't see the
# stale content). Reads happen below, inside the lock, so whoever runs last sees
# the freshest window state and its render wins. acquire_lock is non-blocking
# (flock is Linux-only); the burst-prone hooks run backgrounded (-b), so retry
# briefly instead of racing. On pathological contention, proceed unlocked rather
# than wedge — a later reflow still settles it.
reflow_lock="${TMPDIR:-/tmp}/lazytmux-reflow.lock.${SESSION//\//_}"
for ((i = 0; i < 40; i++)); do
	acquire_lock "$reflow_lock" && break
	sleep 0.05
done

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# --- Single pass: collect window data + pane processes ---
declare -a indices
declare -A win_procs # keyed by window_index, space-separated unique procs
total=0
has_zoom=0

# @window_task's free-form text may contain '|', and read drops any extra
# delimiters into the final field, so nothing after it can shift the columns
# before it — @window_bridge_name (sanitized, no '|') is safe there.
# @window_ai_name is sanitized (kebab, no '|') so it sits safely before it.
# @crew_name (agent codename, stamped by an external fan-out harness) is
# token-safe (no '|'). Its @crew_color pairs with it but is read straight from the
# window option in the template, so only the name is pulled here (for width).
# @bridge_win/window_name sit after it: bridge_win is "1" or empty, and a
# window_name containing '|' is no worse off here than at the very end.
FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}|#{@issue_provider}|#{@issue_id}|#{@issue_title}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@issue_branch}|#{@crew_name}|#{@window_ai_name}|#{@bridge_win}|#{window_name}|#{@window_task}|#{@window_bridge_name}'
declare -A win_short win_short_dw win_long_dw
declare -A win_id win_id_dw win_rest_short win_rest_long win_pr win_pr_dw
declare -A win_crew win_crew_dw win_crew_disp win_zoom_dw
pr_colw=0   # widest PR segment → shared PR column width (0 when no window has a PR)
crew_colw=0 # widest codename → shared agent-badge column (0 when no window is tagged)
while IFS='|' read -r idx branch pane_path zoomed iprov iid ititle prnum prstate prcheck prmerge ibranch crew wai bridge wname wtask bname; do
	indices+=("$idx")
	# The zoom marker (" 󰁌", 2 cells) is emitted inline by LABEL_Z on zoomed
	# windows; carve it from that window's label budget so its grid slot stays
	# colw wide (mirrors the crew badge). has_zoom reserves the same 2 cells in
	# the single-line fit test, where the marker is appended to the full label.
	win_zoom_dw[$idx]=0
	((zoomed)) && has_zoom=1 && win_zoom_dw[$idx]=2

	# Remote-bridge mirror window (#167 @bridge_win opt-out): label it with the
	# daemon-owned remote name (@window_bridge_name), NOT #{window_name} — the
	# latter is clobbered by automatic-rename on the real config (#196). Fall
	# back to window_name before the daemon's first write. The issue/PR/branch
	# context belongs to the launcher's repo, not the remote window this
	# mirrors — skip enrichment entirely.
	if [[ $bridge == 1 ]]; then
		bwname="${bname:-$wname}"
		win_short[$idx]="$bwname"
		win_id[$idx]=""
		win_rest_short[$idx]="$bwname"
		win_pr[$idx]=""
		measure_display_width "$bwname"
		win_short_dw[$idx]=$REPLY_DW
		win_id_dw[$idx]=0
		win_pr_dw[$idx]=0
		win_crew[$idx]=""
		win_crew_dw[$idx]=0
		win_rest_long[$idx]="$bwname"
		win_long_dw[$idx]=$REPLY_DW
		((total++))
		continue
	fi

	# Stamp belongs to the branch it was written for. If the pane has since
	# cd'd to a different branch, build the label from the current branch
	# instead — the stamp stays on the window and reappears on cd back.
	if [[ -n $iid && $ibranch != "$branch" ]]; then
		iprov="" iid="" ititle=""
		prnum="" prstate="" prcheck="" prmerge=""
	fi

	build_window_label short "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path" "$prmerge" "$wtask" "$wai"
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

	# Agent codename badge (external fan-out harness stamps @crew_name/@crew_color).
	# Its own shared column, mirroring the PR pattern: charged per-window in the
	# single-line fit test, as a uniform padded column in the multi-line grid. The
	# trailing separator space is folded into the segment so the width math is exact.
	if [[ -n $crew ]]; then
		win_crew[$idx]="${crew} "
		measure_display_width "${win_crew[$idx]}"
		win_crew_dw[$idx]=$REPLY_DW
		((win_crew_dw[$idx] > crew_colw)) && crew_colw=${win_crew_dw[$idx]}
	else
		win_crew[$idx]=""
		win_crew_dw[$idx]=0
	fi

	# Long mode only changes the remainder (title / full branch); the id and PR
	# segments are mode-independent.
	build_window_label long "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path" "$prmerge" "$wtask" "$wai"
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

# Fixed icon-column width for the slot math below. The icon *content*
# (@window_icon_padded) is owned solely by tmux-update-icons — don't write it
# here too: this script can't source lib-claude, so it would drop the colored
# claude glyph on every --force reflow, flickering it out until the next tick.
max_icon_width=$((MAX_ICONS * 3 + 2))

# --- Layout: pick label detail (long/short) + column width, then pack ---
# Slot = idx_width + ": "(2) + name + pr + " "(1) + icon column.
# The PR segment carries its own leading space, so no PR adds nothing.
# The shared pr column (pr_colw) is only charged in the multi-line uniform
# slot; single-line entries are unpadded, so the one-row fit charges each
# window its own PR width — otherwise one window growing a PR inflates the
# fit test by pr_colw × window count and flips to compact despite free space.
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
# Fixed ago column in the multi-line slot: the value ticks between reflows and
# flips empty↔set on Claude state changes without triggering a reflow, so a
# live-width column would drift. 1 space + 3 right-aligned cells = 4.
AGO_W=4
slot_overhead=$((idx_width + 3 + max_icon_width)) # ": " + trailing space + icons
overhead=$((slot_overhead + pr_colw + AGO_W))     # + shared pr, ago cols (crew badge is per-window, carved from the label below — not a shared column)

available=$((WIDTH - PREFIX_WIDTH))
zoom_extra=0
((has_zoom)) && zoom_extra=2
SEP_WIDTH=3 # " │ "
MAX_WIN_LINES=3
# Floor for rung 2.5 (truncated long grid): keep it only while each column can
# still show the id + ~12 chars of branch (≈24 for typical 8-char issue ids).
# Below it the grid degrades to illegible slivers, so fall through to short.
LONG_TRUNC_FLOOR=24

# Aggregate widths for the long and short label variants. The grid column
# (colw) is sized to the widest id + rest + badge, so a tagged window's label
# and badge fit its own column — the badge is carved from rest_avail below, so
# omitting it here lets the widest tagged window overflow past the icon column.
# The rest (branch/title) is capped at MAX_REST_WIDTH so one very long name
# can't stretch the whole grid; id and badge are never capped. any_capped forces
# truncation on in the long grid rung. The single-line fit tests stay uncapped —
# that path renders full names via the global format, so it must reserve them.
MAX_REST_WIDTH=40
any_capped=0
colw_long=0
colw_short=0
total_long=0
total_short=0
for idx in "${indices[@]}"; do
	rest_long=$((win_long_dw[$idx] - win_id_dw[$idx]))
	((rest_long > MAX_REST_WIDTH)) && {
		rest_long=$MAX_REST_WIDTH
		any_capped=1
	}
	label_long=$((win_id_dw[$idx] + rest_long + win_crew_dw[$idx]))
	((label_long > colw_long)) && colw_long=$label_long
	rest_short=$((win_short_dw[$idx] - win_id_dw[$idx]))
	((rest_short > MAX_REST_WIDTH)) && rest_short=$MAX_REST_WIDTH
	label_short=$((win_id_dw[$idx] + rest_short + win_crew_dw[$idx]))
	((label_short > colw_short)) && colw_short=$label_short
	((total_long += win_long_dw[$idx] + slot_overhead + win_pr_dw[$idx] + win_crew_dw[$idx]))
	((total_short += win_short_dw[$idx] + slot_overhead + win_pr_dw[$idx] + win_crew_dw[$idx]))
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

# Detail ladder: long on one row → long within MAX rows → long truncated within
# MAX rows → short (compact id) on one row → short within MAX rows → short
# truncated to fit. Keeping the branch clipped beats dropping it for a bare id;
# a compact line still beats illegible slivers, so it is the deeper rung.
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
		# colw_long caps the rest at MAX_REST_WIDTH; truncate the over-long names
		# down to it so they fit the column instead of overflowing past the icons.
		((any_capped)) && trunc=1
	else
		# Rung 2.5: long labels truncated into MAX rows. Pack the fewest columns
		# that fit every window in MAX_WIN_LINES and clip the branch/title to the
		# resulting width — the id (never-truncated prefix) and PR (its own
		# reserved column) survive. Taken only while a column clears
		# LONG_TRUNC_FLOOR; below that the grid is slivers, so fall through to
		# the short ladder.
		long_cols=$(((total + MAX_WIN_LINES - 1) / MAX_WIN_LINES))
		long_trunc_colw=$(((available - (long_cols - 1) * SEP_WIDTH) / long_cols - overhead))
		((long_trunc_colw > colw_long)) && long_trunc_colw=$colw_long
		if ((long_trunc_colw >= LONG_TRUNC_FLOOR)); then
			labels_mode=long
			colw=$long_trunc_colw
			trunc=1
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
	# The agent badge (win_crew_dw, 0 when untagged) and zoom marker (win_zoom_dw,
	# 0 unless zoomed) both render inline off this window's label; steal their
	# width so the slot stays colw+overhead wide regardless.
	rest_avail=$((colw - win_id_dw[$idx] - win_crew_dw[$idx] - win_zoom_dw[$idx]))
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

	# Agent badge: the codename + separator space, emitted after the index (see
	# ENTRY). Blank for untagged windows, which then render a pristine full-width
	# label with no leading gap.
	win_crew_disp[$idx]="${win_crew[$idx]}"
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

# Columns per row in the reflowed grid (single-line mode keeps all on one row).
# Consumed by tmux-window-nav for vertical (row-to-row) window movement.
if ((needs_multiline)); then
	window_per=$per
else
	window_per=$total
fi

# --- Batch simple commands via tmux source, direct calls for complex formats ---
declare -a tmux_cmds=()

# Per-window vars use tmux's argv command-sequence form — one tmux exec per
# window, the 8 sets joined by literal ';' arguments — instead of one exec per
# set (8N execs before). Not `tmux source -`: source re-parses a text stream, so
# free-form issue titles with quotes/';'/'#' would break it. In argv form each
# value is its own execve argument and is never reparsed, so titles pass verbatim.
for idx in "${indices[@]}"; do
	target="${SESSION}:${idx}"
	tmux \
		set -w -t "$target" @window_label_short "${win_short[$idx]}" ';' \
		set -w -t "$target" @window_label_id "${win_id[$idx]}" ';' \
		set -w -t "$target" @window_label_rest_short "${win_rest_short[$idx]}" ';' \
		set -w -t "$target" @window_label_rest_long "${win_rest_long[$idx]}" ';' \
		set -w -t "$target" @window_label_disp "${win_disp[$idx]}" ';' \
		set -w -t "$target" @window_pr_plain "${win_pr[$idx]}" ';' \
		set -w -t "$target" @window_pr_disp "${win_pr_disp[$idx]}" ';' \
		set -w -t "$target" @window_crew_disp "${win_crew_disp[$idx]}"
done

# Split points and status line count
tmux_cmds+=("set -t '$SESSION' @window_split '$split1'")
tmux_cmds+=("set -t '$SESSION' @window_split2 '$split2'")
tmux_cmds+=("set -t '$SESSION' @window_per '$window_per'")
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
if log_enabled; then
	lines=2
	((current_line >= 1)) && lines=3
	((current_line >= 2)) && lines=4
	log_event reflow event recompute forced "$FORCE" wins "${cache_key%%:*}" \
		width "$WIDTH" split1 "$split1" split2 "$split2" lines "$lines" \
		labels_mode "$labels_mode" sess "$SESSION"
fi
{
	printf '%s\n' "${tmux_cmds[@]}"
	echo "refresh-client -S"
} | tmux source -

# Common format fragments
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "
ICON='#{@window_icon_padded}'
# Name column: bold identity prefix + column-padded remainder (id + disp fill
# colw exactly). The id is always bold; the remainder stays bold on the active
# window (BASE turns bold on for the whole marker) and drops to regular weight
# elsewhere. Label content changes outside structural events (issue stamp, PR
# arrival) re-enter via the providers' forced reflow calls.
NAME="#[bold]#{@window_label_id}#{?window_active,,#[nobold]}#{@window_label_disp}"
LABEL_Z="${NAME}#{?window_zoomed_flag, 󰁌,}"
IDX="#{p${idx_width}:window_index}"
# Base tab color: the active window's "index: label" text takes bold mauve
# (Catppuccin's accent); the rest dim on the default bg. Text-only — no fill, no
# underline — so colored process glyphs and the PR badge keep their own colors.
# The accent is scoped to "index: label"; ICONFG ends it before the icon column.
BASE="#{?window_active,#[fg=#{@thm_mauve}#,bg=#{@thm_bg}#,bold],#[fg=#{@thm_subtext_0}#,bg=#{@thm_bg}]}"
# End the active accent before the icons: bright fg on the default bg with bold
# off, so glyphs render as on inactive tabs (only brighter) and the colored
# Claude icon shows its state color. No-op on inactive tabs.
ICONFG="#{?window_active,#[fg=#{@thm_fg}#,bg=#{@thm_bg}#,nobold],}"
# PR segment colored by state on every tab. Merged/closed are terminal and
# checked first (merged=mauve, closed=overlay0), so a leftover pending/failed
# rollup can't tint them peach/red; then conflicting/failing=red, pending=peach,
# success/open=green. closed = a dead/superseded PR, dimmed so it can't read as
# a live one. No PR → no color directive, and @window_pr_disp is just column
# padding. Rendered last in the slot, so its state color only runs into the
# separator, which sets its own color.
PRCOLOR="#{?#{&&:#{@pr_number},#{!=:#{@pr_number},none}},#{?#{==:#{@pr_state},merged},#[fg=#{@thm_mauve}],#{?#{==:#{@pr_state},closed},#[fg=#{@thm_overlay_0}],#{?#{||:#{==:#{@pr_check_state},failure},#{==:#{@pr_mergeable},conflicting}},#[fg=#{@thm_red}],#{?#{==:#{@pr_check_state},pending},#[fg=#{@thm_peach}],#[fg=#{@thm_green}]}}}},}"
# "Last active" column for halted Claude windows (@window_claude_ago, kept fresh
# by tmux-update-icons). Right-aligned and padded to AGO_W's fixed width so the
# value (and an empty value, for active/non-claude windows) always occupies the
# same cells — this is what keeps grid columns aligned as the value ticks and appears.
AGO=" #[fg=#{@thm_overlay_1}]#{p-3:@window_claude_ago}"
# Agent-badge segment, emitted after "index: " (the @window_crew_disp carries a
# trailing separator space). Tinted by its stamped @crew_color; re-assert BASE
# after it so the label reverts to the tab color instead of inheriting the tint.
# Emitted only when at least one window is tagged (crew_colw > 0); untagged
# windows carry an empty @window_crew_disp and render a gapless full-width label.
CREW=""
((crew_colw > 0)) && CREW="#{?#{@crew_color},#[fg=#{@crew_color}#,bg=#{@thm_bg}],}#{@window_crew_disp}${BASE}"
ENTRY="#[range=window|#{window_index}]#[nobold]${BASE}${IDX}: ${CREW}${LABEL_Z}${ICONFG} ${ICON}${PRCOLOR}#{@window_pr_disp}${AGO}#[norange]"

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
