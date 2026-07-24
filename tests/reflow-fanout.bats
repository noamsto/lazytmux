#!/usr/bin/env bats
# Fan-out reflow correctness (issue #150): when the dispatcher opens worker
# windows and stamps @crew_name/@crew_color on them, the crew badge must appear
# and the grid must realign WITHOUT waiting for an unrelated structural event
# (a window close) to bust the win_count:WIDTH reflow cache.
#
# Runs the real scripts against a private, config-less tmux server so bare
# `tmux` calls inside the scripts resolve to it (via TMUX_TMPDIR), never the
# developer's own server.

setup() {
	command -v tmux >/dev/null || skip "tmux not on PATH"

	TDIR="$BATS_TEST_TMPDIR"
	export TMUX_TMPDIR="$TDIR/tmux"
	mkdir -p "$TMUX_TMPDIR"
	unset TMUX
	# Isolated Claude state root so update-icons' pane/task/name scans find nothing.
	export CLAUDE_STATUS_DIR="$TDIR/claude-status"
	mkdir -p "$CLAUDE_STATUS_DIR"
	# Pin TMPDIR so the reflow lock lands where the test can find it.
	export TMPDIR="$TDIR"

	# Fake reflow: records each invocation's argv so we can assert it fired.
	REFLOW_LOG="$TDIR/reflow.log"
	FAKE_REFLOW="$TDIR/fake-reflow"
	cat >"$FAKE_REFLOW" <<-EOF
		#!/bin/sh
		echo "\$*" >>"$REFLOW_LOG"
	EOF
	chmod +x "$FAKE_REFLOW"

	# Runnable update-icons with Nix placeholders resolved.
	UPDATE_ICONS="$TDIR/update-icons.sh"
	local licons
	licons="$TDIR/lib-icons.sh"
	sed -e 's/@ICON_MAP@//' -e 's/@FALLBACK_ICON@//' scripts/lib-icons.sh >"$licons"
	sed \
		-e "s|@lib_icons@|$licons|g" \
		-e "s|@lib_claude@|$PWD/scripts/lib-claude.sh|g" \
		-e "s|@reflow@|$FAKE_REFLOW|g" \
		-e 's|@MAX_ICONS@|5|g' \
		scripts/tmux-update-icons.sh >"$UPDATE_ICONS"

	# Runnable reflow with Nix placeholders resolved.
	REFLOW="$TDIR/reflow.sh"
	local lenrich
	lenrich="$TDIR/lib-enrich.sh"
	sed \
		-e 's/@providers@/linear github/g' \
		-e 's/@enrich_icon_linear@/L/g' -e 's/@enrich_icon_github@/G/g' \
		-e 's/@enrich_icon_pending@/P/g' -e 's/@enrich_icon_success@/S/g' \
		-e 's/@enrich_icon_failure@/F/g' -e 's/@enrich_icon_merged@/M/g' \
		-e 's/@enrich_icon_closed@/X/g' -e 's/@enrich_icon_conflict@/C/g' \
		scripts/lib-enrich.sh >"$lenrich"
	sed \
		-e "s|@lib_icons@|$licons|g" \
		-e "s|@lib_enrich@|$lenrich|g" \
		-e "s|@lib_log@|$PWD/scripts/lib-log.sh|g" \
		-e "s|@lib_reflow@|$PWD/scripts/lib-reflow.sh|g" \
		-e 's|@MAX_ICONS@|5|g' \
		scripts/tmux-reflow-windows.sh >"$REFLOW"

	tmux -f /dev/null new-session -d -s S -x 200 -y 50
	tmux set -g base-index 0
	local v
	for v in thm_bg thm_mauve thm_subtext_0 thm_fg thm_overlay_0 thm_overlay_1 thm_peach thm_green thm_red; do
		tmux set -g "@$v" "#000000"
	done
}

teardown() {
	tmux kill-server 2>/dev/null || true
}

run_update_icons() {
	: >"$REFLOW_LOG"
	# `|| true`: update-icons fires the reflow with `… & disown`; our fake reflow
	# is instant, so it can be reaped before `disown` runs and make disown (the
	# last command) exit non-zero. tmux ignores this #() callback's exit code, and
	# the reflow log is written before disown regardless — so assert on the log,
	# not the exit status.
	bash "$UPDATE_ICONS" S >/dev/null 2>&1 || true
	# update-icons backgrounds the reflow (`& disown`); wait briefly for the log.
	for _ in $(seq 1 40); do
		[[ -s $REFLOW_LOG ]] && return 0
		sleep 0.05
	done
	return 0
}

@test "crew stamp triggers a forced reflow" {
	run_update_icons # baseline tick seeds window state
	tmux set -wq -t S:0 @crew_name "atlas"
	tmux set -wq -t S:0 @crew_color "#ff0000"

	run_update_icons
	grep -q -- '--force' "$REFLOW_LOG"
}

@test "an unchanged crew name does not re-fire the reflow" {
	tmux set -wq -t S:0 @crew_name "atlas"
	run_update_icons # detects atlas -> fires (and records the seen value)

	run_update_icons # nothing changed
	[ ! -s "$REFLOW_LOG" ]
}

@test "a held reflow lock defers a concurrent reflow's write (no lost update)" {
	tmux new-window -d
	tmux new-window -d # 3 windows -> reflow computes key 3:200
	local lock="$TDIR/lazytmux-reflow.lock.S"
	mkdir "$lock" # simulate an in-flight reflow holding the lock
	tmux set -q @reflow_key "sentinel"

	bash "$REFLOW" S 200 --force >/dev/null 2>&1 &
	local rpid=$!

	sleep 0.3
	# blocked on the lock -> must not have clobbered anything yet
	[ "$(tmux show -v @reflow_key)" = "sentinel" ]

	rmdir "$lock" # holder finishes
	wait "$rpid"
	# now it acquired, recomputed against fresh state, and stamped the real key
	[ "$(tmux show -v @reflow_key)" = "3:200" ]
}

@test "zoom marker is carved from the label so the grid slot stays uniform" {
	# The inline " 󰁌" marker (LABEL_Z) is 2 cells; a zoomed window must reserve
	# them from its own label budget, or its grid slot renders 2 cells wide and
	# shoves that row's icons/PR/separator right (issue #150 follow-up).
	tmux set -wq -t S:0 @branch "aaa"
	tmux new-window -d # S:1 -> the zoomed twin
	tmux set -wq -t S:1 @branch "aaa"
	tmux new-window -d # S:2 -> long branch drives colw so the twins pad, not clip
	tmux set -wq -t S:2 @branch "a-very-long-branch-name-here"

	tmux split-window -d -t S:1
	tmux resize-pane -Z -t S:1
	[ "$(tmux display -t S:1 -p '#{window_zoomed_flag}')" = "1" ]

	# Narrow enough to force the multi-line grid (where the carve matters).
	bash "$REFLOW" S 80 --force >/dev/null 2>&1

	# shellcheck source=/dev/null
	source "$TDIR/lib-icons.sh"
	dw() { measure_display_width "$1"; }
	dw "$(tmux show -wv -t S:0 @window_label_disp)"
	local plain=$REPLY_DW
	dw "$(tmux show -wv -t S:1 @window_label_disp)"
	local zoomed=$REPLY_DW
	# Same content + same colw; the zoomed twin's remainder is exactly 2 shorter.
	[ "$plain" -eq "$((zoomed + 2))" ]
}

@test "a bridge window labels from @window_bridge_name, not the clobbered window_name" {
	# Simulate the real-config clobber: window_name is the wrong cwd-derived
	# name, but the daemon-owned @window_bridge_name holds the remote name.
	tmux set -wq -t S:0 @bridge_win 1
	tmux set-window-option -t S:0 automatic-rename off
	tmux rename-window -t S:0 lazytmux            # the wrong name
	tmux set -wq -t S:0 @window_bridge_name shell # the remote name

	bash "$REFLOW" S 200 --force >/dev/null 2>&1

	[ "$(tmux show -wv -t S:0 @window_label_short)" = "shell" ]
}
