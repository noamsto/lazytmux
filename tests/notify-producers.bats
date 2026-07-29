#!/usr/bin/env bats
# shellcheck disable=SC2016 # the $-prefixed tmux target is literal, not an expansion
# Producer transition filtering (#164). The single most important assertion in
# this file is that `done` does NOT notify: it is every turn's terminal state,
# and notifying on it would train the user to ignore notifications.
#
# The router is faked via LZTMUX_NOTIFY_BIN — a capture script that appends its
# argv to one log. Everything the producers reach through tmux is a fake tmux on
# PATH; nothing here may touch a real server.

load helper

CSU="scripts/claude-status-update.sh"

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/claude-status"
	export NOTIFY_LOG="$BATS_TEST_TMPDIR/notify.log"
	# Deliberately no ENRICH_CACHE_DIR: lib-enrich.sh assigns it
	# unconditionally (not ${VAR:-…}), so exporting it here would be overwritten
	# at source time. Do not re-add it and do not rely on it isolating anything —
	# the PR cases run through mock mode, which never reads the cache.
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"
	unset TMUX TMUX_PANE

	# Fake tmux: window_active for the unseen computation, session_name for the
	# session lookup, the prev @pr_* snapshot for write_pr_options.
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		case "$*" in
		*@pr_number*) printf '%s\n' "${FAKE_PREV:-}" ;;
		*window_active*) printf '%s\n' "${FAKE_WIN_ACTIVE:-1}" ;;
		*session_name*) printf '%s\n' s1 ;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"

	cat >"$FAKEBIN/fake-notify" <<-'EOF'
		#!/bin/sh
		printf '%s\n' "$*" >>"$NOTIFY_LOG"
	EOF
	chmod +x "$FAKEBIN/fake-notify"

	export PATH="$FAKEBIN:$PATH"
	export LZTMUX_NOTIFY_BIN="$FAKEBIN/fake-notify"
	make_pr_enrich
}

# The call is detached, so poll rather than assume it completed.
wait_for() {
	for _ in $(seq 1 40); do
		[[ -f $1 ]] && return 0
		sleep 0.05
	done
	return 1
}

# Asserts nothing fired. The producers are fire-and-forget, so give the
# grandchild a beat before concluding silence.
#
# CRITICAL for every caller: `assert_silent` can only prove silence if no
# EARLIER notification is still in flight. `detach` returns before its
# grandchild appends to $NOTIFY_LOG, so seeding a prior state with a NOTIFYING
# write (waiting/error/denied) and then `rm -f "$NOTIFY_LOG"` races that append
# — the log reappears and the assertion fails intermittently even when the
# behaviour under test was correct. Seed with a NON-notifying state
# (processing/idle/compacting/clear), or `wait_for "$NOTIFY_LOG"` before the rm.
assert_silent() {
	sleep 0.2
	[ ! -f "$NOTIFY_LOG" ]
}

# --- claude ---

@test "claude: first waiting with no prior state file notifies (the none sentinel)" {
	run bash "$CSU" waiting --pane %5 --session s1
	[ "$status" -eq 0 ]
	wait_for "$NOTIFY_LOG"
	grep -qF -- '--source claude' "$NOTIFY_LOG"
	grep -qF -- '--level info' "$NOTIFY_LOG"
	grep -qF -- '--pane %5' "$NOTIFY_LOG"
	grep -qF -- 'none → waiting' "$NOTIFY_LOG"
	[ "$(wc -l <"$NOTIFY_LOG")" -eq 1 ]
}

@test "claude: denied notifies at warn, error at error" {
	bash "$CSU" processing --pane %5 --session s1
	rm -f "$NOTIFY_LOG"
	bash "$CSU" denied --pane %5 --session s1
	wait_for "$NOTIFY_LOG"
	grep -qF -- '--level warn' "$NOTIFY_LOG"

	rm -f "$NOTIFY_LOG"
	bash "$CSU" error --pane %5 --session s1
	wait_for "$NOTIFY_LOG"
	grep -qF -- '--level error' "$NOTIFY_LOG"
}

@test "claude: done does NOT notify" {
	# Seed with `processing`, NOT `waiting`: a notifying seed leaves a detached
	# write in flight that the rm below would race. processing → done is still a
	# real transition, so the gate is exercised exactly as intended.
	bash "$CSU" processing --pane %5 --session s1
	# "done" quoted: it is a state name here, not a loop keyword (SC1010).
	run bash "$CSU" "done" --pane %5 --session s1 --force
	[ "$status" -eq 0 ]
	assert_silent
}

@test "claude: processing, idle, compacting and clear do NOT notify" {
	# Same rule: every seed is a non-notifying state, so nothing is ever in
	# flight when assert_silent runs. The seed just has to differ from $st so
	# each case is a genuine transition rather than a same-state re-write.
	for st in processing idle compacting clear; do
		seed=processing
		[ "$st" = processing ] && seed=idle
		bash "$CSU" "$seed" --pane %5 --session s1
		run bash "$CSU" "$st" --pane %5 --session s1 --force
		[ "$status" -eq 0 ]
		assert_silent
	done
}

@test "claude: a same-state re-write does not re-notify" {
	# Here the `waiting` seed IS the point, so the wait_for before the rm is
	# mandatory: without it the first notification's detached append races the rm.
	bash "$CSU" waiting --pane %5 --session s1
	wait_for "$NOTIFY_LOG"
	rm -f "$NOTIFY_LOG"
	bash "$CSU" waiting --pane %5 --session s1
	assert_silent
}

@test "claude: the pane state file is unchanged and the script still exits 0" {
	FAKE_WIN_ACTIVE=0 run bash "$CSU" waiting --pane %5 --session s1
	[ "$status" -eq 0 ]
	run cat "$CLAUDE_STATUS_DIR/panes/5"
	[[ $output == *"state=waiting"* ]]
	[[ $output == *"session=s1"* ]]
	[[ $output == *"unseen=1"* ]]
	run cut -d= -f1 "$CLAUDE_STATUS_DIR/panes/5"
	[ "$output" = "state
timestamp
session
unseen" ]
}

@test "claude: an unsubstituted @notify@ disables the call and does not break the write" {
	run env -u LZTMUX_NOTIFY_BIN bash "$CSU" waiting --pane %5 --session s1
	[ "$status" -eq 0 ]
	assert_silent
	grep -qx 'state=waiting' "$CLAUDE_STATUS_DIR/panes/5"
}

# --- pr ---

# pr_mock NUMBER STATE CHECK — drives write_pr_options through mock mode.
pr_mock() {
	rm -f "$NOTIFY_LOG"
	bash "$PR_ENRICH_SCRIPT" --target '$3:@7' --branch feat/x \
		--mock-pr-number "$1" --mock-pr-state "$2" --mock-check-state "$3" \
		--mock-pr-title 'Add the thing'
}

@test "pr: a state flip to merged notifies at info" {
	FAKE_PREV='12|open|success||' pr_mock 12 merged success
	wait_for "$NOTIFY_LOG"
	grep -qF -- '--source pr' "$NOTIFY_LOG"
	grep -qF -- '--level info' "$NOTIFY_LOG"
	grep -qF -- '--window $3:@7' "$NOTIFY_LOG"
	grep -qF -- 'PR merged' "$NOTIFY_LOG"
	grep -qF -- '#12 Add the thing' "$NOTIFY_LOG"
}

@test "pr: a check flip to failure notifies at error, to success at info" {
	FAKE_PREV='12|open|pending||' pr_mock 12 open failure
	wait_for "$NOTIFY_LOG"
	grep -qF -- '--level error' "$NOTIFY_LOG"
	grep -qF -- 'checks failed' "$NOTIFY_LOG"

	FAKE_PREV='12|open|pending||' pr_mock 12 open success
	wait_for "$NOTIFY_LOG"
	grep -qF -- '--level info' "$NOTIFY_LOG"
	grep -qF -- 'checks passed' "$NOTIFY_LOG"
}

@test "pr: the first-ever stamp does NOT notify (none in number, empty state/check)" {
	FAKE_PREV='none|||' pr_mock 12 open success
	assert_silent
	FAKE_PREV='||||' pr_mock 12 merged success
	assert_silent
}

@test "pr: an unchanged re-write does NOT notify" {
	FAKE_PREV='12|open|success||' pr_mock 12 open success
	assert_silent
}

@test "pr: pending, closed and an already-merged re-write do NOT notify" {
	FAKE_PREV='12|open|success||' pr_mock 12 open pending
	assert_silent
	FAKE_PREV='12|open|success||' pr_mock 12 closed success
	assert_silent
	FAKE_PREV='12|merged|success||' pr_mock 12 merged success
	assert_silent
}

@test "pr: an unsubstituted @notify@ disables the call" {
	rm -f "$NOTIFY_LOG"
	FAKE_PREV='12|open|success||' env -u LZTMUX_NOTIFY_BIN bash "$PR_ENRICH_SCRIPT" \
		--target '$3:@7' --branch feat/x --mock-pr-number 12 \
		--mock-pr-state merged --mock-check-state success --mock-pr-title T
	assert_silent
}
