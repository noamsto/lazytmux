package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxZoxideSuggestions caps the suggestions section below the session list.
const maxZoxideSuggestions = 15

// suggestion is a zoxide directory offered for session creation.
type suggestion struct {
	path string // normalized absolute dir path
	name string // derived tmux session name
}

// sessionNameFromPath derives a tmux-safe session name from a directory path:
// basename with '.' and ':' replaced (tmux forbids them in session names).
func sessionNameFromPath(p string) string {
	base := filepath.Base(strings.TrimRight(p, "/"))
	if base == "/" || base == "." {
		return ""
	}
	return strings.NewReplacer(".", "_", ":", "_").Replace(base)
}

// normalizePath cleans and symlink-resolves a path for dedupe comparisons.
// Nonexistent paths keep the cleaned form.
func normalizePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// zoxideSuggestions filters rank-ordered, normalized zoxide paths against
// existing sessions (by path and by derived name) and cuts to limit.
// Duplicate derived names among suggestions keep only the higher-ranked dir.
func zoxideSuggestions(paths []string, sessionPaths, sessionNames map[string]bool, limit int) []suggestion {
	out := make([]suggestion, 0, limit)
	seen := make(map[string]bool)
	for _, p := range paths {
		if sessionPaths[p] {
			continue
		}
		name := sessionNameFromPath(p)
		if name == "" || sessionNames[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, suggestion{path: p, name: name})
		if len(out) == limit {
			break
		}
	}
	return out
}

// collectZoxide returns ranked zoxide dirs not already covered by a session.
// Missing zoxide binary, errors, or dead dirs degrade to no suggestions.
func collectZoxide(sessions []sessionData) []suggestion {
	out, err := exec.Command("zoxide", "query", "-l").Output()
	if err != nil {
		return nil
	}
	sessionPaths := make(map[string]bool, len(sessions))
	sessionNames := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		sessionPaths[normalizePath(s.path)] = true
		sessionNames[s.name] = true
	}
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		p := normalizePath(l)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		paths = append(paths, p)
	}
	return zoxideSuggestions(paths, sessionPaths, sessionNames, maxZoxideSuggestions)
}
