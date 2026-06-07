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
