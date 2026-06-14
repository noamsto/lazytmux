#!/usr/bin/env bash
# Degrade gracefully: lazytmux not installed, or CC running outside a lazytmux
# tmux pane → silently no-op instead of erroring on every hook event.
command -v claude-status-update >/dev/null 2>&1 || exit 0

# `task`: capture the latest user prompt (UserPromptSubmit hook delivers it as
# JSON on stdin) as the pane's freeform task label. Needs jq to parse; no-op
# without it rather than mangling the prompt.
if [[ ${1:-} == task ]]; then
	command -v jq >/dev/null 2>&1 || exit 0
	prompt=$(jq -r '.prompt // empty' 2>/dev/null) || exit 0
	[[ -n $prompt ]] || exit 0
	exec claude-status-update task set "$prompt"
fi

exec claude-status-update "$@"
