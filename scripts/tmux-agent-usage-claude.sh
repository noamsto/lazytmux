#!/usr/bin/env bash
# Claude usage-limit provider: query the OAuth usage endpoint (the one /usage
# renders) with the CLI's own token and atomically rewrite
# $CACHE_DIR/claude.json for tmux-statusline. A failed fetch (offline, expired
# token, non-JSON) leaves the previous cache untouched.
set -uo pipefail

CACHE_DIR="${LAZYTMUX_AGENT_USAGE_DIR:-/tmp/lazytmux-agent-usage}"
CREDS="${CLAUDE_CREDENTIALS:-$HOME/.claude/.credentials.json}"

token=$(jq -r '.claudeAiOauth.accessToken // empty' "$CREDS" 2>/dev/null)
[[ -n $token ]] || exit 0

resp=$(curl -fsS --max-time 10 \
	-H "Authorization: Bearer $token" \
	-H "anthropic-beta: oauth-2025-04-20" \
	https://api.anthropic.com/api/oauth/usage 2>/dev/null) || exit 0

out=$(jq -c '
	def w(l; u): if u == null then empty else {label: l, pct: (u | floor)} end;
	{
		windows: [
			w("5h"; .five_hour.utilization),
			w("7d"; .seven_day.utilization)
		],
		monthly: (if .extra_usage.is_enabled == true and .extra_usage.utilization != null
			then {label: "mo", pct: (.extra_usage.utilization | floor)}
			else null end)
	}' <<<"$resp" 2>/dev/null) || exit 0

mkdir -p "$CACHE_DIR" 2>/dev/null
tmp="$CACHE_DIR/.claude.json.$$"
printf '%s\n' "$out" >"$tmp" && mv -f "$tmp" "$CACHE_DIR/claude.json"
