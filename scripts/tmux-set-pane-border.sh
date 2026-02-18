#!/usr/bin/env bash
# Set pane-border-format with expanded @thm_* color values
# Called from tmux config after catppuccin theme loads
# Nested #{@thm_*} inside #[] style tags don't expand at render time,
# so we read and interpolate them here

thm_mauve=$(tmux show -gv @thm_mauve 2>/dev/null | tr -d '"')
thm_green=$(tmux show -gv @thm_green 2>/dev/null | tr -d '"')
thm_bg=$(tmux show -gv @thm_bg 2>/dev/null | tr -d '"')
thm_overlay_1=$(tmux show -gv @thm_overlay_1 2>/dev/null | tr -d '"')

# Only set if we got all the colors (catppuccin theme is loaded)
if [[ -n $thm_mauve && -n $thm_green && -n $thm_bg && -n $thm_overlay_1 ]]; then
  # Format: dot (●) on active pane when split and not zoomed, clean line otherwise
  tmux setw -g pane-border-format \
    "#{?#{&&:#{pane_active},#{&&:#{>:#{window_panes},1},#{==:#{window_zoomed_flag},0}}},#[fg=${thm_mauve}]━━ #[fg=${thm_green}]●#[fg=${thm_mauve}] ━━,#[bg=${thm_bg},fg=${thm_overlay_1}]━━━━━}"
fi
