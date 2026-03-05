#!/usr/bin/env bash
# Session picker wrapper that pre-computes claude status before opening choose-tree
# This avoids tmux's #() caching issue where first-time commands return empty
#
# Color trick: choose-tree -F renders #[style] from variable expansion (#{@var})
# but NOT from literal text. So we bake colors into tmux variables.
#
# choose-tree shows "session_name: FORMAT" — so we don't include name in format.
# To align the dir icon column, we pad with spaces to compensate for name length.

set -euo pipefail

# Read theme colors and icons from tmux
thm_blue=$(tmux show -gv @thm_blue 2>/dev/null || echo "blue")
thm_green=$(tmux show -gv @thm_green 2>/dev/null || echo "green")
icon_dir=$(tmux show -gv @icon_dir 2>/dev/null || echo "")
icon_branch=$(tmux show -gv @icon_branch 2>/dev/null || echo "")

# Icon map (Nix-generated)
# shellcheck disable=SC2190  # icon map entries are Nix-generated placeholders
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK="@FALLBACK_ICON@"
MAX_ICONS=@MAX_ICONS@

# --- Claude status: read all pane files once, bucket by session ---
PANES_DIR="/tmp/claude-status/panes"
ICON_CLAUDE="󱙺"
SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
printf -v _now '%(%s)T' -1

# Per-session claude state tallies: sess_claude_<state>[session]=count
declare -A sess_claude_waiting sess_claude_compacting sess_claude_processing sess_claude_done sess_claude_idle

if [[ -d $PANES_DIR ]]; then
	for pf in "$PANES_DIR"/*; do
		[[ -f $pf ]] || continue
		state="" timestamp="" pane_session=""
		while IFS='=' read -r key val; do
			case "$key" in
			state) state="$val" ;;
			timestamp) timestamp="$val" ;;
			session) pane_session="$val" ;;
			esac
		done <"$pf"
		[[ -n $pane_session && -n $state ]] || continue
		# Staleness checks
		if [[ -n $timestamp ]]; then
			age=$((_now - timestamp))
			[[ $state == "waiting" && $age -gt 30 ]] && state="processing"
			[[ $state == "processing" && $age -gt 15 ]] && state="done"
		fi
		# Tally into per-session counters
		case "$state" in
		waiting) sess_claude_waiting[$pane_session]=$((${sess_claude_waiting[$pane_session]:-0} + 1)) ;;
		compacting) sess_claude_compacting[$pane_session]=$((${sess_claude_compacting[$pane_session]:-0} + 1)) ;;
		processing) sess_claude_processing[$pane_session]=$((${sess_claude_processing[$pane_session]:-0} + 1)) ;;
		done) sess_claude_done[$pane_session]=$((${sess_claude_done[$pane_session]:-0} + 1)) ;;
		idle) sess_claude_idle[$pane_session]=$((${sess_claude_idle[$pane_session]:-0} + 1)) ;;
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

format_session_claude() {
	local s=$1
	local w=${sess_claude_waiting[$s]:-0} k=${sess_claude_compacting[$s]:-0}
	local p=${sess_claude_processing[$s]:-0} d=${sess_claude_done[$s]:-0} i=${sess_claude_idle[$s]:-0}
	((w + k + p + d + i == 0)) && return
	local icon color
	if ((w > 0)); then
		icon="󰔟"
		color=$C_W
	elif ((k > 0)); then
		icon=""
		color=$C_K
	elif ((p > 0)); then
		icon="${SPINNER_FRAMES[$((_now % ${#SPINNER_FRAMES[@]}))]}" color=$C_P
	elif ((d > 0)); then
		icon="󰸞"
		color=$C_D
	else
		icon="󰒲"
		color=$C_I
	fi
	echo "${ICON_CLAUDE} ${color}${icon}${C_R} "
}

# --- Pre-compute colored icons into global vars ---
tmux set -g @picker_icon_dir "#[fg=${thm_blue}]${icon_dir}#[fg=default]"
tmux set -g @picker_icon_branch "#[fg=${thm_green}]${icon_branch}#[fg=default]"

# First pass: collect session data and find max name width for icon alignment
declare -a sessions=() paths=() statuses=() icons_arr=()
max_name=0

while IFS=$'\t' read -r sess sess_path; do
	[[ -n $sess ]] || continue
	status=$(format_session_claude "$sess")
	short_path="${sess_path/#$HOME/\~}"

	# Collect unique process icons across all panes in this session
	declare -A sess_seen=()
	declare -a sess_procs=()
	while IFS= read -r proc; do
		[[ -z $proc ]] && continue
		if [[ -z ${sess_seen[$proc]+x} ]]; then
			sess_seen[$proc]=1
			sess_procs+=("$proc")
		fi
	done < <(tmux list-panes -t "$sess" -s -F '#{pane_current_command}' 2>/dev/null)

	sess_icons=""
	icon_count=0
	for proc in "${sess_procs[@]}"; do
		((icon_count >= MAX_ICONS)) && break
		sess_icons+="${ICON_MAP[$proc]:-$FALLBACK}"
		((icon_count++)) || true
	done
	unset sess_seen sess_procs

	sessions+=("$sess")
	paths+=("$short_path")
	statuses+=("$status")
	icons_arr+=("$sess_icons")
	((${#sess} > max_name)) && max_name=${#sess}
done < <(tmux list-sessions -F '#{session_name}	#{session_path}')

# Second pass: set alignment padding, path, and status per session
for i in "${!sessions[@]}"; do
	name="${sessions[$i]}"
	pad_len=$((max_name - ${#name}))
	padding=$(printf '%*s' "$pad_len" '')
	tmux set -t "$name" @picker_pad "$padding"
	tmux set -t "$name" @picker_path "${paths[$i]}"
	tmux set -t "$name" @picker_icons "${icons_arr[$i]}"
	tmux set -t "$name" @claude_status "${statuses[$i]}"
done

# Format: [padding] [dir icon] path  [claude status]
# tmux's tree prefix shows "session_name:" before this, padding aligns the icon column
tmux choose-tree -Zs -O name \
	-F '#{?window_format,#{window_name}#{?#{@branch}, #{@picker_icon_branch} #{=20:@branch},},#{@picker_pad}#{@picker_icons} #{@picker_icon_dir} #{@picker_path} #{@claude_status}}' \
	'switch-client -t "%1"'
