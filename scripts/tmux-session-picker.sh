#!/usr/bin/env bash
# Session picker: Go binary generates ANSI output, fzf-tmux provides the popup.
# Background refresh every 1s via /dev/tcp. ctrl-x kills sessions, ctrl-/ toggles preview.

# shellcheck disable=SC2016,SC2001  # SC2016: fzf uses single-quoted commands; SC2001: ANSI regex needs sed
set -euo pipefail

FZF=@fzf@

if [[ ${1:-} == "--generate" ]]; then
	exec @picker_generate@
fi

SELF="$0"
FZF_TMUX="${FZF%fzf}fzf-tmux"
PORT=$((RANDOM % 10000 + 40000))

# Background refresh via /dev/tcp (no curl dependency).
(
	sleep 0.3
	while sleep 1; do
		body="reload($SELF --generate)"
		exec 3<>/dev/tcp/127.0.0.1/"$PORT" 2>/dev/null || exit 0
		printf 'POST / HTTP/1.0\r\nHost: localhost\r\nContent-Length: %d\r\n\r\n%s' \
			"${#body}" "$body" >&3
		cat <&3 >/dev/null 2>&1
		exec 3>&-
	done
) &

# Preview and kill commands — must use bash (fzf inherits user's fish shell)
PREVIEW_CMD='bash -c '"'"'
	sess=$(echo "$0" | sed "s/\x1b\[[0-9;]*m//g" | awk "{print \$2}")
	tmux capture-pane -t "$sess" -p -e 2>/dev/null
'"'"' {}'

KILL_CMD='bash -c '"'"'
	sess=$(echo "$0" | sed "s/\x1b\[[0-9;]*m//g" | awk "{print \$2}")
	tmux kill-session -t "$sess" 2>/dev/null
'"'"' {}'

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 80%,60% -- \
		--listen "$PORT" \
		--ansi \
		--nth 2 \
		--header-lines 1 \
		--layout reverse \
		--border rounded \
		--border-label ' Sessions ' \
		--pointer '▸' \
		--prompt '  ' \
		--no-info \
		--margin 0 \
		--padding 0,1 \
		--preview "$PREVIEW_CMD" \
		--preview-window 'right:50%:wrap:follow:hidden' \
		--bind "ctrl-r:reload($SELF --generate)" \
		--bind 'ctrl-/:toggle-preview' \
		--bind "ctrl-x:execute-silent($KILL_CMD)+reload($SELF --generate)" \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || true

# shellcheck disable=SC2001
session_name=$(sed 's/\x1b\[[0-9;]*m//g' <<<"$selected")
session_name=$(awk '{print $2}' <<<"$session_name")
[[ -n $session_name ]] && tmux switch-client -t "$session_name"
exit 0
