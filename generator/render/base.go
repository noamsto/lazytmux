package render

import (
	"fmt"
	"strings"

	"github.com/noamsto/lazytmux/generator/paths"
)

// pluginConfigs is the catppuccin option block, which tmux must see before the
// plugin is sourced. The blank line it ends with is part of the value, not
// decoration: the template line that emits it contributes a newline of its own,
// and both are needed.
const pluginConfigs = `# catppuccin theme
# Detect theme from state file on first load (theme-toggle sets flavor before re-source)
# The x-prefix keeps @catppuccin_flavor non-word-initial and safe when empty.
if-shell '[ x#{q:@catppuccin_flavor} = x ]' \
  'if-shell "grep -q light \"$HOME/.local/state/theme-state.json\" 2>/dev/null" \
    "set -g @catppuccin_flavor latte" \
    "set -g @catppuccin_flavor mocha"'
set -g @catppuccin_status_background 'none'
set -g @catppuccin_window_status_style 'none'
set -g @catppuccin_window_flags 'icon'
set -g @catppuccin_window_flags_icon_last " 󰖰"
set -g @catppuccin_window_flags_icon_current " 󰖯"
set -g @catppuccin_window_flags_icon_zoom " 󰁌"
set -g @catppuccin_window_flags_icon_mark " 󰃀"
set -g @catppuccin_window_flags_icon_silent " 󰂛"
set -g @catppuccin_window_flags_icon_activity " 󱅫"
set -g @catppuccin_window_flags_icon_bell " 󰂞"
set -g @catppuccin_pane_status_enabled 'off'
set -g @catppuccin_pane_border_status 'off'

`

// pluginRunShells sources the plugins. Order matters: theme first, then others.
func pluginRunShells(p *paths.Paths) string {
	return fmt.Sprintf("run-shell \"%s %s\"\nrun-shell %s\nrun-shell %s\nrun-shell %s\n",
		p.Bash,
		p.Plugins["catppuccin"],
		p.Plugins["better-mouse-mode"],
		p.Plugins["vim-tmux-navigator"],
		p.Plugins["tmux-fzf"])
}

// defaultShellConfig emits the default-shell line, or nothing when no shell is
// configured. The trailing newline-plus-four-spaces is a byte contract, not a
// typo: the value is interpolated mid-line, so whenever it is non-empty the
// history-limit line that follows it comes out four-space indented.
func defaultShellConfig(shell *string) string {
	if shell == nil {
		return ""
	}
	return "set -g default-shell " + *shell + "\n    "
}

// terminalConfig derives the terminal-features lines from the outer terminal's
// TERM string. The wildcard suffix matches version variants (e.g.
// "xterm-ghostty-1.2"). Every line carries defaultShellConfig's trailing
// indent, for the same reason.
func terminalConfig(term *string, sixelTerminals []string) string {
	var b strings.Builder
	if term != nil {
		fmt.Fprintf(&b, "set -as terminal-features '%s*:RGB:extkeys'\n    ", *term)
	}
	for _, t := range sixelTerminals {
		fmt.Fprintf(&b, "set -as terminal-features '%s*:sixel'\n    ", t)
	}
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
