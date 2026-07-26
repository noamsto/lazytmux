package main

import (
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"
)

func main() {
	// --no-timeout: dismiss on keypress only (for on-demand launch, where the
	// auto-dismiss timeout of the fresh-session welcome would be wrong).
	timeout := splashTimeoutSec
	if slices.Contains(os.Args[1:], "--no-timeout") {
		timeout = 0
	}
	// --static: single already-resolved frame, no periodic redraw (for
	// bandwidth-light remote/SSH attaches).
	static := slices.Contains(os.Args[1:], "--static")
	m := newModel(detectTheme(), splashTips, splashPrefix, timeout, static)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		os.Exit(1)
	}
}
