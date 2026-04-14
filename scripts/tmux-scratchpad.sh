#!/usr/bin/env bash
# tmux-scratchpad: Toggle a per-session scratch tmux session in a popup.
# Called by keybinding via run-shell with #{session_name} expanded by tmux.
# Usage: tmux-scratchpad SESSION_NAME
set -euo pipefail

SESSION=${1:-}

# No-op when already inside a scratchpad (prevents nesting)
case "$SESSION" in
scratch-*) exit 0 ;;
esac

SCRATCH="scratch-${SESSION}"
BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")

# Configure the scratch session's status bar on first creation.
# We set a bottom hints bar so the user always knows how to exit.
if ! tmux has-session -t "=${SCRATCH}" 2>/dev/null; then
	tmux new-session -d -s "${SCRATCH}"
	# Single-line bottom status bar — hints only, no session/branch/claude overhead
	tmux set -t "=${SCRATCH}" status-position bottom
	tmux set -t "=${SCRATCH}" status 1
	tmux set -t "=${SCRATCH}" status-style "bg=#{@thm_bg}"
	tmux set -t "=${SCRATCH}" status-format[0] \
		'#[align=center,fg=#{@thm_lavender}]` d#[fg=#{@thm_overlay_1}]:hide  #[fg=#{@thm_lavender}]exit#[fg=#{@thm_overlay_1}]:close'
fi

tmux display-popup -E -w 80% -h 80% -b rounded \
	-T " scratch: ${SESSION} " \
	-S "fg=${BORDER_FG}" \
	"tmux attach-session -t '=${SCRATCH}'"
