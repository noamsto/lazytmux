package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// detectTheme reads $XDG_STATE_HOME/theme-state.json (same contract as the
// shell scripts and the picker), defaulting to "dark" when absent/unparseable.
func detectTheme() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		xdg = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	data, err := os.ReadFile(filepath.Join(xdg, "theme-state.json"))
	if err != nil {
		return "dark"
	}
	var cfg struct {
		Theme string `json:"theme"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.Theme == "" {
		return "dark"
	}
	return cfg.Theme
}
