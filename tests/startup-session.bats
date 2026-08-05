#!/usr/bin/env bats
# shellcheck disable=SC2016 # ${...} here is literal Nix source being matched

MODULE="$BATS_TEST_DIRNAME/../modules/home-manager.nix"

import_vars_block() {
	sed -n '/startupImportVars =/,/^$/p' "$MODULE"
}

# The list appended after the gated block — vars imported unconditionally.
import_vars_ungated() {
	import_vars_block | sed -n '/++ \[/p'
}

unit_block() {
	sed -n '/tmux-startup = lib.mkIf cfg.startupSession.enable {/,/^          };$/p' "$MODULE"
}

graphical_gate_block() {
	unit_block | sed -n '/lib.optionalAttrs (!cfg.startupSession.headless)/,/};/p'
}

install_block() {
	unit_block | sed -n '/Install = {/,/};/p'
}

@test "headless is opt-in" {
	run sed -n '/headless = lib.mkOption {/,/};/p' "$MODULE"
	[ "$status" -eq 0 ]
	[[ $output == *'type = lib.types.bool;'* ]]
	[[ $output == *'default = false;'* ]]
}

@test "graphical-session ordering sits behind the headless gate" {
	run graphical_gate_block
	[ "$status" -eq 0 ]
	[[ $output == *'After = ["graphical-session.target"];'* ]]
	[[ $output == *'Wants = ["graphical-session.target"];'* ]]
}

@test "WantedBy follows headless" {
	run install_block
	[ "$status" -eq 0 ]
	[[ $output == *'if cfg.startupSession.headless'* ]]
	[[ $output == *'then "default.target"'* ]]
	[[ $output == *'else "graphical-session.target"'* ]]
}

# A headless start has no graphical session to read DISPLAY/WAYLAND_DISPLAY
# from; the terminal vars come from the unit's own Environment, so they stay.
@test "display vars are imported only when not headless" {
	run import_vars_block
	[ "$status" -eq 0 ]
	[[ $output == *'lib.optionals (!cfg.startupSession.headless)'* ]]
	[[ $output == *'"DISPLAY"'* ]]
	[[ $output == *'"WAYLAND_DISPLAY"'* ]]

	run import_vars_ungated
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

linger_option_block() {
	sed -n '/linger = lib.mkOption {/,/^      };$/p' "$MODULE"
}

activation_linger_block() {
	sed -n '/startupLinger = lib.mkIf/,/^          );$/p' "$MODULE"
}

@test "linger defaults on" {
	run linger_option_block
	[ "$status" -eq 0 ]
	[[ $output == *'type = lib.types.bool;'* ]]
	[[ $output == *'default = true;'* ]]
}

@test "linger activation is gated on Linux and an enabled startup session" {
	run activation_linger_block
	[ "$status" -eq 0 ]
	[[ $output == *'lib.mkIf (isLinux && cfg.startupSession.enable)'* ]]
}

@test "linger=true enables lingering, guarded so it is idempotent" {
	run activation_linger_block
	[ "$status" -eq 0 ]
	[[ $output == *'if cfg.startupSession.linger'* ]]
	# The enable path first checks the current state, then enables.
	[[ $output == *'show-user "$USER" -p Linger --value'* ]]
	[[ $output == *'enable-linger "$USER"'* ]]
}

# The opted-out path must WARN, never mutate: the whole point of linger=false is
# "I manage this myself".
@test "linger=false warns and does not enable" {
	run bash -c 'sed -n "/else ..$/,/^              ..$/p" <(sed -n "/startupLinger = lib.mkIf/,/^          );$/p" "'"$MODULE"'")'
	[ "$status" -eq 0 ]
	[[ $output == *'die at logout'* ]]
	[[ $output != *'enable-linger'* ]]
}

# The asymmetry that makes default-on safe: activation never reverses lingering,
# so it cannot clobber a user who enabled it for another reason.
@test "activation never disables lingering" {
	run activation_linger_block
	[ "$status" -eq 0 ]
	[[ $output != *'disable-linger'* ]]
}
