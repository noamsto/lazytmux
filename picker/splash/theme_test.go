package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectThemeFromFixture(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "theme-state.json"), []byte(`{"theme":"light"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", dir)
	if got := detectTheme(); got != "light" {
		t.Errorf("detectTheme = %q, want light", got)
	}
}

func TestDetectThemeMissingFileDefaultsDark(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := detectTheme(); got != "dark" {
		t.Errorf("detectTheme = %q, want dark", got)
	}
}
