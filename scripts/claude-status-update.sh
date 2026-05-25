#!/usr/bin/env bash
# Update Claude status for tmux integration
# Called by Claude Code hooks to track session state
# Usage: claude-status-update <state> [--pane PANE_ID] [--session SESSION_NAME]
#
# States: processing, waiting, done, idle, compacting, error, denied
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
		elif [[ ${pane_commands[$pane_file]} != "claude" && ${pane_commands[$pane_file]} != "opencode" ]]; then
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
force=0

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
	--force)
		force=1
		shift
		;;
	*)
		shift
		;;
	esac
done

# Validate state
case "$state" in
processing | waiting | done | idle | compacting | error | denied | clear | cleanup | mark-seen) ;;
*)
	echo "Error: Invalid state '$state'. Use: processing, waiting, done, idle, compacting, denied, clear, cleanup, mark-seen" >&2
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

# Handle mark-seen: clear unseen flag for all panes in the active window.
# Called by tmux hooks on window/session switch.
# Usage: claude-status-update mark-seen --session <name> --window <index>
if [[ $state == "mark-seen" ]]; then
	win_target=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--session)
			shift
			session_name="$1"
			shift
			;;
		--window)
			shift
			win_target="$1"
			shift
			;;
		*) shift ;;
		esac
	done
	[[ -n $session_name && -n $win_target ]] || exit 0
	[[ -d $PANES_DIR ]] || exit 0
	# Get pane IDs in the target window
	declare -A target_panes
	while IFS= read -r pid; do
		target_panes["${pid#%}"]=1
	done < <(tmux list-panes -t "${session_name}:${win_target}" -F '#{pane_id}' 2>/dev/null)
	# Remove unseen flag from matching pane files
	changed=false
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		[[ -n ${target_panes[$pane_file]+x} ]] || continue
		grep -q '^unseen=1$' "$pf" || continue
		# Rewrite without the unseen line
		grep -v '^unseen=' "$pf" >"${pf}.tmp" && mv "${pf}.tmp" "$pf"
		changed=true
	done
	if $changed && [[ -n ${TMUX:-} ]]; then
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

# Don't let benign transitions overwrite higher-priority interactive states.
# - "processing" from rapid Pre/PostToolUse hooks must not clobber waiting/error/denied.
# - "done" from Stop must not clobber error/waiting/denied — if a tool failed
#   right before Stop fired, the user should still see the failure.
# --force bypasses this guard (used by UserPromptSubmit: a new user prompt is an
# explicit reset signal and must clear any stale terminal state).
if [[ $force -eq 0 ]] && [[ $state == "processing" || $state == "done" ]] && [[ -f "$PANES_DIR/$pane_file" ]]; then
	cur_state=""
	while IFS='=' read -r key val; do
		[[ $key == "state" ]] && {
			cur_state="$val"
			break
		}
	done <"$PANES_DIR/$pane_file"
	case "$cur_state" in
	waiting | error | denied) exit 0 ;;
	esac
fi

# For terminal states (done/error/waiting), check if the pane's window is
# currently focused. If not, mark as unseen so the status bar can show an
# attention indicator that persists through staleness dimming.
unseen_line=""
case "$state" in
done | error | waiting | denied)
	win_active=$(tmux display-message -t "$pane_id" -p '#{window_active}' 2>/dev/null) || win_active="1"
	[[ $win_active == "0" ]] && unseen_line=$'\n'"unseen=1"
	;;
esac

# Write pane state with timestamp
printf -v _now '%(%s)T' -1
cat >"$PANES_DIR/$pane_file" <<EOF
state=$state
timestamp=$_now
session=$session_name${unseen_line}
EOF

# Force immediate tmux status bar refresh (if in tmux)
if [[ -n ${TMUX:-} ]]; then
	tmux refresh-client -S 2>/dev/null || true
fi
