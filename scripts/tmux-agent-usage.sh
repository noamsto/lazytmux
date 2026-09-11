#!/usr/bin/env bash
# Coding-agent usage-limit poller. Two entry modes:
#   --tick      cheap gate from status-format[0]; daemonizes a pass when stale
#   --tick-run  one pass: refresh every authed agent's cache concurrently
# Always exits 0. Cache: /tmp/og-agent-usage/<agent>.json, rendered by
# tmux-statusline (Go). Providers curl the usage endpoints with the CLIs' own
# stored tokens — no extra API keys.
#
# A pass only runs while a coding-agent pane exists somewhere: the display
# gate (tmux-statusline) hides the segment otherwise, so polling would burn
# provider quota for an invisible segment. Both modes check that gate, and in
# tick mode it precedes the .last-tick stamp — a gated-out tick that spent the
# cycle would leave the segment on the previous session's numbers for a second
# refresh window after an agent starts.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_log@

CACHE_DIR="${OG_AGENT_USAGE_DIR:-/tmp/og-agent-usage}"
REFRESH_SECONDS="@refresh_seconds@"
# Space-separated pane-command basenames from the agentdetect manifests
# (claude codex cursor-agent) — same source as the update-icons sweep.
AGENT_COMMANDS="@AGENT_COMMANDS@"

# agent_running: true while some pane's foreground command is a coding agent.
# One `list-panes` fork, reached only past the refresh window in tick mode.
agent_running() {
	local cmds cmd base agent
	cmds=$(tmux list-panes -a -F '#{pane_current_command}' 2>/dev/null) || return 1
	while IFS= read -r cmd; do
		base=${cmd##*/}
		base=${base#.}
		base=${base%-wrapped}
		for agent in $AGENT_COMMANDS; do
			[[ $base == "$agent" ]] && return 0
		done
	done <<<"$cmds"
	return 1
}

mode="tick"
[[ ${1:-} == "--tick-run" ]] && mode="tickrun"

if [[ $mode == "tick" ]]; then
	last_tick="$CACHE_DIR/.last-tick"
	if [[ -f $last_tick ]] && ((EPOCHSECONDS - $(file_mtime "$last_tick") < REFRESH_SECONDS)); then
		exit 0
	fi
	# Gate BEFORE the stamp: a tick with no agent must not spend the cycle, or
	# the first tick after an agent appears waits out another one and the segment
	# comes back showing the last agent session's numbers.
	agent_running || exit 0
	# Mark fresh BEFORE daemonizing (same best-effort trade as tmux-pr-enrich):
	# a crashed pass waits one cycle.
	mkdir -p "$CACHE_DIR" 2>/dev/null
	touch "$last_tick"
	detach "${BASH_SOURCE[0]}" --tick-run
	exit 0
fi

# --- tick-run ---
# Re-checked here because --tick-run is its own entry point, and an agent can
# exit between the tick's gate and the detached pass.
agent_running || exit 0
mkdir -p "$CACHE_DIR" 2>/dev/null

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
