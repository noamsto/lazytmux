#!/usr/bin/env bash
# The command og-remote-open gives the mirror session's initial window, so the
# client lands on a loading screen rather than a stray local shell while the
# daemon dials, enumerates the remote session and builds the first mirror
# window. The daemon's `respawn-pane -k` for that window is what ends this
# process — it must never exit on its own: before stampMirrorWindow the window
# still carries remain-on-exit off, so a pane that exits takes the single-window
# mirror session with it.
#
# Deliberately no `set -e`: every read below is best-effort (the phase file may
# not exist yet, tmux may refuse a stale pane target), and an aborted loading
# screen is exactly the session-killing exit described above.
set -uo pipefail

host="${1:-}"
sess="${2:-}"
phase_file="${3:-}"

frames=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏)
esc=$'\033'
reset="${esc}[0m"

# ansi_fg turns a #rrggbb theme value into a truecolor SGR, REPLY-style. A
# value tmux never set, or a named color, yields no escape at all rather than
# a wrong one.
ansi_fg() {
	local hex="${1#\#}"
	if [[ ! $hex =~ ^[0-9a-fA-F]{6}$ ]]; then
		REPLY=""
		return
	fi
	printf -v REPLY '%s[38;2;%d;%d;%dm' "$esc" "0x${hex:0:2}" "0x${hex:2:2}" "0x${hex:4:2}"
}

# One tmux call for geometry and palette, read once: the screen lives only for
# the length of a bridge setup, so a resize inside it costs a briefly
# off-centre spinner.
cols=80 rows=24 accent="" dim="" fg=""
pane_target=()
[[ -n ${TMUX_PANE:-} ]] && pane_target=(-t "$TMUX_PANE")
geom="$(tmux display-message -p "${pane_target[@]}" '#{pane_width}|#{pane_height}|#{@thm_mauve}|#{@thm_subtext_0}|#{@thm_text}' 2>/dev/null || true)"
IFS='|' read -r geom_cols geom_rows thm_accent thm_dim thm_fg <<<"$geom"
[[ $geom_cols =~ ^[1-9][0-9]*$ ]] && cols="$geom_cols"
[[ $geom_rows =~ ^[1-9][0-9]*$ ]] && rows="$geom_rows"
ansi_fg "${thm_accent:-}"
accent="$REPLY"
ansi_fg "${thm_dim:-}"
dim="$REPLY"
ansi_fg "${thm_fg:-}"
fg="$REPLY"

title="$host"
[[ -n $sess ]] && title+=" : $sess"

# centered clears the whole row before drawing, so a caption never leaves the
# tail of a longer one behind it.
centered() {
	local row="$1" width="$2" body="$3"
	local pad=$(((cols - width) / 2))
	((pad < 0)) && pad=0
	printf '%s[%d;1H%s[2K%*s%s' "$esc" "$row" "$esc" "$pad" "" "$body"
}

title_row=$(((rows - 2) / 2))
((title_row < 1)) && title_row=1

printf '%s[2J%s[?25l' "$esc" "$esc"
trap 'printf "%s[?25h" "$esc"' EXIT

i=0
while :; do
	phase=""
	[[ -n $phase_file && -r $phase_file ]] && read -r phase <"$phase_file"
	centered "$title_row" $((${#title} + 3)) "${accent}${frames[i]}${reset}  ${fg}${title}${reset}"
	centered $((title_row + 2)) "${#phase}" "${dim}${phase}${reset}"
	i=$(((i + 1) % ${#frames[@]}))
	sleep 0.1
done
