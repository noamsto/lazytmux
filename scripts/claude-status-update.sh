#!/usr/bin/env bash
# Update Claude status for tmux integration
# Called by Claude Code hooks to track session state
# Usage: claude-status-update <state> [--pane PANE_ID] [--session SESSION_NAME]
#
# States: processing, waiting, done, idle
# Environment: Uses $TMUX_PANE and tmux to detect session if not provided

set -euo pipefail

STATE_DIR="/tmp/claude-status"
PANES_DIR="$STATE_DIR/panes"

# Ensure directories exist
mkdir -p "$PANES_DIR"

# Function to clean up stale pane entries
# Removes entries for panes that either:
#   1. No longer exist in tmux
#   2. Exist but are no longer running claude
cleanup_stale_panes() {
	[[ -d $PANES_DIR ]] || return 0
	command -v tmux &>/dev/null || return 0

	# Build lookup: pane_id (without %) -> current_command
	declare -A pane_commands
	while IFS=$'\t' read -r pid cmd; do
		pane_commands["${pid#%}"]="$cmd"
	done < <(tmux list-panes -a -F '#{pane_id}	#{pane_current_command}' 2>/dev/null || true)

	# Check each pane file
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		local pane_file="${pf##*/}"

		local should_remove=false

		if [[ -z ${pane_commands[$pane_file]+x} ]]; then
			# Pane no longer exists in tmux
			should_remove=true
		elif [[ ${pane_commands[$pane_file]} != "claude" ]]; then
			# Pane exists but is no longer running claude
			should_remove=true
		fi

		if $should_remove; then
			rm -f "$pf"
		fi
	done
}

# Parse arguments
state="${1:-}"
pane_id="${TMUX_PANE:-}"
session_name=""

shift || true
while [[ $# -gt 0 ]]; do
	case "$1" in
	--pane)
		pane_id="$2"
		shift 2
		;;
	--session)
		session_name="$2"
		shift 2
		;;
	*)
		shift
		;;
	esac
done

# Validate state
case "$state" in
processing | waiting | done | idle | compacting | clear | cleanup) ;;
*)
	echo "Error: Invalid state '$state'. Use: processing, waiting, done, idle, compacting, clear, cleanup" >&2
	exit 1
	;;
esac

# Handle cleanup command (removes stale pane entries)
if [[ $state == "cleanup" ]]; then
	cleanup_stale_panes
	# Force immediate tmux refresh
	if [[ -n ${TMUX:-} ]]; then
		tmux refresh-client -S 2>/dev/null || true
	fi
	exit 0
fi

# If no pane_id, silently exit (common when called from non-tmux context)
if [[ -z $pane_id ]]; then
	exit 0
fi

# Get session name from tmux if not provided
if [[ -z $session_name ]] && command -v tmux &>/dev/null; then
	session_name=$(tmux display-message -p -t "$pane_id" '#{session_name}' 2>/dev/null || true)
fi

# Clean pane_id for filename (remove % prefix if present)
pane_file="${pane_id#%}"

# Handle clear state (cleanup)
if [[ $state == "clear" ]]; then
	rm -f "$PANES_DIR/$pane_file"
	# Force immediate tmux refresh
	if [[ -n ${TMUX:-} ]]; then
		tmux refresh-client -S 2>/dev/null || true
	fi
	exit 0
fi

# Write pane state with timestamp
printf -v _now '%(%s)T' -1
cat >"$PANES_DIR/$pane_file" <<EOF
state=$state
timestamp=$_now
session=$session_name
EOF

# Force immediate tmux status bar refresh (if in tmux)
if [[ -n ${TMUX:-} ]]; then
	tmux refresh-client -S 2>/dev/null || true
fi
