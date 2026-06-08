package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// maxZoxideSuggestions caps the suggestions section below the session list.
const maxZoxideSuggestions = 15

var sessionNameReplacer = strings.NewReplacer(".", "_", ":", "_")

// suggestion is a zoxide directory offered for session creation.
type suggestion struct {
	path string // normalized absolute dir path
	name string // derived tmux session name
}

// sessionNameFromPath derives a tmux-safe session name from a directory path:
// basename with '.' and ':' replaced (tmux forbids them in session names).
func sessionNameFromPath(p string) string {
	base := filepath.Base(filepath.Clean(p))
	if base == "/" || base == "." {
		return ""
	}
	return sessionNameReplacer.Replace(base)
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

// sessionFilterMaps builds the path/name suppression sets from real sessions.
// Scratch sessions are hidden helpers; they must not suppress the suggestion
// for the dir they happen to live in.
func sessionFilterMaps(sessions []sessionData) (paths, names map[string]bool) {
	paths = make(map[string]bool, len(sessions))
	names = make(map[string]bool, len(sessions))
	for _, s := range sessions {
		if strings.HasPrefix(s.name, "scratch-") {
			continue
		}
		paths[normalizePath(s.path)] = true
		names[s.name] = true
	}
	return paths, names
}

// isExcluded reports whether path matches any blacklist pattern. A pattern
// matches when it equals the path, is an ancestor dir of it (subtree exclude),
// or globs the full path or basename via filepath.Match. So "/tmp/*" drops
// /tmp children, ".ssh" drops any dir named .ssh, and "/home/x/Downloads"
// drops that dir and everything under it. Malformed globs are skipped.
func isExcluded(path string, patterns []string) bool {
	base := filepath.Base(path)
	for _, pat := range patterns {
		if pat == path || strings.HasPrefix(path, pat+"/") {
			return true
		}
		if ok, err := filepath.Match(pat, path); err == nil && ok {
			return true
		}
		if ok, err := filepath.Match(pat, base); err == nil && ok {
			return true
		}
	}
	return false
}

// parseExcludePatterns splits a comma-separated blacklist option into trimmed,
// non-empty patterns.
func parseExcludePatterns(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// collectZoxide returns ranked zoxide dirs not already covered by a session
// and not matching an exclude pattern. Missing zoxide binary, errors, or dead
// dirs degrade to no suggestions.
func collectZoxide(sessions []sessionData, exclude []string) []suggestion {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zoxide", "query", "-l").Output()
	if err != nil {
		return nil
	}
	sessionPaths, sessionNames := sessionFilterMaps(sessions)
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		p := normalizePath(l)
		if isExcluded(p, exclude) {
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		paths = append(paths, p)
	}
	return zoxideSuggestions(paths, sessionPaths, sessionNames, maxZoxideSuggestions)
}

// createAndSwitch creates a detached session at path (unless name already
// exists) and switches the attached client to it. zoxide add keeps the dir's
// rank fresh: the new session's shell never cd's, so zoxide never sees it.
func createAndSwitch(name, path string) error {
	if exec.Command("tmux", "has-session", "-t", "="+name).Run() != nil {
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", path).CombinedOutput(); err != nil {
			return fmt.Errorf("new-session: %s", strings.TrimSpace(string(out)))
		}
	}
	exec.Command("tmux", "switch-client", "-t", "="+name).Run() //nolint:errcheck
	exec.Command("zoxide", "add", path).Run()                   //nolint:errcheck
	return nil
}

// listDir renders a directory listing for the preview pane, preferring eza.
func listDir(path string) string {
	if eza, err := exec.LookPath("eza"); err == nil {
		if out, err := exec.Command(eza, "-la", "--color=always", "--group-directories-first", path).Output(); err == nil {
			return strings.TrimRight(string(out), "\n ")
		}
	}
	out, err := exec.Command("ls", "-la", path).Output()
	if err != nil {
		return "(no preview available)"
	}
	return strings.TrimRight(string(out), "\n ")
}
