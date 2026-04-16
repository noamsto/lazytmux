#!/usr/bin/env bash
# tmux-scratchpad: Toggle a per-session scratch tmux session in a popup.
#
# Two calling modes (mirrors the picker's --generate pattern):
#   tmux-scratchpad SESSION_NAME    — from keybinding: create session + open popup
#   tmux-scratchpad --attach NAME   — from inside popup: configure hints + exec attach
set -euo pipefail

# ── Inner mode: runs inside the display-popup ──────────────────────────────
if [[ ${1:-} == --attach ]]; then
	SCRATCH="scratch-${2:-}"
	# Re-apply hints bar every open so reflow overwrites don't stick.
	# Batch via tmux source (1 socket call instead of 4).
	printf '%s\n' \
		"set -t '$SCRATCH' detach-on-destroy on" \
		"set -t '$SCRATCH' status-position bottom" \
		"set -t '$SCRATCH' status 1" \
		"set -t '$SCRATCH' status-style 'bg=#{@thm_bg}'" \
		"set -t '$SCRATCH' status-format[0] '#[align=center,fg=#{@thm_lavender}]\` d#[fg=#{@thm_overlay_1}]:hide  #[fg=#{@thm_lavender}]exit#[fg=#{@thm_overlay_1}]:close'" |
		tmux source - 2>/dev/null || true
	# new-session -A is the correct way to attach inside a display-popup
	# (attach-session doesn't work reliably in popup PTY context).
	exec tmux new-session -A -s "$SCRATCH"
fi

# ── Outer mode: called from keybinding via run-shell ───────────────────────
SESSION=${1:-}

# No-op when already inside a scratchpad (prevents nesting)
case "$SESSION" in
scratch-*) exit 0 ;;
esac

SCRATCH="scratch-${SESSION}"
BORDER_FG=$(tmux show -gv @thm_overlay_1 2>/dev/null || echo "#7f849c")
SELF="${BASH_SOURCE[0]}"

# Create scratch session if needed (|| true so set -e doesn't fire on collision)
tmux new-session -d -s "$SCRATCH" 2>/dev/null || true

tmux display-popup -E -w 80% -h 80% -b rounded \
	-T " scratch: ${SESSION} " \
	-S "fg=${BORDER_FG}" \
	"'${SELF}' --attach '${SESSION}'"
