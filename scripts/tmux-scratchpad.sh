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
	# Hints live in the popup top-border title (set in outer mode); the
	# scratch session itself runs without a status bar.
	# Direct argv, not a command string piped to `tmux source -`: the name is
	# session-derived, and a "'" in it closes the '$SCRATCH' quoting so tmux
	# reparses the remainder as command words (measured: "too few arguments").
	tmux set -t "$SCRATCH" detach-on-destroy on 2>/dev/null || true
	tmux set -t "$SCRATCH" status off 2>/dev/null || true
	# new-session -A is the correct way to attach inside a display-popup
	# (attach-session doesn't work reliably in popup PTY context).
	exec tmux new-session -A -s "$SCRATCH"
fi

# ── Outer mode: called from keybinding via run-shell ───────────────────────
CLIENT=""
if [[ ${1:-} == --client ]]; then
	CLIENT=${2:-}
	shift 2 || shift
fi

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

# Title doubles as a hint bar: key bindings are colored to stand out from
# the descriptions. tmux interpolates #{@thm_*} format strings in -T.
TITLE=" #[fg=#{@thm_lavender}]scratch: ${SESSION}#[fg=#{@thm_overlay_1}]  ·  #[fg=#{@thm_lavender}]\`d#[fg=#{@thm_overlay_1}] hide  ·  #[fg=#{@thm_lavender}]exit#[fg=#{@thm_overlay_1}] close "

# Pin the client: unpinned, tmux re-resolves to the session's most-recently-active
# client, which on a bridged host can be the tty-less control client (#346).
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
# display-popup -E runs its argument through a shell, so both halves have to be
# shell-quoted by us: the session name is remote-derived on a bridged session
# (lztmux-remote-open names it from the remote's list), and a "'" in it would
# otherwise close the quoting and execute the rest.
printf -v POPUP_CMD '%q --attach %q' "$SELF" "$SESSION"
tmux display-popup "${POPUP_CLIENT[@]}" -E -w 80% -h 80% -b rounded \
	-T "$TITLE" \
	-S "fg=${BORDER_FG}" \
	"$POPUP_CMD"
