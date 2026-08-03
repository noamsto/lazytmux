#!/usr/bin/env bash
# Coding-agent usage-limit poller. Two entry modes:
#   --tick      cheap gate from status-format[0]; daemonizes a pass when stale
#   --tick-run  one pass: refresh every authed agent's cache concurrently
# Always exits 0. Cache: /tmp/lazytmux-agent-usage/<agent>.json, rendered by
# tmux-statusline (Go). Providers curl the usage endpoints with the CLIs' own
# stored tokens — no extra API keys.
#
# A pass only runs while a coding-agent pane exists somewhere: the display
# gate (tmux-statusline) hides the segment otherwise, so polling would burn
# provider quota for an invisible segment.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_log@

CACHE_DIR="${LAZYTMUX_AGENT_USAGE_DIR:-/tmp/lazytmux-agent-usage}"
REFRESH_SECONDS="@refresh_seconds@"
# Space-separated pane-command basenames from the agentdetect manifests
# (claude codex cursor-agent) — same source as the update-icons sweep.
AGENT_COMMANDS="@AGENT_COMMANDS@"

mode="tick"
[[ ${1:-} == "--tick-run" ]] && mode="tickrun"

if [[ $mode == "tick" ]]; then
	last_tick="$CACHE_DIR/.last-tick"
	if [[ -f $last_tick ]] && ((EPOCHSECONDS - $(file_mtime "$last_tick") < REFRESH_SECONDS)); then
		exit 0
	fi
	# Mark fresh BEFORE daemonizing (same best-effort trade as tmux-pr-enrich):
	# a crashed pass waits one cycle.
	mkdir -p "$CACHE_DIR" 2>/dev/null
	touch "$last_tick"
	detach "${BASH_SOURCE[0]}" --tick-run
	exit 0
fi

# --- tick-run ---
mkdir -p "$CACHE_DIR" 2>/dev/null

cmds=$(tmux list-panes -a -F '#{pane_current_command}' 2>/dev/null) || exit 0
running=0
while IFS= read -r cmd; do
	base=${cmd##*/}
	base=${base#.}
	base=${base%-wrapped}
	for agent in $AGENT_COMMANDS; do
		[[ $base == "$agent" ]] && {
			running=1
			break 2
		}
	done
done <<<"$cmds"
((running)) || exit 0

# Per-provider lock: two overlapping passes (stale .last-tick race) otherwise
# curl the same endpoint twice; the atomic cache write makes the loser harmless.
pids=()
[[ -f $HOME/.claude/.credentials.json ]] && (
	acquire_lock "$CACHE_DIR/.lock-claude" || exit 0
	"@usage_claude@"
) &
pids+=($!)
[[ -f $HOME/.codex/auth.json ]] && (
	acquire_lock "$CACHE_DIR/.lock-codex" || exit 0
	"@usage_codex@"
) &
pids+=($!)
[[ -f $HOME/.config/cursor/auth.json ]] && (
	acquire_lock "$CACHE_DIR/.lock-cursor" || exit 0
	"@usage_cursor@"
) &
pids+=($!)
((${#pids[@]})) && wait "${pids[@]}" 2>/dev/null
exit 0
