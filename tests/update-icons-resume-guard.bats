#!/usr/bin/env bats
bats_require_minimum_version 1.5.0 # run !
# Regression test for the RESUME_CLAUDE stamper in tmux-update-icons.sh: it
# iterates claude_pane_ids() (panes/* UNION screen/*), so a Cursor/Codex pane
# that only has a screen/<id> file (agent-detect scrapes every known agent CLI
# unconditionally, regardless of which hooks are enabled) is yielded too. For
# such a pane read_pane_state never sets a transcript, so desired="" — and
# unless the stamper's guard is scoped to values it owns, that clobbers back
# to empty any @remux_relaunch a different agent's own hook just stamped.
#
# Runs the real script against a private, config-less tmux server (same
# pattern as update-icons-enrich-trigger.bats), so bare `tmux` calls inside it
# see a real pane, not a fake.

setup() {
	command -v tmux >/dev/null || skip "tmux not on PATH"

	TDIR="$BATS_TEST_TMPDIR"
	export TMUX_TMPDIR="$TDIR/tmux"
	mkdir -p "$TMUX_TMPDIR"
	unset TMUX
	export CLAUDE_STATUS_DIR="$TDIR/claude-status"
	mkdir -p "$CLAUDE_STATUS_DIR/panes" "$CLAUDE_STATUS_DIR/screen"
	export TMPDIR="$TDIR"

	# Fake reflow: update-icons kicks it on every branch change; a no-op is
	# fine here, this file only asserts on @remux_relaunch stamping.
	FAKE_REFLOW="$TDIR/fake-reflow"
	cat >"$FAKE_REFLOW" <<-EOF
		#!/bin/sh
		exit 0
	EOF
	chmod +x "$FAKE_REFLOW"

	# Runnable update-icons with Nix placeholders resolved.
	UPDATE_ICONS="$TDIR/update-icons.sh"
	licons="$TDIR/lib-icons.sh"
	sed -e 's/@ICON_MAP@//' -e 's/@FALLBACK_ICON@//' scripts/lib-icons.sh >"$licons"
	sed \
		-e "s|@lib_icons@|$licons|g" \
		-e "s|@lib_claude@|$PWD/scripts/lib-claude.sh|g" \
		-e "s|@reflow@|$FAKE_REFLOW|g" \
		-e 's|@MAX_ICONS@|5|g' \
		scripts/tmux-update-icons.sh >"$UPDATE_ICONS"

	REPO="$TDIR/repo"
	mkdir -p "$REPO"
	git -C "$REPO" init -q
	git -C "$REPO" config user.email t@t
	git -C "$REPO" config user.name t
	git -C "$REPO" config commit.gpgsign false
	git -C "$REPO" commit -q --allow-empty -m init
	git -C "$REPO" branch -q -M main

	tmux -f /dev/null new-session -d -s S -c "$REPO" -x 200 -y 50
	tmux set -g base-index 0
	local v
	for v in thm_bg thm_mauve thm_subtext_0 thm_fg thm_overlay_0 thm_overlay_1 thm_peach thm_green thm_red; do
		tmux set -g "@$v" "#000000"
	done

	PANE_ID="$(tmux list-panes -t S -F '#{pane_id}')"
	PANE_ID="${PANE_ID#%}"
}

teardown() {
	tmux kill-server 2>/dev/null || true
}

run_update_icons() {
	# $2 = RESUME_CLAUDE ("on"), same arg tmux itself passes via #{@resume_claude}.
	bash "$UPDATE_ICONS" S on >/dev/null 2>&1 || true
}

@test "screen-only pane (no hook transcript) does not clobber another agent's relaunch stamp" {
	printf 'state=idle\ntimestamp=%s\n' "$(date +%s)" >"$CLAUDE_STATUS_DIR/screen/$PANE_ID"
	tmux set -p -t "%$PANE_ID" @remux_relaunch "cursor-agent --resume abc123"

	run_update_icons

	[ "$(tmux show -pv -t "%$PANE_ID" @remux_relaunch)" = "cursor-agent --resume abc123" ]
}

@test "hook pane with a transcript still stamps claude --resume <uuid>" {
	TRANSCRIPT="$TDIR/deadbeef-0000-0000-0000-000000000001.jsonl"
	: >"$TRANSCRIPT"
	{
		echo "state=processing"
		echo "timestamp=$(date +%s)"
		echo "session=work"
		echo "transcript=$TRANSCRIPT"
	} >"$CLAUDE_STATUS_DIR/panes/$PANE_ID"

	run_update_icons

	[ "$(tmux show -pv -t "%$PANE_ID" @remux_relaunch)" = "claude --resume deadbeef-0000-0000-0000-000000000001" ]
}

@test "a pane that moved from Codex/Cursor to a live Claude session overwrites the foreign stamp" {
	TRANSCRIPT="$TDIR/cafebabe-0000-0000-0000-000000000003.jsonl"
	: >"$TRANSCRIPT"
	{
		echo "state=processing"
		echo "timestamp=$(date +%s)"
		echo "session=work"
		echo "transcript=$TRANSCRIPT"
	} >"$CLAUDE_STATUS_DIR/panes/$PANE_ID"
	tmux set -p -t "%$PANE_ID" @remux_relaunch "codex resume abc123"

	run_update_icons

	[ "$(tmux show -pv -t "%$PANE_ID" @remux_relaunch)" = "claude --resume cafebabe-0000-0000-0000-000000000003" ]
}

@test "no write is issued when the stamp already matches the desired value" {
	TRANSCRIPT="$TDIR/11111111-0000-0000-0000-000000000002.jsonl"
	: >"$TRANSCRIPT"
	{
		echo "state=processing"
		echo "timestamp=$(date +%s)"
		echo "session=work"
		echo "transcript=$TRANSCRIPT"
	} >"$CLAUDE_STATUS_DIR/panes/$PANE_ID"
	tmux set -p -t "%$PANE_ID" @remux_relaunch "claude --resume 11111111-0000-0000-0000-000000000002"

	REAL_TMUX="$(command -v tmux)"
	SPY_DIR="$TDIR/tmux-spy"
	SPY_LOG="$TDIR/tmux-calls.log"
	mkdir -p "$SPY_DIR"
	: >"$SPY_LOG"
	cat >"$SPY_DIR/tmux" <<-SPYEOF
		#!/bin/sh
		printf '%s\n' "\$*" >>"$SPY_LOG"
		exec "$REAL_TMUX" "\$@"
	SPYEOF
	chmod +x "$SPY_DIR/tmux"

	PATH="$SPY_DIR:$PATH" run_update_icons

	[ "$(tmux show -pv -t "%$PANE_ID" @remux_relaunch)" = "claude --resume 11111111-0000-0000-0000-000000000002" ]
	run ! grep -q '^set .*@remux_relaunch' "$SPY_LOG"
}
