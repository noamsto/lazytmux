#!/usr/bin/env bash
# Render one image to the current pane, choosing the best backend for the outer
# terminal. Selection is isolated in choose_renderer for testability.
# Usage: claude-image-render <path> [cols] [rows]
#        claude-image-render --choose <client_termname> <has_kitten 0|1>   (test hook)
set -euo pipefail

choose_renderer() { # $1=client_termname  $2=has_kitten(0|1) → prints backend id
	local term="$1" kitten="$2"
	case "$term" in
	xterm-kitty* | xterm-ghostty*)
		if [[ $kitten == 1 ]]; then echo kitten; else echo chafa-kitty; fi
		;;
	foot* | *wezterm* | xterm* | contour* | konsole*)
		echo chafa-sixel
		;;
	*)
		echo chafa-symbols
		;;
	esac
}

if [[ ${1:-} == --choose ]]; then
	choose_renderer "${2:-}" "${3:-0}"
	exit 0
fi

path="${1:-}"
cols="${2:-}"
rows="${3:-}"
[[ -f $path ]] || {
	printf '[missing: %s]\n' "$path"
	exit 0
}

term="$(tmux display-message -p '#{client_termname}' 2>/dev/null || echo "${TERM:-}")"
has_kitten=0
command -v kitten >/dev/null 2>&1 && has_kitten=1
backend="$(choose_renderer "$term" "$has_kitten")"

size_chafa=()
[[ -n $cols && -n $rows ]] && size_chafa=(--size "${cols}x${rows}")

case "$backend" in
kitten)
	# No --place: kitten auto-fits the pane, and --place would disable
	# --unicode-placeholder (the tmux-survivable placement mode).
	kitten icat --clear --unicode-placeholder --transfer-mode=memory "$path"
	;;
chafa-kitty)
	chafa -f kitty --passthrough tmux "${size_chafa[@]}" "$path"
	;;
chafa-sixel)
	chafa -f sixel "${size_chafa[@]}" "$path"
	;;
chafa-symbols)
	chafa -f symbols "${size_chafa[@]}" "$path"
	;;
esac
