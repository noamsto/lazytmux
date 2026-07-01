#!/usr/bin/env bats

load helper

setup() {
	export XDG_STATE_HOME="$BATS_TEST_TMPDIR/state"
	export LAZYTMUX_DEBUG_SENTINEL="$BATS_TEST_TMPDIR/debug.on"
	setup_lib_log
}

@test "log_event is a no-op when the sentinel is absent" {
	log_event claude event transition from idle to processing
	[ ! -f "$LAZYTMUX_LOG_FILE" ]
}

@test "log_event writes a JSON line when armed" {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	log_event claude event transition from idle to processing
	run cat "$LAZYTMUX_LOG_FILE"
	[[ $output == *'"cat":"claude"'* ]]
	[[ $output == *'"event":"transition"'* ]]
	[[ $output == *'"from":"idle"'* ]]
	[[ $output == *'"to":"processing"'* ]]
}

@test "all values are quoted (numeric-looking session names stay strings)" {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	log_event claude sess 10 win 2
	run cat "$LAZYTMUX_LOG_FILE"
	[[ $output == *'"sess":"10"'* ]]
	[[ $output == *'"win":"2"'* ]]
}

@test "_json_escape handles backslash, quote, tab, newline, control chars" {
	_json_escape $'a"b\\c\td\ne\x01f'
	[ "$REPLY" = 'a\"b\\c\tdef' ]
}

@test "rotation moves the log to .1 at the cap" {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	export LAZYTMUX_LOG_MAX_BYTES=200
	for i in $(seq 1 20); do log_event t k "value-$i-padding-padding-padding-padding"; done
	[ -f "$LAZYTMUX_LOG_FILE.1" ]
}

@test "acquire_lock creates the lock dir" {
	# Called directly (not via `run`): the EXIT trap fires at test-end, so the
	# dir is still present for the assertion.
	local lock="$BATS_TEST_TMPDIR/x.lock"
	acquire_lock "$lock"
	[ -d "$lock" ]
}

@test "a fresh lock dir (live holder) blocks acquire" {
	local lock="$BATS_TEST_TMPDIR/held.lock"
	mkdir "$lock"
	run acquire_lock "$lock"
	[ "$status" -eq 1 ]
}

@test "acquire_lock steals a stale lock dir" {
	local lock="$BATS_TEST_TMPDIR/stale.lock"
	mkdir "$lock"
	export LAZYTMUX_LOCK_STALE_SECONDS=0
	run acquire_lock "$lock"
	[ "$status" -eq 0 ]
}

@test "acquire_lock clears a leftover plain file (old flock-redirect artifact)" {
	local lock="$BATS_TEST_TMPDIR/leftover.lock"
	: >"$lock"
	acquire_lock "$lock"
	[ -d "$lock" ]
}
