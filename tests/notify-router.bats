#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031 # bats @test blocks run in subshells; export is intentional
# shellcheck disable=SC2016 # tmux formats (#{...}) and $-prefixed targets are literal
# Router unit suite (#164). Every tmux call goes to a FAKE tmux on PATH — a bare
# tmux in a script under test reaches the LIVE server and has clobbered a real
# window in this repo before.
#
# The argv assertions are the point of this suite, not decoration: a
# display-message with no -c from a detached process exits 0 and shows NOTHING,
# so a router that routed correctly and never named a client would look finished
# in review and do nothing in practice.

load helper

setup() {
	export LZTMUX_NOTIFY_DIR="$BATS_TEST_TMPDIR/notify"
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"
	unset TMUX TMUX_PANE

	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		printf '%s\n' "$*" >>"$TMUX_LOG"
		case "$1" in
		display-message)
			# Only the router's single fetch passes -p; the render call does not.
			case "$*" in *" -p "*) printf '%s\n' "$FAKE_INFO" ;; esac
			;;
		list-clients) printf '%s\n' "$FAKE_CLIENTS" ;;
		esac
		exit 0
	EOF
	chmod +x "$FAKEBIN/tmux"
	export PATH="$FAKEBIN:$PATH"
	export FAKE_CLIENTS="/dev/pts/1"
	make_notify_router
}

# One event file must exist; echo its path.
only_event() {
	# Not named `f`: callers assign the result to a scalar `f`, and a local array
	# of the same name makes shellcheck read those scalars as arrays (SC2178).
	local ev=("$LZTMUX_NOTIFY_DIR"/events/*)
	[ "${#ev[@]}" -eq 1 ]
	[ -f "${ev[0]}" ]
	printf '%s' "${ev[0]}"
}

active_info='@7|1|1|$3|1500000000|#ff5555|#fab387|#a6e3a1|#9399b2|mysess'
background_info='@9|0|1|$3|1500000000|#ff5555|#fab387|#a6e3a1|#9399b2|mysess'

# --- the pure gating decision, table-driven ---

@test "notify_route: message only for an active window in an attached session" {
	setup_lib_notify
	notify_route 1 1
	[ "$REPLY" = message ]
	notify_route 1 5
	[ "$REPLY" = message ]
	for pair in "0 1" "1 0" "0 0" "x y" "1 x" "1 ''" "'' 1"; do
		eval "notify_route $pair"
		[ "$REPLY" = history ]
	done
	notify_route 1
	[ "$REPLY" = history ]
	notify_route
	[ "$REPLY" = history ]
}

# --- the message path, asserted on argv ---

@test "message path: routed=message, one display-message per attached client" {
	export FAKE_INFO="$active_info"
	run bash "$NOTIFY_ROUTER" emit --source claude --level error --pane %5 \
		--title 'boom' --body 'processing → error'
	[ "$status" -eq 0 ]
	grep -qx 'routed=message' "$(only_event)"
	[ "$(grep -c 'display-message -c ' "$TMUX_LOG")" -eq 1 ]
	grep -qF -- '-c /dev/pts/1' "$TMUX_LOG"
	grep -qF 'list-clients -t $3' "$TMUX_LOG"
}

@test "message path: two attached clients get exactly two distinct renders" {
	export FAKE_INFO="$active_info"
	export FAKE_CLIENTS="/dev/pts/1
/dev/pts/2"
	run bash "$NOTIFY_ROUTER" emit --source claude --level info --pane %5 --title hi
	[ "$status" -eq 0 ]
	[ "$(grep -c 'display-message -c ' "$TMUX_LOG")" -eq 2 ]
	grep -qF -- '-c /dev/pts/1' "$TMUX_LOG"
	grep -qF -- '-c /dev/pts/2' "$TMUX_LOG"
}

@test "message path: rendered argv carries resolved hex, explicit -d and -C" {
	export FAKE_INFO="$active_info"
	run bash "$NOTIFY_ROUTER" emit --source claude --level error --pane %5 --title boom
	[ "$status" -eq 0 ]
	grep -qF '#[fg=#ff5555]' "$TMUX_LOG"
	grep -qF '#[fg=#9399b2]' "$TMUX_LOG" # locator colour
	# Scoped to the RENDER line only: the router's own single fetch always logs a
	# literal '#{@thm_' (that's the argv it sends tmux to resolve), so an
	# unscoped grep over the whole log is unsatisfiable regardless of correctness.
	# The property that matters is that the RENDERED display-message carries no
	# unresolved format.
	run bash -c "grep 'display-message -c ' \"$TMUX_LOG\" | grep -qF '#{@thm_'"
	[ "$status" -ne 0 ] # never a literal format reference in the rendered line
	grep -qF -- '-d 4000' "$TMUX_LOG"
	grep -qF -- ' -C ' "$TMUX_LOG"
	run bash -c "grep 'display-message -c ' \"$TMUX_LOG\" | grep -qF -- ' -N '"
	[ "$status" -ne 0 ] # -N must never reappear: it is what caused #306
}

@test "message path: bell uses the short dwell" {
	export FAKE_INFO="$active_info"
	run bash "$NOTIFY_ROUTER" emit --source bell --level warn --window @7 --title bell
	[ "$status" -eq 0 ]
	grep -qF -- '-d 1500' "$TMUX_LOG"
}

@test "message path: level colours map info/warn/error" {
	export FAKE_INFO="$active_info"
	bash "$NOTIFY_ROUTER" emit --source pr --level info --pane %5 --title i
	grep -qF '#[fg=#a6e3a1]' "$TMUX_LOG"
	bash "$NOTIFY_ROUTER" emit --source pr --level warn --pane %5 --title w
	grep -qF '#[fg=#fab387]' "$TMUX_LOG"
}

@test "message path: an empty theme value omits the colour, never emits #[fg=]" {
	export FAKE_INFO='@7|1|1|$3|1500000000|||||mysess'
	run bash "$NOTIFY_ROUTER" emit --source claude --level error --pane %5 --title boom
	[ "$status" -eq 0 ]
	run grep -qF '#[fg=]' "$TMUX_LOG"
	[ "$status" -ne 0 ]
	grep -qF 'boom' "$TMUX_LOG"
}

@test "message path: a # in the title is doubled before it reaches the format" {
	export FAKE_INFO="$active_info"
	run bash "$NOTIFY_ROUTER" emit --source pr --level info --pane %5 \
		--title 'PR merged' --body '#42 fix the thing'
	[ "$status" -eq 0 ]
	grep -qF '##42 fix the thing' "$TMUX_LOG"
	# The stored value keeps the single '#' — doubling is display-only.
	grep -qx 'body=#42 fix the thing' "$(only_event)"
}

@test "message path: a % in the title is doubled so strftime cannot eat it" {
	# display-message runs its argument through strftime: an unescaped '%d' in a
	# perfectly ordinary PR title renders the day of the month instead.
	export FAKE_INFO="$active_info"
	run bash "$NOTIFY_ROUTER" emit --source pr --level info --pane %5 \
		--title 'cut build time by 50%' --body '#42 covered 80%d of cases'
	[ "$status" -eq 0 ]
	grep -qF 'cut build time by 50%%' "$TMUX_LOG"
	grep -qF '80%%d of cases' "$TMUX_LOG"
	# Stored values keep the single '%' — escaping is display-only.
	grep -qx 'title=cut build time by 50%' "$(only_event)"
	grep -qx 'body=#42 covered 80%d of cases' "$(only_event)"
}

# --- the background path ---

@test "history path: routed=history and ZERO display-message calls" {
	export FAKE_INFO="$background_info"
	run bash "$NOTIFY_ROUTER" emit --source pr --level info --window '$3:@9' --title 'PR merged'
	[ "$status" -eq 0 ]
	grep -qx 'routed=history' "$(only_event)"
	run grep -q 'display-message -c ' "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "history path: an active window in a detached session still routes to history" {
	export FAKE_INFO='@7|1|0|$3|1500000000|#ff5555|#fab387|#a6e3a1|#9399b2|mysess'
	run bash "$NOTIFY_ROUTER" emit --source claude --level info --pane %5 --title hi
	[ "$status" -eq 0 ]
	grep -qx 'routed=history' "$(only_event)"
	run grep -q 'display-message -c ' "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

# --- the on-disk format ---

@test "event file: exact key set, normalized window, body omitted when empty" {
	export FAKE_INFO="$background_info"
	run bash "$NOTIFY_ROUTER" emit --source bell --level warn --window '$3:@9' --title bell
	[ "$status" -eq 0 ]
	f="$(only_event)"
	run cut -d= -f1 "$f"
	[ "$output" = "ts
source
level
window
session
title
routed" ]
	grep -qx 'window=@9' "$f" # normalized from $3:@9
	grep -qx 'session=mysess' "$f"
	grep -qE '^ts=[0-9]+$' "$f"
}

@test "event file: a title with newlines and control chars is sanitized to one line" {
	export FAKE_INFO="$background_info"
	run bash "$NOTIFY_ROUTER" emit --source claude --level info --window @9 \
		--title "$(printf 'a\nb\tc  d')"
	[ "$status" -eq 0 ]
	f="$(only_event)"
	[ "$(wc -l <"$f")" -eq 7 ]
	grep -qx 'title=a b c d' "$f"
}

@test "event file: filenames sort chronologically" {
	export FAKE_INFO="$background_info"
	bash "$NOTIFY_ROUTER" emit --source pr --level info --window @9 --title first
	bash "$NOTIFY_ROUTER" emit --source pr --level info --window @9 --title second
	cd "$LZTMUX_NOTIFY_DIR/events"
	names=(*)
	[ "${#names[@]}" -eq 2 ]
	grep -qx 'title=first' "${names[0]}"
	grep -qx 'title=second' "${names[1]}"
}

# --- malformed invocations ---

@test "malformed invocations exit 0 and write nothing" {
	export FAKE_INFO="$active_info"
	while read -r args; do
		[ -n "$args" ] || continue
		rm -rf "$LZTMUX_NOTIFY_DIR"
		# timeout catches a `shift 2` spin loudly instead of hanging the suite.
		# shellcheck disable=SC2086 # unquoted on purpose: each line is an argv
		run timeout 5 bash "$NOTIFY_ROUTER" $args
		[ "$status" -eq 0 ]
		[ ! -d "$LZTMUX_NOTIFY_DIR/events" ] ||
			[ -z "$(ls -A "$LZTMUX_NOTIFY_DIR/events")" ]
	done <<-'EOF'
		emit --source toast --level info --pane %5 --title t
		emit --source claude --level debug --pane %5 --title t
		emit --source claude --level info --pane %5
		emit --source claude --level info --title t
		emit --source claude --level info --pane %5 --window @9 --title t
		emit --source claude --level info --pane %5 --title t --bogus x
		emit --source claude --level info --pane
		emit
		list
	EOF
}

@test "an empty tmux resolution writes nothing and exits 0" {
	export FAKE_INFO=""
	run bash "$NOTIFY_ROUTER" emit --source claude --level info --pane %99 --title t
	[ "$status" -eq 0 ]
	[ ! -d "$LZTMUX_NOTIFY_DIR/events" ] || [ -z "$(ls -A "$LZTMUX_NOTIFY_DIR/events")" ]
}

# --- pruning. Mirrors tests/prune-stale-state.bats, backdated `stamp`
# helper included. Called directly: the prune is library behaviour, and the
# router's own call is covered by the emit cases above.

SERVER_START=1500000000

stamp() {
	printf 'x' >"$1"
	touch -t 200001010000 "$1"
}

@test "prune drops events older than server start, keeps fresh ones" {
	setup_lib_log
	setup_lib_notify
	mkdir -p "$NOTIFY_EVENTS_DIR"
	stamp "$NOTIFY_EVENTS_DIR/1400000000-000-8"
	printf 'fresh' >"$NOTIFY_EVENTS_DIR/1900000000-000-9"
	notify_prune "$SERVER_START"
	[ ! -e "$NOTIFY_EVENTS_DIR/1400000000-000-8" ]
	[ -e "$NOTIFY_EVENTS_DIR/1900000000-000-9" ]
	[ "$(cat "$NOTIFY_MARKER")" = "$SERVER_START" ]
	[ ! -d "$NOTIFY_PRUNE_LOCK" ] # released by the EXIT trap
}

@test "prune is a no-op for the same server (marker gate)" {
	setup_lib_log
	setup_lib_notify
	mkdir -p "$NOTIFY_EVENTS_DIR"
	notify_prune "$SERVER_START"
	stamp "$NOTIFY_EVENTS_DIR/1400000000-000-8"
	notify_prune "$SERVER_START"
	[ -e "$NOTIFY_EVENTS_DIR/1400000000-000-8" ]
}

@test "prune re-runs when the server start changes" {
	setup_lib_log
	setup_lib_notify
	mkdir -p "$NOTIFY_EVENTS_DIR"
	notify_prune "$SERVER_START"
	stamp "$NOTIFY_EVENTS_DIR/1400000000-000-8"
	notify_prune $((SERVER_START + 1000))
	[ ! -e "$NOTIFY_EVENTS_DIR/1400000000-000-8" ]
	[ "$(cat "$NOTIFY_MARKER")" = "$((SERVER_START + 1000))" ]
}

@test "prune with an empty server start is a no-op and writes no marker" {
	setup_lib_log
	setup_lib_notify
	mkdir -p "$NOTIFY_EVENTS_DIR"
	stamp "$NOTIFY_EVENTS_DIR/1400000000-000-8"
	notify_prune ""
	[ -e "$NOTIFY_EVENTS_DIR/1400000000-000-8" ]
	[ ! -e "$NOTIFY_MARKER" ]
}
