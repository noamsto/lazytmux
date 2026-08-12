#!/usr/bin/env bash
# Upsert lazytmux Cursor relaunch-stamp hooks into ~/.cursor/hooks.json.
# Usage: cursor-relaunch-hooks-install <cursor-relaunch-stamp-path>
# Strips prior entries whose command contains /bin/cursor-relaunch-stamp, then
# appends the stamp command to sessionStart and beforeSubmitPrompt. Leaves
# aeye/user/cursor-status-hook entries alone. Fails on malformed JSON.
set -euo pipefail

stamp="${1:?usage: cursor-relaunch-hooks-install <cursor-relaunch-stamp-path>}"
hooks_file="${CURSOR_HOOKS_FILE:-${CURSOR_HOME:-$HOME/.cursor}/hooks.json}"
marker='/bin/cursor-relaunch-stamp'

if ! command -v jq >/dev/null 2>&1; then
	echo "cursor-relaunch-hooks-install: jq is required but not found on PATH" >&2
	exit 1
fi

mkdir -p "$(dirname "$hooks_file")"

if [[ -f $hooks_file ]]; then
	if ! jq empty "$hooks_file" >/dev/null 2>&1; then
		echo "cursor-relaunch-hooks-install: malformed JSON in $hooks_file — refusing to clobber" >&2
		exit 1
	fi
	existing=$(cat "$hooks_file")
else
	existing='{"version":1,"hooks":{}}'
fi

# shellcheck disable=SC2016
merged=$(jq -n --argjson existing "$existing" --arg stamp "$stamp" --arg marker "$marker" '
	def strip($m):
		map(select((.command // "") | contains($m) | not));
	($existing + {version: ($existing.version // 1)}) as $base |
	($base.hooks // {}) as $h |
	$base * {hooks: (
		$h
		| .sessionStart = ((.sessionStart // [] | strip($marker)) + [
			{command: $stamp, timeout: 15}
		])
		| .beforeSubmitPrompt = ((.beforeSubmitPrompt // [] | strip($marker)) + [
			{command: $stamp, timeout: 15}
		])
	)}
')

tmp=$(mktemp)
printf '%s\n' "$merged" >"$tmp"
mv "$tmp" "$hooks_file"
