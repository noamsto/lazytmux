#!/usr/bin/env bash
# Session picker: Go binary generates ANSI output, fzf-tmux provides the popup.
# Initial data piped for instant display, --listen for 1s auto-refresh.

set -euo pipefail

FZF=@fzf@

if [[ ${1:-} == "--generate" ]]; then
	exec @picker_generate@
fi

SELF="$0"
FZF_TMUX="${FZF%fzf}fzf-tmux"
PORT=$((RANDOM % 10000 + 40000))

# Background refresh: sends reload every 1s via fzf's HTTP API.
# Uses bash /dev/tcp (no curl dependency). Self-terminates on closed port.
(
	sleep 1
	while sleep 1; do
		body="reload($SELF --generate)"
		exec 3<>/dev/tcp/127.0.0.1/"$PORT" 2>/dev/null || exit 0
		printf 'POST / HTTP/1.0\r\nHost: localhost\r\nContent-Length: %d\r\n\r\n%s' \
			"${#body}" "$body" >&3
		cat <&3 >/dev/null 2>&1
		exec 3>&-
	done
) &

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 70%,50% -- \
		--listen "$PORT" \
		--ansi \
		--no-sort \
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
		--bind "ctrl-r:reload($SELF --generate)" \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || true

# shellcheck disable=SC2001
session_name=$(sed 's/\x1b\[[0-9;]*m//g' <<<"$selected")
session_name=$(awk '{print $2}' <<<"$session_name")
[[ -n $session_name ]] && tmux switch-client -t "$session_name"
exit 0
