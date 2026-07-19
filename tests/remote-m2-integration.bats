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
	DST_CONF="$BATS_TEST_TMPDIR/dst.conf"
	printf 'set -g base-index 1\nset -g remain-on-exit on\n' >"$DST_CONF"
	SRC="tmux -L m2src -f /dev/null" # stands in for the "remote"
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
	$SRC resize-pane -t rem.0 -x 60

	# local: pre-created at the same size, one pane — the daemon's
	# convergence step (refresh-client -C) is then a no-op, so the remote's
	# 60/149 split survives untouched.
	$DST new-session -d -s host-sess -x 210 -y 52

	run timeout 10 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 0 --local-sess host-sess \
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
		--session rem --window 0 --local-sess host-sess \
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
		--session rem --window 0 --local-sess host-sess \
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

	$SRC split-window -h -t rem

	# Wait for the reconciled 2-pane mirror.
	for _ in $(seq 1 40); do
		n="$($DST list-panes -t host-sess:1 -F '#{pane_id}' 2>/dev/null | wc -l)"
		[ "$n" -eq 2 ] && break
		sleep 0.1
	done

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	[ "$src_dims" = "$dst_dims" ]
}
