#!/usr/bin/env bash
set -euo pipefail

# Smart copy-mode for Claude Code panes.
# Claude Code with CLAUDE_CODE_NO_FLICKER=1 uses the alternate screen buffer,
# hiding scrollback from tmux. Ctrl+o → [ dumps the transcript to native
# scrollback on demand.
#
# enter: detects Claude Code, dumps transcript, then enters copy-mode
# exit is handled by a pane-mode-changed hook in tmux.conf that checks
# @claude-copy-mode and sends Escape to leave the transcript viewer

pane_info=$(tmux display-message -p '#{pane_tty} #{alternate_on}')
tty="${pane_info% *}"
alt_screen="${pane_info#* }"

# Only dump transcript if Claude Code is running AND using the alternate screen
# (CLAUDE_CODE_NO_FLICKER=1). Without alt-screen, scrollback is already in tmux.
# Need ps -t to filter by TTY; pgrep can't do this
# shellcheck disable=SC2009
if [[ $alt_screen == "1" ]] && ps -o comm= -t "$tty" 2>/dev/null | grep -qE '^(claude|claude-code)$'; then
	tmux set-option -p @claude-copy-mode 1
	tmux send-keys C-o
	sleep 0.3
	tmux send-keys '['
	sleep 0.5
fi
tmux copy-mode
