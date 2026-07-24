# Sources lib-enrich.sh for bats with Nix placeholders stubbed to defaults.
# Run from repo root: bats tests/enrich.bats
setup_lib_enrich() {
	local tmp
	tmp="$(mktemp)"
	sed \
		-e 's/@providers@/linear github/g' \
		-e 's/@enrich_icon_linear@/L/g' \
		-e 's/@enrich_icon_github@/G/g' \
		-e 's/@enrich_icon_pending@/P/g' \
		-e 's/@enrich_icon_success@/S/g' \
		-e 's/@enrich_icon_failure@/F/g' \
		-e 's/@enrich_icon_merged@/M/g' \
		-e 's/@enrich_icon_closed@/X/g' \
		-e 's/@enrich_icon_conflict@/C/g' \
		scripts/lib-enrich.sh >"$tmp"
	# shellcheck source=/dev/null
	source "$tmp"
	rm -f "$tmp"
}

setup_lib_icons() {
	local tmp
	tmp="$(mktemp)"
	# Stub the @ICON_MAP@ / @FALLBACK_ICON@ Nix placeholders so the file sources.
	sed -e 's/@ICON_MAP@//' -e 's/@FALLBACK_ICON@//' scripts/lib-icons.sh >"$tmp"
	# shellcheck source=/dev/null
	source "$tmp"
	rm -f "$tmp"
}

setup_lib_claude() {
	# lib-claude.sh has no Nix placeholders; source directly.
	# shellcheck source=/dev/null
	source scripts/lib-claude.sh
}

setup_lib_log() {
	# lib-log.sh has no Nix placeholders; source directly.
	# shellcheck source=/dev/null
	source scripts/lib-log.sh
}

setup_lib_reflow() {
	# lib-reflow.sh has no Nix placeholders; source directly.
	# shellcheck source=/dev/null
	source scripts/lib-reflow.sh
}

# Builds a runnable claude-status with the @lib_claude@ placeholder resolved.
# Sets CLAUDE_STATUS_SCRIPT to the path.
make_claude_status() {
	CLAUDE_STATUS_SCRIPT="$BATS_TEST_TMPDIR/claude-status.sh"
	sed "s|@lib_claude@|$PWD/scripts/lib-claude.sh|" scripts/claude-status.sh >"$CLAUDE_STATUS_SCRIPT"
}

# Sources claude-status.sh's function definitions (count_for_session,
# count_for_window, tally_state, ...) into the current shell, stopping before
# the "# --- Main ---" CLI-parsing section so it's safe to call under bats
# without the script consuming bats' own args or exiting early.
setup_claude_status_functions() {
	setup_lib_claude
	local tmp
	tmp="$(mktemp)"
	sed -n '1,/^# --- Main ---/p' scripts/claude-status.sh | sed '$d; /^source @lib_claude@$/d' >"$tmp"
	# shellcheck source=/dev/null
	source "$tmp"
	rm -f "$tmp"
}
