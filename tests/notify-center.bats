#!/usr/bin/env bats
# History center (#164). Seeds event files, runs the materialized center with
# </dev/null so its dismiss-keypress read returns at once, asserts on stdout.
# No tmux at all — the center never calls it.

load helper

setup() {
	export LZTMUX_NOTIFY_DIR="$BATS_TEST_TMPDIR/notify"
	unset TMUX TMUX_PANE
	make_notify_center
	NOW=$(date +%s)
}

# seed NAME TS SOURCE LEVEL WINDOW SESSION TITLE [BODY]
seed() {
	mkdir -p "$LZTMUX_NOTIFY_DIR/events"
	{
		printf 'ts=%s\nsource=%s\nlevel=%s\nwindow=%s\nsession=%s\ntitle=%s\n' \
			"$2" "$3" "$4" "$5" "$6" "$7"
		[ -n "${8:-}" ] && printf 'body=%s\n' "$8"
		printf 'routed=history\n'
	} >"$LZTMUX_NOTIFY_DIR/events/$1"
}

# Everything in the store, names and contents, for the never-writes assertion.
snapshot() {
	local f
	for f in "$LZTMUX_NOTIFY_DIR"/.server_start "$LZTMUX_NOTIFY_DIR"/events/*; do
		[ -e "$f" ] || continue
		printf '%s\n' "$f"
		cat "$f"
	done
}

@test "center: newest first, driven by the epoch-prefixed filename" {
	seed "1700000000-100-1" "$((NOW - 7200))" pr info @1 s1 oldest
	seed "1900000000-100-3" "$((NOW - 30))" claude error @3 s1 newest 'processing → error'
	seed "1800000000-100-2" "$((NOW - 300))" bell warn @2 s1 middle
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	[ "${#lines[@]}" -eq 3 ]
	[[ ${lines[0]} == *newest* ]]
	[[ ${lines[1]} == *middle* ]]
	[[ ${lines[2]} == *oldest* ]]
}

@test "center: one line per event, with age, locator, and body when present" {
	seed "1900000000-100-3" "$((NOW - 30))" claude error @3 mysess 'boom' 'processing → error'
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	[ "${#lines[@]}" -eq 1 ]
	[[ ${lines[0]} == *"30s"* ]]
	[[ ${lines[0]} == *"mysess:@3"* ]]
	[[ ${lines[0]} == *"boom"* ]]
	[[ ${lines[0]} == *"processing → error"* ]]
}

@test "center: a line with no body is still well formed" {
	seed "1900000000-100-3" "$((NOW - 90))" bell warn @3 mysess 'bell'
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	[ "${#lines[@]}" -eq 1 ]
	[[ ${lines[0]} == *"1m"* ]]
	[[ ${lines[0]} == *"mysess:@3 "* ]]
	[[ ${lines[0]} == *"bell"* ]]
}

@test "center: over the row cap, the older line is present and N is correct" {
	# stdout is a pipe here, so stty fails and the fallback applies:
	# rows=20 -> cap=18. 25 events -> 18 shown, 7 older.
	for i in $(seq 10 34); do
		seed "19000000${i}-100-$i" "$NOW" pr info "@$i" s1 "t$i"
	done
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	[ "${#lines[@]}" -eq 19 ]
	[ "${lines[18]}" = "… +7 older" ]
}

@test "center: an empty events dir shows the empty state and exits 0" {
	mkdir -p "$LZTMUX_NOTIFY_DIR/events"
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	[ "$output" = "no notifications" ]
}

@test "center: a missing events dir shows the empty state and exits 0" {
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	[ "$output" = "no notifications" ]
}

@test "center: never writes and never prunes" {
	# The marker holds an epoch far newer than nothing and far older than the
	# seeded files' mtimes: a center that grew a prune would delete them.
	seed "1900000000-100-3" "$NOW" claude error @3 s1 boom
	seed "1900000001-100-4" "$NOW" pr info @4 s1 merged
	printf '1500000000\n' >"$LZTMUX_NOTIFY_DIR/.server_start"
	before="$(snapshot)"
	run bash "$NOTIFY_CENTER" </dev/null
	[ "$status" -eq 0 ]
	after="$(snapshot)"
	[ "$before" = "$after" ]
	[ ! -d "$LZTMUX_NOTIFY_DIR/.prune.lock" ]
}
