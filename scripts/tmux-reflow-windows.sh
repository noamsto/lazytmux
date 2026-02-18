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
max_text_len=0
total=0
FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{pane_current_command}|#{window_panes}'
while IFS='|' read -r idx branch pane_path cmd panes; do
  indices+=("$idx")
  commands+=("$cmd")
  pane_counts+=("$panes")

  # Compute text length for padding (branch name or dir basename, no icon)
  if [[ -n $branch ]]; then
    if ((${#branch} > 20)); then
      text_len=21 # 20 chars + …
    else
      text_len=${#branch}
    fi
  else
    dirname=${pane_path##*/}
    text_len=${#dirname}
  fi
  ((text_len > max_text_len)) && max_text_len=$text_len
  ((total++))
done < <(tmux list-windows -t "$SESSION" -F "$FMT")

[[ $total -eq 0 ]] && exit 0

# Discover nerd font binary path from tmux's automatic-rename-format
# (the plugin embeds its full nix store path there)
NERD_BIN=$(tmux show-option -gv automatic-rename-format 2>/dev/null |
  grep -oP '#\(\K[^ ]+tmux-nerd-font-window-name' || true)

# Cache nerd font icon per window as @window_icon_display option
# Runs only on hook events, not every status-interval
for ((j = 0; j < total; j++)); do
  if [[ -n $NERD_BIN && -x $NERD_BIN ]]; then
    icon=$("$NERD_BIN" "${commands[$j]}" "${pane_counts[$j]:-1}")
  else
    icon=""
  fi
  tmux set -w -t "$SESSION:${indices[$j]}" @window_icon_display "$icon"
done

# Compute split points using padded slot width
# Each slot: "N: " (3) + icon (2) + space (1) + padded_text (max_text_len) + claude_status (5) + " │ " (3) = max_text_len + 14
# Note: nerd font icons are 1 display col each; emoji icons (🤖🐟) are 2 cols
# Zoom indicator not reserved — appended only when zoomed (rare, minor overflow OK)
slot_width=$((max_text_len + 14))
available=$((WIDTH - PREFIX_WIDTH))

cumulative=0
current_line=0
split1=999
split2=999
prev_idx=

for ((j = 0; j < total; j++)); do
  if ((cumulative + slot_width > available && cumulative > 0)); then
    ((current_line++))
    if ((current_line == 1)); then
      split1=$prev_idx
    elif ((current_line == 2)); then
      split2=$prev_idx
      break
    fi
    cumulative=$slot_width
  else
    cumulative=$((cumulative + slot_width))
  fi
  prev_idx=${indices[$j]}
done

tmux set -t "$SESSION" @window_split "$split1"
tmux set -t "$SESSION" @window_split2 "$split2"

if ((current_line >= 2)); then
  tmux set -t "$SESSION" status 4
elif ((current_line == 1)); then
  tmux set -t "$SESSION" status 3
else
  tmux set -t "$SESSION" status 2
fi

# Preserve status-format[0] (session info line) at session level
# Setting session-level status-format[1+] overrides global inheritance for ALL indices
FMT0=$(tmux show -gv status-format[0] 2>/dev/null)
[[ -n $FMT0 ]] && tmux set -t "$SESSION" status-format[0] "$FMT0"

# Common format fragments
ICON='#{@window_icon_display}'
TEXT='#{?#{@branch},#{=20:@branch}#{?#{==:#{=20:@branch},#{@branch}},,…},#{b:pane_current_path}}'
CLAUDE='#(claude-status --window '"'"'#{session_name}:#{window_index}'"'"')'
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "

if ((current_line == 0)); then
  # Single line: compact, no padding — just use window_name directly
  ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]#{window_index}: #{window_name},#[fg=#{@thm_subtext_0}#,nobold]#{window_index}: #[fg=#{@thm_fg}]#{window_name}}#{?window_zoomed_flag, 󰁌,}${CLAUDE}#[norange]"
  tmux set -t "$SESSION" status-format[1] \
    "#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:${ENTRY}#{?window_end_flag,,${SEP}}}"
  tmux set -t "$SESSION" status-format[2] ""
  tmux set -t "$SESSION" status-format[3] ""
else
  # Multi-line: padded columns with icon separated from text
  # Zoom indicator inside padded area so it consumes padding space, not extra width
  # +2 for " 󰁌" (space + 1-char icon) when zoomed
  P=$((max_text_len + 2))
  TEXT_Z="${TEXT}#{?window_zoomed_flag, 󰁌,}"
  ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]#{window_index}: ${ICON} #{p${P}:${TEXT_Z}},#[fg=#{@thm_subtext_0}#,nobold]#{window_index}: #[fg=#{@thm_fg}]${ICON} #{p${P}:${TEXT_Z}}}${CLAUDE}#[norange]"

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
