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

# Derived at build time from the shipped manifests' match_commands (agentCommands
# in config/tmux.conf.nix), so a manifest can't ship without being swept.
# ${AGENT_COMMANDS:-...} lets tests inject a list, same as AGENT_DETECT_BIN below.
AGENT_COMMANDS="${AGENT_COMMANDS:-@AGENT_COMMANDS@}"
# ${AGENT_DETECT_BIN:-...} lets tests inject a real path via env; Nix build
# substitution still wins in the shipped script (no env var set at runtime).
AGENT_DETECT_BIN="${AGENT_DETECT_BIN:-@agent_detect_bin@}"
# Empty when enrich is disabled (Nix build-time substitution, same as
# tmux-reconcile-window's issue-stamp wiring); ${ISSUE_STAMP_BIN:-...} likewise
# lets tests inject a real path via env.
ISSUE_STAMP_BIN="${ISSUE_STAMP_BIN:-@issue_stamp@}"

# normalize_wrapped_cmd CMD
# Strips nix makeWrapper's `.foo-wrapped` shape down to `foo` (what
# pane_current_command reports for every agent CLI on a nix host); passes
# unwrapped names through unchanged. Sets REPLY.
normalize_wrapped_cmd() {
	REPLY="$1"
	[[ $REPLY == .*-wrapped ]] && REPLY="${REPLY#.}" && REPLY="${REPLY%-wrapped}"
}

# Arms `agent-detect` on agent panes that don't already have a live pipe,
# stamps each agent pane's presence for lib-claude's dead-agent floor, and reaps
# claude-status state for panes list-panes -a no longer reports (issue #341) —
# a third job riding the same list-panes roundtrip. Using #{pane_pipe} as the
# gate means a dead parser (pipe closes -> pane_pipe 0) self-heals on a later
# tick. The three jobs gate independently — reaping runs whenever the
# roundtrip happens at all, since (unlike arm/stamp) it must not depend on
# agent-detect or the dead-agent floor being enabled.
arm_agent_detect() {
	local arm=1 stamp=0
	[[ $AGENT_DETECT_BIN == @* ]] && arm=0
	[[ -n ${CLAUDE_LIVE_DIR:-} && ${CLAUDE_ASSUME_DEAD_AFTER:-0} =~ ^[0-9]+$ ]] &&
		((CLAUDE_ASSUME_DEAD_AFTER > 0)) && stamp=1
	# The sweep is a full-server list-panes — a second tmux roundtrip per tick,
	# multiplied by attached sessions. Arming (new pane, dead pipe) only needs
	# seconds-level latency, so run every 5th tick (CLAUDE_NOW = this tick's
	# epoch second).
	((CLAUDE_NOW % 5)) && return 0

	# Bail on a failed list-panes rather than reading an empty stream: an empty
	# result is indistinguishable from "no agent panes", and stamping .sweep
	# after one would assert a pass that never observed anything — the reader
	# would then read every live pane's lagging stamp as a dead agent.
	local rows
	rows=$(tmux list-panes -a -F '#{pane_id}|#{pane_current_command}|#{pane_pipe}' 2>/dev/null) || return 0
	claude_reap_dead_panes "$rows"

	((arm || stamp)) || return 0

	if ((stamp)) && [[ ! -d $CLAUDE_LIVE_DIR ]]; then
		mkdir -p "$CLAUDE_LIVE_DIR"
	fi

	local pid cmd piped
	while IFS='|' read -r pid cmd piped; do
		# A here-string of an empty result still yields one blank line.
		[[ -n $pid ]] || continue
		normalize_wrapped_cmd "$cmd"
		case " $AGENT_COMMANDS " in *" $REPLY "*) ;; *) continue ;; esac
		((stamp)) && printf '%s\n' "$CLAUDE_NOW" >"$CLAUDE_LIVE_DIR/${pid#%}"
		[[ $piped == 0 ]] || continue
		((arm)) && tmux pipe-pane -o -t "$pid" "$AGENT_DETECT_BIN ${pid#%}"
	done <<<"$rows"

	# Strictly after the last per-pane stamp, and written nowhere else: the
	# reader takes a fresh .sweep as proof that every agent pane of that pass was
	# stamped, so a lagging per-pane stamp means "no agent here" with no grace
	# window to wait out after a resume.
	((stamp)) && printf '%s\n' "$CLAUDE_NOW" >"$CLAUDE_LIVE_DIR/.sweep"
	return 0
}

main() {
	SESSION=${1:-$(tmux display-message -p '#{session_name}')}
	# $2 is #{@resume_claude}, expanded by the status format — avoids a show-option
	# fork per tick. "on" enables stamping each Claude pane's @remux_relaunch override
	# so tmux-remux resumes the session (not a bare shell) on restore.
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
	# list-panes -a, not -s: this script is invoked from status-format[0], which
	# tmux only evaluates for a client drawing a status line. Sessions with no
	# attached client never get their own tick, so one attached pass has to stamp
	# every window (#580). Window arrays are keyed session:index so indices that
	# collide across sessions don't merge.
	declare -A pane_to_win win_procs win_pane_path win_cur_branch win_active_pane win_cur_task win_cur_name pane_cur_relaunch
	declare -A win_cur_display win_cur_padded win_cur_ago win_cur_rename win_cur_crew win_cur_crew_seen win_cur_bridge
	declare -A all_sess sess_cur_active_icon sess_cur_session_fg sess_active_proc sess_active_win
	# '|' delimiter, not tab: tab is IFS-whitespace, so an empty middle field (a
	# window with no @branch yet) collapses and shifts every later field left,
	# corrupting cur_branch/active flags. '@window_task' is free-form so it stays
	# last — read drops any stray '|' it contains into that final field.
	# @window_ai_name is sanitized free of '|' (claude-status-update), so it is safe
	# as a fixed middle field. @remux_relaunch is "claude --resume <uuid>" — no '|'
	# either, so it also stays a fixed middle field before the free-form task.
	# The icon/ago/rename/session fields are our own writes read back for
	# change-gating: glyphs, #[fg=…] codes, spaces, and hex colors — never '|'.
	# @crew_name (harness-stamped codename) and @crew_seen (our shadow of it) are
	# kebab tokens, so they sit safely before the free-form task; @bridge_win is
	# "1" or empty and @bridge_proc is a command name, so both do too.
	while IFS='|' read -r pane_id sess idx pane_path proc cur_branch pane_active window_active cur_ai_name cur_relaunch cur_display cur_padded cur_ago cur_rename opt_active_icon opt_session_fg cur_crew cur_crew_seen cur_bridge bridge_proc cur_task; do
		[[ -n $pane_id ]] || continue
		# A mirror pane runs the bridge renderer; @bridge_proc carries what the
		# remote pane is actually running, which is what the icons should show.
		[[ -n $bridge_proc ]] && proc="$bridge_proc"
		wkey="$sess:$idx"
		pane_to_win["${pane_id#%}"]="$wkey"
		pane_cur_relaunch["${pane_id#%}"]="$cur_relaunch"
		all_sess[$sess]=1
		# Session options (same on every row of a session) must be copied here:
		# the EOF read that ends the loop blanks the read variables themselves.
		sess_cur_active_icon[$sess]="$opt_active_icon"
		sess_cur_session_fg[$sess]="$opt_session_fg"
		# First pane per window wins for path/branch/task — panes in a window share a
		# cwd, and @window_task/@branch are window options (same for every pane).
		if [[ -z ${win_pane_path[$wkey]+x} ]]; then
			win_pane_path[$wkey]="$pane_path"
			win_cur_branch[$wkey]="$cur_branch"
			win_cur_task[$wkey]="$cur_task"
			win_cur_name[$wkey]="$cur_ai_name"
			win_cur_display[$wkey]="$cur_display"
			win_cur_padded[$wkey]="$cur_padded"
			win_cur_ago[$wkey]="$cur_ago"
			win_cur_rename[$wkey]="$cur_rename"
			win_cur_crew[$wkey]="$cur_crew"
			win_cur_crew_seen[$wkey]="$cur_crew_seen"
			win_cur_bridge[$wkey]="$cur_bridge"
		fi
		# The task file is keyed by the pane Claude runs in, so resolve the genuinely
		# active pane (list-panes orders by index, not active-first).
		[[ $pane_active == 1 ]] && win_active_pane[$wkey]="${pane_id#%}"
		[[ $window_active == 1 ]] && sess_active_win[$sess]="$idx"
		# Track each session's active pane command (active pane in that session's
		# active window) — @active_pane_icon is session-scoped.
		[[ $pane_active == 1 && $window_active == 1 ]] && sess_active_proc[$sess]="$proc"
		# Collect unique processes per window
		[[ -z $proc ]] && continue
		existing="${win_procs[$wkey]:-}"
		case " $existing " in
		*" $proc "*) ;;
		*) win_procs[$wkey]="${existing:+$existing }$proc" ;;
		esac
	done < <(tmux list-panes -a -F '#{pane_id}|#{session_name}|#{window_index}|#{pane_current_path}|#{pane_current_command}|#{@branch}|#{pane_active}|#{window_active}|#{@window_ai_name}|#{@remux_relaunch}|#{@window_icon_display}|#{@window_icon_padded}|#{@window_claude_ago}|#{automatic-rename}|#{@active_pane_icon}|#{@claude_session_fg}|#{@crew_name}|#{@crew_seen}|#{@bridge_win}|#{@bridge_proc}|#{@window_task}')

	arm_agent_detect

	# --- Claude status: read pane files, bucket by session:index ---
	declare -A win_claude_state win_claude_fade win_claude_unseen win_claude_ts
	# Per-window per-state counts + that state's freshest pane fade/unseen
	# (keys: "<wkey>,<state>"); the winning state is resolved after the loop.
	declare -A win_cnt win_state_fade win_state_unseen win_has_claude
	# Per-session tally drives that session's status-bar tint (@claude_session_fg)
	declare -A sess_w sess_k sess_p sess_d sess_i sess_e sess_dn sess_int sess_min_fade sess_unseen
	while IFS= read -r pane_file; do
		[[ -n $pane_file ]] || continue
		win_idx="${pane_to_win[$pane_file]:-}"
		[[ -n $win_idx ]] || continue
		s="${win_idx%:*}"
		read_pane_state "$CLAUDE_PANES_DIR/$pane_file" || continue
		state="$REPLY"
		fade=$REPLY_FADE
		unseen=$REPLY_UNSEEN

		# Stamp the pane's resume override so tmux-remux relaunches the actual
		# Claude session (not a bare shell) on restore. The transcript basename
		# is the session UUID. Set only on change — @remux_relaunch is read back via
		# the batched list-panes above, so a stable pane forks nothing per tick.
		if [[ $RESUME_CLAUDE == on ]]; then
			uuid="${REPLY_TRANSCRIPT##*/}"
			uuid="${uuid%.jsonl}"
			desired=""
			[[ -n $uuid ]] && desired="claude --resume $uuid"
			cur="${pane_cur_relaunch[$pane_file]:-}"
			# An empty desired means no real transcript (a screen-only agent-detect
			# ghost entry) — refuse to clobber a Codex/Cursor hook's own stamp with
			# nothing. A non-empty desired is positive evidence of a live Claude
			# session, so it may overwrite a foreign stamp (a pane that moved from
			# Codex/Cursor to Claude).
			# No -q, unlike the batched sets below: it made a lost write return 0
			# and print nothing, so the only trace was a downstream
			# "invalid option" (#373).
			if [[ -n $desired ]] && [[ $desired != "$cur" ]]; then
				tmux set -p -t "%$pane_file" @remux_relaunch "$desired"
			fi
		fi
		# Freshest pane timestamp per window drives the "last active" label
		[[ -n $REPLY_TS ]] && ((REPLY_TS > ${win_claude_ts[$win_idx]:-0})) &&
			win_claude_ts[$win_idx]=$REPLY_TS
		# Session aggregate: count states, freshest pane wins the fade
		case "$state" in
		error) ((sess_e[$s]++)) ;;
		waiting) ((sess_w[$s]++)) ;;
		compacting) ((sess_k[$s]++)) ;;
		interrupted) ((sess_int[$s]++)) ;;
		processing) ((sess_p[$s]++)) ;;
		done) ((sess_d[$s]++)) ;;
		idle) ((sess_i[$s]++)) ;;
		denied) ((sess_dn[$s]++)) ;;
		esac
		((fade < ${sess_min_fade[$s]:-100})) && sess_min_fade[$s]=$fade
		[[ $unseen == 1 ]] && sess_unseen[$s]=1
		# Per-window: tally the state and track the freshest fade / any-unseen for
		# it. The winning state (and its pane's fade) is picked after the loop.
		key="$win_idx,$state"
		win_cnt[$key]=$((${win_cnt[$key]:-0} + 1))
		if [[ -z ${win_state_fade[$key]:-} ]] || ((fade < win_state_fade[$key])); then
			win_state_fade[$key]=$fade
		fi
		[[ $unseen == 1 ]] && win_state_unseen[$key]=1
		win_has_claude[$win_idx]=1
	done < <(claude_pane_ids)

	# Resolve each window's icon state from its counts via the shared priority
	# order, then adopt the winning state's freshest pane fade/unseen. Using
	# claude_priority_state (not a hand-rolled merge) keeps the ordering — and
	# every state, incl. denied — identical to the session tint below. Counts are
	# gathered in claude_priority_state's positional order; keys route through the
	# scalar $key because a literal-comma subscript is unsafe (shfmt -s mangles it).
	prio_order=(waiting compacting processing "done" idle error denied interrupted)
	for win_idx in "${!win_has_claude[@]}"; do
		counts=()
		for st in "${prio_order[@]}"; do
			key="$win_idx,$st"
			counts+=("${win_cnt[$key]:-0}")
		done
		claude_priority_state "${counts[@]}"
		key="$win_idx,$REPLY"
		win_claude_state[$win_idx]="$REPLY"
		win_claude_fade[$win_idx]="${win_state_fade[$key]:-0}"
		win_claude_unseen[$win_idx]="${win_state_unseen[$key]:-0}"
	done

	# Session-name color: tint with that session's aggregate claude state, faded
	# by its freshest pane's age. Empty when no claude panes — the format falls
	# back to the theme's session color.
	declare -A sess_fg
	for s in "${!all_sess[@]}"; do
		claude_priority_state "${sess_w[$s]:-0}" "${sess_k[$s]:-0}" "${sess_p[$s]:-0}" "${sess_d[$s]:-0}" "${sess_i[$s]:-0}" "${sess_e[$s]:-0}" "${sess_dn[$s]:-0}" "${sess_int[$s]:-0}"
		claude_faded_hex "$REPLY" "${sess_min_fade[$s]:-100}" "${sess_unseen[$s]:-0}"
		sess_fg[$s]=$REPLY
	done

	# --- Compute process icons + claude per window, measure display widths ---
	declare -a all_idx=()
	declare -A win_icons win_icon_dw win_display
	declare -A sess_need_reflow

	# Collect all tmux set commands to batch via `tmux source -`
	tmux_cmds=""

	for wkey in "${!win_pane_path[@]}"; do
		all_idx+=("$wkey")
		s="${wkey%:*}"
		idx="${wkey##*:}"
		pane_path="${win_pane_path[$wkey]}"
		target="$wkey"

		# Task label tracks the active pane's self-reported "what Claude is doing"
		# phrase (UserPromptSubmit hook). It can change in any window, so poll every
		# window each tick — a single small file read. Set directly (not batched via
		# `source -`): the phrase is free-form and would break the command parser.
		task=""
		[[ -f "$CLAUDE_TASKS_DIR/${win_active_pane[$wkey]}" ]] &&
			IFS= read -r task <"$CLAUDE_TASKS_DIR/${win_active_pane[$wkey]}"
		if [[ $task != "${win_cur_task[$wkey]:-}" ]]; then
			tmux set -qw -t "$target" @window_task "$task"
			sess_need_reflow[$s]=1
		fi

		# AI name: the active pane's Claude-set window title (claude-status-update
		# name set, prompted by the UserPromptSubmit nudge on fallback windows).
		# build_window_label prefers it over the raw task. Mirror like the task —
		# free-form, set directly, only on change so reflow isn't kicked every tick.
		ai_name=""
		[[ -f "$CLAUDE_NAMES_DIR/${win_active_pane[$wkey]}" ]] &&
			IFS= read -r ai_name <"$CLAUDE_NAMES_DIR/${win_active_pane[$wkey]}"
		if [[ $ai_name != "${win_cur_name[$wkey]:-}" ]]; then
			tmux set -qw -t "$target" @window_ai_name "$ai_name"
			sess_need_reflow[$s]=1
		fi

		# Crew badge: the fan-out harness stamps @crew_name directly, and no tmux
		# hook fires on a user-option set — so poll for a change here and kick the
		# forced reflow, like task/branch. The multi-line grid's badge column is
		# reflow-computed (@window_crew_disp + crew_colw), so a name change must
		# recompute; @crew_color is read live by the format and needs no reflow.
		# @crew_seen is our own shadow of the last name we acted on.
		if [[ ${win_cur_crew[$wkey]:-} != "${win_cur_crew_seen[$wkey]:-}" ]]; then
			tmux set -qw -t "$target" @crew_seen "${win_cur_crew[$wkey]:-}"
			sess_need_reflow[$s]=1
		fi

		# Branch detection forks git per window. A branch only changes in the window
		# where a checkout/cd happens, so poll only the invoking session's active
		# window each tick; other sessions' active windows and every inactive window
		# trust their cached @branch (worktrunk stamps it on switch).
		# A window with no @branch yet (manual new-window, restore, never-attached
		# session) is polled once to seed it, then trusted — this caps the steady
		# git fork rate at ~1/tick plus unseeded windows.
		if [[ ($s == "$SESSION" && $idx == "${sess_active_win[$SESSION]:-}") || -z ${win_cur_branch[$wkey]:-} ]]; then
			# timeout so a stuck git (NFS stall, held index.lock) can't wedge the
			# whole icon updater — it degrades to the cached branch for that tick.
			branch=$(timeout 2 git -C "$pane_path" branch --show-current 2>/dev/null) || branch=""
			if [[ $branch != "${win_cur_branch[$wkey]:-}" ]]; then
				# Direct argv (not the tmux_cmds/`tmux source -` batch below): a git
				# branch name can legally contain a single quote, which a batched
				# single-quoted token has no escape for — `tmux source -` would
				# reparse it and let an adversarial branch name inject arbitrary
				# tmux commands. Only forks on a genuine transition (rare), so this
				# doesn't touch the steady-state hot path.
				tmux set-option -t "$target" -w @branch "$branch"
				# Re-derive git root when branch changes (different repo or worktree)
				git_root=$(timeout 2 git -C "$pane_path" rev-parse --show-toplevel 2>/dev/null) || git_root=""
				tmux set-option -t "$target" -w @git_root "$git_root"
				sess_need_reflow[$s]=1
				# Auto re-stamp (#137): a genuine transition (previous branch non-empty,
				# so this isn't the initial seed already covered by post-switch/
				# reconcile-window) means a `git checkout -b` happened in-place — the
				# new branch's issue/PR never got a chance to stamp. Re-fire so
				# @issue_* catches up; serialized through tmux-issue-stamp's own
				# per-window lock, so this never races post-switch or `enrich`.
				if [[ -n $ISSUE_STAMP_BIN && $ISSUE_STAMP_BIN != @* && -n ${win_cur_branch[$wkey]:-} && -n $branch ]]; then
					"$ISSUE_STAMP_BIN" "$target" "$git_root" "$branch" >/dev/null 2>&1 &
					disown
				fi
			fi
		fi

		# Build process icons from batched data
		build_proc_icons "${win_procs[$wkey]:-}" "$MAX_ICONS"
		proc_icon_str="${REPLY% }"
		icon="$REPLY"
		# shellcheck disable=SC2153 # REPLY_DW set by build_proc_icons (sourced lib)
		icon_dw=$REPLY_DW

		# Append colored claude status icon (shares the icon column)
		c_state="${win_claude_state[$wkey]:-}"
		display="${proc_icon_str}"
		claude_colored_icon "$c_state" "${win_claude_fade[$wkey]:-0}" "${win_claude_unseen[$wkey]:-0}"
		if [[ -n $REPLY ]]; then
			icon+="$REPLY"
			((icon_dw += 2)) # 1-cell nerd font icon + 1 space
			# Add to display with space separator if process icons exist
			[[ -n $display ]] && display+=" "
			display+="${REPLY% }" # strip trailing space for display
		fi

		win_icons[$wkey]="$icon"
		win_icon_dw[$wkey]=$icon_dw
		win_display[$wkey]="$display"

		# "Last active" time: shown only for halted states (the live icon already
		# conveys active ones). A bare unit like "5m" is parser-safe, so batch it.
		# Gated on change (read back for free via the batched list-panes) — it only
		# ticks over once a minute.
		ago=""
		case "$c_state" in
		idle | done | interrupted | error)
			ts="${win_claude_ts[$wkey]:-0}"
			if ((ts > 0 && CLAUDE_NOW > ts)); then
				claude_ago "$((CLAUDE_NOW - ts))"
				ago="$REPLY"
			fi
			;;
		esac
		if [[ $ago != "${win_cur_ago[$wkey]:-}" ]]; then
			tmux_cmds+="set -qw -t '$target' @window_claude_ago '$ago'"$'\n'
		fi
	done

	# Set per-session active pane icon and claude tint (from batched data)
	for s in "${!all_sess[@]}"; do
		active_icon=""
		proc="${sess_active_proc[$s]:-}"
		normalize_wrapped_cmd "$proc"
		proc="$REPLY"
		[[ -n $proc ]] && active_icon="${ICON_MAP[$proc]:-}"
		if [[ $active_icon != "${sess_cur_active_icon[$s]:-}" ]]; then
			tmux_cmds+="set -q -t '$s' @active_pane_icon '$active_icon'"$'\n'
		fi
		if [[ ${sess_fg[$s]:-} != "${sess_cur_session_fg[$s]:-}" ]]; then
			tmux_cmds+="set -q -t '$s' @claude_session_fg '${sess_fg[$s]}'"$'\n'
		fi
	done

	# --- Second pass: set unpadded + padded icon variables ---
	# Fixed column: worst case MAX_ICONS emoji (3 cells each) + 1 nerd font claude (2 cells)
	TARGET_DW=$((MAX_ICONS * 3 + 2))
	for wkey in "${all_idx[@]}"; do
		target="$wkey"

		# Unpadded (for window names — process icons + colored claude)
		if [[ ${win_display[$wkey]} != "${win_cur_display[$wkey]:-}" ]]; then
			tmux_cmds+="set -qw -t '$target' @window_icon_display '${win_display[$wkey]}'"$'\n'
		fi

		# Re-assert automatic-rename: window names are derived (label + icon via
		# automatic-rename-format) and allow-rename is off, so it must stay on.
		# tmux-remux restore creates windows with `new-window -n`, which flips it
		# off and freezes the name on a stale label; this self-heals it. Gated on
		# the effective value (boolean options expand to 0/1 in formats).
		#
		# Except on a mirror window (#167 @bridge_win opt-out), where the daemon
		# owns the name: turning automatic-rename back on undoes its
		# rename-window, and tmux only re-derives a name when the active pane
		# produces output — an idle renderer never does, so the name freezes on
		# whatever the format yielded at that instant (the launcher's cwd).
		if [[ ${win_cur_bridge[$wkey]:-} == 1 ]]; then
			if [[ ${win_cur_rename[$wkey]:-} == 1 ]]; then
				tmux_cmds+="set -qw -t '$target' automatic-rename off"$'\n'
			fi
		elif [[ ${win_cur_rename[$wkey]:-} != 1 ]]; then
			tmux_cmds+="set -qw -t '$target' automatic-rename on"$'\n'
		fi

		# Padded (for status bar — process icons + claude, fixed width)
		pad_to_width "${win_icons[$wkey]}" "${win_icon_dw[$wkey]}" "$TARGET_DW"
		if [[ $REPLY != "${win_cur_padded[$wkey]:-}" ]]; then
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
	# prompt, so kick a forced reflow here. Per session whose labels actually
	# changed, not only the invoking session: an unattached session's first seed
	# of @branch would otherwise leave its grid stale until someone attaches.
	# The call below is the reflow store path (not a bare name) so a config
	# reload repoints it without a tmux server restart.
	for s in "${!sess_need_reflow[@]}"; do
		@reflow@ "$s" --force >/dev/null 2>&1 &
		disown
	done
}

[[ ${BASH_SOURCE[0]} == "$0" ]] && main "$@"
