#!/usr/bin/env bash
# tmux-reflow-windows: Compute window layout and set status-format lines
# Handles split points, dynamic padding, icon caching, and status line count (2-4).
# Called by hooks on window add/remove/resize, NOT every status-interval.
#
# Key design: icon and text are separated for alignment.
# Icons (🤖, 🐟, ⚙️) have variable char-to-display-width ratios,
# so padding only the text portion (ASCII branch/dir names) gives
# consistent column alignment regardless of icon encoding.

# Accept session/width as args (from hooks) or fall back to display-message
SESSION=${1:-$(tmux display-message -p '#{session_name}')}
WIDTH=${2:-$(tmux display-message -p '#{client_width}')}

PREFIX_WIDTH=5 # " ├─ " or " ╰─ "

# Collect window data and compute max TEXT length (without icon)
# Use | delimiter (not tab) because IFS whitespace chars collapse empty fields
declare -a indices commands pane_counts
max_text_len=0     # capped at 20, for multi-line padded column width
max_text_len_raw=0 # uncapped, for split-point calculation (single-line uses full names)
total=0
has_zoom=0      # whether any window is currently zoomed
has_truncated=0 # whether any branch/dir name exceeds 20 chars (gets "…" suffix)
FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{pane_current_command}|#{window_panes}|#{window_zoomed_flag}'
while IFS='|' read -r idx branch pane_path cmd panes zoomed; do
	indices+=("$idx")
	commands+=("$cmd")
	pane_counts+=("$panes")
	((zoomed)) && has_zoom=1

	# Compute text length (branch name or dir basename, no icon)
	if [[ -n $branch ]]; then
		text_len=${#branch}
	else
		dirname=${pane_path##*/}
		text_len=${#dirname}
	fi
	((text_len > max_text_len_raw)) && max_text_len_raw=$text_len
	# Cap at 20 for multi-line padding (matches #{=20:...} truncation)
	capped=$text_len
	((capped > 20)) && {
		has_truncated=1
		capped=20
	}
	((capped > max_text_len)) && max_text_len=$capped
	((total++))
done < <(tmux list-windows -t "$SESSION" -F "$FMT")

[[ $total -eq 0 ]] && exit 0

# Discover nerd font binary path from tmux's automatic-rename-format
# (the plugin embeds its full nix store path there)
NERD_BIN=$(tmux show-option -gv automatic-rename-format 2>/dev/null |
	grep -oP '#\(\K[^ ]+tmux-nerd-font-window-name' || true)

# Cache nerd font icon per window as @window_icon_display option
# Runs only on hook events, not every status-interval
# Pad single-width glyphs (nerd fonts, 3 UTF-8 bytes) with a trailing space
# so they match double-width emoji (4 UTF-8 bytes) for column alignment.
for ((j = 0; j < total; j++)); do
	if [[ -n $NERD_BIN && -x $NERD_BIN ]]; then
		icon=$("$NERD_BIN" "${commands[$j]}" "${pane_counts[$j]:-1}")
	else
		icon=""
	fi
	# Nerd font glyphs are 3 bytes (PUA, 1 display col); emoji are 4 bytes (2 display cols).
	# Append a space to single-width icons for consistent 2-col alignment.
	if [[ -n $icon && ${#icon} -eq 1 ]]; then
		byte_len=$(printf '%s' "$icon" | wc -c)
		if ((byte_len <= 3)); then
			icon="$icon "
		fi
	fi
	tmux set -w -t "$SESSION:${indices[$j]}" @window_icon_display "$icon"
done

# Compute split points
# Each slot: "N: " (idx_width+2) + icon (2) + space (1) + text + claude_status (5) + " │ " (3) = text + idx_width + 13
# Note: nerd font icons are 1 display col each; emoji icons (🤖🐟) are 2 cols
# Zoom indicator: " 󰁌" = 2 display cols (space + 1-col nerd icon)
# Only one window can be zoomed at a time.
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
available=$((WIDTH - PREFIX_WIDTH))

# Two-pass approach:
# 1. Check if single-line fits using raw (uncapped) text widths, since single-line
#    renders full #{window_name} without truncation.
# 2. If not, compute multi-line split points using capped widths (multi-line
#    truncates text to 20 chars with #{=20:...}).
#
# Single-line: zoom adds 2 to one slot (only one window can be zoomed).
# Multi-line padding area (P) reserves: +2 for zoom icon, +1 for "…" ellipsis
#   when any name is truncated. slot_width_capped = P + idx_width + 13.
zoom_extra=0
((has_zoom)) && zoom_extra=2
ellipsis_extra=0
((has_truncated)) && ellipsis_extra=1
slot_width_raw=$((max_text_len_raw + idx_width + 13))
slot_width_capped=$((max_text_len + 2 + ellipsis_extra + idx_width + 13))

# Check if everything fits on one line (conservative: uses max-width slot for all)
# Add zoom_extra (2) if any window is zoomed — only one can be at a time.
if ((slot_width_raw * total + zoom_extra <= available)); then
	needs_multiline=0
else
	needs_multiline=1
fi

cumulative=0
current_line=0
split1=999
split2=999
prev_idx=

if ((needs_multiline)); then
	# Use capped slot width for multi-line split points
	for ((j = 0; j < total; j++)); do
		if ((cumulative + slot_width_capped > available && cumulative > 0)); then
			((current_line++))
			if ((current_line == 1)); then
				split1=$prev_idx
			elif ((current_line == 2)); then
				split2=$prev_idx
				break
			fi
			cumulative=$slot_width_capped
		else
			cumulative=$((cumulative + slot_width_capped))
		fi
		prev_idx=${indices[$j]}
	done
fi

tmux set -t "$SESSION" @window_split "$split1"
tmux set -t "$SESSION" @window_split2 "$split2"

if ((current_line >= 2)); then
	tmux set -t "$SESSION" status 4
elif ((current_line >= 1)); then
	tmux set -t "$SESSION" status 3
else
	# needs_multiline with no splits still uses status 2 (truncated one-row format)
	tmux set -t "$SESSION" status 2
fi

# Preserve status-format[0] (session info line) at session level
# Setting session-level status-format[1+] overrides global inheritance for ALL indices
FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"

# Common format fragments
ICON='#{@window_icon_display}'
TEXT='#{?#{@branch},#{=20:@branch}#{?#{==:#{=20:@branch},#{@branch}},,…},#{=20:#{b:pane_current_path}}#{?#{==:#{=20:#{b:pane_current_path}},#{b:pane_current_path}},,…}}'
CLAUDE='#(@claude_status_bin@ --window '"'"'#{session_name}:#{window_index}'"'"')'
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "

if ((!needs_multiline && current_line == 0)); then
	# Single line: compact, no padding — just use window_name directly
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]#{window_index}: #{window_name},#[fg=#{@thm_subtext_0}#,nobold]#{window_index}: #[fg=#{@thm_fg}]#{window_name}}#{?window_zoomed_flag, 󰁌,}${CLAUDE}#[norange]"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
	tmux set -t "$SESSION" status-format[2] ""
	tmux set -t "$SESSION" status-format[3] ""
elif ((current_line == 0)); then
	# Truncated single row: names too long for single-line format but capped text
	# fits on one row. Uses padded/truncated entry on a single ╰─ line.
	P=$((max_text_len + 2 + ellipsis_extra))
	TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
	IDX="#{p${idx_width}:window_index}"
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: ${ICON} #{p${P}:${TEXT_Z}},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]${ICON} #{p${P}:${TEXT_Z}}}${CLAUDE}#[norange]"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
	tmux set -t "$SESSION" status-format[2] ""
	tmux set -t "$SESSION" status-format[3] ""
else
	# Multi-line: padded columns with icon separated from text
	# Zoom indicator inside padded area so it consumes padding space, not extra width
	# +2 for " 󰁌" (space + 1-char icon) when zoomed, +1 for "…" if any name truncated
	P=$((max_text_len + 2 + ellipsis_extra))
	TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
	# Right-pad index to consistent width using tmux's padding: #{pN:window_index}
	IDX="#{p${idx_width}:window_index}"
	ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: ${ICON} #{p${P}:${TEXT_Z}},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]${ICON} #{p${P}:${TEXT_Z}}}${CLAUDE}#[norange]"

	# Line 1: windows 1..split1
	PREFIX1="#{?#{e|>|:#{session_windows},#{@window_split}},├,╰}─"
	tmux set -t "$SESSION" status-format[1] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ${PREFIX1} #{W:#{?#{e|<=|:#{window_index},#{@window_split}},${ENTRY}#{?window_end_flag,,#{?#{e|==|:#{window_index},#{@window_split}},,${SEP}}},}}"

	# Line 2: windows split1+1..split2
	PREFIX2="#{?#{e|>|:#{session_windows},#{@window_split2}},├,╰}─"
	tmux set -t "$SESSION" status-format[2] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ${PREFIX2} #{W:#{?#{e|>|:#{window_index},#{@window_split}},#{?#{e|<=|:#{window_index},#{@window_split2}},${ENTRY}#{?window_end_flag,,#{?#{e|==|:#{window_index},#{@window_split2}},,${SEP}}},},}}"

	# Line 3: windows beyond split2
	tmux set -t "$SESSION" status-format[3] \
		"#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:#{?#{e|>|:#{window_index},#{@window_split2}},${ENTRY}#{?window_end_flag,,${SEP}},}}"
fi
