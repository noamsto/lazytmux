package main

import (
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	m := newModel(detectTheme(), splashTips, splashPrefix, splashTimeoutSec)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		os.Exit(1)
	}
}
