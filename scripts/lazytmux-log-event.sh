#!/usr/bin/env bash
# Thin CLI around lib-log's log_event, so non-bash callers (the Go picker) can
# log without reimplementing the helper. No-ops when debug is off.
# Usage: lazytmux-log-event <category> [key value]...
set -euo pipefail
# shellcheck source=/dev/null
source "@lib_log@"
log_event "$@"
