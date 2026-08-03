#!/usr/bin/env bash
# Cursor usage-limit provider: query the auth/usage endpoint with the CLI's own
# token and atomically rewrite $CACHE_DIR/cursor.json for tmux-statusline.
#
# The endpoint reports per-model request counts against a monthly cap
# (startOfMonth), so Cursor only ever fills the monthly slot. Enterprise tiers
# cap nothing: every maxRequestUsage is null and the normalized cache stays
# empty, which hides the agent from the status segment entirely.
set -uo pipefail

CACHE_DIR="${LAZYTMUX_AGENT_USAGE_DIR:-/tmp/lazytmux-agent-usage}"
AUTH="${CURSOR_AUTH:-$HOME/.config/cursor/auth.json}"

token=$(jq -r '.accessToken // empty' "$AUTH" 2>/dev/null)
[[ -n $token ]] || exit 0

resp=$(curl -fsS --max-time 10 \
	-H "Authorization: Bearer $token" \
	https://api2.cursor.sh/auth/usage 2>/dev/null) || exit 0

out=$(jq -c '
	[to_entries[]
		| select(.value | type == "object" and .maxRequestUsage != null)
		| (100 * .value.numRequestsTotal / .value.maxRequestUsage | floor)]
	| if length == 0 then {windows: [], monthly: null}
		else {windows: [], monthly: {label: "mo", pct: max}} end
	' <<<"$resp" 2>/dev/null) || exit 0

mkdir -p "$CACHE_DIR" 2>/dev/null
tmp="$CACHE_DIR/.cursor.json.$$"
printf '%s\n' "$out" >"$tmp" && mv -f "$tmp" "$CACHE_DIR/cursor.json"
