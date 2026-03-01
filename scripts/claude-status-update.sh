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
SESSIONS_DIR="$STATE_DIR/sessions"

# Ensure directories exist
mkdir -p "$PANES_DIR" "$SESSIONS_DIR"

# Function to clean up stale pane entries (panes that no longer exist in tmux)
cleanup_stale_panes() {
	[[ -d $PANES_DIR ]] || return 0
	command -v tmux &>/dev/null || return 0

	# Get list of all active tmux pane IDs
	local active_panes
	active_panes=$(tmux list-panes -a -F '#{pane_id}' 2>/dev/null | sed 's/^%//' || true)

	# Check each pane file
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		local pane_file
		pane_file=$(basename "$pf")

		# If pane doesn't exist in tmux, remove the file
		if ! echo "$active_panes" | grep -q "^${pane_file}$"; then
			local pane_session
			pane_session=$(grep "^session=" "$pf" 2>/dev/null | cut -d= -f2)
			rm -f "$pf"
			# Update session aggregate if we know the session
			if [[ -n $pane_session ]]; then
				update_session_aggregate "$pane_session"
			fi
		fi
	done
}

# Sanitize session name for use as filename (replace / with _)
sanitize_filename() {
	echo "${1//\//_}"
}

# Function to update session aggregate state
update_session_aggregate() {
	local sess="$1"
	local sess_file
	sess_file=$(sanitize_filename "$sess")
	local has_waiting=false
	local has_processing=false
	local has_compacting=false
	local has_done=false
	local has_idle=false
	local count=0

	# Scan all pane files for this session
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue

		local pane_session pane_state
		pane_session=$(grep "^session=" "$pf" 2>/dev/null | cut -d= -f2)
		pane_state=$(grep "^state=" "$pf" 2>/dev/null | cut -d= -f2)

		[[ $pane_session == "$sess" ]] || continue

		((count++)) || true

		case "$pane_state" in
		waiting) has_waiting=true ;;
		processing) has_processing=true ;;
		compacting) has_compacting=true ;;
		done) has_done=true ;;
		idle) has_idle=true ;;
		esac
	done

	# Determine aggregate state (priority: waiting > compacting > processing > done > idle)
	local agg_state="idle"
	if $has_waiting; then
		agg_state="waiting"
	elif $has_compacting; then
		agg_state="compacting"
	elif $has_processing; then
		agg_state="processing"
	elif $has_done; then
		agg_state="done"
	elif $has_idle; then
		agg_state="idle"
	fi

	# Write session aggregate (use sanitized filename)
	if [[ $count -gt 0 ]]; then
		cat >"$SESSIONS_DIR/$sess_file" <<EOF
state=$agg_state
count=$count
timestamp=$(date +%s)
EOF
	else
		rm -f "$SESSIONS_DIR/$sess_file"
	fi
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
	# Update session aggregate
	if [[ -n $session_name ]]; then
		update_session_aggregate "$session_name"
	fi
	# Force immediate tmux refresh
	if [[ -n ${TMUX:-} ]]; then
		tmux refresh-client -S 2>/dev/null || true
	fi
	exit 0
fi

# Write pane state with timestamp
cat >"$PANES_DIR/$pane_file" <<EOF
state=$state
timestamp=$(date +%s)
session=$session_name
EOF

# Update session aggregate
if [[ -n $session_name ]]; then
	update_session_aggregate "$session_name"
fi

# Force immediate tmux status bar refresh (if in tmux)
if [[ -n ${TMUX:-} ]]; then
	tmux refresh-client -S 2>/dev/null || true
fi
