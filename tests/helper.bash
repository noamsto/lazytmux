# Sources lib-enrich.sh for bats with Nix placeholders stubbed to defaults.
# Run from repo root: bats tests/enrich.bats
setup_lib_enrich() {
	local tmp
	tmp="$(mktemp)"
	# Stub the @providers@ Nix placeholder (added by provider_priority_list in a
	# later task) so the un-built script is sourceable in tests.
	sed 's/@providers@/linear github/g' scripts/lib-enrich.sh >"$tmp"
	# shellcheck disable=SC1090
	source "$tmp"
	rm -f "$tmp"
}
