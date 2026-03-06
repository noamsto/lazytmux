#!/usr/bin/env bash
# Window picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.

set -euo pipefail

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")

# Icon map (Nix-generated)
# shellcheck disable=SC2190  # icon map entries are Nix-generated placeholders
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK="@FALLBACK_ICON@"
MAX_ICONS=@MAX_ICONS@
MAX_ICONS_PICKER=@MAX_ICONS_PICKER@

# --- Claude status: read all pane files once, bucket by window and session ---
PANES_DIR="/tmp/claude-status/panes"
SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
printf -v _now '%(%s)T' -1

# Build pane_id → window target mapping from a single tmux call
declare -A pane_to_window  # pane_id (no %) → "session:window_index"
declare -A pane_to_session # pane_id (no %) → session_name
while IFS=$'\t' read -r pane_id sess win_idx; do
	pane_to_window["${pane_id#%}"]="${sess}:${win_idx}"
	pane_to_session["${pane_id#%}"]="$sess"
done < <(tmux list-panes -a -F '#{pane_id}	#{session_name}	#{window_index}')

# Per-window and per-session claude state tallies
declare -A win_waiting win_compacting win_processing win_done win_idle
declare -A sess_waiting sess_compacting sess_processing sess_done sess_idle

if [[ -d $PANES_DIR ]]; then
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		pane_file="${pf##*/}"
		state="" timestamp=""
		while IFS='=' read -r key val; do
			case "$key" in
			state) state="$val" ;;
			timestamp) timestamp="$val" ;;
			esac
		done <"$pf"
		[[ -n $state ]] || continue
		target="${pane_to_window[$pane_file]:-}"
		sess="${pane_to_session[$pane_file]:-}"
		[[ -n $target ]] || continue
		# Staleness checks
		if [[ -n $timestamp ]]; then
			age=$((_now - timestamp))
			[[ $state == "waiting" && $age -gt 30 ]] && state="processing"
			[[ $state == "processing" && $age -gt 15 ]] && state="done"
		fi
		# Tally per window
		case "$state" in
		waiting) win_waiting[$target]=$((${win_waiting[$target]:-0} + 1)) ;;
		compacting) win_compacting[$target]=$((${win_compacting[$target]:-0} + 1)) ;;
		processing) win_processing[$target]=$((${win_processing[$target]:-0} + 1)) ;;
		done) win_done[$target]=$((${win_done[$target]:-0} + 1)) ;;
		idle) win_idle[$target]=$((${win_idle[$target]:-0} + 1)) ;;
		esac
		# Tally per session
		case "$state" in
		waiting) sess_waiting[$sess]=$((${sess_waiting[$sess]:-0} + 1)) ;;
		compacting) sess_compacting[$sess]=$((${sess_compacting[$sess]:-0} + 1)) ;;
		processing) sess_processing[$sess]=$((${sess_processing[$sess]:-0} + 1)) ;;
		done) sess_done[$sess]=$((${sess_done[$sess]:-0} + 1)) ;;
		idle) sess_idle[$sess]=$((${sess_idle[$sess]:-0} + 1)) ;;
		esac
	done
fi

# Detect theme for colors
theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
theme="dark"
if [[ -f $theme_file ]]; then
	theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4) || true
fi
if [[ $theme == "light" ]]; then
	C_W="#[fg=#fe640b]" C_K="#[fg=#04a5e5]" C_P="#[fg=#179299]" C_D="#[fg=#40a02b]" C_I="#[fg=#6c6f85]"
else
	C_W="#[fg=#fab387]" C_K="#[fg=#89dceb]" C_P="#[fg=#94e2d5]" C_D="#[fg=#a6e3a1]" C_I="#[fg=#6c7086]"
fi
C_R="#[fg=default]"

format_claude() {
	local w=${1:-0} k=${2:-0} p=${3:-0} d=${4:-0} i=${5:-0}
	((w + k + p + d + i == 0)) && return
	local icon color
	if ((w > 0)); then
		icon="󰔟"
		color=$C_W
	elif ((k > 0)); then
		icon=""
		color=$C_K
	elif ((p > 0)); then
		icon="${SPINNER_FRAMES[$((_now % ${#SPINNER_FRAMES[@]}))]}"
		color=$C_P
	elif ((d > 0)); then
		icon="󰸞"
		color=$C_D
	else
		icon="󰒲"
		color=$C_I
	fi
	echo "${color}${icon}${C_R} "
}

# --- Collect process icons with single list-panes -a call (already done above) ---
# Reuse the pane data: collect unique procs per window and per session
declare -A win_seen_procs sess_seen_procs
declare -A win_proc_list sess_proc_list

while IFS=$'\t' read -r sess win_idx proc; do
	[[ -n $proc ]] || continue
	target="${sess}:${win_idx}"
	if [[ -z ${win_seen_procs["${target}:${proc}"]+x} ]]; then
		win_seen_procs["${target}:${proc}"]=1
		win_proc_list[$target]+="${win_proc_list[$target]:+ }$proc"
	fi
	if [[ -z ${sess_seen_procs["${sess}:${proc}"]+x} ]]; then
		sess_seen_procs["${sess}:${proc}"]=1
		sess_proc_list[$sess]+="${sess_proc_list[$sess]:+ }$proc"
	fi
done < <(tmux list-panes -a -F '#{session_name}	#{window_index}	#{pane_current_command}')
unset win_seen_procs sess_seen_procs

# --- First pass: build icon strings and measure display widths ---
declare -A win_icons_str win_icons_dw
declare -A sess_icons_str sess_icons_dw

# Per-window icons (capped at MAX_ICONS)
while IFS=$'\t' read -r sess sess_id win_idx sess_path; do
	[[ -n $win_idx ]] || continue
	target="${sess}:${win_idx}"

	icons=""
	count=0
	# shellcheck disable=SC2086  # intentional word splitting
	for proc in ${win_proc_list[$target]:-}; do
		((count >= MAX_ICONS)) && break
		proc_icon="${ICON_MAP[$proc]:-$FALLBACK}"
		[[ -z $proc_icon ]] && continue
		icons+="$proc_icon "
		((count++)) || true
	done
	c_icon=$(format_claude \
		"${win_waiting[$target]:-0}" "${win_compacting[$target]:-0}" \
		"${win_processing[$target]:-0}" "${win_done[$target]:-0}" "${win_idle[$target]:-0}")
	[[ -n $c_icon ]] && icons+="$c_icon"
	stripped=$(printf '%s' "$icons" | sed 's/#\[[^]]*\]//g')
	win_icons_str[$target]="$icons"
	win_icons_dw[$target]=$(printf '%s' "$stripped" | wc -L)
done < <(tmux list-windows -a -F '#{session_name}	#{session_id}	#{window_index}	#{session_path}')

# Per-session icons (capped at MAX_ICONS_PICKER for more coverage)
while IFS=$'\t' read -r sess sess_id; do
	[[ -n $sess ]] || continue
	icons=""
	count=0
	# shellcheck disable=SC2086  # intentional word splitting
	for proc in ${sess_proc_list[$sess]:-}; do
		((count >= MAX_ICONS_PICKER)) && break
		proc_icon="${ICON_MAP[$proc]:-$FALLBACK}"
		[[ -z $proc_icon ]] && continue
		icons+="$proc_icon "
		((count++)) || true
	done
	c_icon=$(format_claude \
		"${sess_waiting[$sess]:-0}" "${sess_compacting[$sess]:-0}" \
		"${sess_processing[$sess]:-0}" "${sess_done[$sess]:-0}" "${sess_idle[$sess]:-0}")
	[[ -n $c_icon ]] && icons+="$c_icon"
	stripped=$(printf '%s' "$icons" | sed 's/#\[[^]]*\]//g')
	sess_icons_str[$sess]="$icons"
	sess_icons_dw[$sess]=$(printf '%s' "$stripped" | wc -L)
done < <(tmux list-sessions -F '#{session_name}	#{session_id}')

# --- Compute dynamic column widths ---
max_win_dw=0
for target in "${!win_icons_dw[@]}"; do
	((win_icons_dw[$target] > max_win_dw)) && max_win_dw=${win_icons_dw[$target]}
done
WIN_COL=$((max_win_dw > 0 ? max_win_dw + 1 : 0))

max_sess_dw=0
for sess in "${!sess_icons_dw[@]}"; do
	((sess_icons_dw[$sess] > max_sess_dw)) && max_sess_dw=${sess_icons_dw[$sess]}
done
SESS_COL=$((max_sess_dw > 0 ? max_sess_dw + 1 : 0))

# --- Second pass: pad and build tmux commands ---
declare -a tmux_cmds=()

tmux_cmds+=("set -g @picker_icon_branch '#[fg=${thm_green}]${icon_branch}#[fg=default]'")
tmux_cmds+=("set -g @picker_icon_dir '#[fg=${thm_blue}]${icon_dir}#[fg=default]'")

# Per-window
while IFS=$'\t' read -r sess sess_id win_idx sess_path; do
	[[ -n $win_idx ]] || continue
	target="${sess}:${win_idx}"
	id_target="${sess_id}:${win_idx}"

	pad_needed=$((WIN_COL - win_icons_dw[$target]))
	((pad_needed < 0)) && pad_needed=0
	printf -v pad '%*s' "$pad_needed" ''
	tmux_cmds+=("set -w -t '${id_target}' @picker_win_icons '${win_icons_str[$target]}${pad}'")

	short_path="${sess_path/#$HOME/\~}"
	tmux_cmds+=("set -t '${sess_id}' @picker_path '$short_path'")
done < <(tmux list-windows -a -F '#{session_name}	#{session_id}	#{window_index}	#{session_path}')

# Per-session
while IFS=$'\t' read -r sess sess_id; do
	[[ -n $sess ]] || continue
	pad_needed=$((SESS_COL - sess_icons_dw[$sess]))
	((pad_needed < 0)) && pad_needed=0
	printf -v pad '%*s' "$pad_needed" ''
	tmux_cmds+=("set -t '${sess_id}' @picker_icons '${sess_icons_str[$sess]:-}${pad}'")
done < <(tmux list-sessions -F '#{session_name}	#{session_id}')

# Execute all tmux set commands in a single invocation
printf '%s\n' "${tmux_cmds[@]}" | tmux source -

# Session rows: [icons + claude] [dir icon] path
# Window rows:  [icons + claude] name [zoomed] [branch icon] branch
tmux choose-tree -Zw -O name \
	-F '#{?window_format,#{@picker_win_icons}#{window_name}#{?window_zoomed_flag, 󰁌,}#{?#{@branch}, #{@picker_icon_branch} #{=30:@branch},},#{@picker_icons}#{@picker_icon_dir} #{=30:@picker_path}}' \
	'switch-client -t "%1"'
