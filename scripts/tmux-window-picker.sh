#!/usr/bin/env bash
# Window picker: tree view with session headers and window rows.
# Go binary generates ANSI output, fzf-tmux provides the popup.
# Background refresh every 1s. Preview shows pane content.

# shellcheck disable=SC2016,SC2001  # SC2016: fzf uses single-quoted commands; SC2001: ANSI regex needs sed
set -euo pipefail

FZF=@fzf@

if [[ ${1:-} == "--generate" ]]; then
	exec @picker_generate@ --windows
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

# Target is field 1 (before TAB). {1} in fzf extracts it.
# Session rows: "session_name"  Window rows: "session_name:window_index"
PREVIEW_CMD='bash -c '"'"'tmux capture-pane -t "$0" -p -e 2>/dev/null'"'"' {1}'
KILL_CMD='bash -c '"'"'
	target="$0"
	if [[ "$target" == *:* ]]; then
		tmux kill-window -t "$target" 2>/dev/null
	else
		tmux kill-session -t "$target" 2>/dev/null
	fi
'"'"' {1}'

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 90%,85% -- \
		--listen "$PORT" \
		--ansi \
		--delimiter '\t' \
		--with-nth 2 \
		--header-lines 1 \
		--layout reverse \
		--border rounded \
		--border-label ' Windows ' \
		--pointer '▸' \
		--prompt '  ' \
		--no-info \
		--margin 0 \
		--padding 0,1 \
		--preview "$PREVIEW_CMD" \
		--preview-window 'right:50%:wrap:follow' \
		--bind "ctrl-r:reload($SELF --generate)" \
		--bind 'ctrl-/:toggle-preview' \
		--bind "ctrl-x:execute-silent($KILL_CMD)+reload($SELF --generate)" \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || true

[[ -z $selected ]] && exit 0
target=$(cut -f1 <<<"$selected")
[[ -n $target ]] && tmux switch-client -t "$target"
exit 0
