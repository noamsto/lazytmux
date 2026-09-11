package render

import (
	"strings"
	"testing"

	"github.com/noamsto/tmux-og/generator/config"
	"github.com/noamsto/tmux-og/generator/paths"
)

// The trailing "\n    " is invisible in a diff and is what indents the line the
// value is interpolated ahead of, so it gets its own assertion.
func TestDefaultShellConfig(t *testing.T) {
	if got := defaultShellConfig(nil); got != "" {
		t.Fatalf("no shell = %q, want empty", got)
	}
	shell := "/run/current-system/sw/bin/fish"
	want := "set -g default-shell /run/current-system/sw/bin/fish\n    "
	if got := defaultShellConfig(&shell); got != want {
		t.Fatalf("shell = %q, want %q", got, want)
	}
}

func TestTerminalConfig(t *testing.T) {
	term := "xterm-ghostty"
	tests := []struct {
		name   string
		term   *string
		sixels []string
		want   string
	}{
		{"neither", nil, nil, ""},
		{"term only", &term, nil, "set -as terminal-features 'xterm-ghostty*:RGB:extkeys'\n    "},
		{
			"sixels only", nil, []string{"foot", "wezterm"},
			"set -as terminal-features 'foot*:sixel'\n    " +
				"set -as terminal-features 'wezterm*:sixel'\n    ",
		},
		{
			"both", &term, []string{"foot"},
			"set -as terminal-features 'xterm-ghostty*:RGB:extkeys'\n    " +
				"set -as terminal-features 'foot*:sixel'\n    ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terminalConfig(tt.term, tt.sixels); got != tt.want {
				t.Fatalf("terminalConfig = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPluginConfigsEndsWithBlankLine(t *testing.T) {
	if !strings.HasSuffix(pluginConfigs, "set -g @catppuccin_pane_border_status 'off'\n\n") {
		t.Fatalf("pluginConfigs tail = %q", pluginConfigs[len(pluginConfigs)-60:])
	}
}

func TestPluginRunShells(t *testing.T) {
	p := &paths.Paths{
		Bash: "/store/bash",
		Plugins: map[string]string{
			"catppuccin":         "/store/catppuccin.tmux",
			"better-mouse-mode":  "/store/mouse.tmux",
			"vim-tmux-navigator": "/store/nav.tmux",
			"tmux-fzf":           "/store/fzf.tmux",
		},
	}
	want := "run-shell \"/store/bash /store/catppuccin.tmux\"\n" +
		"run-shell /store/mouse.tmux\n" +
		"run-shell /store/nav.tmux\n" +
		"run-shell /store/fzf.tmux\n"
	if got := pluginRunShells(p); got != want {
		t.Fatalf("pluginRunShells = %q, want %q", got, want)
	}
}

func TestFocusFollowsMouse(t *testing.T) {
	on := Build(&config.Config{Tmux: config.Tmux{FocusFollowsMouse: true}}, &paths.Paths{})
	off := Build(&config.Config{}, &paths.Paths{})
	if on.FocusFollowsMouse != "on" || off.FocusFollowsMouse != "off" {
		t.Fatalf("FocusFollowsMouse = %q / %q", on.FocusFollowsMouse, off.FocusFollowsMouse)
	}
}
