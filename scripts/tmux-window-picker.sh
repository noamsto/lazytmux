#!/usr/bin/env bash
# Window picker: Go binary generates ANSI output, fzf-tmux provides the popup.
# Background refresh every 1s via /dev/tcp. Preview shows pane content.

# shellcheck disable=SC2016  # Single-quoted strings are intentional (fzf --preview/--bind)
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

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 85%,75% -- \
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
		--preview 'tmux capture-pane -t {1} -p -e 2>/dev/null' \
		--preview-window 'right:50%:wrap:follow' \
		--bind "ctrl-r:reload($SELF --generate)" \
		--bind 'ctrl-/:toggle-preview' \
		--bind "ctrl-x:execute-silent(tmux kill-window -t {1})+reload($SELF --generate)" \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || true

# Extract target (before TAB)
target=$(cut -f1 <<<"$selected")
[[ -n $target ]] && tmux switch-client -t "$target"
exit 0
