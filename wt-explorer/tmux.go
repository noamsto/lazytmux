package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// killTmuxWindow finds and kills the tmux window associated with a worktree path.
// Matches by @worktree option first, then falls back to pane_current_path.
// No-op if tmux is not running or window not found.
func killTmuxWindow(repoRoot, worktreePath string) {
	sessionName := filepath.Base(repoRoot)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() != nil {
		return
	}

	// Single list-windows call with both match fields
	out, err := exec.Command("tmux", "list-windows", "-t", sessionName,
		"-F", "#{window_index}\t#{@worktree}\t#{pane_current_path}").Output()
	if err != nil {
		return
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[1] == worktreePath || parts[2] == worktreePath {
			_ = exec.Command("tmux", "kill-window", "-t", sessionName+":"+parts[0]).Run()
			return
		}
	}
}
