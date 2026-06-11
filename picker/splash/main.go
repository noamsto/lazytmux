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
	m := newModel(detectTheme(), splashTips, splashPrefix, timeout)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		os.Exit(1)
	}
}
