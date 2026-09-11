package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestNoRemoteHostsConfigured(t *testing.T) {
	if got := splitHosts(""); len(got) != 0 {
		t.Fatalf("splitHosts(\"\") = %v, want empty", got)
	}
	if got := splitHosts("   "); len(got) != 0 {
		t.Fatalf("splitHosts(whitespace) = %v, want empty", got)
	}
	if got := splitHosts("halo  laptop"); len(got) != 2 {
		t.Fatalf("splitHosts(two hosts) = %v, want 2 entries", got)
	}
}

// TestExplicitConfigSkipsDefaultPath: an explicit --config must not require
// config.DefaultPath() to succeed, since og-doctor exists precisely for
// minimal environments where $HOME may be unset.
func TestExplicitConfigSkipsDefaultPath(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// code may be 1 if the local machine is missing checklist tools (that's
	// an unrelated, environment-dependent local-check result) — the fix
	// under test is that no *error* comes back from a missing $HOME.
	var stdout bytes.Buffer
	if _, err := run([]string{"--config", cfgPath}, &stdout); err != nil {
		t.Fatalf("run with explicit --config should not need $HOME: %v", err)
	}
}
