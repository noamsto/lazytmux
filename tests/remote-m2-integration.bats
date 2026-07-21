#!/usr/bin/env bats
# Offline M2.1 daemon integration: mirror a local "remote" tmux window into a
# local "host" tmux window via the daemon's --test-local seam (two separate
# tmux -L servers, no ssh). DAEMON / RENDERER are prebuilt absolute store
# paths (flake.nix); fall back to `go build` for local runs.
#
# TMUX_TMPDIR is a short, fixed /tmp dir rather than $BATS_TEST_TMPDIR: tmux
# -L resolves to "$TMUX_TMPDIR/tmux-<uid>/<name>", and a long bats tmpdir
# path pushes that past the unix socket 108-char limit ("File name too
# long"). DST_CONF sets base-index 1 (the daemon hardcodes local window
# ":1", matching the real lazytmux host convention) and remain-on-exit on:
# once the daemon exits (timeout/kill), every renderer's socket connection
# drops and its pane's command exits, and without remain-on-exit the pane —
# then the window, then the last-session server — would tear itself down
# before the assertions below get to read pane dims.

setup() {
	export TMUX_TMPDIR="/tmp/lztmux-m2-bats-$$"
	rm -rf "$TMUX_TMPDIR"
	mkdir -p "$TMUX_TMPDIR"
	# DST sets global pane-base-index 1, matching the real host's render
	# config (this bit the M2.1 smoke test): spawnRenderer/kill-pane target
	# the local pane by 0-based loop index (daemon.go), which now relies on
	# the daemon stamping a window-level pane-base-index 0 override on every
	# mirror window to stay correct despite the global 1. pane-border-status
	# alone still eats a row per pane regardless of pane-base-index, so DST
	# needs it to match SRC's dims.
	DST_CONF="$BATS_TEST_TMPDIR/dst.conf"
	printf 'set -g base-index 1\nset -g pane-base-index 1\nset -g status on\nset -g pane-border-status top\nset -g remain-on-exit on\n' >"$DST_CONF"
	SRC_CONF="$BATS_TEST_TMPDIR/src.conf"
	printf 'set -g base-index 1\nset -g pane-base-index 1\nset -g status on\nset -g pane-border-status top\n' >"$SRC_CONF"
	SRC="tmux -L m2src -f $SRC_CONF" # stands in for the "remote", full render config
	DST="tmux -L m2dst -f $DST_CONF" # the local mirror target

	if [[ -z ${DAEMON:-} ]]; then
		DAEMON="$BATS_TEST_TMPDIR/daemon"
		(cd "$BATS_TEST_DIRNAME/../picker" && go build -o "$DAEMON" ./remotebridge/cmd/daemon)
	fi
	if [[ -z ${RENDERER:-} ]]; then
		RENDERER="$BATS_TEST_TMPDIR/renderer"
		(cd "$BATS_TEST_DIRNAME/../picker" && go build -o "$RENDERER" ./remotebridge/cmd/renderer)
	fi

	$SRC kill-server 2>/dev/null || true
	$DST kill-server 2>/dev/null || true
}

teardown() {
	$SRC kill-server 2>/dev/null || true
	$DST kill-server 2>/dev/null || true
	rm -rf "$TMUX_TMPDIR"
}

# sorted_dims prints TARGET_ARGS's pane dims, one "WxH" per line, sorted —
# used to compare SRC's and DST's pane sets independent of pane order.
sorted_dims() {
	$1 list-panes -t "$2" -F '#{pane_width}x#{pane_height}' | sort
}

@test "daemon mirrors a 2-pane remote window with matching pane dims" {
	# remote: a 210x52 window, uneven horizontal split.
	$SRC new-session -d -s rem -x 210 -y 52
	$SRC split-window -h -t rem
	$SRC resize-pane -t rem.1 -x 60

	# local: pre-created at the same size, one pane — the daemon's
	# convergence step (refresh-client -C) is then a no-op, so the remote's
	# 60/149 split survives untouched.
	$DST new-session -d -s host-sess -x 210 -y 52

	run timeout 10 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/d1.sock"
	[ "$status" -eq 0 ] || [ "$status" -eq 124 ] # 124 = timeout; the daemon stays up

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]

	dst_panes="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"
	[ "$dst_panes" -eq 2 ]
}

# M1 regression anchor: a single-pane remote window mirrors to a single local
# pane at matching dims, with no split applied.
@test "daemon mirrors a 1-pane remote window with no split (M1 anchor)" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	run timeout 10 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/d2.sock"
	[ "$status" -eq 0 ] || [ "$status" -eq 124 ]

	dst_panes="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"
	[ "$dst_panes" -eq 1 ]

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	[ "$src_dims" = "$dst_dims" ]
}

# Should-have: exercises the reconcile path (daemon.go reconcileLayout) —
# a remote split mid-session must land a matching pane locally.
@test "daemon reconciles a mid-session remote split" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/d3.sock" >"$BATS_TEST_TMPDIR/d3.log" 2>&1 &
	daemon_pid=$!

	# Wait for the daemon to have wired the renderer into pane 0 (its
	# respawn-pane replaces the pane's shell) before splitting the remote —
	# a split fired before the daemon reaches its main read loop is a
	# %layout-change that arrives mid-setup and is silently skipped (readReply
	# only consumes reply blocks, discarding async notifications), so this
	# gate (not just "pane count == 1", which is trivially true from the
	# window's initial shell pane) is what makes the timing deterministic.
	for _ in $(seq 1 40); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# An even split (tmux's default) can't distinguish a correct reconcile
	# (re-applying select-layout with the remote's L.Raw) from a broken one
	# that only fixes up the pane count — both land at the same geometry.
	# Resize uneven, mirroring case 1, so the assertion is load-bearing.
	$SRC split-window -h -t rem
	$SRC resize-pane -t rem.1 -x 30

	# Wait for the reconciled 2-pane mirror at matching (uneven) dims.
	for _ in $(seq 1 40); do
		n="$($DST list-panes -t host-sess:1 -F '#{pane_id}' 2>/dev/null | wc -l)"
		if [ "$n" -eq 2 ] && [ "$(sorted_dims "$DST" host-sess:1)" = "$(sorted_dims "$SRC" rem)" ]; then
			break
		fi
		sleep 0.1
	done

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
}

@test "daemon mirrors a 3-window remote session into 3 local windows" {
	$SRC new-session -d -s rem -x 100 -y 30
	$SRC new-window -t rem
	$SRC new-window -t rem
	$DST new-session -d -s host-sess -x 100 -y 30

	run timeout 6 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dm.sock"
	[ "$status" -eq 0 ] || [ "$status" -eq 124 ]

	src_wins="$($SRC list-windows -t rem -F '#{window_id}' | wc -l)"
	dst_wins="$($DST list-windows -t host-sess -F '#{window_id}' | wc -l)"
	[ "$src_wins" -eq 3 ]
	[ "$dst_wins" -eq 3 ]
}

@test "daemon reflects remote new-window / rename-window / kill-window" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dr.sock" \
		>"$BATS_TEST_TMPDIR/dr.log" 2>&1 &
	daemon_pid=$!

	# Gate: wait until the first window's pane is a renderer (daemon in its loop).
	for _ in $(seq 1 40); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# Add a remote window -> a new local window appears.
	$SRC new-window -t rem
	for _ in $(seq 1 40); do
		n="$($DST list-windows -t host-sess -F '#{window_id}' 2>/dev/null | wc -l)"
		[ "$n" -eq 2 ] && break
		sleep 0.1
	done
	[ "$n" -eq 2 ]

	# Gate: wait until the new window's own pipeline has also settled (its
	# pane is a renderer) before renaming — a rename fired while the
	# window-add's own reply round-trip is still in flight races the
	# non-routing-aware reader used during that pipeline and gets dropped.
	for _ in $(seq 1 40); do
		cmd2="$($DST list-panes -t host-sess:2 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd2 == *renderer* ]] && break
		sleep 0.1
	done

	# Rename it remotely -> local window name follows.
	newwin="$($SRC list-windows -t rem -F '#{window_id}' | tail -1)"
	$SRC rename-window -t "$newwin" bridged-name
	for _ in $(seq 1 40); do
		names="$($DST list-windows -t host-sess -F '#{window_name}' 2>/dev/null)"
		[[ $names == *bridged-name* ]] && break
		sleep 0.1
	done
	[[ $names == *bridged-name* ]]

	# Make window 1 active before killing window 2, so the kill targets a
	# NON-active window: it emits a clean %window-close with no concurrent
	# %session-window-changed / %layout-change reconcile. Killing the ACTIVE
	# window can interleave %window-close with a %layout-change round-trip whose
	# routing-aware reader swallows the close (a known async-notification
	# limitation, tracked as an M2.3 follow-up) — that races under CI load.
	$SRC select-window -t rem:1

	# Kill the added remote window -> its local window goes away (session survives).
	$SRC kill-window -t "$newwin"
	for _ in $(seq 1 40); do
		n="$($DST list-windows -t host-sess -F '#{window_id}' 2>/dev/null | wc -l)"
		[ "$n" -eq 1 ] && break
		sleep 0.1
	done
	[ "$n" -eq 1 ]

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true
}

@test "daemon re-converges the remote after a local resize" {
	# Bring up a 1-window mirror at 100x30 (SRC == DST), then RESIZE the local
	# window to 120x40. The resize watcher polls the local size and must push
	# the new dims onto the remote (refresh-client -C), so SRC's window converges
	# to 120x40 without any control-stream event driving it.
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dz.sock" \
		>"$BATS_TEST_TMPDIR/dz.log" 2>&1 &
	daemon_pid=$!

	# Gate: wait until the pane is a renderer (daemon reached its main loop and
	# the watcher goroutine is running) before resizing.
	for _ in $(seq 1 40); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# resize-window sticks on the detached DST session (no attached client to
	# override it under window-size latest), so the local mirror is now 120x40.
	$DST resize-window -t host-sess:1 -x 120 -y 40

	# Poll until the watcher (1s interval) pushes the new size to the remote.
	for _ in $(seq 1 40); do
		dims="$($SRC display-message -p -t rem -F '#{window_width}x#{window_height}' 2>/dev/null)"
		[ "$dims" = "120x40" ] && break
		sleep 0.1
	done

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$dims" = "120x40" ]
}

@test "daemon converges when DST size != SRC size (ConvergeCmd resizes remote)" {
	# remote starts 120x40; local mirror created at 100x30 — the daemon's
	# refresh-client -C must push 100x30 onto the remote so pane dims converge.
	$SRC new-session -d -s rem -x 120 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 100 -y 30

	run timeout 6 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dc.sock"
	[ "$status" -eq 0 ] || [ "$status" -eq 124 ]

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
	# And the remote actually shrank to the local width (convergence, not no-op).
	[ "$($SRC display-message -p -t rem -F '#{window_width}')" -eq 100 ]
}
