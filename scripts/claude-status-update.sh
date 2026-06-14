#!/usr/bin/env bash
# Update Claude status for tmux integration
# Called by Claude Code hooks to track session state
# Usage: claude-status-update <state> [--pane PANE_ID] [--session SESSION_NAME]
#
# States: processing, waiting, done, idle, compacting, error, denied
# Environment: Uses $TMUX_PANE and tmux to detect session if not provided

set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
PANES_DIR="$STATE_DIR/panes"
ISSUES_DIR="$STATE_DIR/issues"
TASKS_DIR="$STATE_DIR/tasks"
IMAGES_DIR="$STATE_DIR/images"

# Ensure directories exist
mkdir -p "$PANES_DIR"

# Event logging (no-op unless debug armed). Guarded so the RAW script still runs
# under tests/claude-issues.bats, where @lib_log@ is not substituted.
# shellcheck source=/dev/null
if [[ -f "@lib_log@" ]]; then
	source "@lib_log@"
else
	log_enabled() { return 1; }
	log_event() { :; }
fi

# Function to clean up stale pane entries
# Removes entries only for panes that no longer exist in tmux. A live pane is
# kept even when its foreground command isn't claude/opencode: Claude shells
# out constantly (builds, pagers, git), and that momentary command must not be
# read as "session gone" — doing so wipes issue stamps mid-session. Genuine
# exit is handled by the SessionEnd hook (`clear`), not by sampling here.
cleanup_stale_panes() {
	[[ -d $PANES_DIR ]] || return 0
	command -v tmux &>/dev/null || return 0

	# Build lookup: pane_id (without %) -> 1 for every pane that still exists
	declare -A pane_exists
	while IFS=$'\t' read -r pid _; do
		pane_exists["${pid#%}"]=1
	done < <(tmux list-panes -a -F '#{pane_id}	#{pane_current_command}' 2>/dev/null || true)

	# Check each pane file
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		local pane_file="${pf##*/}"

		if [[ -z ${pane_exists[$pane_file]+x} ]]; then
			rm -f "$pf" "$ISSUES_DIR/${pf##*/}" "$TASKS_DIR/${pf##*/}" "$IMAGES_DIR/${pf##*/}.jsonl"
		fi
	done

	# Orphaned issue / task files (pane gone)
	for inf in "$ISSUES_DIR"/* "$TASKS_DIR"/*; do
		[[ -f $inf ]] || continue
		if [[ -z ${pane_exists[${inf##*/}]+x} ]]; then
			rm -f "$inf"
		fi
	done
}

# Parse arguments
state="${1:-}"
pane_id="${TMUX_PANE:-}"
session_name=""
force=0

shift || true

# Issue self-report: tracks which issues CC is working on, in a file SEPARATE
# from the pane state file — state hooks fire around the very Bash call that
# runs `issue add`, so sharing the pane file would lose updates.
if [[ $state == "issue" ]]; then
	action="${1:-}"
	shift || true
	id=""
	case "$action" in
	add | done)
		if [[ ${1:-} != --* ]]; then
			id="${1:-}"
			shift || true
		fi
		;;
	clear) ;;
	*)
		echo "Error: Invalid issue action '$action'. Use: add, done, clear" >&2
		exit 1
		;;
	esac
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--pane)
			pane_id="$2"
			shift 2
			;;
		*) shift ;;
		esac
	done
	if [[ $action != "clear" && ! $id =~ ^[A-Za-z0-9_-]+$ ]]; then
		echo "Error: Invalid issue id '$id' (allowed: A-Z a-z 0-9 _ -)" >&2
		exit 1
	fi
	[[ -z $pane_id ]] && exit 0
	issues_file="$ISSUES_DIR/${pane_id#%}"
	case "$action" in
	add)
		mkdir -p "$ISSUES_DIR"
		current=""
		[[ -f $issues_file ]] && IFS= read -r current <"$issues_file" || true
		case ",$current," in
		*",$id,"*) ;;
		*)
			[[ -n $current ]] && current+=","
			printf '%s\n' "$current$id" >"$issues_file"
			;;
		esac
		;;
	done)
		if [[ -f $issues_file ]]; then
			IFS= read -r current <"$issues_file" || true
			keep=()
			IFS=',' read -r -a ids <<<"$current"
			for i in "${ids[@]}"; do
				[[ $i == "$id" || -z $i ]] || keep+=("$i")
			done
			if ((${#keep[@]})); then
				(
					IFS=','
					printf '%s\n' "${keep[*]}"
				) >"$issues_file"
			else
				rm -f "$issues_file"
			fi
		fi
		;;
	clear)
		rm -f "$issues_file"
		;;
	esac
	log_enabled && log_event claude event issue op "$action" id "${id:-}" pane "%${pane_id#%}"
	if [[ -n ${TMUX:-} ]]; then
		tmux refresh-client -S 2>/dev/null || true
	fi
	exit 0
fi

# Task self-report: a short freeform phrase for "what this pane's Claude is
# doing", captured from the latest user prompt (UserPromptSubmit hook). Kept in
# its own file like the issue ids; surfaces as the window-label fallback when no
# branch/issue/PR distinguishes the window (multiple Claudes in one checkout).
if [[ $state == "task" ]]; then
	action="${1:-}"
	shift || true
	text=""
	case "$action" in
	set)
		# Text is mandatory and free-form (a captured prompt), so take it
		# verbatim — even a leading "--" is content here, not a flag.
		text="${1:-}"
		shift || true
		;;
	clear) ;;
	*)
		echo "Error: Invalid task action '$action'. Use: set, clear" >&2
		exit 1
		;;
	esac
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--pane)
			pane_id="$2"
			shift 2
			;;
		*) shift ;;
		esac
	done
	[[ -z $pane_id ]] && exit 0
	tasks_file="$TASKS_DIR/${pane_id#%}"
	case "$action" in
	set)
		# Squeeze to one line, drop control chars, trim, cap length — the value
		# becomes a tmux window option and a status-bar segment. Delete cntrl (not
		# keep [:print:]): tr is byte-oriented, so [:print:] would strip every
		# non-ASCII byte and mangle UTF-8 (emoji, accents, RTL text).
		clean=$(printf '%s' "$text" | tr '\n\r\t' '   ' | tr -d '[:cntrl:]' | tr -s ' ')
		clean="${clean# }"
		clean="${clean:0:60}"
		clean="${clean% }"
		if [[ -n $clean ]]; then
			mkdir -p "$TASKS_DIR"
			printf '%s\n' "$clean" >"$tasks_file"
		fi
		;;
	clear)
		rm -f "$tasks_file"
		;;
	esac
	if [[ -n ${TMUX:-} ]]; then
		tmux refresh-client -S 2>/dev/null || true
	fi
	exit 0
fi

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
	rm -f "$PANES_DIR/$pane_file" "$ISSUES_DIR/$pane_file" "$TASKS_DIR/$pane_file"
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

# Log only real transitions (from != to). Pre/PostToolUse both fire "processing"
# on every tool call, so logging every write would flood. Prior-state read +
# tmux id lookups happen only when debug is armed.
if log_enabled; then
	prior_state="none"
	if [[ -f "$PANES_DIR/$pane_file" ]]; then
		while IFS='=' read -r _k _v; do
			[[ $_k == state ]] && {
				prior_state="$_v"
				break
			}
		done <"$PANES_DIR/$pane_file"
	fi
	if [[ $prior_state != "$state" ]]; then
		win_id=$(tmux display-message -t "$pane_id" -p '#{window_id}' 2>/dev/null || true)
		win_idx=$(tmux display-message -t "$pane_id" -p '#{window_index}' 2>/dev/null || true)
		log_event claude event transition from "$prior_state" to "$state" \
			pane "$pane_id" win_id "$win_id" win "$win_idx" sess "$session_name"
	fi
fi

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
