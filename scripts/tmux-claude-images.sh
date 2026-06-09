#!/usr/bin/env bash
# Toggle a split pane showing the image carousel for the invoking pane's Claude
# session. Bound to prefix+I, and runnable by Claude itself — it targets
# $TMUX_PANE (set for the keybind's run-shell and for a Claude Bash call alike),
# falling back to the active pane.
set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
IMAGES_DIR="$STATE_DIR/images"

src_pane="${TMUX_PANE:-$(tmux display-message -p '#{pane_id}')}"
manifest="$IMAGES_DIR/${src_pane#%}.jsonl"
[[ -s $manifest ]] || {
	tmux display-message "no images yet for this pane"
	exit 0
}

# Toggle: kill the existing viewer tagged for this source pane if present.
existing="$(tmux list-panes -F '#{pane_id} #{@claude_img_src}' |
	awk -v s="$src_pane" '$2 == s {print $1; exit}')"
if [[ -n $existing ]]; then
	tmux kill-pane -t "$existing"
	exit 0
fi

viewer="$(tmux split-window -h -P -F '#{pane_id}' "@picker_generate@ --gallery '$src_pane'")"
tmux set-option -p -t "$viewer" @claude_img_src "$src_pane"
