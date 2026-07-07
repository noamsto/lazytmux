#!/usr/bin/env bats
load helper

setup() {
	export TMUX_PANE="%7"
	STAMP="$BATS_TEST_DIRNAME/../scripts/codex-relaunch-stamp.sh"
	# Capture tmux invocations instead of running real tmux.
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	mkdir -p "$BATS_TEST_TMPDIR/bin"
	cat >"$BATS_TEST_TMPDIR/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$TMUX_LOG"
EOF
	chmod +x "$BATS_TEST_TMPDIR/bin/tmux"
	export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

@test "stamps @ts_relaunch from session_id on stdin" {
	run bash "$STAMP" <<'EOF'
{"session_id":"019f3b53-487c-7973-8103-8e2828a5fd72","hook_event_name":"SessionStart","source":"startup"}
EOF
	[ "$status" -eq 0 ]
	grep -qF 'set-option -p -t %7 @ts_relaunch codex resume 019f3b53-487c-7973-8103-8e2828a5fd72' "$TMUX_LOG"
}

@test "no-op when TMUX_PANE unset" {
	unset TMUX_PANE
	run bash "$STAMP" <<'EOF'
{"session_id":"019f3b53-487c-7973-8103-8e2828a5fd72"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}

@test "no-op when session_id missing" {
	run bash "$STAMP" <<'EOF'
{"hook_event_name":"SessionStart"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}
