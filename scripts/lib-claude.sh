#!/usr/bin/env bash
# Shared Claude status utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.

# shellcheck disable=SC2034  # used by scripts that source this library
CLAUDE_STATUS_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
CLAUDE_PANES_DIR="$CLAUDE_STATUS_DIR/panes"
CLAUDE_SCREEN_DIR="$CLAUDE_STATUS_DIR/screen"
CLAUDE_ISSUES_DIR="$CLAUDE_STATUS_DIR/issues"
CLAUDE_TASKS_DIR="$CLAUDE_STATUS_DIR/tasks"
CLAUDE_NAMES_DIR="$CLAUDE_STATUS_DIR/names"
CLAUDE_INTERRUPT_DIR="$CLAUDE_STATUS_DIR/interrupt"
CLAUDE_SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
CLAUDE_ICON_WAITING="󰔟"
CLAUDE_ICON_COMPACTING="󰡍"
CLAUDE_ICON_DONE="󰸞"
CLAUDE_ICON_IDLE="󰒲"
CLAUDE_ICON_ERROR="󰅚"       # nerd: nf-md-close_circle_outline
CLAUDE_ICON_DENIED="󰔟"      # same clock as waiting, different color
CLAUDE_ICON_INTERRUPTED="󰜺" # nerd: nf-md-cancel — user-interrupted (Esc) turn

# Timestamp cache (set once per script invocation)
printf -v CLAUDE_NOW '%(%s)T' -1

# Staleness fade — the color stays the state's bright hue until its threshold,
# then eases toward dim grey over CLAUDE_FADE_DURATION seconds (not a hard snap).
CLAUDE_STALE_WAITING=30
CLAUDE_STALE_COMPACTING=60
CLAUDE_STALE_PROCESSING=300
CLAUDE_STALE_DONE=60
CLAUDE_STALE_ERROR=120
CLAUDE_STALE_DENIED=60
CLAUDE_FADE_DURATION=45

# Interrupt detection. No Claude Code hook fires on an Esc-interrupt, so a turn
# abandoned mid-flight just leaves the pane stuck at `processing` forever. The
# only durable trace is a marker line the interrupt writes into the transcript.
# read_pane_state reclassifies such a pane to `interrupted` — but only once it
# has been quiet past CLAUDE_INTERRUPT_CHECK_AGE, so normal tool churn (which
# refreshes the timestamp every PreToolUse/PostToolUse) never reads a transcript.
# A genuinely long-running tool keeps its `tool_use` block at the tail, not the
# marker, so it is not a false positive — it just costs one tail per read while
# it runs.
#
# The verdict is cached per pane at interrupt/<id> and reused while the stamp
# is newer than the transcript. Without it, a pane that stays `interrupted`
# (a derived state — panes/<id> still says `processing`) would fork tail every
# tick of every poller until its next prompt.
CLAUDE_INTERRUPT_CHECK_AGE=8
CLAUDE_INTERRUPT_MARKER="Request interrupted by user"

# claude_prune_stale_state SERVER_START
# Drops pane-id-keyed status files left behind by a previous tmux server. tmux
# restarts pane ids at %0 on server (re)start, so a restored pane can reuse a
# dead pane's id and inherit its cached name/task/issue — surfacing an unrelated
# window's label. The files carry no server generation, so mtime vs the server's
# start_time is the only signal: anything written before this server booted is
# stale. A marker file holding the current start_time gates the directory scan
# to once per server, so the per-tick status poller that calls this stays cheap.
claude_prune_stale_state() {
	local server_start=$1
	[[ -z $server_start ]] && return 0
	local marker="$CLAUDE_STATUS_DIR/.server_start"
	[[ -r $marker && $(<"$marker") == "$server_start" ]] && return 0
	local dir f mt
	for dir in "$CLAUDE_PANES_DIR" "$CLAUDE_SCREEN_DIR" "$CLAUDE_ISSUES_DIR" "$CLAUDE_TASKS_DIR" "$CLAUDE_NAMES_DIR" "$CLAUDE_INTERRUPT_DIR"; do
		[[ -d $dir ]] || continue
		for f in "$dir"/*; do
			[[ -f $f ]] || continue
			# GNU stat -c first, BSD/macOS stat -f fallback (mirrors lib-log.sh file_mtime).
			mt=$(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f" 2>/dev/null || echo 0)
			((mt < server_start)) && rm -f "$f"
		done
	done
	mkdir -p "$CLAUDE_STATUS_DIR"
	printf '%s\n' "$server_start" >"$marker"
}

# read_pane_state PANE_FILE_PATH
# Reads a pane state file and computes its staleness fade.
# Sets REPLY to the state string, REPLY_FADE to 0..100 (0 = fresh/full color,
# 100 = fully dim), REPLY_UNSEEN to 0 or 1, REPLY_TS to the pane's last-write
# epoch (the "last active" time; empty when the file carries no timestamp).
# Unseen means the agent reached a terminal state while the user was in another
# window. It pins the fade to 0 — the icon stays bright until the user focuses
# that window.
#
# Merges two sources keyed by the same pane id: the hook-written pane file
# (PANE_FILE_PATH, e.g. panes/<id>) and a screen-scraped state at
# screen/<id> (written by agent-detect for non-Claude agents). A fresh hook
# always wins — screen is only ground truth once the hook has gone stale
# (past its CLAUDE_STALE_* threshold) with no fresher signal of its own. The
# interrupt reclassifier below only makes sense for a stale *hook* reading
# with no screen fallback, so it's gated to that path.
# Returns 1 if neither source has a state.
read_pane_state() {
	local pane_file="$1"

	local state="" timestamp="" unseen="" session="" transcript="" key val
	if [[ -f $pane_file ]]; then
		while IFS='=' read -r key val; do
			case "$key" in
			state) state="$val" ;;
			timestamp) timestamp="$val" ;;
			unseen) unseen="$val" ;;
			session) session="$val" ;;
			transcript) transcript="$val" ;;
			esac
		done <"$pane_file"
	fi

	local screen_file="$CLAUDE_SCREEN_DIR/${pane_file##*/}"
	local screen_state="" screen_timestamp=""
	if [[ -f $screen_file ]]; then
		while IFS='=' read -r key val; do
			case "$key" in
			state) screen_state="$val" ;;
			timestamp) screen_timestamp="$val" ;;
			esac
		done <"$screen_file"
	fi

	if [[ -n $state ]]; then
		# max_age gates the screen-override only. The scraper distinguishes an
		# active spinner from a quiet input box and nothing more, so it must not
		# override a human-blocking hook state: `waiting`/`error`/`denied` look
		# identical to `idle` on screen and would be wrongly downgraded (the
		# session tally keeps them, so the window would silently disagree). Those
		# states are self-correcting — the next hook write clears them — so only
		# stale *active* states, which can stick when a completion hook is
		# missed, are corrected from the screen's live reading.
		local max_age=0
		case "$state" in
		compacting) max_age=$CLAUDE_STALE_COMPACTING ;;
		processing) max_age=$CLAUDE_STALE_PROCESSING ;;
		done) max_age=$CLAUDE_STALE_DONE ;;
		esac

		if ((max_age > 0)) && ((CLAUDE_NOW - timestamp > max_age)) && [[ -n $screen_state ]]; then
			# Hook stale past its threshold and a parser reading exists — screen
			# is live ground truth. Skip the reclassifier below: it only makes
			# sense for the hook's own `processing` state.
			state="$screen_state"
			timestamp="$screen_timestamp"
			unseen=""
			session=""
			transcript=""
		else
			# Hook governs (fresh, no threshold, or stale with no screen
			# fallback). Reclassify a long-quiet `processing` pane as
			# `interrupted` when the transcript tail holds the interrupt marker
			# (see CLAUDE_INTERRUPT_* above). Pinned bright (no stale entry →
			# fade 0; unseen=1) so the window stays noticeable until the next
			# prompt overwrites the file back to `processing`.
			if [[ $state == processing && -n $transcript && -n $timestamp ]] &&
				((CLAUDE_NOW - timestamp > CLAUDE_INTERRUPT_CHECK_AGE)); then
				# Reuse the cached verdict while nothing was appended to the
				# transcript ([[ -nt ]] is a fork-free mtime compare); a new
				# marker makes the transcript newer and forces a re-read. An
				# empty read (stamp caught mid-write by a concurrent poller)
				# falls through to a recompute.
				local stamp="$CLAUDE_INTERRUPT_DIR/${pane_file##*/}" verdict=""
				[[ $stamp -nt $transcript ]] && IFS= read -r verdict <"$stamp" 2>/dev/null
				if [[ -z $verdict ]]; then
					local tailbuf
					tailbuf=$(tail -n 2 "$transcript" 2>/dev/null) || tailbuf=""
					verdict=0
					[[ $tailbuf == *"$CLAUDE_INTERRUPT_MARKER"* ]] && verdict=1
					[[ -d $CLAUDE_INTERRUPT_DIR ]] || mkdir -p "$CLAUDE_INTERRUPT_DIR"
					printf '%s\n' "$verdict" >"$stamp"
				fi
				if [[ $verdict == 1 ]]; then
					state="interrupted"
					unseen="1"
				fi
			fi
		fi
	elif [[ -n $screen_state ]]; then
		state="$screen_state"
		timestamp="$screen_timestamp"
	else
		return 1
	fi

	REPLY_FADE=0
	if [[ -n $timestamp ]]; then
		local age=$((CLAUDE_NOW - timestamp)) start=0
		case "$state" in
		waiting) start=$CLAUDE_STALE_WAITING ;;
		compacting) start=$CLAUDE_STALE_COMPACTING ;;
		processing) start=$CLAUDE_STALE_PROCESSING ;;
		done) start=$CLAUDE_STALE_DONE ;;
		error) start=$CLAUDE_STALE_ERROR ;;
		denied) start=$CLAUDE_STALE_DENIED ;;
		esac
		if ((start > 0 && age > start)); then
			if ((age >= start + CLAUDE_FADE_DURATION)); then
				REPLY_FADE=100
			else
				REPLY_FADE=$(((age - start) * 100 / CLAUDE_FADE_DURATION))
			fi
		fi
	fi

	REPLY_UNSEEN=0
	[[ $unseen == "1" ]] && REPLY_UNSEEN=1

	REPLY_SESSION="$session"
	REPLY_TRANSCRIPT="$transcript"
	REPLY_TS="$timestamp"
	REPLY="$state"
}

# claude_pane_ids
# Emits the union of pane ids (basenames, no leading %) that have EITHER a
# hook state file (panes/<id>) OR a screen state file (screen/<id>), one per
# line, deduped, sorted numerically. Renderers iterate this instead of
# globbing panes/* alone so screen-only panes (non-Claude agents with no
# hook, e.g. Codex) surface too — read_pane_state already merges both sources
# once handed a panes/<id> path. Sorted output gives callers (e.g. issue-id
# collection) a stable order — associative-array iteration order is
# unspecified. Sorted in bash (ids are small integers, the set is a handful):
# forking `sort` costs more than sorting on this once-a-second path.
claude_pane_ids() {
	local -A seen=()
	local f id
	for f in "$CLAUDE_PANES_DIR"/* "$CLAUDE_SCREEN_DIR"/*; do
		[[ -f $f ]] || continue
		id="${f##*/}"
		seen["$id"]=1
	done
	local ids=("${!seen[@]}") i j k
	for ((i = 1; i < ${#ids[@]}; i++)); do
		k=${ids[i]}
		for ((j = i - 1; j >= 0 && ids[j] > k; j--)); do
			ids[j + 1]=${ids[j]}
		done
		ids[j + 1]=$k
	done
	[[ -n ${ids[*]:-} ]] || return 0
	printf '%s\n' "${ids[@]}"
}

# claude_ago SECONDS
# Formats an age in seconds as a single compact unit: 47s, 5m, 2h, 3d.
# Sets REPLY. Mirrors relAgo in picker/statusline/claude.go.
claude_ago() {
	local s="$1"
	if ((s < 60)); then
		REPLY="${s}s"
	elif ((s < 3600)); then
		REPLY="$((s / 60))m"
	elif ((s < 86400)); then
		REPLY="$((s / 3600))h"
	else
		REPLY="$((s / 86400))d"
	fi
}

# claude_state_icon STATE
# Maps state to its plain icon glyph (no color codes).
# Sets REPLY to the icon string, empty if unknown state.
claude_state_icon() {
	case "$1" in
	processing) REPLY="${CLAUDE_SPINNER_FRAMES[$((CLAUDE_NOW % ${#CLAUDE_SPINNER_FRAMES[@]}))]}" ;;
	waiting) REPLY="$CLAUDE_ICON_WAITING" ;;
	compacting) REPLY="$CLAUDE_ICON_COMPACTING" ;;
	done) REPLY="$CLAUDE_ICON_DONE" ;;
	idle) REPLY="$CLAUDE_ICON_IDLE" ;;
	error) REPLY="$CLAUDE_ICON_ERROR" ;;
	denied) REPLY="$CLAUDE_ICON_DENIED" ;;
	interrupted) REPLY="$CLAUDE_ICON_INTERRUPTED" ;;
	*) REPLY="" ;;
	esac
}

# setup_claude_colors
# Detects light/dark theme and sets per-state raw hex (H_*) plus the formatted
# C_* tmux color strings built from them. Must be called before any of the
# color/icon helpers below.
setup_claude_colors() {
	local theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
	local theme="dark"
	if [[ -f $theme_file ]]; then
		# Fork-free parse: slurp the small file and regex out the theme value
		# (grep|cut here forked twice on every colored status render).
		local content=""
		IFS= read -r -d '' content <"$theme_file" 2>/dev/null || true
		[[ $content =~ \"theme\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]] && theme="${BASH_REMATCH[1]}"
	fi

	if [[ $theme == "light" ]]; then
		H_W="#fe640b" H_K="#04a5e5" H_P="#179299" H_D="#40a02b" H_I="#6c6f85" H_E="#d20f39" H_DN="#df8e1d" H_INT="#8839ef"
	else
		H_W="#fab387" H_K="#89dceb" H_P="#94e2d5" H_D="#a6e3a1" H_I="#6c7086" H_E="#f38ba8" H_DN="#f9e2af" H_INT="#cba6f7"
	fi
	C_W="#[fg=$H_W]" C_K="#[fg=$H_K]" C_P="#[fg=$H_P]" C_D="#[fg=$H_D]" C_I="#[fg=$H_I]" C_E="#[fg=$H_E]" C_DN="#[fg=$H_DN]" C_INT="#[fg=$H_INT]"
	C_R="#[fg=default]"
}

# fade_hex FROM_HEX TO_HEX PCT
# Linearly interpolates between two #rrggbb colors; PCT 0 = FROM, 100 = TO.
# Sets REPLY to the resulting #rrggbb. Pure arithmetic — safe in hot paths.
fade_hex() {
	local from=$1 to=$2 pct=$3
	local fr=$((16#${from:1:2})) fg=$((16#${from:3:2})) fb=$((16#${from:5:2}))
	local tr=$((16#${to:1:2})) tg=$((16#${to:3:2})) tb=$((16#${to:5:2}))
	printf -v REPLY '#%02x%02x%02x' \
		$((fr + (tr - fr) * pct / 100)) \
		$((fg + (tg - fg) * pct / 100)) \
		$((fb + (tb - fb) * pct / 100))
}

# claude_faded_hex STATE [FADE] [UNSEEN]
# Sets REPLY to the state's hex, eased toward dim grey (H_I) by FADE (0..100).
# UNSEEN=1 pins to full color. Empty for an unknown state.
# Must call setup_claude_colors first.
claude_faded_hex() {
	case "$1" in
	waiting) REPLY=$H_W ;;
	compacting) REPLY=$H_K ;;
	processing) REPLY=$H_P ;;
	done) REPLY=$H_D ;;
	idle) REPLY=$H_I ;;
	error) REPLY=$H_E ;;
	denied) REPLY=$H_DN ;;
	interrupted) REPLY=$H_INT ;;
	*)
		REPLY=""
		return
		;;
	esac
	local fade=${2:-0}
	[[ ${3:-0} == 1 ]] && fade=0
	# `if`, not `&&` — a trailing false `((...))` would make this the function's
	# non-zero exit status and trip `set -e` in callers (claude-status).
	if ((fade > 0)); then
		fade_hex "$REPLY" "$H_I" "$fade"
	fi
}

# claude_colored_icon STATE [FADE] [UNSEEN]
# Returns tmux-colored icon string for a state.
# Sets REPLY to "#[fg=...]ICON#[fg=default] " or empty if unknown.
# FADE 0..100 eases the color toward dim grey; UNSEEN=1 keeps it bright.
# Must call setup_claude_colors first.
claude_colored_icon() {
	claude_state_icon "$1"
	local icon=$REPLY
	[[ -n $icon ]] || {
		REPLY=""
		return
	}
	claude_faded_hex "$1" "${2:-0}" "${3:-0}"
	REPLY="#[fg=${REPLY}]${icon}${C_R} "
}

# claude_priority_state WAITING COMPACTING PROCESSING DONE IDLE ERROR DENIED INTERRUPTED
# Given counts per state, returns the highest-priority non-zero state.
# Sets REPLY to state string, empty if all zero.
claude_priority_state() {
	local w=$1 k=$2 p=$3 d=$4 i=$5 e=${6:-0} dn=${7:-0} int=${8:-0}
	if ((e > 0)); then
		REPLY="error"
	elif ((w > 0)); then
		REPLY="waiting"
	elif ((dn > 0)); then
		REPLY="denied"
	elif ((k > 0)); then
		REPLY="compacting"
	elif ((int > 0)); then
		REPLY="interrupted"
	elif ((p > 0)); then
		REPLY="processing"
	elif ((d > 0)); then
		REPLY="done"
	elif ((i > 0)); then
		REPLY="idle"
	else
		REPLY=""
	fi
}

# tally_claude_state STATE ARRAY_PREFIX
# Increments the associative array entry ${ARRAY_PREFIX}[$STATE] by 1.
# Usage: tally_claude_state "processing" "win_claude"
#   → increments win_claude_processing
# This is a convenience for the common tally pattern.
tally_claude_state() {
	local state="$1" prefix="$2"
	local varname="${prefix}_${state}"
	# Use nameref for clean indirect access
	declare -n _ref="$varname" 2>/dev/null || return 0
	_ref=$((_ref + 1))
}

# format_issue_list MAX [ID...]
# Joins issue ids with spaces, capped at MAX ids followed by "+N" overflow.
# Sets REPLY to e.g. "ENG-1 ENG-2 ENG-3 +2", empty if no ids.
format_issue_list() {
	local max="$1"
	shift
	if (($# == 0)); then
		REPLY=""
		return
	fi
	if (($# <= max)); then
		REPLY="$*"
		return
	fi
	local overflow=$(($# - max))
	REPLY="${*:1:max} +$overflow"
}
