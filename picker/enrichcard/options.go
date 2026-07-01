package main

import (
	"os/exec"
	"strings"
)

// winState is the live per-window enrichment data, read from tmux window
// options. The card reflects these — it never re-derives issue/PR/claude data.
type winState struct {
	issueProvider, issueID, issueTitle, issueURL           string
	prNumber, prTitle, prState, prCheck, prURL, prMergeable string
	branch, worktree, gitRoot                              string
	task, claudeAgo, paneIcon                              string
}

// readWindowState runs one `tmux show-options -w -t <target>` and parses it.
// On any error (e.g. the window closed) it returns the zero winState; callers
// keep their last good state.
func readWindowState(target string) winState {
	var w winState
	out, err := exec.Command("tmux", "show-options", "-w", "-t", target).Output()
	if err != nil {
		return w
	}
	parseWindowOptions(string(out), &w)
	return w
}

// parseWindowOptions parses `show-options -w` lines (`@name value` or
// `@name "quoted value"`) into w; unknown options are ignored.
func parseWindowOptions(out string, w *winState) {
	for _, line := range strings.Split(out, "\n") {
		name, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		val = unquote(strings.TrimSpace(val))
		switch name {
		case "@issue_provider":
			w.issueProvider = val
		case "@issue_id":
			w.issueID = val
		case "@issue_title":
			w.issueTitle = val
		case "@issue_url":
			w.issueURL = val
		case "@pr_number":
			w.prNumber = val
		case "@pr_title":
			w.prTitle = val
		case "@pr_state":
			w.prState = val
		case "@pr_check_state":
			w.prCheck = val
		case "@pr_url":
			w.prURL = val
		case "@pr_mergeable":
			w.prMergeable = val
		case "@branch":
			w.branch = val
		case "@worktree":
			w.worktree = val
		case "@git_root":
			w.gitRoot = val
		case "@window_task":
			w.task = val
		case "@window_claude_ago":
			w.claudeAgo = val
		case "@active_pane_icon":
			w.paneIcon = val
		}
	}
}

// detectBaseBranch returns the repo's default branch (e.g. "main"/"master") via
// origin/HEAD, or "" if it can't be determined. Run once at launch — the base
// doesn't change during a popup's lifetime, so it stays out of the tick.
func detectBaseBranch(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
}

// unquote strips a matched surrounding quote pair. tmux show-options quotes
// values needing it with double quotes (e.g. spaces) and renders an empty value
// as ''. Both styles must be stripped, else a cleared option like `@branch ''`
// parses as the literal "''" and defeats the empty-value fallbacks/guards.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
