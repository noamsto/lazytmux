#!/usr/bin/env bash
# Degrade gracefully: lazytmux not installed, or CC running outside a lazytmux
# tmux pane → silently no-op instead of erroring on every hook event.
command -v claude-status-update >/dev/null 2>&1 || exit 0
exec claude-status-update "$@"
