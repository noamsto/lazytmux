#!/usr/bin/env bats
# shellcheck disable=SC2016 # ${...} here is literal Nix source being matched

MODULE="$BATS_TEST_DIRNAME/../modules/home-manager.nix"

import_vars_block() {
	sed -n '/startupImportVars =/,/^$/p' "$MODULE"
}

unit_block() {
	sed -n '/tmux-startup = lib.mkIf cfg.startupSession.enable {/,/^          };$/p' "$MODULE"
}

@test "headless is opt-in" {
	run sed -n '/headless = lib.mkOption {/,/};/p' "$MODULE"
	[ "$status" -eq 0 ]
	[[ $output == *'type = lib.types.bool;'* ]]
	[[ $output == *'default = false;'* ]]
}

@test "graphical-session ordering is dropped when headless" {
	run unit_block
	[ "$status" -eq 0 ]
	[[ $output == *'lib.optionalAttrs (!cfg.startupSession.headless)'* ]]
	# After/Wants must live inside the gated block, not unconditionally.
	run bash -c 'sed -n "/tmux-startup = lib.mkIf cfg.startupSession.enable {/,/^          };$/p" "'"$MODULE"'" | sed -n "/lib.optionalAttrs (!cfg.startupSession.headless)/,/};/p"'
	[ "$status" -eq 0 ]
	[[ $output == *'After = ["graphical-session.target"];'* ]]
	[[ $output == *'Wants = ["graphical-session.target"];'* ]]
}

@test "WantedBy follows headless" {
	run bash -c 'sed -n "/Install = {/,/};/p" "'"$MODULE"'"'
	[ "$status" -eq 0 ]
	[[ $output == *'if cfg.startupSession.headless'* ]]
	[[ $output == *'then "default.target"'* ]]
	[[ $output == *'else "graphical-session.target"'* ]]
}

# A headless start has no graphical session to read DISPLAY/WAYLAND_DISPLAY
# from, but the terminal vars still come from the unit's own Environment.
@test "display vars are imported only when not headless" {
	run import_vars_block
	[ "$status" -eq 0 ]
	[[ $output == *'lib.optionals (!cfg.startupSession.headless)'* ]]
	[[ $output == *'"DISPLAY"'* ]]
	[[ $output == *'"WAYLAND_DISPLAY"'* ]]

	# The terminal trio sits outside the gate.
	run bash -c 'sed -n "/startupImportVars =/,/^$/p" "'"$MODULE"'" | sed -n "/++ \[/p"'
	[ "$status" -eq 0 ]
	[[ $output == *'"COLORTERM"'* ]]
	[[ $output == *'"TERM"'* ]]
	[[ $output == *'"TERMINFO"'* ]]
}

@test "ExecStartPre consumes the gated var list" {
	run unit_block
	[ "$status" -eq 0 ]
	[[ $output == *'import-environment ${lib.concatStringsSep " " startupImportVars}'* ]]
}
