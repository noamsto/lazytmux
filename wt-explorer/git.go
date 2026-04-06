package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Worktree represents a git worktree with its metadata and stale detection info.
type Worktree struct {
	Branch        string
	Path          string
	StaleReason   string
	DirtyFiles    int
	UnpushedLog   []string
	LastCommit    string
	DetailsLoaded bool
}

func (w *Worktree) IsStale() bool {
	return w.StaleReason != ""
}

func detectDefaultBranch(repoRoot string) (string, error) {
	for _, branch := range []string{"main", "master"} {
		if exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
			return branch, nil
		}
	}
	return "", fmt.Errorf("could not find main or master branch")
}

func listWorktrees(repoRoot, defaultBranch string) ([]Worktree, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreesPorcelain(string(out), repoRoot, defaultBranch), nil
}

func parseWorktreesPorcelain(output, repoRoot, defaultBranch string) []Worktree {
	var worktrees []Worktree
	var currentPath string

	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			currentPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			branch := strings.TrimPrefix(line, "branch refs/heads/")
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

// detectStale marks worktrees as stale using three strategies, ordered cheapest first:
// merged branches (local), deleted remote branches (local refs), GitHub PR state (network).
func detectStale(repoRoot, defaultBranch string, worktrees []Worktree) {
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--merged", defaultBranch).Output()
	if err == nil {
		merged := parseBranchList(string(out))
		for i := range worktrees {
			if merged[worktrees[i].Branch] {
				worktrees[i].StaleReason = "merged into " + defaultBranch
			}
		}
	}

	for i := range worktrees {
		if worktrees[i].IsStale() {
			continue
		}
		ref := "refs/remotes/origin/" + worktrees[i].Branch
		if exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", ref).Run() != nil {
			worktrees[i].StaleReason = "remote branch deleted"
		}
	}

	var unchecked []int
	for i := range worktrees {
		if !worktrees[i].IsStale() {
			unchecked = append(unchecked, i)
		}
	}
	if len(unchecked) == 0 {
		return
	}

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
			cmd := exec.Command(ghPath, "pr", "list",
				"--head", worktrees[i].Branch,
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
		worktrees[r.index].StaleReason = "PR #" + r.prNum + " merged"
	}
}

func parseBranchList(output string) map[string]bool {
	branches := make(map[string]bool)
	for line := range strings.SplitSeq(output, "\n") {
		if len(line) < 3 {
			continue
		}
		name := strings.TrimSpace(line[2:])
		if name != "" {
			branches[name] = true
		}
	}
	return branches
}

func loadWorktreeDetails(wt *Worktree) {
	out, err := exec.Command("git", "-C", wt.Path, "status", "--porcelain").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			wt.DirtyFiles = 0
		} else {
			wt.DirtyFiles = len(lines)
		}
	}

	out, err = exec.Command("git", "-C", wt.Path, "log", "--oneline", "@{upstream}..HEAD").Output()
	if err == nil {
		text := strings.TrimSpace(string(out))
		if text != "" {
			wt.UnpushedLog = strings.Split(text, "\n")
		}
	}

	out, err = exec.Command("git", "-C", wt.Path, "log", "-1",
		"--format=%h %s (%cr)", "--date=relative").Output()
	if err == nil {
		wt.LastCommit = strings.TrimSpace(string(out))
	}

	wt.DetailsLoaded = true
}

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

func fetchPrune(repoRoot string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "fetch", "--prune")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
