#!/usr/bin/env bash
# Toggle a split pane that browses the image manifest of the active Claude pane.
# Outer mode (keybind): toggle the viewer pane on/off.
# Inner mode (--view PANE): full-pane keyboard navigator.
set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
IMAGES_DIR="$STATE_DIR/images"
SELF="${BASH_SOURCE[0]}"

if [[ ${1:-} == --view ]]; then
	src_pane="$2"
	manifest="$IMAGES_DIR/${src_pane#%}.jsonl"
	mapfile -t lines < <(grep -v '^[[:space:]]*$' "$manifest")
	n=${#lines[@]}
	((n > 0)) || {
		echo "no images"
		sleep 1
		exit 0
	}
	i=0
	while true; do
		clear
		path="$(jq -r '.path' <<<"${lines[$i]}")"
		src="$(jq -r '.source' <<<"${lines[$i]}")"
		read -r cols rows < <(tmux display-message -p '#{pane_width} #{pane_height}') || true
		claude-image-render "$path" "$cols" "$((rows - 1))" || true
		printf '\n[%d/%d] %s · %s   n/p · 1-9=jump · q quit ' \
			"$((i + 1))" "$n" "$(basename "$path")" "$src"
		read -rsn1 key || break
		case "$key" in
		n) i=$(((i + 1) % n)) ;;
		p) i=$(((i - 1 + n) % n)) ;;
		q) break ;;
		[0-9])
			num=$((key - 1))
			((num >= 0 && num < n)) && i=$num
			;;
		esac
	done
	exit 0
fi

# Outer mode (keybind): toggle viewer pane for the active (Claude) pane.
src_pane="$(tmux display-message -p '#{pane_id}')"
manifest="$IMAGES_DIR/${src_pane#%}.jsonl"
[[ -s $manifest ]] || {
	tmux display-message "no images yet for this pane"
	exit 0
}

existing="$(tmux list-panes -F '#{pane_id} #{@claude_img_src}' |
	awk -v s="$src_pane" '$2 == s {print $1; exit}')"
if [[ -n $existing ]]; then
	tmux kill-pane -t "$existing"
	exit 0
fi

viewer="$(tmux split-window -h -P -F '#{pane_id}' "'$SELF' --view '$src_pane'")"
tmux set-option -p -t "$viewer" @claude_img_src "$src_pane"
