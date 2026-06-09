#!/usr/bin/env bash
# Toggle a split pane that browses the image manifest of the active Claude pane.
# Outer mode (keybind): toggle the viewer pane on/off.
# Inner mode (--view PANE): full-pane keyboard navigator.
set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
IMAGES_DIR="$STATE_DIR/images"

if [[ ${1:-} == --view ]]; then
	src_pane="$2"
	start_idx=0
	if [[ ${3:-} == --start && -n ${4:-} ]]; then
		start_idx="$4"
	fi
	manifest="$IMAGES_DIR/${src_pane#%}.jsonl"

	# Load valid (parseable) manifest lines into `lines`; skips blank/corrupt.
	load_manifest() {
		mapfile -t lines < <(grep -v '^[[:space:]]*$' "$manifest" 2>/dev/null |
			while IFS= read -r ln; do jq -e . >/dev/null 2>&1 <<<"$ln" && printf '%s\n' "$ln"; done)
		n=${#lines[@]}
	}

	load_manifest
	((n > 0)) || {
		echo "no images"
		sleep 1
		exit 0
	}

	i=$start_idx
	((i >= 0)) || i=0
	((i < n)) || i=$((n - 1))
	prev=-1
	while true; do
		# Redraw only when the selection changed — no flicker on no-op keys.
		if ((i != prev)); then
			printf '\033[2J\033[3J\033[H'
			path="$(jq -r '.path' <<<"${lines[$i]}")"
			src="$(jq -r '.source' <<<"${lines[$i]}")"
			read -r cols rows < <(tmux display-message -p '#{pane_width} #{pane_height}') || true
			claude-image-render "$path" "$cols" "$((rows - 1))" || true
			printf '\n[%d/%d] %s · %s   j/k n/p move · g/G ends · 1-9 jump · r reload · q quit ' \
				"$((i + 1))" "$n" "$(basename "$path")" "$src"
			prev=$i
		fi
		read -rsn1 key || break
		case "$key" in
		n | j) i=$(((i + 1) % n)) ;;
		p | k) i=$(((i - 1 + n) % n)) ;;
		g) i=0 ;;
		G) i=$((n - 1)) ;;
		r)
			load_manifest
			((n > 0)) || break
			((i < n)) || i=$((n - 1))
			prev=-1
			;;
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

viewer="$(tmux split-window -h -P -F '#{pane_id}' "@picker_generate@ --gallery '$src_pane' --viewer '$0'")"
tmux set-option -p -t "$viewer" @claude_img_src "$src_pane"
