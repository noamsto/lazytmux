#!/usr/bin/env bash
# Cursor sessionStart + beforeSubmitPrompt hook: stamp this pane's
# @remux_relaunch so tmux-remux resumes the Cursor session (not a bare shell)
# on restore. conversation_id and session_id are the same chat id; preferring
# conversation_id is deliberate, not arbitrary. sessionStart alone would only
# resume once — Cursor never re-fires it on --resume — so beforeSubmitPrompt
# re-stamps on every turn, including post-resume turns.
set -euo pipefail

[[ -n ${TMUX_PANE:-} ]] || exit 0
command -v tmux >/dev/null 2>&1 || exit 0

# Fork-free slurp + regex (no jq dependency), mirroring codex-relaunch-stamp.sh.
input=""
IFS= read -r -d '' input || true

chat_id=""
if [[ $input =~ \"conversation_id\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]] && [[ -n ${BASH_REMATCH[1]} ]]; then
	chat_id="${BASH_REMATCH[1]}"
elif [[ $input =~ \"session_id\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]] && [[ -n ${BASH_REMATCH[1]} ]]; then
	chat_id="${BASH_REMATCH[1]}"
fi

[[ -n $chat_id ]] || exit 0
# @remux_relaunch is exec'd verbatim via /bin/sh -c by tmux-remux on a future
# restore, so a chat id carrying shell metacharacters (spaces, ;, $, backticks)
# must never reach it. Cursor's own ids are UUIDs in practice; reject anything
# else rather than stamp an exploitable command.
[[ $chat_id =~ ^[A-Za-z0-9._-]+$ ]] || exit 0
# Cursor hooks have a stdout contract (see cursor-status-hook.sh) and the pane
# can vanish between hook-fire and this call, so both output and exit status
# must be suppressed.
tmux set-option -p -t "$TMUX_PANE" @remux_relaunch "cursor-agent --resume $chat_id" >/dev/null 2>&1 || true
