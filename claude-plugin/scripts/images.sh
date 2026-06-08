#!/usr/bin/env bash
# Degrade gracefully: lazytmux not installed, or CC outside a lazytmux pane →
# silently no-op instead of erroring on every PostToolUse event.
command -v claude-images-update >/dev/null 2>&1 || exit 0
exec claude-images-update
