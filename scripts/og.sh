#!/usr/bin/env bash
# Dispatcher over lazytmux's curated public scripts: resolves <noun> [<verb>]
# to an absolute store path and execs it with the remaining arguments
# untouched. See docs/superpowers/specs/2026-09-10-og-dispatcher-design.md.
set -euo pipefail

declare -A OG_TARGET=()
declare -A OG_SUMMARY=()
declare -a OG_ORDER=()

# shellcheck source=/dev/null
source @og_table@

print_all_help() {
	echo "Usage: og <noun> [<verb>] [args...]"
	echo
	local verb noun prev_noun=""
	for verb in "${OG_ORDER[@]}"; do
		noun="${verb%% *}"
		if [[ -n $prev_noun && $noun != "$prev_noun" ]]; then
			echo
		fi
		printf '  %-22s %s\n' "$verb" "${OG_SUMMARY[$verb]}"
		prev_noun="$noun"
	done
	return 0
}

print_noun_help() {
	local noun="$1" verb
	echo "Usage: og $noun <verb> [args...]"
	echo
	for verb in "${OG_ORDER[@]}"; do
		[[ $verb == "$noun "* ]] && printf '  %-22s %s\n' "$verb" "${OG_SUMMARY[$verb]}"
	done
	return 0
}

noun_has_subverbs() {
	local noun="$1" verb
	for verb in "${OG_ORDER[@]}"; do
		[[ $verb == "$noun "* ]] && return 0
	done
	return 1
}

unknown_command() {
	echo "og: unknown command: $*" >&2
	echo "Run 'og help' for a list of commands." >&2
	exit 2
}

if [[ $# -eq 0 ]]; then
	print_all_help
	exit 0
fi

if [[ $# -eq 1 && ($1 == "help" || $1 == "--help" || $1 == "-h") ]]; then
	print_all_help
	exit 0
fi

verb=""
target=""
remaining=()

if [[ $# -ge 2 ]]; then
	two="$1 $2"
	if [[ -n ${OG_TARGET[$two]+x} ]]; then
		verb="$two"
		target="${OG_TARGET[$two]}"
		remaining=("${@:3}")
	fi
fi

# A noun with subverbs (status, notify) also owning a bare one-token verb
# takes the bare match whenever a second token can't be an attempted subverb
# spelling: the whole command, or a flag (subverbs are always bare words, so
# a leading "-" can only be a passthrough argument). A bare second word is
# ambiguous between a passthrough argument and a misspelled/unknown subverb,
# so it falls through to the unknown_command/print_noun_help path below --
# --help/-h stay excluded from the flag case since those spellings are
# themselves how a noun's own help is requested.
if [[ -z $verb && -n $1 && -n ${OG_TARGET[$1]+x} ]] && {
	[[ $# -eq 1 ]] ||
		! noun_has_subverbs "$1" ||
		{ [[ ${2-} == -* ]] && [[ $2 != "--help" && $2 != "-h" ]]; }
}; then
	verb="$1"
	target="${OG_TARGET[$1]}"
	remaining=("${@:2}")
fi

if [[ -z $verb ]]; then
	if [[ $# -eq 1 ]] && noun_has_subverbs "$1"; then
		print_noun_help "$1"
		exit 0
	fi
	if [[ $# -eq 2 && ($2 == "help" || $2 == "--help" || $2 == "-h") ]] && noun_has_subverbs "$1"; then
		print_noun_help "$1"
		exit 0
	fi
	unknown_command "$@"
fi

if [[ ${#remaining[@]} -eq 1 && (${remaining[0]} == "--help" || ${remaining[0]} == "-h") ]]; then
	printf 'og %s — %s\n' "$verb" "${OG_SUMMARY[$verb]}"
	printf '%s\n' "$target"
	exit 0
fi

exec "$target" "${remaining[@]}"
