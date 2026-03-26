#!/usr/bin/env bash
# Window picker: Go binary generates ANSI output, fzf-tmux provides the popup.
# Background refresh every 1s via /dev/tcp. Preview shows pane content.

# shellcheck disable=SC2016,SC2001  # SC2016: fzf --preview/--bind use single quotes; SC2001: ANSI regex needs sed
set -euo pipefail

FZF=@fzf@

if [[ ${1:-} == "--generate" ]]; then
	exec @picker_generate@ --windows
fi

# Extract "session:window_index" from a picker line.
# Session name is always field 2 (after icon/tree), window index from "N: name".
extract_target() {
	local clean
	clean=$(sed 's/\x1b\[[0-9;]*m//g' <<<"$1")
	local sess win
	sess=$(awk '{print $2}' <<<"$clean")
	win=$(grep -oP '\b\d+(?=:)' <<<"$clean" | head -1)
	echo "${sess}:${win}"
}

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
	clean=$(echo "$0" | sed "s/\x1b\[[0-9;]*m//g")
	sess=$(echo "$clean" | awk "{print \$2}")
	win=$(echo "$clean" | grep -oP "\b\d+(?=:)" | head -1)
	tmux capture-pane -t "${sess}:${win}" -p -e 2>/dev/null
'"'"' {}'

KILL_CMD='bash -c '"'"'
	clean=$(echo "$0" | sed "s/\x1b\[[0-9;]*m//g")
	sess=$(echo "$clean" | awk "{print \$2}")
	win=$(echo "$clean" | grep -oP "\b\d+(?=:)" | head -1)
	tmux kill-window -t "${sess}:${win}" 2>/dev/null
'"'"' {}'

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 90%,85% -- \
		--listen "$PORT" \
		--ansi \
		--nth 2,5 \
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
target=$(extract_target "$selected")
[[ -n $target && $target != ":" ]] && tmux switch-client -t "$target"
exit 0
