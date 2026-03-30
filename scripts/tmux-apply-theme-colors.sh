#!/usr/bin/env bash
# Apply theme-dependent colors after catppuccin loads
# Handles: pane borders, which-key popup, tmux-fingers hints
# Runs at config load and on theme-toggle (via config re-source)

# Read catppuccin color variables
thm_crust=$(tmux show -gv @thm_crust 2>/dev/null | tr -d '"')
thm_mantle=$(tmux show -gv @thm_mantle 2>/dev/null | tr -d '"')
thm_bg=$(tmux show -gv @thm_bg 2>/dev/null | tr -d '"')
thm_surface_0=$(tmux show -gv @thm_surface_0 2>/dev/null | tr -d '"')
thm_overlay_1=$(tmux show -gv @thm_overlay_1 2>/dev/null | tr -d '"')
thm_mauve=$(tmux show -gv @thm_mauve 2>/dev/null | tr -d '"')
thm_green=$(tmux show -gv @thm_green 2>/dev/null | tr -d '"')
thm_peach=$(tmux show -gv @thm_peach 2>/dev/null | tr -d '"')
thm_teal=$(tmux show -gv @thm_teal 2>/dev/null | tr -d '"')

# Bail if catppuccin hasn't loaded yet
[[ -z $thm_mauve || -z $thm_bg ]] && exit 0

# --- Pane borders ---
# Nested #{@thm_*} inside #[] don't expand at render time, so we interpolate here
tmux setw -g pane-border-format \
	"#{?#{&&:#{pane_active},#{&&:#{>:#{window_panes},1},#{==:#{window_zoomed_flag},0}}},#[fg=${thm_mauve}]━━ #[fg=${thm_green}]●#[fg=${thm_mauve}] ━━,#[bg=${thm_bg},fg=${thm_overlay_1}]━━━━━}"

# --- which-key popup ---
tmux set -g @which-key-popup-bg "${thm_mantle}"
tmux set -g @which-key-popup-fg "${thm_mauve}"

# --- tmux-fingers hints ---
tmux set -g @fingers-hint-style "fg=${thm_crust},bg=${thm_mauve},bold"
tmux set -g @fingers-highlight-style "fg=${thm_peach},bg=${thm_surface_0}"
tmux set -g @fingers-selected-hint-style "fg=${thm_crust},bg=${thm_green},bold"
tmux set -g @fingers-selected-highlight-style "fg=${thm_teal},bg=${thm_surface_0}"
