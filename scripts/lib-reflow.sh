#!/usr/bin/env bash
# Pure window-grid layout math for tmux-reflow-windows. Sourced (not
# executed) — no tmux calls, no Nix build-time placeholders, so it's directly
# testable under bats (tests/reflow.bats).
# Functions use the REPLY convention (set REPLY* instead of echoing) to avoid
# subshell forks, matching lib-icons.sh / lib-claude.sh / lib-enrich.sh.

# shellcheck disable=SC2034  # REPLY* outputs are used by callers

# Narrowest a starved column may get before the label is all ellipsis. Columns
# only reach it once even the incompressible parts overflow the row, where
# running past the row width is unavoidable anyway.
REFLOW_MIN_COLW=6

# reflow_fit_columns PER AVAILABLE OVERHEAD SEP_WIDTH FLOORS WANTS
#
# Size the PER columns of one grid row. FLOORS and WANTS are space-separated
# per-window widths in window order; the window at position p sits in column
# p % PER, so a column takes the max over the windows stacked in it. A floor is
# the part the renderer cannot shrink (issue id + agent badge + zoom marker); a
# want additionally covers the branch/title.
#
# Columns are sized independently because alignment only needs a width to match
# *down* a column, never across — charging every column the widest window's
# width (one uniform colw) is what overflowed the row in issue #271.
#
# Sets REPLY_COLWS (space-separated column widths), REPLY_FITS (1 when every
# column reached its full want) and REPLY_MIN_STARVED (narrowest width among
# the columns that did not, 0 when none did).
reflow_fit_columns() {
	local per=$1 available=$2 overhead=$3 sep_width=$4
	local -a floors wants
	read -ra floors <<<"$5"
	read -ra wants <<<"$6"
	((per < 1)) && per=1

	local -a cf=() cw=() width=() demand=()
	local i c n=${#floors[@]}
	for ((c = 0; c < per; c++)); do
		cf[c]=0
		cw[c]=0
	done
	for ((i = 0; i < n; i++)); do
		c=$((i % per))
		((floors[i] > cf[c])) && cf[c]=${floors[i]}
		((wants[i] > cw[c])) && cw[c]=${wants[i]}
	done

	local budget=$((available - (per - 1) * sep_width - per * overhead))
	((budget < 0)) && budget=0

	# Plain assignments, not ((sum += ...)): a standalone arithmetic command whose
	# result is 0 exits 1, which aborts under the errexit the bats suite runs with.
	local sum_f=0 sum_w=0
	for ((c = 0; c < per; c++)); do
		sum_f=$((sum_f + cf[c]))
		sum_w=$((sum_w + cw[c]))
	done

	if ((sum_w <= budget)); then
		REPLY_COLWS="${cw[*]}"
		REPLY_FITS=1
		REPLY_MIN_STARVED=0
		return
	fi
	REPLY_FITS=0

	if ((sum_f > budget)); then
		# Even the incompressible parts overflow. Split the budget evenly and
		# let the caller clip identities to match; REFLOW_MIN_COLW keeps a
		# column from degrading to a bare ellipsis.
		local even=$((budget / per)) extra=$((budget % per))
		for ((c = 0; c < per; c++)); do
			width[c]=$((even + (c < extra ? 1 : 0)))
			((width[c] < REFLOW_MIN_COLW)) && width[c]=$REFLOW_MIN_COLW
		done
	else
		# Floors first, then hand the slack out in proportion to unmet demand.
		# give <= demand holds for every column because slack < total_demand
		# (sum_w > budget), so none overshoots its want and needs clamping.
		local slack=$((budget - sum_f)) total_demand=0 handed=0 leftover
		for ((c = 0; c < per; c++)); do
			demand[c]=$((cw[c] - cf[c]))
			total_demand=$((total_demand + demand[c]))
		done
		for ((c = 0; c < per; c++)); do
			width[c]=$((cf[c] + slack * demand[c] / total_demand))
			handed=$((handed + width[c] - cf[c]))
		done
		# Integer division leaves up to per-1 cells unspent; give them to the
		# columns still short of their want.
		leftover=$((slack - handed))
		for ((c = 0; c < per && leftover > 0; c++)); do
			((width[c] < cw[c])) || continue
			width[c]=$((width[c] + 1))
			leftover=$((leftover - 1))
		done
	fi

	REPLY_MIN_STARVED=0
	for ((c = 0; c < per; c++)); do
		((width[c] >= cw[c])) && continue
		((REPLY_MIN_STARVED == 0 || width[c] < REPLY_MIN_STARVED)) && REPLY_MIN_STARVED=${width[c]}
	done
	REPLY_COLWS="${width[*]}"
}

# reflow_pick_layout FLOORS WANTS_LONG WANTS_SHORT TOTAL_LONG TOTAL_SHORT
#                     TOTAL AVAILABLE ZOOM_EXTRA OVERHEAD SEP_WIDTH
#                     MAX_WIN_LINES LONG_TRUNC_FLOOR
#
# Detail ladder: long on one row -> long grid with every column at its full
# want -> long grid with starved columns (rung 2.5) -> short (compact id) on
# one row -> short grid at full want -> short grid packed to fit. Keeping the
# branch clipped beats dropping it for a bare id; a compact line still beats
# illegible slivers, so it is the deeper rung.
#
# Each grid rung takes the fewest rows that satisfy it, since fewer rows means
# more columns per row and therefore narrower columns.
#
# Sets REPLY_LABELS_MODE (long|short), REPLY_COLWS (space-separated column
# widths, valid whenever REPLY_NEEDS_MULTILINE=1), REPLY_NEEDS_MULTILINE (0|1)
# and REPLY_PER (columns per row).
reflow_pick_layout() {
	local floor_list=$1 want_long_list=$2 want_short_list=$3
	local total_long=$4 total_short=$5 total=$6 available=$7 zoom_extra=$8
	local overhead=$9 sep_width=${10} max_win_lines=${11} long_trunc_floor=${12}

	if ((total_long + zoom_extra <= available)); then
		REPLY_LABELS_MODE=long
		REPLY_NEEDS_MULTILINE=0
		REPLY_PER=$total
		REPLY_COLWS=""
		return
	fi

	REPLY_NEEDS_MULTILINE=1
	# Fewest columns the row cap allows -- the widest a column can ever be, and
	# what both truncating rungs settle on.
	local widest_per=$(((total + max_win_lines - 1) / max_win_lines))
	local rows per

	for ((rows = 1; rows <= max_win_lines; rows++)); do
		per=$(((total + rows - 1) / rows))
		reflow_fit_columns "$per" "$available" "$overhead" "$sep_width" "$floor_list" "$want_long_list"
		if ((REPLY_FITS)); then
			REPLY_LABELS_MODE=long
			REPLY_PER=$per
			return
		fi
	done

	# Rung 2.5: long labels with starved columns. Taken only while every starved
	# column still clears LONG_TRUNC_FLOOR (id + ~12 chars of branch); below
	# that the grid is slivers, so fall through to the short ladder.
	reflow_fit_columns "$widest_per" "$available" "$overhead" "$sep_width" "$floor_list" "$want_long_list"
	if ((REPLY_MIN_STARVED >= long_trunc_floor)); then
		REPLY_LABELS_MODE=long
		REPLY_PER=$widest_per
		return
	fi

	REPLY_LABELS_MODE=short
	if ((total_short + zoom_extra <= available)); then
		REPLY_NEEDS_MULTILINE=0
		REPLY_PER=$total
		REPLY_COLWS=""
		return
	fi

	for ((rows = 1; rows <= max_win_lines; rows++)); do
		per=$(((total + rows - 1) / rows))
		reflow_fit_columns "$per" "$available" "$overhead" "$sep_width" "$floor_list" "$want_short_list"
		if ((REPLY_FITS)); then
			REPLY_PER=$per
			return
		fi
	done

	# Deepest rung: compact ids packed into the widest columns the row cap
	# allows, starved and clipped as needed.
	reflow_fit_columns "$widest_per" "$available" "$overhead" "$sep_width" "$floor_list" "$want_short_list"
	REPLY_PER=$widest_per
}
