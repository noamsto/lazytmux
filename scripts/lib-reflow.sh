#!/usr/bin/env bash
# Pure window-grid layout math for tmux-reflow-windows. Sourced (not
# executed) — no tmux calls, no Nix build-time placeholders, so it's directly
# testable under bats (tests/reflow.bats).
# Functions use the REPLY convention (set REPLY* instead of echoing) to avoid
# subshell forks, matching lib-icons.sh / lib-claude.sh / lib-enrich.sh.

# shellcheck disable=SC2034  # REPLY* outputs are used by callers

# reflow_lines_for_colw CW OVERHEAD AVAILABLE SEP_WIDTH TOTAL
# Rows needed to pack TOTAL windows into uniform CW-wide columns (+OVERHEAD
# per slot) within AVAILABLE width, separated by SEP_WIDTH.
# Sets REPLY=rows, REPLY_PER=columns per row.
reflow_lines_for_colw() {
	local cw=$1 overhead=$2 available=$3 sep_width=$4 total=$5
	local slot per
	slot=$((cw + overhead))
	per=$(((available + sep_width) / (slot + sep_width)))
	((per < 1)) && per=1
	REPLY_PER=$per
	REPLY=$(((total + per - 1) / per))
}

# reflow_pick_layout COLW_LONG COLW_SHORT TOTAL_LONG TOTAL_SHORT ANY_CAPPED
#                     TOTAL AVAILABLE ZOOM_EXTRA OVERHEAD SEP_WIDTH
#                     MAX_WIN_LINES LONG_TRUNC_FLOOR
#
# Detail ladder: long on one row -> long within MAX rows -> long truncated
# within MAX rows (rung 2.5) -> short (compact id) on one row -> short within
# MAX rows -> short truncated to fit. Keeping the branch clipped beats
# dropping it for a bare id; a compact line still beats illegible slivers, so
# it is the deeper rung.
#
# Sets REPLY_LABELS_MODE (long|short), REPLY_COLW, REPLY_TRUNC (0|1),
# REPLY_NEEDS_MULTILINE (0|1), REPLY_PER (columns per row for REPLY_COLW,
# valid whenever REPLY_NEEDS_MULTILINE=1).
reflow_pick_layout() {
	local colw_long=$1 colw_short=$2 total_long=$3 total_short=$4 any_capped=$5
	local total=$6 available=$7 zoom_extra=$8 overhead=$9 sep_width=${10}
	local max_win_lines=${11} long_trunc_floor=${12}

	REPLY_LABELS_MODE=long
	REPLY_COLW=$colw_long
	REPLY_TRUNC=0
	REPLY_NEEDS_MULTILINE=1

	if ((total_long + zoom_extra <= available)); then
		REPLY_LABELS_MODE=long
		REPLY_COLW=$colw_long
		REPLY_NEEDS_MULTILINE=0
	else
		reflow_lines_for_colw "$colw_long" "$overhead" "$available" "$sep_width" "$total"
		if ((REPLY <= max_win_lines)); then
			REPLY_LABELS_MODE=long
			REPLY_COLW=$colw_long
			# colw_long caps the rest at MAX_REST_WIDTH; truncate the over-long
			# names down to it so they fit the column instead of overflowing
			# past the icons.
			((any_capped)) && REPLY_TRUNC=1
		else
			# Rung 2.5: long labels truncated into MAX rows. Pack the fewest
			# columns that fit every window in MAX_WIN_LINES and clip the
			# branch/title to the resulting width -- the id (never-truncated
			# prefix) and PR (its own reserved column) survive. Taken only
			# while a column clears LONG_TRUNC_FLOOR; below that the grid is
			# slivers, so fall through to the short ladder.
			local long_cols long_trunc_colw
			long_cols=$(((total + max_win_lines - 1) / max_win_lines))
			long_trunc_colw=$(((available - (long_cols - 1) * sep_width) / long_cols - overhead))
			((long_trunc_colw > colw_long)) && long_trunc_colw=$colw_long
			if ((long_trunc_colw >= long_trunc_floor)); then
				REPLY_LABELS_MODE=long
				REPLY_COLW=$long_trunc_colw
				REPLY_TRUNC=1
			elif ((total_short + zoom_extra <= available)); then
				REPLY_LABELS_MODE=short
				REPLY_COLW=$colw_short
				REPLY_NEEDS_MULTILINE=0
			else
				REPLY_LABELS_MODE=short
				REPLY_COLW=$colw_short
				reflow_lines_for_colw "$colw_short" "$overhead" "$available" "$sep_width" "$total"
				if ((REPLY > max_win_lines)); then
					# even compact ids overflow the rows -> truncate to fit
					REPLY_TRUNC=1
					local cols
					cols=$(((total + max_win_lines - 1) / max_win_lines))
					REPLY_COLW=$(((available - (cols - 1) * sep_width) / cols - overhead))
					((REPLY_COLW < 6)) && REPLY_COLW=6
					((REPLY_COLW > colw_short)) && REPLY_COLW=$colw_short
				fi
			fi
		fi
	fi

	REPLY_PER=$total
	if ((REPLY_NEEDS_MULTILINE)); then
		reflow_lines_for_colw "$REPLY_COLW" "$overhead" "$available" "$sep_width" "$total"
	fi
}
