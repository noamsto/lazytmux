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

# Extraction: strip ANSI → detect row type.
# Session rows: no ├─/╰─ → field 2 = session name → switch to session
# Window rows: has ├─/╰─ → field 1 = session name, grep N: = window index
# (Session name is dim but present on window rows for extraction)

PREVIEW_CMD='bash -c '"'"'
	clean=$(echo "$0" | sed "s/\x1b\[[0-9;]*m//g")
	if echo "$clean" | grep -qP "[├╰]─"; then
		sess=$(echo "$clean" | awk "{print \$1}")
		win=$(echo "$clean" | grep -oP "\d+(?=:)" | head -1)
		tmux capture-pane -t "${sess}:${win}" -p -e 2>/dev/null
	else
		sess=$(echo "$clean" | awk "{print \$2}")
		tmux capture-pane -t "$sess" -p -e 2>/dev/null
	fi
'"'"' {}'

KILL_CMD='bash -c '"'"'
	clean=$(echo "$0" | sed "s/\x1b\[[0-9;]*m//g")
	if echo "$clean" | grep -qP "[├╰]─"; then
		sess=$(echo "$clean" | awk "{print \$1}")
		win=$(echo "$clean" | grep -oP "\d+(?=:)" | head -1)
		tmux kill-window -t "${sess}:${win}" 2>/dev/null
	else
		sess=$(echo "$clean" | awk "{print \$2}")
		tmux kill-session -t "$sess" 2>/dev/null
	fi
'"'"' {}'

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 90%,85% -- \
		--listen "$PORT" \
		--ansi \
		--nth 1..3 \
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

clean=$(sed 's/\x1b\[[0-9;]*m//g' <<<"$selected")
if echo "$clean" | grep -qP '[├╰]─'; then
	sess=$(awk '{print $1}' <<<"$clean")
	win=$(grep -oP '\d+(?=:)' <<<"$clean" | head -1)
	tmux switch-client -t "${sess}:${win}"
else
	sess=$(awk '{print $2}' <<<"$clean")
	tmux switch-client -t "$sess"
fi
exit 0
