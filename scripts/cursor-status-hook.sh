#!/usr/bin/env bash
# Thin Cursor-hook entrypoint → claude-status-update.
# Stdout must stay empty: Cursor injects hook stdout additional_context into the model.
set -euo pipefail

csu="$(dirname "$0")/claude-status-update"
if [[ ! -x $csu ]]; then
	csu=$(command -v claude-status-update 2>/dev/null || true)
fi
[[ -n ${csu:-} && -x $csu ]] || exit 0

"$csu" "$@" >/dev/null 2>&1 || true
