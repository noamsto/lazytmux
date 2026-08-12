#!/usr/bin/env bats
# cursor-relaunch-hooks-install upserts relaunch-stamp hooks without clobbering unrelated entries.

setup() {
	export CURSOR_HOOKS_FILE="$BATS_TEST_TMPDIR/hooks.json"
	STAMP="$BATS_TEST_TMPDIR/bin/cursor-relaunch-stamp"
	mkdir -p "$(dirname "$STAMP")"
	: >"$STAMP"
	chmod +x "$STAMP"
	INSTALL="$BATS_TEST_DIRNAME/../scripts/cursor-relaunch-hooks-install.sh"
	MODULE="$BATS_TEST_DIRNAME/../modules/home-manager.nix"
}

@test "creates hooks.json from empty and stamps both arrays" {
	run bash "$INSTALL" "$STAMP"
	[ "$status" -eq 0 ]
	[ -f "$CURSOR_HOOKS_FILE" ]

	run jq -r '.hooks.sessionStart[].command' "$CURSOR_HOOKS_FILE"
	[ "$status" -eq 0 ]
	[[ $output == *"$STAMP"* ]]

	run jq -r '.hooks.beforeSubmitPrompt[].command' "$CURSOR_HOOKS_FILE"
	[ "$status" -eq 0 ]
	[[ $output == *"$STAMP"* ]]
}

@test "preserves aeye and cursor-status-hook entries in both arrays" {
	cat >"$CURSOR_HOOKS_FILE" <<'EOF'
{
  "version": 1,
  "hooks": {
    "sessionStart": [
      {"command": "/nix/store/aeye/adapters/cursor/scripts/session-reset.sh", "timeout": 15},
      {"command": "/etc/profiles/per-user/noams/bin/cursor-status-hook cleanup", "timeout": 15},
      {"command": "/etc/profiles/per-user/noams/bin/cursor-status-hook idle", "timeout": 15}
    ],
    "beforeSubmitPrompt": [
      {"command": "/etc/profiles/per-user/noams/bin/cursor-status-hook processing --force", "timeout": 15}
    ]
  }
}
EOF

	run bash "$INSTALL" "$STAMP"
	[ "$status" -eq 0 ]

	# aeye kept in sessionStart
	run jq -r '.hooks.sessionStart[] | select(.command | contains("aeye")) | .command' "$CURSOR_HOOKS_FILE"
	[[ $output == *session-reset.sh* ]]

	# both cursor-status-hook sessionStart entries kept
	count_status_start=$(jq '[.hooks.sessionStart[] | select(.command | contains("/bin/cursor-status-hook"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_status_start" -eq 2 ]

	# cursor-status-hook beforeSubmitPrompt entry kept
	run jq -r '.hooks.beforeSubmitPrompt[] | select(.command | contains("/bin/cursor-status-hook")) | .command' "$CURSOR_HOOKS_FILE"
	[[ $output == *"processing --force"* ]]

	# new stamp entry present exactly once in each array
	count_stamp_start=$(jq '[.hooks.sessionStart[] | select(.command | contains("/bin/cursor-relaunch-stamp"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_stamp_start" -eq 1 ]
	count_stamp_before=$(jq '[.hooks.beforeSubmitPrompt[] | select(.command | contains("/bin/cursor-relaunch-stamp"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_stamp_before" -eq 1 ]
}

@test "upsert is idempotent" {
	cat >"$CURSOR_HOOKS_FILE" <<'EOF'
{
  "version": 1,
  "hooks": {
    "sessionStart": [
      {"command": "/nix/store/aeye/adapters/cursor/scripts/session-reset.sh", "timeout": 15},
      {"command": "/etc/profiles/per-user/noams/bin/cursor-status-hook cleanup", "timeout": 15},
      {"command": "/etc/profiles/per-user/noams/bin/cursor-status-hook idle", "timeout": 15}
    ],
    "beforeSubmitPrompt": [
      {"command": "/etc/profiles/per-user/noams/bin/cursor-status-hook processing --force", "timeout": 15}
    ]
  }
}
EOF

	run bash "$INSTALL" "$STAMP"
	[ "$status" -eq 0 ]
	run bash "$INSTALL" "$STAMP"
	[ "$status" -eq 0 ]

	count_stamp_start=$(jq '[.hooks.sessionStart[] | select(.command | contains("/bin/cursor-relaunch-stamp"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_stamp_start" -eq 1 ]
	count_stamp_before=$(jq '[.hooks.beforeSubmitPrompt[] | select(.command | contains("/bin/cursor-relaunch-stamp"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_stamp_before" -eq 1 ]

	count_aeye=$(jq '[.hooks.sessionStart[] | select(.command | contains("aeye"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_aeye" -eq 1 ]
	count_status_start=$(jq '[.hooks.sessionStart[] | select(.command | contains("/bin/cursor-status-hook"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_status_start" -eq 2 ]
	count_status_before=$(jq '[.hooks.beforeSubmitPrompt[] | select(.command | contains("/bin/cursor-status-hook"))] | length' "$CURSOR_HOOKS_FILE")
	[ "$count_status_before" -eq 1 ]
}

@test "refuses to clobber malformed hooks.json" {
	echo 'not-json' >"$CURSOR_HOOKS_FILE"
	run bash "$INSTALL" "$STAMP"
	[ "$status" -ne 0 ]
	[[ $output == *malformed* ]] || [[ $stderr == *malformed* ]]
	[ "$(cat "$CURSOR_HOOKS_FILE")" = "not-json" ]
}

@test "resumeCursor is opt-in and wires the relaunch-stamp hook install" {
	run sed -n '/resumeCursor = lib.mkOption/,/^      };/p' "$MODULE"
	[ "$status" -eq 0 ]
	[[ $output == *'default = false;'* ]]

	run grep -F 'resumeCursorEnable' "$MODULE"
	[ "$status" -eq 0 ]

	run grep -F 'provisionCursorResumeHook' "$MODULE"
	[ "$status" -eq 0 ]

	run grep -F 'cursor-relaunch-hooks-install' "$MODULE"
	[ "$status" -eq 0 ]

	run grep -F 'profileDirectory}/bin/cursor-relaunch-stamp' "$MODULE"
	[ "$status" -eq 0 ]
}
