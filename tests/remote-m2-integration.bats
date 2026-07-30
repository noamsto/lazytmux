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
	if [[ -z ${CTL:-} ]]; then
		CTL="$BATS_TEST_TMPDIR/ctl"
		(cd "$BATS_TEST_DIRNAME/../picker" && go build -o "$CTL" ./remotebridge/cmd/ctl)
	fi

	$SRC kill-server 2>/dev/null || true
	$DST kill-server 2>/dev/null || true
}

teardown() {
	$SRC kill-server 2>/dev/null || true
	$DST kill-server 2>/dev/null || true
	tmux -L m2obs kill-server 2>/dev/null || true # pty host for the attached-client test
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

	"$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/d1.sock" \
		>"$BATS_TEST_TMPDIR/d1.log" 2>&1 &
	daemon_pid=$!

	# Gate: wait until the pane is a renderer so the layout pipeline has settled.
	for _ in $(seq 1 40); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# Capture state before killing (SIGTERM triggers teardown → DST session gone).
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	dst_panes="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
	[ "$dst_panes" -eq 2 ]
}

# M1 regression anchor: a single-pane remote window mirrors to a single local
# pane at matching dims, with no split applied.
@test "daemon mirrors a 1-pane remote window with no split (M1 anchor)" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/d2.sock" \
		>"$BATS_TEST_TMPDIR/d2.log" 2>&1 &
	daemon_pid=$!

	# Gate: wait until the pane is a renderer before capturing state.
	for _ in $(seq 1 40); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# Capture state before killing (SIGTERM triggers teardown → DST session gone).
	dst_panes="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$dst_panes" -eq 1 ]
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

	# Capture state before killing (SIGTERM triggers teardown → DST session gone).
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
}

@test "daemon mirrors a 3-window remote session into 3 local windows" {
	$SRC new-session -d -s rem -x 100 -y 30
	$SRC new-window -t rem
	$SRC new-window -t rem
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dm.sock" \
		>"$BATS_TEST_TMPDIR/dm.log" 2>&1 &
	daemon_pid=$!

	# Gate: wait until all 3 windows have a renderer pane.
	for _ in $(seq 1 60); do
		wins="$($DST list-windows -t host-sess -F '#{window_id}' 2>/dev/null | wc -l)"
		[ "$wins" -eq 3 ] && break
		sleep 0.1
	done

	# Capture state before killing (SIGTERM triggers teardown → DST session gone).
	src_wins="$($SRC list-windows -t rem -F '#{window_id}' | wc -l)"
	dst_wins="$($DST list-windows -t host-sess -F '#{window_id}' | wc -l)"
	src_names="$($SRC list-windows -t rem -F '#{window_name}' | sort | tr '\n' ',')"
	dst_names="$($DST list-windows -t host-sess -F '#{@window_bridge_name}' | sort | tr '\n' ',')"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$src_wins" -eq 3 ]
	[ "$dst_wins" -eq 3 ]

	# Each mirror window carries its remote name in @window_bridge_name.
	[ "$dst_names" = "$src_names" ]
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

	# Rename it remotely -> local @window_bridge_name follows (window_name is
	# derived by reflow, which this vanilla tmux -L server does not run).
	newwin="$($SRC list-windows -t rem -F '#{window_id}' | tail -1)"
	$SRC rename-window -t "$newwin" bridged-name
	for _ in $(seq 1 40); do
		names="$($DST list-windows -t host-sess -F '#{@window_bridge_name}' 2>/dev/null)"
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

# Regression for #231: a remote geometry change emits %layout-change without
# new pane output. The geometry-only reconcile must re-seed the renderer so
# its screen stays identical to the remote after the local window is re-fit.
@test "daemon repaints mirrored content after a remote geometry change" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dg.sock" \
		>"$BATS_TEST_TMPDIR/dg.log" 2>&1 &
	daemon_pid=$!

	# Gate on an observable paint rather than pane_current_command: tmux reports
	# renderer process names differently on Darwin. Retry the startup marker so
	# an output emitted while the daemon is still wiring its first pane is not
	# lost to setup's reply reader.
	painted=no
	for _ in $(seq 1 20); do
		$SRC send-keys -t rem "printf 'RESEED_GEOMETRY_9F3Q\\n'" Enter
		for _ in $(seq 1 10); do
			out="$($DST capture-pane -p -t host-sess:1 2>/dev/null)"
			[[ $out == *RESEED_GEOMETRY_9F3Q* ]] && {
				painted=yes
				break 2
			}
			sleep 0.1
		done
	done
	[ "$painted" = yes ]

	# A pty-hosted second remote client starts at the current size, then shrinks.
	# The per-window cap permits this smaller client to resize the remote without
	# producing pane output, which must re-fit and re-seed the local mirror.
	OBS="tmux -L m2obs"
	$OBS new-session -d -s obs -x 100 -y 30 "$SRC attach -t rem"
	for _ in $(seq 1 40); do
		clients="$($SRC list-clients -t rem 2>/dev/null | wc -l | tr -d ' ')"
		[ "$clients" -ge 2 ] && break
		sleep 0.1
	done
	[ "$clients" -ge 2 ]
	initial_dims="$(sorted_dims "$SRC" rem)"
	$OBS resize-window -t obs -x 80 -y 24

	# Let the re-seed frame drain without adding any remote output.
	for _ in $(seq 1 50); do
		src_dims="$(sorted_dims "$SRC" rem)"
		dst_dims="$(sorted_dims "$DST" host-sess:1)"
		[ "$src_dims" != "$initial_dims" ] && [ "$dst_dims" = "$src_dims" ] && break
		sleep 0.1
	done
	src_screen="$($SRC capture-pane -p -t rem)"
	dst_screen="$($DST capture-pane -p -t host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$src_dims" != "$initial_dims" ]
	[ "$dst_dims" = "$src_dims" ]
	[ "$dst_screen" = "$src_screen" ]
}

@test "daemon converges when DST size != SRC size (ConvergeCmd resizes remote)" {
	# remote starts 120x40; local mirror created at 100x30 — the daemon's
	# refresh-client -C must push 100x30 onto the remote so pane dims converge.
	$SRC new-session -d -s rem -x 120 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 100 -y 30

	# Run in the background and poll for convergence — a foreground `run timeout`
	# would fire SIGTERM on timeout, which now triggers teardown (kill-session),
	# taking the DST server down before the assertions below can read it.
	"$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dc.sock" \
		>"$BATS_TEST_TMPDIR/dc.log" 2>&1 &
	daemon_pid=$!

	# Poll until SRC converges to DST's size (100x30).
	for _ in $(seq 1 60); do
		w="$($SRC display-message -p -t rem -F '#{window_width}' 2>/dev/null || echo 0)"
		[ "$w" -eq 100 ] && break
		sleep 0.1
	done

	# Capture dims before killing (teardown kills DST session on SIGTERM).
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
	# And the remote actually shrank to the local width (convergence, not no-op).
	[ "$($SRC display-message -p -t rem -F '#{window_width}' 2>/dev/null)" -eq 100 ]
}

# Regression for #201: a human client attached to the same remote session used
# to win the size negotiation outright — under window-size latest,
# clients_calculate_size skips every client but w->latest, so the bridge's
# whole-client "refresh-client -C WxH" was ignored and the mirror painted a
# screen of the human's size into a pane of ours (garbled, overlapping text).
# The per-window form is a clamp applied after that calculation, so it holds
# regardless of who is latest.
@test "daemon clamps the remote even with a bigger human client attached" {
	# OBS is a third tmux server used purely as a pty host: its pane runs a
	# real `attach` to SRC, which is the only way to give SRC an attached
	# client (of a known size) inside bats.
	OBS="tmux -L m2obs"
	$OBS kill-server 2>/dev/null || true

	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	# The "human": 160 wide, and the last client to touch the window, so
	# w->latest is theirs and not the bridge's.
	$OBS new-session -d -s obs -x 160 -y 50 "$SRC attach -t rem"
	for _ in $(seq 1 40); do
		n="$($SRC list-clients -t rem 2>/dev/null | wc -l | tr -d ' ')"
		[ "$n" -ge 1 ] && break
		sleep 0.1
	done
	[ "$n" -ge 1 ] # the pty client really attached; otherwise this proves nothing
	human_w="$($SRC display-message -p -t rem -F '#{window_width}' 2>/dev/null)"
	[ "$human_w" -eq 160 ] # and it owns the window size before the bridge starts

	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dh.sock" \
		>"$BATS_TEST_TMPDIR/dh.log" 2>&1 &
	daemon_pid=$!

	for _ in $(seq 1 60); do
		w="$($SRC display-message -p -t rem -F '#{window_width}' 2>/dev/null || echo 0)"
		[ "$w" -eq 100 ] && break
		sleep 0.1
	done

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true
	$OBS kill-server 2>/dev/null || true

	# Clamped to the mirror's width despite the wider client owning latest...
	[ "$w" -eq 100 ]
	# ...and the local mirror was fitted to the remote, so the pane the
	# renderer paints into is exactly the screen it receives.
	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
}

# Regression for #185: a SIGTERM'd daemon must run teardown, not leave its unix
# socket + pidfile behind (which blocks the next launch from binding) and not
# orphan the local mirror session. teardown otherwise runs only on %exit/EOF.
@test "daemon removes socket + pidfile and kills the local session on SIGTERM" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	sock="$BATS_TEST_TMPDIR/dt.sock"
	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$sock" >"$BATS_TEST_TMPDIR/dt.log" 2>&1 &
	daemon_pid=$!

	# Wait until it has bound the socket AND written its pidfile.
	for _ in $(seq 1 50); do
		[ -S "$sock" ] && [ -f "$sock.pid" ] && break
		sleep 0.1
	done
	[ -S "$sock" ]
	[ -f "$sock.pid" ]

	kill -TERM "$daemon_pid"
	wait "$daemon_pid" 2>/dev/null || true

	[ ! -e "$sock" ]
	[ ! -e "$sock.pid" ]
	run $DST has-session -t host-sess
	[ "$status" -ne 0 ]
}

# Regression for #183: with the default pause-after, live %output produced
# AFTER the renderer is wired must still paint into the mirror. The dims-only
# cases above never read pane CONTENT, so they can't catch a frozen stream:
# tmux pauses the pane ~1s after attach and the %pause/%continue re-seed must
# actually resume output. Assert real content lands in the local pane.
@test "daemon paints live remote output after the renderer is wired (pause-after default)" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dp.sock" \
		>"$BATS_TEST_TMPDIR/dp.log" 2>&1 &
	daemon_pid=$!

	# Gate: renderer wired (daemon in its main loop, pause-after armed).
	for _ in $(seq 1 50); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# Output produced strictly after wiring — past the initial seed, so only the
	# live stream (or a %continue re-seed) can paint it.
	$SRC send-keys -t rem 'echo LIVEPAINT_9F3Q' Enter

	painted=no
	for _ in $(seq 1 50); do
		out="$($DST capture-pane -p -t host-sess:1 2>/dev/null)"
		[[ $out == *LIVEPAINT_9F3Q* ]] && {
			painted=yes
			break
		}
		sleep 0.15
	done

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$painted" = yes ]
}

# pane_map prints TARGET's panes in pane_index order, one id per line: the
# remote's own #{pane_id} for SRC, the mirror's #{@bridge_pane} carrier for DST.
# Comparing the two ORDERED lists asserts the mirror's core invariant — local
# pane i renders remote pane i — and sidesteps the base-index difference between
# the servers (SRC/DST run pane-base-index 1; the daemon forces 0 on every mirror
# window), since list-panes emits in index order either way.
pane_map() {
	$1 list-panes -t "$2" -F "$3"
}

# bridge_up starts a daemon mirroring SRC session "rem" into DST "host-sess" and
# blocks until the mirror is actually live. Sets `daemon_pid` and `sock`.
#
# It deliberately does NOT gate on pane_current_command containing "renderer":
# tmux reports renderer process names differently on Darwin, so that gate never
# matches there (the same reason PR #233 switched its own gate to an observable
# paint). The pre-existing cases in this file only *poll* that condition and fall
# through, so they survive it; a case that asserts it fails on Darwin only.
#
# Two portable observables instead:
#   1. every mirror pane carries @bridge_pane, which the daemon stamps itself;
#   2. output typed on the remote actually paints into the mirror, which is what
#      proves the daemon reached its main read loop and the router is wired — the
#      precondition every case here needs before it touches the remote.
#
# Both waits are bounded by ONE deadline, BRIDGE_UP_BUDGET_SECS, and the moment it
# passes the call fails with bridge_up_failed diagnostics. A CI stall must cost
# this suite seconds per case, not minutes: nine cases each silently burning a
# long per-observable timeout is how a missing observable turns into a
# half-hour job.
BRIDGE_UP_BUDGET_SECS=12

bridge_up() {
	local want_panes="$1" tag="$2"
	local marker="BRIDGEUP_${tag}_$$"
	local deadline=$((SECONDS + BRIDGE_UP_BUDGET_SECS))
	sock="$BATS_TEST_TMPDIR/$tag.sock"
	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$sock" \
		>"$BATS_TEST_TMPDIR/$tag.log" 2>&1 &
	daemon_pid=$!

	local stamped=0
	while [ "$SECONDS" -lt "$deadline" ]; do
		stamped="$($DST list-panes -t host-sess:1 -F '#{@bridge_pane}' 2>/dev/null | grep -c '^%' || true)"
		[ "$stamped" -eq "$want_panes" ] && break
		sleep 0.1
	done
	if [ "$stamped" -ne "$want_panes" ]; then
		bridge_up_failed "$tag" "only $stamped/$want_panes mirror panes carry @bridge_pane after ${BRIDGE_UP_BUDGET_SECS}s"
		return 1
	fi

	# Re-send the marker each round: output produced while setup's own reply reader
	# is still running is dropped rather than routed, so a single send can be lost.
	while [ "$SECONDS" -lt "$deadline" ]; do
		$SRC send-keys -t rem "printf '$marker\\n'" Enter
		local _inner
		for _inner in $(seq 1 10); do
			if mirror_contains "$want_panes" "$marker"; then
				return 0
			fi
			sleep 0.1
		done
	done
	bridge_up_failed "$tag" "mirror never painted the startup marker within ${BRIDGE_UP_BUDGET_SECS}s"
	return 1
}

# mirror_contains reports whether any of the first $1 mirror panes shows $2.
# capture-pane takes one pane, and the remote's active pane (hence the pane the
# marker lands in) is not necessarily index 0 — split-window makes the new pane
# active — so check them all.
mirror_contains() {
	local panes="$1" needle="$2" i
	for ((i = 0; i < panes; i++)); do
		if $DST capture-pane -p -t "host-sess:1.$i" 2>/dev/null | grep -q "$needle"; then
			return 0
		fi
	done
	return 1
}

# bridge_up_failed prints why the gate gave up plus the daemon's own log, so a
# CI failure here is diagnosable without a re-run.
bridge_up_failed() {
	printf 'bridge_up(%s): %s\n--- daemon log ---\n' "$1" "$2" >&3
	tail -40 "$BATS_TEST_TMPDIR/$1.log" >&3 2>/dev/null || true
}

# Regression for the pre-existing reconcile hole M2.3 had to close: layout
# traversal order means a split of a NON-LAST pane is a mid-list INSERT
# (measured: %0 %1 %2 split at %0 -> %0 %3 %1 %2), which the old three-case
# reconcile (identical / tail-append / tail-removal) classified as an
# "unsupported pane reshuffle" and skipped — leaving the mirror silently stale.
@test "daemon reconciles a mid-list remote split (non-last pane)" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -h -t rem
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 150 -y 40

	bridge_up 3 dmi

	# Split the FIRST remote pane: a mid-list insert, not a tail-append.
	first="$($SRC list-panes -t rem -F '#{pane_id}' | head -1)"
	$SRC split-window -v -t "$first"

	for _ in $(seq 1 60); do
		src_map="$(pane_map "$SRC" rem '#{pane_id}')"
		dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
		[ "$src_map" = "$dst_map" ] && break
		sleep 0.15
	done

	src_map="$(pane_map "$SRC" rem '#{pane_id}')"
	dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	# 4 panes, in the remote's order, each wired to the right remote pane.
	[ "$(printf '%s\n' "$src_map" | wc -l)" -eq 4 ]
	[ "$src_map" = "$dst_map" ]
	[ "$src_dims" = "$dst_dims" ]
}

# The mirror-image hole: killing a non-last pane is a mid-list REMOVAL.
@test "daemon reconciles a mid-list remote kill (non-last pane)" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -h -t rem
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 150 -y 40

	bridge_up 3 dmk

	# Kill the MIDDLE remote pane.
	mid="$($SRC list-panes -t rem -F '#{pane_id}' | sed -n 2p)"
	$SRC kill-pane -t "$mid"

	for _ in $(seq 1 60); do
		src_map="$(pane_map "$SRC" rem '#{pane_id}')"
		dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
		[ "$src_map" = "$dst_map" ] && break
		sleep 0.15
	done

	src_map="$(pane_map "$SRC" rem '#{pane_id}')"
	dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$(printf '%s\n' "$src_map" | wc -l)" -eq 2 ]
	[ "$src_map" = "$dst_map" ]
	# the killed pane is gone from the mirror's carriers
	survivor_hit="$(printf '%s\n' "$dst_map" | grep -cx "$mid" || true)"
	[ "$survivor_hit" -eq 0 ]
}

# A remote swap-pane permutes the pane set without changing its membership —
# neither a tail-append nor a tail-removal, so the old reconcile skipped it and
# each pane kept painting at its old cell. Asserts IDENTITY (which local pane
# carries which remote pane) and CONTENT (the marker moved with its pane), not
# just geometry: dims alone cannot tell a correct permutation from a no-op.
@test "daemon reconciles a remote swap-pane, content following the pane" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 150 -y 40

	bridge_up 2 dsw

	# Mark the FIRST remote pane, and wait for the marker to reach the mirror.
	first="$($SRC list-panes -t rem -F '#{pane_id}' | head -1)"
	$SRC send-keys -t "$first" 'echo SWAPMARK_7K2' Enter
	for _ in $(seq 1 60); do
		out="$($DST capture-pane -p -t host-sess:1.0 2>/dev/null)"
		[[ $out == *SWAPMARK_7K2* ]] && break
		sleep 0.15
	done
	[[ $out == *SWAPMARK_7K2* ]]

	# Swap it with the other pane on the remote.
	$SRC swap-pane -t "$first" -D

	for _ in $(seq 1 60); do
		src_map="$(pane_map "$SRC" rem '#{pane_id}')"
		dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
		[ "$src_map" = "$dst_map" ] && [ "$(printf '%s\n' "$src_map" | head -1)" != "$first" ] && break
		sleep 0.15
	done

	src_map="$(pane_map "$SRC" rem '#{pane_id}')"
	dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
	# the marked pane is now second on both sides; its content must be there too
	moved="$($DST capture-pane -p -t host-sess:1.1 2>/dev/null)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	# the permutation really happened on the remote...
	[ "$(printf '%s\n' "$src_map" | head -1)" != "$first" ]
	# ...the mirror's carrier order follows it...
	[ "$src_map" = "$dst_map" ]
	# ...and the content rode along with its pane.
	[[ $moved == *SWAPMARK_7K2* ]]
}

# === M2.3: structural input (ctl -> daemon -> remote -> mirror) ===
#
# These drive the ctl binary directly against the daemon's socket, which is the
# right seam here: the bats servers run vanilla configs with no lazytmux
# keybindings, so there is nothing for a gate to intercept. The tmux-config half
# of M2.3 (the if-shell gates on @bridge_win/@bridge_pane) is therefore NOT
# covered by these tests — see tests/tmux-next38-readiness.bats for the parts of
# it that CI can see, and the PR body for what only a two-machine run can.

# remote_pane_of prints the remote pane id the mirror's local pane $1 renders,
# i.e. the @bridge_pane carrier a real keybind would pass to ctl.
remote_pane_of() {
	$DST display-message -p -t "host-sess:1.$1" -F '#{@bridge_pane}'
}

@test "ctl split-h splits the REMOTE pane and the mirror follows" {
	$SRC new-session -d -s rem -x 150 -y 40
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 1 c1

	# The daemon stamps the socket on the mirror session, so a keybind can find it.
	[ "$($DST show-options -v -t host-sess @bridge_sock)" = "$sock" ]
	# ...and tags the window, which is what gates the bindings.
	[ "$($DST show-options -w -v -t host-sess:1 @bridge_win)" = "1" ]

	pane="$(remote_pane_of 0)"
	[ -n "$pane" ]

	run "$CTL" --sock "$sock" split-h "$pane"
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		src_n="$($SRC list-panes -t rem -F '#{pane_id}' | wc -l)"
		dst_n="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"
		[ "$src_n" -eq 2 ] && [ "$dst_n" -eq 2 ] && break
		sleep 0.15
	done

	src_n="$($SRC list-panes -t rem -F '#{pane_id}' | wc -l)"
	src_map="$(pane_map "$SRC" rem '#{pane_id}')"
	dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	# The split happened on the REMOTE, not just locally.
	[ "$src_n" -eq 2 ]
	# ...and the mirror wired the new pane to it, at matching dims.
	[ "$src_map" = "$dst_map" ]
	[ "$src_dims" = "$dst_dims" ]
}

@test "ctl resize resizes the REMOTE pane and the mirror converges" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -v -t rem
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 2 c2

	pane="$(remote_pane_of 0)"
	before="$($SRC display-message -p -t "$pane" -F '#{pane_height}')"

	run "$CTL" --sock "$sock" resize "$pane" U 5
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		after="$($SRC display-message -p -t "$pane" -F '#{pane_height}')"
		[ "$after" != "$before" ] && [ "$(sorted_dims "$DST" host-sess:1)" = "$(sorted_dims "$SRC" rem)" ] && break
		sleep 0.15
	done

	after="$($SRC display-message -p -t "$pane" -F '#{pane_height}')"
	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$after" != "$before" ] # the remote really resized
	[ "$src_dims" = "$dst_dims" ]
}

@test "ctl swap permutes the REMOTE panes and the mirror's wiring follows" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 2 c3

	first="$(remote_pane_of 0)"

	run "$CTL" --sock "$sock" swap "$first" D
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		src_map="$(pane_map "$SRC" rem '#{pane_id}')"
		dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"
		[ "$(printf '%s\n' "$src_map" | head -1)" != "$first" ] && [ "$src_map" = "$dst_map" ] && break
		sleep 0.15
	done

	src_map="$(pane_map "$SRC" rem '#{pane_id}')"
	dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$(printf '%s\n' "$src_map" | head -1)" != "$first" ]
	[ "$src_map" = "$dst_map" ]
}

@test "ctl kill-pane kills the REMOTE pane and the mirror loses it" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 2 c4

	victim="$(remote_pane_of 1)"

	run "$CTL" --sock "$sock" kill-pane "$victim"
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		src_n="$($SRC list-panes -t rem -F '#{pane_id}' | wc -l)"
		dst_n="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"
		[ "$src_n" -eq 1 ] && [ "$dst_n" -eq 1 ] && break
		sleep 0.15
	done

	src_n="$($SRC list-panes -t rem -F '#{pane_id}' | wc -l)"
	src_map="$(pane_map "$SRC" rem '#{pane_id}')"
	dst_map="$(pane_map "$DST" host-sess:1 '#{@bridge_pane}')"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$src_n" -eq 1 ]
	[ "$src_map" = "$dst_map" ]
}

@test "ctl new-window creates a REMOTE window and the mirror gains one" {
	$SRC new-session -d -s rem -x 150 -y 40
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 1 c5

	pane="$(remote_pane_of 0)"

	run "$CTL" --sock "$sock" new-window "$pane"
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		src_w="$($SRC list-windows -t rem -F '#{window_id}' | wc -l)"
		dst_w="$($DST list-windows -t host-sess -F '#{window_id}' | wc -l)"
		[ "$src_w" -eq 2 ] && [ "$dst_w" -eq 2 ] && break
		sleep 0.15
	done

	src_w="$($SRC list-windows -t rem -F '#{window_id}' | wc -l)"
	dst_w="$($DST list-windows -t host-sess -F '#{window_id}' | wc -l)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$src_w" -eq 2 ]
	[ "$dst_w" -eq 2 ]
}

@test "ctl rename renames the REMOTE window and @window_bridge_name follows" {
	$SRC new-session -d -s rem -x 150 -y 40
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 1 c6

	pane="$(remote_pane_of 0)"

	run "$CTL" --sock "$sock" rename "$pane" "ctl renamed"
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		src_name="$($SRC display-message -p -t rem:1 -F '#{window_name}')"
		dst_name="$($DST show-options -w -v -t host-sess:1 @window_bridge_name 2>/dev/null || true)"
		[ "$src_name" = "ctl renamed" ] && [ "$dst_name" = "ctl renamed" ] && break
		sleep 0.15
	done

	src_name="$($SRC display-message -p -t rem:1 -F '#{window_name}')"
	dst_name="$($DST show-options -w -v -t host-sess:1 @window_bridge_name 2>/dev/null || true)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$src_name" = "ctl renamed" ]
	[ "$dst_name" = "ctl renamed" ]
}

@test "ctl focus moves the REMOTE active pane without oscillating" {
	$SRC new-session -d -s rem -x 150 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 2 c7

	target="$(remote_pane_of 1)"
	$SRC select-pane -t "$(remote_pane_of 0)"

	run "$CTL" --sock "$sock" focus "$target" 1
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		active="$($SRC display-message -p -t rem -F '#{pane_id}')"
		[ "$active" = "$target" ] && break
		sleep 0.15
	done

	active="$($SRC display-message -p -t rem -F '#{pane_id}')"
	# Settle, then confirm it stayed put rather than ping-ponging.
	sleep 1
	still="$($SRC display-message -p -t rem -F '#{pane_id}')"
	dst_panes="$($DST list-panes -t host-sess:1 -F '#{pane_id}' | wc -l)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$active" = "$target" ]
	[ "$still" = "$target" ]
	[ "$dst_panes" -eq 2 ]
}

# The queue-safety guarantee: a gated keypress against a dead daemon must fail
# fast and non-zero, never stall tmux's command queue on a dial that hangs.
@test "ctl against a dead socket fails fast and non-zero" {
	dead="$BATS_TEST_TMPDIR/dead.sock"
	start=$(date +%s)
	run "$CTL" --sock "$dead" split-h '%1'
	elapsed=$(($(date +%s) - start))
	[ "$status" -ne 0 ]
	[ "$elapsed" -lt 5 ]
	[[ $output == *"unreachable"* ]]
}

# An unmirrored pane id must be refused rather than acted on.
@test "ctl refuses a pane this bridge does not mirror" {
	$SRC new-session -d -s rem -x 150 -y 40
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 1 c8

	run "$CTL" --sock "$sock" split-h '%999'
	status_seen="$status"
	output_seen="$output"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$status_seen" -ne 0 ]
	[[ $output_seen == *"not mirrored"* ]]
}
