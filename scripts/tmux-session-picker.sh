#!/usr/bin/env bash
# Session picker using fzf-tmux popup for full formatting control.
#
# Two modes:
#   (no args)    — open fzf popup, populate via start:reload
#   --generate   — output ANSI-colored session list for fzf

set -euo pipefail

FZF=@fzf@

# --- Generate mode: output one line per session for fzf ---
# Handles its own library sourcing and tmux queries.
# Line format: session_name<TAB>colorized_display
if [[ ${1:-} == "--generate" ]]; then
	# shellcheck source=/dev/null  # Nix store paths substituted at build time
	source @lib_icons@
	# shellcheck source=/dev/null
	source @lib_claude@
	MAX_ICONS=@MAX_ICONS_PICKER@

	# _ansi_fg HEX — sets REPLY to ANSI 24-bit foreground escape
	_ansi_fg() {
		local hex="${1#\#}"
		printf -v REPLY '\033[38;2;%d;%d;%dm' "0x${hex:0:2}" "0x${hex:2:2}" "0x${hex:4:2}"
	}

	# Theme colors (cached via env vars from picker mode when available)
	thm_mauve="${THM_MAUVE:-$(tmux show -gv @thm_mauve 2>/dev/null || echo '#cba6f7')}"
	thm_blue="${THM_BLUE:-$(tmux show -gv @thm_blue 2>/dev/null || echo '#89b4fa')}"
	thm_subtext_0="${THM_SUBTEXT_0:-$(tmux show -gv @thm_subtext_0 2>/dev/null || echo '#a6adc8')}"
	icon_dir="${PICKER_ICON_DIR:-$(tmux show -gv @icon_dir 2>/dev/null || echo '')}"
	icon_session="${PICKER_ICON_SESSION:-$(tmux show -gv @icon_session 2>/dev/null || echo '')}"

	_ansi_fg "$thm_mauve" && C_MAUVE="$REPLY"
	_ansi_fg "$thm_blue" && C_BLUE="$REPLY"
	_ansi_fg "$thm_subtext_0" && C_DIM="$REPLY"
	RESET=$'\033[0m'
	DIM=$'\033[2m'

	# Claude state → ANSI color (same hex values as lib-claude.sh setup_claude_colors,
	# but as ANSI escapes instead of tmux #[fg=...] syntax for fzf compatibility)
	theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
	theme="dark"
	if [[ -f $theme_file ]]; then
		theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4) || true
	fi

	if [[ $theme == "light" ]]; then
		_ansi_fg "#fe640b" && CC_W="$REPLY"
		_ansi_fg "#04a5e5" && CC_K="$REPLY"
		_ansi_fg "#179299" && CC_P="$REPLY"
		_ansi_fg "#40a02b" && CC_D="$REPLY"
		_ansi_fg "#6c6f85" && CC_I="$REPLY"
		_ansi_fg "#d20f39" && CC_E="$REPLY"
	else
		_ansi_fg "#fab387" && CC_W="$REPLY"
		_ansi_fg "#89dceb" && CC_K="$REPLY"
		_ansi_fg "#94e2d5" && CC_P="$REPLY"
		_ansi_fg "#a6e3a1" && CC_D="$REPLY"
		_ansi_fg "#6c7086" && CC_I="$REPLY"
		_ansi_fg "#f38ba8" && CC_E="$REPLY"
	fi

	# --- Claude status: bucket pane states by session ---
	declare -A sess_cw sess_ck sess_cp sess_cd sess_ci sess_ce sess_cs

	if [[ -d $CLAUDE_PANES_DIR ]]; then
		for pf in "$CLAUDE_PANES_DIR"/*; do
			[[ -f $pf ]] || continue
			pane_session=""
			while IFS='=' read -r key val; do
				case "$key" in session) pane_session="$val" ;; esac
			done <"$pf"
			[[ -n $pane_session ]] || continue
			read_pane_state "$pf" || continue
			case "$REPLY" in
			waiting) sess_cw[$pane_session]=$((${sess_cw[$pane_session]:-0} + 1)) ;;
			compacting) sess_ck[$pane_session]=$((${sess_ck[$pane_session]:-0} + 1)) ;;
			processing) sess_cp[$pane_session]=$((${sess_cp[$pane_session]:-0} + 1)) ;;
			done) sess_cd[$pane_session]=$((${sess_cd[$pane_session]:-0} + 1)) ;;
			idle) sess_ci[$pane_session]=$((${sess_ci[$pane_session]:-0} + 1)) ;;
			error) sess_ce[$pane_session]=$((${sess_ce[$pane_session]:-0} + 1)) ;;
			esac
			[[ $REPLY_STALE == 0 ]] && sess_cs[$pane_session]=0
			[[ -n ${sess_cs[$pane_session]+x} ]] || sess_cs[$pane_session]=1
		done
	fi

	# --- Process icons per session ---
	declare -A sess_seen sess_procs
	while IFS=$'\t' read -r sess proc; do
		[[ -n $sess && -n $proc ]] || continue
		if [[ -z ${sess_seen["${sess}:${proc}"]+x} ]]; then
			sess_seen["${sess}:${proc}"]=1
			sess_procs[$sess]+="${sess_procs[$sess]:+ }$proc"
		fi
	done < <(tmux list-panes -a -F '#{session_name}	#{pane_current_command}')
	unset sess_seen

	# Single tmux call: collect session data + build icons in one pass
	declare -A sess_icons sess_idw sess_path_map
	max_name=0
	while IFS=$'\t' read -r sess _ sess_path; do
		[[ -n $sess ]] || continue
		sess_path_map[$sess]="$sess_path"
		((${#sess} > max_name)) && max_name=${#sess}

		build_proc_icons "${sess_procs[$sess]:-}" "$MAX_ICONS"
		icons="$REPLY"
		idw=$REPLY_DW

		claude_priority_state \
			"${sess_cw[$sess]:-0}" "${sess_ck[$sess]:-0}" \
			"${sess_cp[$sess]:-0}" "${sess_cd[$sess]:-0}" \
			"${sess_ci[$sess]:-0}" "${sess_ce[$sess]:-0}"
		if [[ -n $REPLY ]]; then
			cs="$REPLY" stale="${sess_cs[$sess]:-0}"
			claude_state_icon "$cs"
			ci="$REPLY"
			if ((stale)); then
				cc="$DIM"
			else
				case "$cs" in
				waiting) cc=$CC_W ;; compacting) cc=$CC_K ;; processing) cc=$CC_P ;;
				done) cc=$CC_D ;; idle) cc=$CC_I ;; error) cc=$CC_E ;; *) cc="" ;;
				esac
			fi
			icons+="${cc}${ci}${RESET} "
			((idw += 2))
		fi

		sess_icons[$sess]="$icons"
		sess_idw[$sess]=$idw
	done < <(tmux list-sessions -F '#{session_name}	#{session_id}	#{session_path}')
	unset sess_procs

	# Pad icon column to uniform width
	max_idw=0
	for sess in "${!sess_idw[@]}"; do
		((sess_idw[$sess] > max_idw)) && max_idw=${sess_idw[$sess]}
	done
	icon_col=$((max_idw + 1))
	for sess in "${!sess_icons[@]}"; do
		pad_to_width "${sess_icons[$sess]}" "${sess_idw[$sess]}" "$icon_col"
		sess_icons[$sess]="$REPLY"
	done

	# Output: key<TAB>display
	printf -v empty_icons '%*s' "$icon_col" ''
	for sess in "${!sess_path_map[@]}"; do
		short_path="${sess_path_map[$sess]/#$HOME/\~}"
		printf -v padding '%*s' "$((max_name - ${#sess}))" ''
		icons="${sess_icons[$sess]:-$empty_icons}"

		printf '%s\t%s %s%s  %s%s %s\n' \
			"$sess" \
			"${C_MAUVE}${icon_session}${RESET}" \
			"${C_MAUVE}${sess}${RESET}" \
			"$padding" \
			"$icons" \
			"${C_BLUE}${icon_dir}${RESET}" \
			"${C_DIM}${short_path}${RESET}"
	done
	exit 0
fi

# --- Picker mode: minimal startup, fzf-tmux handles the popup ---
# Libraries and tmux queries happen in --generate subprocess, not here.

SELF="$0"
FZF_TMUX="${FZF%fzf}fzf-tmux"

selected=$(
	"$SELF" --generate | "$FZF_TMUX" -p 70%,50% -- \
		--ansi \
		--no-sort \
		--delimiter '\t' \
		--with-nth 2 \
		--nth 1 \
		--layout reverse \
		--border rounded \
		--border-label ' Sessions ' \
		--pointer '▸' \
		--prompt '  ' \
		--no-info \
		--margin 0 \
		--padding 0,1 \
		--bind "ctrl-r:reload($SELF --generate)" \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || true

# Extract session name (first field before tab)
session_name="${selected%%	*}"
[[ -n $session_name ]] && tmux switch-client -t "$session_name"
exit 0
