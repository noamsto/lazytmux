package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noamsto/tmux-og/generator/initcfg"
)

func TestWritesDefaultPath(t *testing.T) {
	out := filepath.Join(t.TempDir(), "config.toml")
	if err := run([]string{"--out", out}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := initcfg.Render()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("config.toml = %q, want %q", got, want)
	}
}

func TestRefusesExistingFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "config.toml")
	const placeholder = "hand-edited\n"
	if err := os.WriteFile(out, []byte(placeholder), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"--out", out})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %q, want it to mention --force", err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != placeholder {
		t.Fatalf("file content = %q, want unchanged %q", got, placeholder)
	}
}

func TestForceOverwrites(t *testing.T) {
	out := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(out, []byte("hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"--out", out, "--force"}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := initcfg.Render()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("config.toml = %q, want %q", got, want)
	}
}

func TestMkdirAllForParent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nested", "dir", "config.toml")
	if err := run([]string{"--out", out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

// TestExplicitOutSkipsDefaultPath: an explicit --out must not require
// config.DefaultPath() to succeed, since og-init exists precisely for
// minimal environments where $HOME may be unset.
func TestExplicitOutSkipsDefaultPath(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	out := filepath.Join(t.TempDir(), "config.toml")
	if err := run([]string{"--out", out}); err != nil {
		t.Fatalf("run with explicit --out should not need $HOME: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}
