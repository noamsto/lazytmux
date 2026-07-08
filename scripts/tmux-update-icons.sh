#!/usr/bin/env bash
# Lightweight icon updater called via #() every status-interval.
# Updates @window_icon_display (unpadded, for window names / top-right)
# and @window_icon_padded (fixed-width, for status bar alignment).
# Includes colored claude status icon in both variables.
# Outputs nothing (side-effect only).

# shellcheck source=/dev/null  # Nix store paths substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_claude@

# Known agent commands (must match the shipped manifests). TODO: derive from
# `agent-detect --commands` once more agents land.
AGENT_COMMANDS="claude codex"
# ${AGENT_DETECT_BIN:-...} lets tests inject a real path via env; Nix build
# substitution still wins in the shipped script (no env var set at runtime).
AGENT_DETECT_BIN="${AGENT_DETECT_BIN:-@agent_detect_bin@}"

# Arms `agent-detect` on agent panes that don't already have a live pipe.
# Using #{pane_pipe} as the gate means a dead parser (pipe closes -> pane_pipe
# 0) self-heals on a later tick.
arm_agent_detect() {
	[[ $AGENT_DETECT_BIN == @* ]] && return 0
	# The sweep is a full-server list-panes — a second tmux roundtrip per tick,
	# multiplied by attached sessions. Arming (new pane, dead pipe) only needs
	# seconds-level latency, so run every 5th tick (CLAUDE_NOW = this tick's
	# epoch second).
	((CLAUDE_NOW % 5)) && return 0

	local pid cmd piped
	while IFS=$'\t' read -r pid cmd piped; do
		[[ $piped == 0 ]] || continue
		case " $AGENT_COMMANDS " in *" $cmd "*) ;; *) continue ;; esac
		tmux pipe-pane -o -t "$pid" "$AGENT_DETECT_BIN ${pid#%}"
	done < <(tmux list-panes -a -F '#{pane_id}	#{pane_current_command}	#{pane_pipe}' 2>/dev/null || true)
}

main() {
	SESSION=${1:-$(tmux display-message -p '#{session_name}')}
	# $2 is #{@resume_claude}, expanded by the status format — avoids a show-option
	# fork per tick. "on" enables stamping each Claude pane's @ts_relaunch override
	# so tmux-state resumes the session (not a bare shell) on restore.
	RESUME_CLAUDE=${2:-}
	# $3 is #{start_time}, expanded by the status format like $2 — avoids a
	# display-message fork per tick; direct invocations (hooks) fall back to one.
	SERVER_START=${3:-$(tmux display-message -p '#{start_time}')}
	MAX_ICONS=@MAX_ICONS@

	setup_claude_colors

	# Purge pane-keyed status left by a previous tmux server before deriving any
	# label, so a restored pane that reused a dead pane's id doesn't inherit its
	# name/task. No-op after the first tick of each server (marker-gated).
	claude_prune_stale_state "$SERVER_START"

	# --- Single batched list-panes call: all data in one tmux IPC roundtrip ---
	declare -A pane_to_win win_procs win_pane_path win_cur_branch win_active_pane win_cur_task win_cur_name pane_cur_relaunch
	declare -A win_cur_display win_cur_padded win_cur_ago win_cur_rename
	active_pane_proc=""
	active_win_idx=""
	cur_active_icon=""
	cur_session_fg=""
	# '|' delimiter, not tab: tab is IFS-whitespace, so an empty middle field (a
	# window with no @branch yet) collapses and shifts every later field left,
	# corrupting cur_branch/active flags. '@window_task' is free-form so it stays
	# last — read drops any stray '|' it contains into that final field.
	# @window_ai_name is sanitized free of '|' (claude-status-update), so it is safe
	# as a fixed middle field. @ts_relaunch is "claude --resume <uuid>" — no '|'
	# either, so it also stays a fixed middle field before the free-form task.
	# The icon/ago/rename/session fields are our own writes read back for
	# change-gating: glyphs, #[fg=…] codes, spaces, and hex colors — never '|'.
	while IFS='|' read -r pane_id idx pane_path proc cur_branch pane_active window_active cur_ai_name cur_relaunch cur_display cur_padded cur_ago cur_rename opt_active_icon opt_session_fg cur_task; do
		pane_to_win["${pane_id#%}"]="$idx"
		pane_cur_relaunch["${pane_id#%}"]="$cur_relaunch"
		# Session options (same on every row) must be copied here: the EOF read
		# that ends the loop blanks the read variables themselves.
		cur_active_icon="$opt_active_icon"
		cur_session_fg="$opt_session_fg"
		# First pane per window wins for path/branch/task — panes in a window share a
		# cwd, and @window_task/@branch are window options (same for every pane).
		if [[ -z ${win_pane_path[$idx]+x} ]]; then
			win_pane_path[$idx]="$pane_path"
			win_cur_branch[$idx]="$cur_branch"
			win_cur_task[$idx]="$cur_task"
			win_cur_name[$idx]="$cur_ai_name"
			win_cur_display[$idx]="$cur_display"
			win_cur_padded[$idx]="$cur_padded"
			win_cur_ago[$idx]="$cur_ago"
			win_cur_rename[$idx]="$cur_rename"
		fi
		# The task file is keyed by the pane Claude runs in, so resolve the genuinely
		# active pane (list-panes orders by index, not active-first).
		[[ $pane_active == 1 ]] && win_active_pane[$idx]="${pane_id#%}"
		[[ $window_active == 1 ]] && active_win_idx="$idx"
		# Track the session's active pane command (active pane in active window)
		[[ $pane_active == 1 && $window_active == 1 ]] && active_pane_proc="$proc"
		# Collect unique processes per window
		[[ -z $proc ]] && continue
		existing="${win_procs[$idx]:-}"
		case " $existing " in
		*" $proc "*) ;;
		*) win_procs[$idx]="${existing:+$existing }$proc" ;;
		esac
	done < <(tmux list-panes -s -t "$SESSION" -F '#{pane_id}|#{window_index}|#{pane_current_path}|#{pane_current_command}|#{@branch}|#{pane_active}|#{window_active}|#{@window_ai_name}|#{@ts_relaunch}|#{@window_icon_display}|#{@window_icon_padded}|#{@window_claude_ago}|#{automatic-rename}|#{@active_pane_icon}|#{@claude_session_fg}|#{@window_task}')

	arm_agent_detect

	# --- Claude status: read pane files, bucket by window index ---
	declare -A win_claude_state win_claude_fade win_claude_unseen win_claude_ts
	# Session-wide tally drives the status-bar session-name tint (@claude_session_fg)
	sess_w=0 sess_k=0 sess_p=0 sess_d=0 sess_i=0 sess_e=0 sess_dn=0 sess_int=0
	sess_min_fade=100 sess_unseen=0
	while IFS= read -r pane_file; do
		[[ -n $pane_file ]] || continue
		win_idx="${pane_to_win[$pane_file]:-}"
		[[ -n $win_idx ]] || continue
		read_pane_state "$CLAUDE_PANES_DIR/$pane_file" || continue
		state="$REPLY"
		fade=$REPLY_FADE
		unseen=$REPLY_UNSEEN

		# Stamp the pane's resume override so tmux-state relaunches the actual
		# Claude session (not a bare shell) on restore. The transcript basename
		# is the session UUID. Set only on change — @ts_relaunch is read back via
		# the batched list-panes above, so a stable pane forks nothing per tick.
		if [[ $RESUME_CLAUDE == on ]]; then
			uuid="${REPLY_TRANSCRIPT##*/}"
			uuid="${uuid%.jsonl}"
			desired=""
			[[ -n $uuid ]] && desired="claude --resume $uuid"
			if [[ $desired != "${pane_cur_relaunch[$pane_file]:-}" ]]; then
				tmux set -pq -t "%$pane_file" @ts_relaunch "$desired"
			fi
		fi
		# Freshest pane timestamp per window drives the "last active" label
		[[ -n $REPLY_TS ]] && ((REPLY_TS > ${win_claude_ts[$win_idx]:-0})) &&
			win_claude_ts[$win_idx]=$REPLY_TS
		# Session aggregate: count states, freshest pane wins the fade
		case "$state" in
		error) ((sess_e++)) ;;
		waiting) ((sess_w++)) ;;
		compacting) ((sess_k++)) ;;
		interrupted) ((sess_int++)) ;;
		processing) ((sess_p++)) ;;
		done) ((sess_d++)) ;;
		idle) ((sess_i++)) ;;
		denied) ((sess_dn++)) ;;
		esac
		((fade < sess_min_fade)) && sess_min_fade=$fade
		[[ $unseen == 1 ]] && sess_unseen=1
		# Priority merge: error > waiting > compacting > interrupted > processing > done > idle
		# Fade and unseen track the winning state's pane
		current="${win_claude_state[$win_idx]:-}"
		case "$state" in
		error) win_claude_state[$win_idx]="error" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		waiting) [[ $current != "error" ]] && win_claude_state[$win_idx]="waiting" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		compacting) [[ $current != "error" && $current != "waiting" ]] && win_claude_state[$win_idx]="compacting" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		interrupted) [[ $current != "error" && $current != "waiting" && $current != "compacting" ]] && win_claude_state[$win_idx]="interrupted" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		processing) [[ $current != "error" && $current != "waiting" && $current != "compacting" && $current != "interrupted" ]] && win_claude_state[$win_idx]="processing" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		done) [[ -z $current || $current == "idle" ]] && win_claude_state[$win_idx]="done" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		idle) [[ -z $current ]] && win_claude_state[$win_idx]="idle" win_claude_fade[$win_idx]=$fade win_claude_unseen[$win_idx]=$unseen ;;
		esac
	done < <(claude_pane_ids)

	# Session-name color: tint with the aggregate claude state, faded by the
	# freshest pane's age. Empty when no claude panes — the format falls back to
	# the theme's session color.
	claude_priority_state "$sess_w" "$sess_k" "$sess_p" "$sess_d" "$sess_i" "$sess_e" "$sess_dn" "$sess_int"
	claude_faded_hex "$REPLY" "$sess_min_fade" "$sess_unseen"
	session_fg=$REPLY

	# --- Compute process icons + claude per window, measure display widths ---
	declare -a all_idx=()
	declare -A win_icons win_icon_dw win_display

	# Collect all tmux set commands to batch via `tmux source -`
	tmux_cmds=""
	branch_changed=0
	labels_changed=0

	for idx in "${!win_pane_path[@]}"; do
		all_idx+=("$idx")
		pane_path="${win_pane_path[$idx]}"
		target="${SESSION}:${idx}"

		# Task label tracks the active pane's self-reported "what Claude is doing"
		# phrase (UserPromptSubmit hook). It can change in any window, so poll every
		# window each tick — a single small file read. Set directly (not batched via
		# `source -`): the phrase is free-form and would break the command parser.
		task=""
		[[ -f "$CLAUDE_TASKS_DIR/${win_active_pane[$idx]}" ]] &&
			IFS= read -r task <"$CLAUDE_TASKS_DIR/${win_active_pane[$idx]}"
		if [[ $task != "${win_cur_task[$idx]:-}" ]]; then
			tmux set -qw -t "$target" @window_task "$task"
			labels_changed=1
		fi

		# AI name: the active pane's Claude-set window title (claude-status-update
		# name set, prompted by the UserPromptSubmit nudge on fallback windows).
		# build_window_label prefers it over the raw task. Mirror like the task —
		# free-form, set directly, only on change so reflow isn't kicked every tick.
		ai_name=""
		[[ -f "$CLAUDE_NAMES_DIR/${win_active_pane[$idx]}" ]] &&
			IFS= read -r ai_name <"$CLAUDE_NAMES_DIR/${win_active_pane[$idx]}"
		if [[ $ai_name != "${win_cur_name[$idx]:-}" ]]; then
			tmux set -qw -t "$target" @window_ai_name "$ai_name"
			labels_changed=1
		fi

		# Branch detection forks git per window. A branch only changes in the window
		# where a checkout/cd happens, so poll only the active window each tick;
		# inactive windows trust their cached @branch (worktrunk stamps it on switch).
		# A window with no @branch yet (manual new-window, restore) is polled once to
		# seed it, then trusted — this caps the steady git fork rate at ~1/tick.
		if [[ $idx == "$active_win_idx" || -z ${win_cur_branch[$idx]:-} ]]; then
			# timeout so a stuck git (NFS stall, held index.lock) can't wedge the
			# whole icon updater — it degrades to the cached branch for that tick.
			branch=$(timeout 2 git -C "$pane_path" branch --show-current 2>/dev/null) || branch=""
			if [[ $branch != "${win_cur_branch[$idx]:-}" ]]; then
				tmux_cmds+="set -qw -t '$target' @branch '$branch'"$'\n'
				# Re-derive git root when branch changes (different repo or worktree)
				git_root=$(timeout 2 git -C "$pane_path" rev-parse --show-toplevel 2>/dev/null) || git_root=""
				tmux_cmds+="set -qw -t '$target' @git_root '$git_root'"$'\n'
				branch_changed=1
			fi
		fi

		# Build process icons from batched data
		build_proc_icons "${win_procs[$idx]:-}" "$MAX_ICONS"
		proc_icon_str="${REPLY% }"
		icon="$REPLY"
		# shellcheck disable=SC2153 # REPLY_DW set by build_proc_icons (sourced lib)
		icon_dw=$REPLY_DW

		# Append colored claude status icon (shares the icon column)
		c_state="${win_claude_state[$idx]:-}"
		display="${proc_icon_str}"
		claude_colored_icon "$c_state" "${win_claude_fade[$idx]:-0}" "${win_claude_unseen[$idx]:-0}"
		if [[ -n $REPLY ]]; then
			icon+="$REPLY"
			((icon_dw += 2)) # 1-cell nerd font icon + 1 space
			# Add to display with space separator if process icons exist
			[[ -n $display ]] && display+=" "
			display+="${REPLY% }" # strip trailing space for display
		fi

		win_icons[$idx]="$icon"
		win_icon_dw[$idx]=$icon_dw
		win_display[$idx]="$display"

		# "Last active" time: shown only for halted states (the live icon already
		# conveys active ones). A bare unit like "5m" is parser-safe, so batch it.
		# Gated on change (read back for free via the batched list-panes) — it only
		# ticks over once a minute.
		ago=""
		case "$c_state" in
		idle | done | interrupted | error)
			ts="${win_claude_ts[$idx]:-0}"
			if ((ts > 0 && CLAUDE_NOW > ts)); then
				claude_ago "$((CLAUDE_NOW - ts))"
				ago="$REPLY"
			fi
			;;
		esac
		if [[ $ago != "${win_cur_ago[$idx]:-}" ]]; then
			tmux_cmds+="set -qw -t '$target' @window_claude_ago '$ago'"$'\n'
		fi
	done

	# Set active pane icon for top-right display (from batched data)
	active_icon=""
	# Normalize nix makeWrapper's `.foo-wrapped` to `foo` (see lib-icons).
	[[ $active_pane_proc == .*-wrapped ]] && active_pane_proc="${active_pane_proc#.}" && active_pane_proc="${active_pane_proc%-wrapped}"
	[[ -n $active_pane_proc ]] && active_icon="${ICON_MAP[$active_pane_proc]:-}"
	if [[ $active_icon != "$cur_active_icon" ]]; then
		tmux_cmds+="set -q -t '$SESSION' @active_pane_icon '$active_icon'"$'\n'
	fi
	if [[ $session_fg != "$cur_session_fg" ]]; then
		tmux_cmds+="set -q -t '$SESSION' @claude_session_fg '$session_fg'"$'\n'
	fi

	# --- Second pass: set unpadded + padded icon variables ---
	# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd font claude (2 cells)
	TARGET_DW=$((MAX_ICONS * 3 + 2))
	for idx in "${all_idx[@]}"; do
		target="${SESSION}:${idx}"

		# Unpadded (for window names — process icons + colored claude)
		if [[ ${win_display[$idx]} != "${win_cur_display[$idx]:-}" ]]; then
			tmux_cmds+="set -qw -t '$target' @window_icon_display '${win_display[$idx]}'"$'\n'
		fi

		# Re-assert automatic-rename: window names are derived (label + icon via
		# automatic-rename-format) and allow-rename is off, so it must stay on.
		# tmux-state restore creates windows with `new-window -n`, which flips it
		# off and freezes the name on a stale label; this self-heals it. Gated on
		# the effective value (boolean options expand to 0/1 in formats).
		if [[ ${win_cur_rename[$idx]:-} != 1 ]]; then
			tmux_cmds+="set -qw -t '$target' automatic-rename on"$'\n'
		fi

		# Padded (for status bar — process icons + claude, fixed width)
		pad_to_width "${win_icons[$idx]}" "${win_icon_dw[$idx]}" "$TARGET_DW"
		if [[ $REPLY != "${win_cur_padded[$idx]:-}" ]]; then
			tmux_cmds+="set -qw -t '$target' @window_icon_padded '$REPLY'"$'\n'
		fi
	done

	# Batch the surviving set commands in one IPC call; a steady-state tick
	# (no spinner, no minute rollover) emits nothing, so skip the fork.
	if [[ -n $tmux_cmds ]]; then
		printf '%s' "$tmux_cmds" | tmux source -
	fi

	# A branch or task change means window labels (built by reflow from
	# @branch/@issue_*/@window_task) are stale — no tmux hook fires on cd or a new
	# prompt, so kick a forced reflow here. The call below is the reflow store path
	# (not a bare name) so a config reload repoints it without a tmux server restart.
	if ((branch_changed || labels_changed)); then
		@reflow@ "$SESSION" --force >/dev/null 2>&1 &
		disown
	fi
}

[[ ${BASH_SOURCE[0]} == "$0" ]] && main "$@"
