#!/usr/bin/env bash
# Session picker using fzf inside display-popup for full formatting control.
#
# Two modes:
#   (no args)    — open fzf picker, refresh on navigation
#   --generate   — output ANSI-colored session list for fzf (called by reload)
#
# Theme colors are cached as env vars so reloads skip tmux server round-trips.

set -euo pipefail

# shellcheck source=/dev/null  # Nix store paths substituted at build time
source @lib_icons@
# shellcheck source=/dev/null
source @lib_claude@

MAX_ICONS=@MAX_ICONS_PICKER@
FZF=@fzf@
CURL=@curl@

# --- Helpers ---

# _ansi_fg HEX — convert "#rrggbb" to ANSI 24-bit foreground escape
# Sets REPLY (avoids subshell forks in hot path).
_ansi_fg() {
	local hex="${1#\#}"
	printf -v REPLY '\033[38;2;%d;%d;%dm' "0x${hex:0:2}" "0x${hex:2:2}" "0x${hex:4:2}"
}

# --- Generate: output one line per session for fzf ---
# Line format: session_name<TAB>colorized_display
generate() {
	# Theme colors (env vars set by picker mode; fallback to tmux for standalone)
	local thm_mauve="${THM_MAUVE:-$(tmux show -gv @thm_mauve 2>/dev/null || echo '#cba6f7')}"
	local thm_blue="${THM_BLUE:-$(tmux show -gv @thm_blue 2>/dev/null || echo '#89b4fa')}"
	local thm_subtext_0="${THM_SUBTEXT_0:-$(tmux show -gv @thm_subtext_0 2>/dev/null || echo '#a6adc8')}"
	local icon_dir="${PICKER_ICON_DIR:-$(tmux show -gv @icon_dir 2>/dev/null || echo '')}"
	local icon_session="${PICKER_ICON_SESSION:-$(tmux show -gv @icon_session 2>/dev/null || echo '')}"

	local C_MAUVE C_BLUE C_DIM RESET DIM
	_ansi_fg "$thm_mauve" && C_MAUVE="$REPLY"
	_ansi_fg "$thm_blue" && C_BLUE="$REPLY"
	_ansi_fg "$thm_subtext_0" && C_DIM="$REPLY"
	RESET=$'\033[0m'
	DIM=$'\033[2m'

	# Claude state → ANSI color (uses same hex values as lib-claude.sh setup_claude_colors,
	# but as ANSI escapes instead of tmux #[fg=...] syntax for fzf compatibility)
	local theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
	local theme="dark"
	if [[ -f $theme_file ]]; then
		theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4) || true
	fi

	local CC_W CC_K CC_P CC_D CC_I CC_E
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
		local pf pane_session key val
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
	local max_name=0
	while IFS=$'\t' read -r sess _ sess_path; do
		[[ -n $sess ]] || continue
		sess_path_map[$sess]="$sess_path"
		((${#sess} > max_name)) && max_name=${#sess}

		build_proc_icons "${sess_procs[$sess]:-}" "$MAX_ICONS"
		local icons="$REPLY" idw=$REPLY_DW

		claude_priority_state \
			"${sess_cw[$sess]:-0}" "${sess_ck[$sess]:-0}" \
			"${sess_cp[$sess]:-0}" "${sess_cd[$sess]:-0}" \
			"${sess_ci[$sess]:-0}" "${sess_ce[$sess]:-0}"
		if [[ -n $REPLY ]]; then
			local cs="$REPLY" stale="${sess_cs[$sess]:-0}"
			claude_state_icon "$cs"
			local ci="$REPLY" cc
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
	local max_idw=0
	for sess in "${!sess_idw[@]}"; do
		((sess_idw[$sess] > max_idw)) && max_idw=${sess_idw[$sess]}
	done
	local icon_col=$((max_idw + 1))
	for sess in "${!sess_icons[@]}"; do
		pad_to_width "${sess_icons[$sess]}" "${sess_idw[$sess]}" "$icon_col"
		sess_icons[$sess]="$REPLY"
	done

	# Output: key<TAB>display
	local empty_icons
	printf -v empty_icons '%*s' "$icon_col" ''
	for sess in "${!sess_path_map[@]}"; do
		local short_path="${sess_path_map[$sess]/#$HOME/\~}"
		local padding
		printf -v padding '%*s' "$((max_name - ${#sess}))" ''
		local icons="${sess_icons[$sess]:-$empty_icons}"

		printf '%s\t%s %s%s  %s%s %s\n' \
			"$sess" \
			"${C_MAUVE}${icon_session}${RESET}" \
			"${C_MAUVE}${sess}${RESET}" \
			"$padding" \
			"$icons" \
			"${C_BLUE}${icon_dir}${RESET}" \
			"${C_DIM}${short_path}${RESET}"
	done
}

# --- Entry point ---
if [[ ${1:-} == "--generate" ]]; then
	generate
	exit 0
fi

# Cache theme colors as env vars for fast reloads
THM_MAUVE=$(tmux show -gv @thm_mauve 2>/dev/null || echo '#cba6f7')
THM_BLUE=$(tmux show -gv @thm_blue 2>/dev/null || echo '#89b4fa')
THM_SUBTEXT_0=$(tmux show -gv @thm_subtext_0 2>/dev/null || echo '#a6adc8')
PICKER_ICON_DIR=$(tmux show -gv @icon_dir 2>/dev/null || echo '')
PICKER_ICON_SESSION=$(tmux show -gv @icon_session 2>/dev/null || echo '')
export THM_MAUVE THM_BLUE THM_SUBTEXT_0 PICKER_ICON_DIR PICKER_ICON_SESSION

SELF="$0"
FZF_TMUX="${FZF%fzf}fzf-tmux"
PORT=$((RANDOM % 10000 + 40000))

# Background loop: reload fzf every 2s via its HTTP API
(
	sleep 2 # give fzf time to bind
	while sleep 2; do
		"$CURL" -s -XPOST "localhost:$PORT" \
			-d "reload($SELF --generate)" 2>/dev/null || break
	done
) &
REFRESH_PID=$!
trap 'kill $REFRESH_PID 2>/dev/null; wait $REFRESH_PID 2>/dev/null || true' EXIT

selected=$(
	generate | "$FZF_TMUX" -p 70%,50% -- \
		--listen "$PORT" \
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
		--bind "focus:reload($SELF --generate)" \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || exit 0

# Extract session name (first field before tab)
session_name="${selected%%	*}"
[[ -n $session_name ]] && tmux switch-client -t "$session_name"
