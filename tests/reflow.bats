#!/usr/bin/env bats
# Covers the reflow detail ladder (scripts/lib-reflow.sh): which label detail
# + column width + row count wins for a given set of aggregate window-label
# widths and terminal width. See scripts/tmux-reflow-windows.sh for how the
# real script derives colw_long/colw_short/total_long/total_short/any_capped
# from per-window data before calling reflow_pick_layout.

load helper

setup() {
	setup_lib_reflow
}

# reflow_pick_layout COLW_LONG COLW_SHORT TOTAL_LONG TOTAL_SHORT ANY_CAPPED
#                     TOTAL AVAILABLE ZOOM_EXTRA OVERHEAD SEP_WIDTH
#                     MAX_WIN_LINES LONG_TRUNC_FLOOR

@test "long labels fit on a single line" {
	reflow_pick_layout 30 10 60 30 0 3 80 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = long ]
	[ "$REPLY_COLW" = 30 ]
	[ "$REPLY_TRUNC" = 0 ]
	[ "$REPLY_NEEDS_MULTILINE" = 0 ]
}

@test "long labels wrap into a grid within MAX_WIN_LINES (rung 2)" {
	# 6 windows at colw_long=30/overhead=10/available=120 pack 2-per-row,
	# exactly 3 rows -- the cliff before rung 2.5 (see next test).
	reflow_pick_layout 30 10 1000 30 0 6 120 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = long ]
	[ "$REPLY_COLW" = 30 ]
	[ "$REPLY_TRUNC" = 0 ]
	[ "$REPLY_NEEDS_MULTILINE" = 1 ]
	[ "$REPLY_PER" = 2 ]
}

@test "6->7 window cliff: one more window drops rung 2 into rung 2.5 (truncated long grid)" {
	# Same width/colw as the previous test, one more window: 2-per-row no
	# longer fits everyone in 3 rows, so it falls to the truncated long grid
	# instead of expanding rows.
	reflow_pick_layout 30 10 1000 30 0 7 120 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = long ]
	[ "$REPLY_TRUNC" = 1 ]
	[ "$REPLY_NEEDS_MULTILINE" = 1 ]
	[ "$REPLY_COLW" -lt 30 ]
	[ "$REPLY_COLW" -ge 24 ]
}

@test "LONG_TRUNC_FLOOR guard: narrow width + high count falls through to short" {
	# Rung 2.5's per-column budget for 10 windows in 60 cells collapses well
	# below LONG_TRUNC_FLOOR (24), so the ladder must not settle on a
	# truncated long grid -- it falls all the way to short.
	reflow_pick_layout 30 8 1000 40 0 10 60 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = short ]
}

@test "short labels fit on a single line" {
	reflow_pick_layout 30 8 500 40 0 5 50 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = short ]
	[ "$REPLY_COLW" = 8 ]
	[ "$REPLY_TRUNC" = 0 ]
	[ "$REPLY_NEEDS_MULTILINE" = 0 ]
}

@test "short labels wrap into a grid within MAX_WIN_LINES" {
	reflow_pick_layout 30 8 500 200 0 5 50 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = short ]
	[ "$REPLY_COLW" = 8 ]
	[ "$REPLY_TRUNC" = 0 ]
	[ "$REPLY_NEEDS_MULTILINE" = 1 ]
}

@test "short labels truncate to fit when even compact ids overflow MAX_WIN_LINES" {
	reflow_pick_layout 30 8 1000 300 0 10 60 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = short ]
	[ "$REPLY_TRUNC" = 1 ]
	[ "$REPLY_NEEDS_MULTILINE" = 1 ]
	[ "$REPLY_COLW" -ge 6 ]
	[ "$REPLY_COLW" -le 8 ]
}

@test "short truncate never goes below the 6-cell floor" {
	# Pathologically narrow width: the computed truncated column would go
	# negative, so it must clamp to 6.
	reflow_pick_layout 30 8 1000 1000 0 20 40 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = short ]
	[ "$REPLY_TRUNC" = 1 ]
	[ "$REPLY_COLW" = 6 ]
}

@test "any_capped forces trunc on in the untruncated long grid (rung 2)" {
	reflow_pick_layout 30 10 1000 30 1 6 120 0 10 3 3 24
	[ "$REPLY_LABELS_MODE" = long ]
	[ "$REPLY_COLW" = 30 ]
	[ "$REPLY_TRUNC" = 1 ]
}

@test "zoom_extra can tip a fitting single line into the grid ladder" {
	# total_long + zoom_extra just exceeds available, unlike the single-line
	# test above with zoom_extra=0.
	reflow_pick_layout 30 10 78 30 0 3 80 4 10 3 3 24
	[ "$REPLY_NEEDS_MULTILINE" = 1 ]
}

@test "reflow_lines_for_colw: rows and columns-per-row for a uniform grid" {
	reflow_lines_for_colw 30 10 120 3 6
	[ "$REPLY_PER" = 2 ]
	[ "$REPLY" = 3 ]
}

@test "reflow_lines_for_colw: never reports fewer than 1 column per row" {
	reflow_lines_for_colw 500 10 50 3 4
	[ "$REPLY_PER" = 1 ]
	[ "$REPLY" = 4 ]
}
