#!/usr/bin/env bash
# Session picker using fzf inside display-popup.
#
# Two modes:
#   --generate   — Go binary outputs ANSI-colored session list for fzf
#   (no args)    — open fzf popup, pipe generate output

set -euo pipefail

FZF=@fzf@

# --- Generate mode: delegate to Go binary for speed (~4ms vs ~85ms) ---
if [[ ${1:-} == "--generate" ]]; then
	exec @picker_generate@
fi

# --- Picker mode: runs inside display-popup ---
# Popup is non-blocking (tmux stays responsive). All child processes
# are killed when the popup closes — no orphaned background loops.

SELF="$0"
CURL=@curl@
PORT=$((RANDOM % 10000 + 40000))

# Background reload loop: auto-killed when popup closes (same process group).
(
	sleep 0.1
	"$CURL" -s -XPOST "localhost:$PORT" \
		-d "reload($SELF --generate)" 2>/dev/null || true
	while sleep 1; do
		"$CURL" -s -XPOST "localhost:$PORT" \
			-d "reload($SELF --generate)" 2>/dev/null || exit 0
	done
) &

selected=$(
	"$SELF" --generate | "$FZF" \
		--no-tmux --no-height \
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
# Session name is second space-delimited field (field 1 is icon)
# shellcheck disable=SC2001
session_name=$(sed 's/\x1b\[[0-9;]*m//g' <<<"$selected")
session_name=$(awk '{print $2}' <<<"$session_name")
[[ -n $session_name ]] && tmux switch-client -t "$session_name"
exit 0
