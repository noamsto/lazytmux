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
	claude-status-update task set "$prompt"

	# Window-naming nudge. On a fallback window (no tracked issue, on the default
	# branch) that has no name yet, ask the pane's Claude — which has full
	# conversation context — to set a concise title. The reminder reaches the
	# model via this UserPromptSubmit hook's stdout. Gated by the @ai_naming
	# global (set from programs.lazytmux.aiNaming.enable); enriched/worktree
	# windows name themselves from issue+branch and are skipped here.
	[[ -n ${TMUX_PANE:-} ]] || exit 0
	command -v tmux >/dev/null 2>&1 || exit 0
	IFS='|' read -r ai_naming issue_id branch ai_name < <(
		tmux display-message -t "$TMUX_PANE" -p \
			'#{@ai_naming}|#{@issue_id}|#{@branch}|#{@window_ai_name}' 2>/dev/null
	)
	[[ $ai_naming == 1 ]] || exit 0
	[[ -z $issue_id ]] || exit 0
	[[ -z $branch || $branch == main || $branch == master ]] || exit 0
	[[ -z $ai_name ]] || exit 0
	cat <<'REMINDER'
<system-reminder>
This tmux window has no descriptive name yet and is not tied to an issue or
branch. If the current task is clear, give this window a concise title (3-6
words) by running once:
  claude-status-update name set "your title here"
Name only the window you are working in. Skip if the task is still vague; if
the focus later shifts to clearly different work, run it again with a new title.
</system-reminder>
REMINDER
	exit 0
fi

exec claude-status-update "$@"
