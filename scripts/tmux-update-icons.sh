#!/usr/bin/env bash
# Lightweight icon updater called via #() every status-interval.
# Only updates @window_icon_display per window — no layout reflow.
# Outputs nothing (side-effect only).

SESSION=${1:-$(tmux display-message -p '#{session_name}')}

# Icon map (Nix-generated)
# shellcheck disable=SC2190  # icon map entries are Nix-generated placeholders
declare -A ICON_MAP=(
	@ICON_MAP@
)
FALLBACK="@FALLBACK_ICON@"
MAX_ICONS=@MAX_ICONS@

while IFS='|' read -r idx _; do
	target="${SESSION}:${idx}"

	# Collect unique processes across all panes in this window
	declare -A seen=()
	declare -a unique_procs=()
	while IFS= read -r proc; do
		[[ -z $proc ]] && continue
		if [[ -z ${seen[$proc]+x} ]]; then
			seen[$proc]=1
			unique_procs+=("$proc")
		fi
	done < <(tmux list-panes -t "$target" -F '#{pane_current_command}' 2>/dev/null)

	# Map to icons, cap at MAX_ICONS
	icon=""
	count=0
	for proc in "${unique_procs[@]}"; do
		((count >= MAX_ICONS)) && break
		icon+="${ICON_MAP[$proc]:-$FALLBACK}"
		((count++)) || true
	done
	unset seen unique_procs

	tmux set -qw -t "$target" @window_icon_display "$icon"
done < <(tmux list-windows -t "$SESSION" -F '#{window_index}|')
