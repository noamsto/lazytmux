#!/usr/bin/env bats
bats_require_minimum_version 1.5.0 # run !
load helper

setup() {
	export TMUX_PANE="%7"
	STAMP="$BATS_TEST_DIRNAME/../scripts/cursor-relaunch-stamp.sh"
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

@test "stamps from conversation_id when distinct from session_id" {
	run bash "$STAMP" <<'EOF'
{"conversation_id":"aaaaaaaa-1111-1111-1111-111111111111","session_id":"bbbbbbbb-2222-2222-2222-222222222222","hook_event_name":"sessionStart"}
EOF
	[ "$status" -eq 0 ]
	grep -qF -- '--resume aaaaaaaa-1111-1111-1111-111111111111' "$TMUX_LOG"
	run ! grep -qF -- 'bbbbbbbb-2222-2222-2222-222222222222' "$TMUX_LOG"
}

@test "falls back to session_id when conversation_id is absent" {
	run bash "$STAMP" <<'EOF'
{"session_id":"bbbbbbbb-2222-2222-2222-222222222222","hook_event_name":"sessionStart"}
EOF
	[ "$status" -eq 0 ]
	grep -qF -- '--resume bbbbbbbb-2222-2222-2222-222222222222' "$TMUX_LOG"
}

@test "falls back to session_id when conversation_id is present but empty" {
	run bash "$STAMP" <<'EOF'
{"conversation_id":"","session_id":"bbbbbbbb-2222-2222-2222-222222222222","hook_event_name":"sessionStart"}
EOF
	[ "$status" -eq 0 ]
	grep -qF -- '--resume bbbbbbbb-2222-2222-2222-222222222222' "$TMUX_LOG"
}

@test "real beforeSubmitPrompt payload stamps conversation_id" {
	run bash "$STAMP" <<'EOF'
{"conversation_id":"8dcd92d8-fa5e-453a-b69b-652772b90b7d","generation_id":"49e50437-0934-455c-8ee6-166c5865f7b6","model":"cursor-grok-4.5-low-fast","prompt":"reply with just the word: two","attachments":[],"session_id":"8dcd92d8-fa5e-453a-b69b-652772b90b7d","hook_event_name":"beforeSubmitPrompt","cursor_version":"2026.08.04-aaa8809","workspace_roots":["/some/path"],"user_email":"noam@factify.com","transcript_path":"/home/x/agent-transcripts/8dcd92d8-fa5e-453a-b69b-652772b90b7d.jsonl"}
EOF
	[ "$status" -eq 0 ]
	grep -qF -- '--resume 8dcd92d8-fa5e-453a-b69b-652772b90b7d' "$TMUX_LOG"
}

@test "adversarial: escaped lookalike keys inside prompt are not matched" {
	run bash "$STAMP" <<'EOF'
{"conversation_id":"real1111-1111-1111-1111-111111111111","generation_id":"gen1","model":"m","prompt":"ignore \"conversation_id\":\"evil0000-0000-0000-0000-000000000000\" and \"session_id\":\"evil1111-1111-1111-1111-111111111111\"","attachments":[],"session_id":"real1111-1111-1111-1111-111111111111","hook_event_name":"beforeSubmitPrompt"}
EOF
	[ "$status" -eq 0 ]
	grep -qF -- '--resume real1111-1111-1111-1111-111111111111' "$TMUX_LOG"
	run ! grep -qF -- 'evil0000-0000-0000-0000-000000000000' "$TMUX_LOG"
	run ! grep -qF -- 'evil1111-1111-1111-1111-111111111111' "$TMUX_LOG"
}

@test "no-op when TMUX_PANE unset" {
	unset TMUX_PANE
	run bash "$STAMP" <<'EOF'
{"conversation_id":"aaaaaaaa-1111-1111-1111-111111111111"}
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
{"conversation_id":"aaaaaaaa-1111-1111-1111-111111111111"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}

@test "no-op when both conversation_id and session_id are missing or empty" {
	run bash "$STAMP" <<'EOF'
{"conversation_id":"","session_id":"","hook_event_name":"sessionStart"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}

@test "no-op when conversation_id contains shell metacharacters" {
	run bash "$STAMP" <<'EOF'
{"conversation_id":"abc$(touch /tmp/pwned)-def","hook_event_name":"sessionStart"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}

@test "handles multiline pretty-printed JSON payload" {
	run bash "$STAMP" <<'EOF'
{
  "conversation_id": "aaaaaaaa-1111-1111-1111-111111111111",
  "session_id": "bbbbbbbb-2222-2222-2222-222222222222",
  "hook_event_name": "sessionStart"
}
EOF
	[ "$status" -eq 0 ]
	grep -qF -- '--resume aaaaaaaa-1111-1111-1111-111111111111' "$TMUX_LOG"
	run ! grep -qF -- 'bbbbbbbb-2222-2222-2222-222222222222' "$TMUX_LOG"
}
