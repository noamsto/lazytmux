#!/usr/bin/env bats

MODULE="$BATS_TEST_DIRNAME/../modules/home-manager.nix"

hook_block() {
	sed -n "/hookBlock = ''/,/^[[:space:]]*'';$/p" "$MODULE"
}

status_provision() {
	sed -n '/provisionCodexStatusHooks =/,/^[[:space:]]*);$/p' "$MODULE"
}

commands_for_event() {
	local event="$1"
	hook_block | awk -v event="$event" '
		$0 ~ "^[[:space:]]*\\[\\[hooks\\." event "\\]\\]$" {
			found = 1
			next
		}
		found && $0 ~ /^[[:space:]]*\[\[hooks\.[[:alpha:]]+\]\]$/ { exit }
		found && $0 ~ /^[[:space:]]*command = / {
			sub(/^[[:space:]]*/, "")
			print
		}
	'
}

assert_event_command() {
	local event="$1" state="$2" suffix="${3:-}" expected csu_ref='$'
	run commands_for_event "$event"
	[ "$status" -eq 0 ]
	csu_ref+='{csu}'
	printf -v expected 'command = "%s %s%s >/dev/null"' "$csu_ref" "$state" "$suffix"
	[ "$output" = "$expected" ]
}

@test "Codex status hooks are opt-in and require the stable profile binary" {
	run sed -n '/codexStatus = {/,/agentIntegration = {/p' "$MODULE"
	[ "$status" -eq 0 ]
	[[ $output == *'default = false;'* ]]

	run grep -F 'programs.lazytmux.codexStatus.enable requires agentIntegration.enable' "$MODULE"
	[ "$status" -eq 0 ]

	run status_provision
	[ "$status" -eq 0 ]
	local dollar='$'
	local config_path="CONFIG=\"${dollar}HOME/.codex/config.toml\""
	local profile_binary="${dollar}{config.home.profileDirectory}/bin/claude-status-update"
	[[ $output == *"$config_path"* ]]
	[[ $output == *"MARKER='# lazytmux-managed: codex status-line hooks'"* ]]
	[[ $output == *"$profile_binary"* ]]
}

@test "Codex hook events map to the supported status states" {
	run commands_for_event SessionStart
	[ "$status" -eq 0 ]
	[ "$output" = $'command = "${csu} cleanup >/dev/null"\ncommand = "${csu} idle >/dev/null"' ]

	for event in PreToolUse PostToolUse PostCompact; do
		assert_event_command "$event" processing
	done

	assert_event_command UserPromptSubmit processing ' --force'
	assert_event_command PermissionRequest waiting
	assert_event_command Stop "done"
	assert_event_command PreCompact compacting
}

@test "Codex hook block does not configure the unsupported Notification event" {
	run hook_block
	[ "$status" -eq 0 ]
	[[ $output != *'[[hooks.Notification]]'* ]]
}
