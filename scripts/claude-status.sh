#!/usr/bin/env bash
# Unified Claude status indicator for tmux
# Merges claude-tmux-indicator and claude-session-status into one script.
#
# Usage:
#   claude-status --pane <pane_id>                     [--format icon|icon-color]
#   claude-status --window <session:index>             [--format icon|icon-color]
#   claude-status --session <name>                     [--format icon|icon-color|short|long|gum]
#
# Formats:
#   icon        Plain icon, count if >1 (no tmux color codes)
#   icon-color  Icon with tmux #[fg=...] color codes, count if >1 (default)
#   short       Icon + total count always (e.g. "󰔟2")
#   long        Text breakdown ("2 processing, 1 waiting")
#   gum         Gum-styled output for sesh picker
#
# Pane/window modes add a leading space (for tmux status bar positioning).
# Session mode has no leading space.

set -euo pipefail

STATE_DIR="/tmp/claude-status"
PANES_DIR="$STATE_DIR/panes"

# --- Icons & Spinner ---

SPINNER_FRAMES=("󰪞" "󰪟" "󰪠" "󰪡" "󰪢" "󰪣" "󰪤" "󰪥")
SPINNER_COUNT=${#SPINNER_FRAMES[@]}
ICON_CLAUDE="󱙺"
ICON_WAITING="󰔟"
ICON_COMPACTING=""
ICON_DONE="󰸞"
ICON_IDLE="󰒲"

get_spinner_frame() {
  echo "${SPINNER_FRAMES[$(($(date +%s) % SPINNER_COUNT))]}"
}

state_icon() {
  case "$1" in
  processing) get_spinner_frame ;;
  waiting) echo "$ICON_WAITING" ;;
  compacting) echo "$ICON_COMPACTING" ;;
  done) echo "$ICON_DONE" ;;
  idle) echo "$ICON_IDLE" ;;
  esac
}

# --- Theme & Colors ---

C_PROCESSING="" C_WAITING="" C_COMPACTING="" C_DONE="" C_IDLE="" C_RESET=""

setup_colors() {
  local theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
  local theme="dark"
  if [[ -f $theme_file ]]; then
    theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4)
  fi

  if [[ $theme == "light" ]]; then
    C_PROCESSING="#[fg=#179299]"
    C_WAITING="#[fg=#fe640b]"
    C_COMPACTING="#[fg=#04a5e5]"
    C_DONE="#[fg=#40a02b]"
    C_IDLE="#[fg=#6c6f85]"
  else
    C_PROCESSING="#[fg=#94e2d5]"
    C_WAITING="#[fg=#fab387]"
    C_COMPACTING="#[fg=#89dceb]"
    C_DONE="#[fg=#a6e3a1]"
    C_IDLE="#[fg=#6c7086]"
  fi
  C_RESET="#[fg=default]"
}

state_color() {
  case "$1" in
  processing) echo "$C_PROCESSING" ;;
  waiting) echo "$C_WAITING" ;;
  compacting) echo "$C_COMPACTING" ;;
  done) echo "$C_DONE" ;;
  idle) echo "$C_IDLE" ;;
  esac
}

# --- State Reading (with staleness checks) ---

read_pane_state() {
  local pane_file="$PANES_DIR/${1#%}"
  [[ -f $pane_file ]] || return 1

  local state timestamp
  state=$(grep "^state=" "$pane_file" 2>/dev/null | cut -d= -f2)
  timestamp=$(grep "^timestamp=" "$pane_file" 2>/dev/null | cut -d= -f2)

  if [[ -n $timestamp ]]; then
    local age=$(($(date +%s) - timestamp))
    # Stale waiting (>30s) -> permission was likely responded to
    [[ $state == "waiting" && $age -gt 30 ]] && state="processing"
    # Stale processing (>15s) -> Stop hook probably didn't fire
    [[ $state == "processing" && $age -gt 15 ]] && state="done"
  fi

  echo "$state"
}

# --- Counting ---

count_processing=0 count_waiting=0 count_compacting=0 count_done=0 count_idle=0 total=0

tally_state() {
  ((total++)) || true
  case "$1" in
  processing) ((count_processing++)) || true ;;
  waiting) ((count_waiting++)) || true ;;
  compacting) ((count_compacting++)) || true ;;
  done) ((count_done++)) || true ;;
  idle) ((count_idle++)) || true ;;
  esac
}

count_for_window() {
  while IFS= read -r pane; do
    [[ -n $pane ]] || continue
    local state
    state=$(read_pane_state "$pane") || continue
    tally_state "$state"
  done < <(tmux list-panes -t "$1" -F '#{pane_id}' 2>/dev/null)
}

count_for_session() {
  for pf in "$PANES_DIR"/*; do
    [[ -f $pf ]] || continue
    local pane_session pane_id
    pane_session=$(grep "^session=" "$pf" 2>/dev/null | cut -d= -f2)
    [[ $pane_session == "$1" ]] || continue
    pane_id=$(basename "$pf")
    local state
    state=$(read_pane_state "$pane_id") || continue
    tally_state "$state"
  done
}

priority_state() {
  if [[ $count_waiting -gt 0 ]]; then
    echo "waiting"
  elif [[ $count_compacting -gt 0 ]]; then
    echo "compacting"
  elif [[ $count_processing -gt 0 ]]; then
    echo "processing"
  elif [[ $count_done -gt 0 ]]; then
    echo "done"
  elif [[ $count_idle -gt 0 ]]; then
    echo "idle"
  fi
}

# --- Output Formatting ---

format_output() {
  local state="$1" count="$2" format="$3" leading_space="$4"
  [[ -n $state ]] || return 0

  local icon prefix=""
  icon=$(state_icon "$state")
  [[ $leading_space == "true" ]] && prefix=" "

  case "$format" in
  icon)
    local count_prefix=""
    [[ $count -gt 1 ]] && count_prefix="${count} "
    echo "${prefix}${count_prefix}${ICON_CLAUDE} ${icon} "
    ;;
  icon-color)
    setup_colors
    # Fixed-width: " 󱙺 X " — no count prefix to keep alignment consistent
    echo "${prefix}${ICON_CLAUDE} $(state_color "$state")${icon}${C_RESET} "
    ;;
  short)
    echo "${prefix}${count} ${ICON_CLAUDE} ${icon}"
    ;;
  long)
    local parts=()
    [[ $count_processing -eq 0 ]] || parts+=("$count_processing processing")
    [[ $count_waiting -eq 0 ]] || parts+=("$count_waiting waiting")
    [[ $count_compacting -eq 0 ]] || parts+=("$count_compacting compacting")
    [[ $count_done -eq 0 ]] || parts+=("$count_done done")
    [[ $count_idle -eq 0 ]] || parts+=("$count_idle idle")
    local IFS=", "
    echo "${parts[*]}"
    ;;
  gum)
    local gum_color label
    case "$state" in
    waiting)
      gum_color=216
      label="$count_waiting waiting"
      ;;
    compacting)
      gum_color=117
      label="$count_compacting compacting"
      ;;
    processing)
      gum_color=183
      label="$count_processing working"
      ;;
    done)
      gum_color=151
      label="$count_done done"
      ;;
    idle)
      gum_color=245
      label="$count_idle idle"
      ;;
    esac
    gum style --foreground "$gum_color" "$icon $label" 2>/dev/null || echo "$icon $label"
    ;;
  esac
}

# --- Main ---

mode="" target="" format=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  --pane)
    mode="pane"
    target="$2"
    shift 2
    ;;
  --session)
    mode="session"
    target="$2"
    shift 2
    ;;
  --window)
    mode="window"
    target="$2"
    shift 2
    ;;
  --format)
    format="$2"
    shift 2
    ;;
  --no-color)
    format="icon"
    shift
    ;;
  *) shift ;;
  esac
done

[[ -n $mode && -n $target ]] || exit 0
[[ -n $format ]] || format="icon-color"

case "$mode" in
pane)
  state=$(read_pane_state "$target") || exit 0
  tally_state "$state"
  format_output "$state" 1 "$format" "true"
  ;;
window)
  count_for_window "$target"
  if [[ $total -eq 0 ]]; then
    # Fixed-width blank: match visible width of " 󱙺 X " (5 display cols)
    # space(1) + claude_icon(1) + space(1) + state_icon(1) + space(1) = 5
    echo "     "
    exit 0
  fi
  format_output "$(priority_state)" "$total" "$format" "true"
  ;;
session)
  count_for_session "$target"
  [[ $total -gt 0 ]] || exit 0
  format_output "$(priority_state)" "$total" "$format" "false"
  ;;
esac
