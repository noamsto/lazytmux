#!/usr/bin/env bats
load helper

setup() {
	export TMUX_PANE="%7"
	STAMP="$BATS_TEST_DIRNAME/../scripts/codex-relaunch-stamp.sh"
	# Capture tmux invocations instead of running real tmux.
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	mkdir -p "$BATS_TEST_TMPDIR/bin"
	# /bin/sh, not /usr/bin/env: the Nix flake-check sandbox has no /usr/bin.
	cat >"$BATS_TEST_TMPDIR/bin/tmux" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$TMUX_LOG"
EOF
	chmod +x "$BATS_TEST_TMPDIR/bin/tmux"
	export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

@test "stamps @remux_relaunch from session_id on stdin" {
	run bash "$STAMP" <<'EOF'
{"session_id":"019f3b53-487c-7973-8103-8e2828a5fd72","hook_event_name":"SessionStart","source":"startup"}
EOF
	[ "$status" -eq 0 ]
	grep -qF 'set-option -p -t %7 @remux_relaunch codex resume 019f3b53-487c-7973-8103-8e2828a5fd72' "$TMUX_LOG"
}

@test "no-op when TMUX_PANE unset" {
	unset TMUX_PANE
	run bash "$STAMP" <<'EOF'
{"session_id":"019f3b53-487c-7973-8103-8e2828a5fd72"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}

@test "no-op when tmux absent" {
	# Empty PATH so the script's `command -v tmux` fails even with a valid pane
	# + id. Invoke bash by absolute path so emptying PATH doesn't hide bash too.
	mkdir -p "$BATS_TEST_TMPDIR/empty"
	local bash_bin
	bash_bin="$(command -v bash)"
	run env PATH="$BATS_TEST_TMPDIR/empty" "$bash_bin" "$STAMP" <<'EOF'
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
