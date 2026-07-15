#!/usr/bin/env bash
# Codex SessionStart hook: stamp this pane's @remux_relaunch so tmux-remux resumes
# the Codex session (not a bare shell) on restore. Reads the hook's JSON payload
# from stdin (session_id) and the pane from $TMUX_PANE. No-ops outside tmux.
#
# Note: in the interactive TUI this hook fires only after the first user turn
# completes, so the stamp lags pane creation — acceptable (nothing to resume
# before then).
set -euo pipefail

[[ -n ${TMUX_PANE:-} ]] || exit 0
command -v tmux >/dev/null 2>&1 || exit 0

# Fork-free slurp + regex (no jq dependency), mirroring the transcript_path
# parse in claude-plugin/scripts/status.sh.
input=""
IFS= read -r -d '' input || true
session_id=""
[[ $input =~ \"session_id\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]] && session_id="${BASH_REMATCH[1]}"

[[ -n $session_id ]] || exit 0
tmux set-option -p -t "$TMUX_PANE" @remux_relaunch "codex resume $session_id"
