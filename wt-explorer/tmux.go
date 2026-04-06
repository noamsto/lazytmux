package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// killTmuxWindow finds and kills the tmux window associated with a worktree path.
// Matches by @worktree window option first, then falls back to pane_current_path.
// No-op if tmux is not running or window not found.
func killTmuxWindow(repoRoot, worktreePath string) {
	sessionName := filepath.Base(repoRoot)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() != nil {
		return
	}

	// Try matching by @worktree option
	if idx := findWindowByFormat(sessionName, "#{window_index}\t#{@worktree}", worktreePath); idx != "" {
		_ = exec.Command("tmux", "kill-window", "-t", sessionName+":"+idx).Run()
		return
	}

	// Fallback: match by pane working directory
	if idx := findWindowByFormat(sessionName, "#{window_index}\t#{pane_current_path}", worktreePath); idx != "" {
		_ = exec.Command("tmux", "kill-window", "-t", sessionName+":"+idx).Run()
	}
}

func findWindowByFormat(session, format, matchPath string) string {
	out, err := exec.Command("tmux", "list-windows", "-t", session, "-F", format).Output()
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		idx, path, ok := strings.Cut(line, "\t")
		if ok && path == matchPath {
			return idx
		}
	}
	return ""
}
