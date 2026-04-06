package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Worktree represents a git worktree with its metadata and stale detection info.
type Worktree struct {
	Branch      string
	Path        string
	StaleReason string
	DirtyFiles  int
	UnpushedLog []string
	LastCommit  string
}

// IsStale reports whether this worktree has been identified as stale.
func (w *Worktree) IsStale() bool {
	return w.StaleReason != ""
}

// detectDefaultBranch returns "main" or "master" based on which branch exists.
func detectDefaultBranch(repoRoot string) (string, error) {
	if exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/main").Run() == nil {
		return "main", nil
	}
	if exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/master").Run() == nil {
		return "master", nil
	}
	return "", fmt.Errorf("could not find main or master branch")
}

// listWorktrees parses `git worktree list --porcelain` and returns non-default worktrees.
func listWorktrees(repoRoot, defaultBranch string) ([]Worktree, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreesPorcelain(string(out), repoRoot, defaultBranch), nil
}

// parseWorktreesPorcelain parses the porcelain output of `git worktree list --porcelain`.
// It skips the main worktree (repoRoot) and the default branch.
func parseWorktreesPorcelain(output, repoRoot, defaultBranch string) []Worktree {
	var worktrees []Worktree
	var currentPath string

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "worktree "):
			currentPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			branch := strings.TrimPrefix(line, "branch refs/heads/")
			// Skip the main worktree directory and the default branch
			cleanPath := filepath.Clean(currentPath)
			cleanRoot := filepath.Clean(repoRoot)
			if cleanPath != cleanRoot && branch != defaultBranch {
				worktrees = append(worktrees, Worktree{
					Branch: branch,
					Path:   currentPath,
				})
			}
			currentPath = ""
		}
	}
	return worktrees
}

// detectStale marks worktrees as stale using three strategies:
// 1. git branch --merged (regular merges)
// 2. Remote branch deleted (after fetch --prune)
// 3. GitHub PR squash-merged via gh CLI (parallel, optional)
func detectStale(repoRoot, defaultBranch string, worktrees []Worktree) {
	checked := make(map[int]bool)

	// Strategy 1: git branch --merged
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--merged", defaultBranch).Output()
	if err == nil {
		merged := parseBranchList(string(out))
		for i := range worktrees {
			if merged[worktrees[i].Branch] {
				worktrees[i].StaleReason = "merged into " + defaultBranch
				checked[i] = true
			}
		}
	}

	// Strategy 2: remote branch deleted
	for i := range worktrees {
		if checked[i] {
			continue
		}
		ref := "refs/remotes/origin/" + worktrees[i].Branch
		err := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", ref).Run()
		if err != nil {
			worktrees[i].StaleReason = "remote branch deleted"
			checked[i] = true
		}
	}

	// Strategy 3: GitHub PR squash-merged (parallel via gh CLI)
	var unchecked []int
	for i := range worktrees {
		if !checked[i] {
			unchecked = append(unchecked, i)
		}
	}

	if len(unchecked) == 0 {
		return
	}

	// Only attempt if gh is available
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return
	}

	type result struct {
		index int
		prNum string
	}

	var (
		mu      sync.Mutex
		results []result
		wg      sync.WaitGroup
	)

	for _, idx := range unchecked {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			branch := worktrees[i].Branch
			cmd := exec.Command(ghPath, "pr", "list",
				"--head", branch,
				"--state", "merged",
				"--json", "number",
				"--jq", ".[0].number",
			)
			cmd.Dir = repoRoot
			out, err := cmd.Output()
			if err != nil {
				return
			}
			prNum := strings.TrimSpace(string(out))
			if prNum != "" {
				mu.Lock()
				results = append(results, result{index: i, prNum: prNum})
				mu.Unlock()
			}
		}(idx)
	}
	wg.Wait()

	for _, r := range results {
		worktrees[r.index].StaleReason = "PR #" + r.prNum + " squash-merged"
	}
}

// parseBranchList parses the output of `git branch` (or `git branch --merged`)
// into a set of branch names. It handles *, +, and space prefixes.
func parseBranchList(output string) map[string]bool {
	branches := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 3 {
			continue
		}
		// First two chars are prefix (e.g., "* ", "+ ", "  ")
		name := strings.TrimSpace(line[2:])
		if name != "" {
			branches[name] = true
		}
	}
	return branches
}

// loadWorktreeDetails populates DirtyFiles, UnpushedLog, and LastCommit.
func loadWorktreeDetails(wt *Worktree) {
	// Dirty file count
	out, err := exec.Command("git", "-C", wt.Path, "status", "--porcelain").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			wt.DirtyFiles = 0
		} else {
			wt.DirtyFiles = len(lines)
		}
	}

	// Unpushed commits
	out, err = exec.Command("git", "-C", wt.Path, "log", "--oneline", "@{upstream}..HEAD").Output()
	if err == nil {
		text := strings.TrimSpace(string(out))
		if text != "" {
			wt.UnpushedLog = strings.Split(text, "\n")
		}
	}

	// Last commit
	out, err = exec.Command("git", "-C", wt.Path, "log", "-1",
		"--format=%h %s (%cr)", "--date=relative").Output()
	if err == nil {
		wt.LastCommit = strings.TrimSpace(string(out))
	}
}

// removeWorktree removes a git worktree, optionally with --force.
func removeWorktree(repoRoot, path string, force bool) error {
	args := []string{"-C", repoRoot, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// fetchPrune runs git fetch --prune to update remote tracking state.
func fetchPrune(repoRoot string) error {
	cmd := exec.Command("git", "-C", repoRoot, "fetch", "--prune")
	cmd.Stdout = nil
	cmd.Stderr = nil
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("git fetch --prune timed out")
	}
}
