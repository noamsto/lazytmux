#!/usr/bin/env bats
# shellcheck disable=SC2016 # grep patterns intentionally contain literal Nix interpolation.

MODULE="$BATS_TEST_DIRNAME/../modules/home-manager.nix"

@test "codex status hooks use the verified first-scope event vocabulary" {
	run grep -F '[[hooks.SessionStart]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.UserPromptSubmit]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.PreToolUse]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.PostToolUse]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.Notification]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.Stop]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.PreCompact]]' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F '[[hooks.SubagentStop]]' "$MODULE"
	[ "$status" -eq 0 ]

	run grep -F '[[hooks.PermissionRequest]]' "$MODULE"
	[ "$status" -ne 0 ]
	run grep -F '[[hooks.PostCompact]]' "$MODULE"
	[ "$status" -ne 0 ]
}

@test "codex status hooks route through the plugin status wrapper" {
	run grep -F 'status = "${../claude-plugin}/scripts/status.sh";' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F 'hookCommand = state: "PATH=${config.home.profileDirectory}/bin:$PATH ${status} ${state} >/dev/null";' "$MODULE"
	[ "$status" -eq 0 ]

	run grep -F 'command = "${hookCommand "idle"}"' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F 'command = "${hookCommand "processing"}"' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F 'command = "${hookCommand "waiting"}"' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F 'command = "${hookCommand "done"}"' "$MODULE"
	[ "$status" -eq 0 ]
	run grep -F 'command = "${hookCommand "compacting"}"' "$MODULE"
	[ "$status" -eq 0 ]
}

@test "codex status hooks do not implement deferred payload-dependent work" {
	run grep -F 'matcher = "permission_prompt"' "$MODULE"
	[ "$status" -ne 0 ]
	run grep -F 'matcher = "idle_prompt"' "$MODULE"
	[ "$status" -ne 0 ]
	run grep -F 'status.sh task' "$MODULE"
	[ "$status" -ne 0 ]
	run grep -F 'transcript_path' "$MODULE"
	[ "$status" -ne 0 ]
}
