#!/usr/bin/env bats

MODULE="$BATS_TEST_DIRNAME/../modules/home-manager.nix"

hook_block() {
	sed -n "/hookBlock = ''/,/^[[:space:]]*'';$/p" "$MODULE"
}

status_provision() {
	sed -n '/provisionCodexStatusHooks =/,/^[[:space:]]*);$/p' "$MODULE"
}

resume_provision() {
	sed -n '/provisionCodexResumeHook =/,/provisionCodexStatusHooks =/p' "$MODULE"
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

	run grep -F 'programs.tmux-og.codexStatus.enable requires agentIntegration.enable' "$MODULE"
	[ "$status" -eq 0 ]

	run status_provision
	[ "$status" -eq 0 ]
	local block="$output"
	local dollar='$'
	local config_path="CONFIG=\"${dollar}HOME/.codex/config.toml\""
	local profile_binary="${dollar}{config.home.profileDirectory}/bin/claude-status-update"
	[[ $block == *"$config_path"* ]]
	# Anchored, not a substring glob: the block also carries a LEGACY_MARKER=
	# line for the old spelling, and `LEGACY_MARKER=` ends with `MARKER=`, so a
	# substring match would be satisfied by the legacy line and stop guarding
	# the marker this module actually writes.
	grep -qE "^[[:space:]]*MARKER='# tmux-og-managed: codex status-line hooks'\$" <<<"$block"
	# The pre-rename spelling survives as a recognised alternative so a machine
	# whose config.toml predates the rename is still migrated rather than grown
	# a second block. Nothing else guards that, so pin it here.
	grep -qE "^[[:space:]]*LEGACY_MARKER='# lazytmux-managed: codex status-line hooks'\$" <<<"$block"
	# shellcheck disable=SC2016
	[[ $block == *'elif grep -qF "$LEGACY_MARKER" "$CONFIG"; then'* ]]
	[[ $block == *"$profile_binary"* ]]
}

@test "Codex resume hook uses the stable profile binary and migrates old paths" {
	run resume_provision
	[ "$status" -eq 0 ]
	local block="$output"
	# shellcheck disable=SC2016
	local replacement='s#^command = .*codex-relaunch-stamp.*#command = \"${resumeBinary}\"#'
	# shellcheck disable=SC2016
	[[ $block == *'resumeBinary = "${config.home.profileDirectory}/bin/codex-relaunch-stamp";'* ]]
	# shellcheck disable=SC2016
	[[ $block == *'if grep -qF "$MARKER" "$CONFIG"; then'* ]]
	# Anchored for the same reason as the status twin: LEGACY_MARKER= ends with
	# MARKER=, so only an anchored match pins the spelling this module writes.
	grep -qE "^[[:space:]]*MARKER='# tmux-og-managed: codex resume-on-restore SessionStart hook'\$" <<<"$block"
	# Same migration guard as the status twin.
	grep -qE "^[[:space:]]*LEGACY_MARKER='# lazytmux-managed: codex resume-on-restore SessionStart hook'\$" <<<"$block"
	# shellcheck disable=SC2016
	[[ $block == *'elif grep -qF "$LEGACY_MARKER" "$CONFIG"; then'* ]]
	[[ $block == *"$replacement"* ]]
	# shellcheck disable=SC2016
	[[ $block != *'${tmuxConfig.script.codex-relaunch-stamp}/bin/codex-relaunch-stamp'* ]]

	run grep -F '++ lib.optionals resumeCodexEnable [tmuxConfig.script.codex-relaunch-stamp]' "$MODULE"
	[ "$status" -eq 0 ]
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
