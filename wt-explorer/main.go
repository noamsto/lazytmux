package main

import (
	"fmt"
	"os"
	"sort"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: wt-explorer <repo-root>\n")
		os.Exit(1)
	}
	repoRoot := os.Args[1]

	info, err := os.Stat(repoRoot)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "error: %s is not a valid directory\n", repoRoot)
		os.Exit(1)
	}

	defaultBranch, err := detectDefaultBranch(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Fetch and prune to get accurate remote state (non-fatal)
	_ = fetchPrune(repoRoot)

	worktrees, err := listWorktrees(repoRoot, defaultBranch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(worktrees) == 0 {
		fmt.Println("No worktrees found (besides default branch).")
		os.Exit(0)
	}

	detectStale(repoRoot, defaultBranch, worktrees)

	// Sort: stale first, then alphabetical by branch name
	sort.Slice(worktrees, func(i, j int) bool {
		si := worktrees[i].IsStale()
		sj := worktrees[j].IsStale()
		if si != sj {
			return si
		}
		return worktrees[i].Branch < worktrees[j].Branch
	})

	// Load details for the first item (will be initially selected in TUI)
	if len(worktrees) > 0 {
		loadWorktreeDetails(&worktrees[0])
	}

	if err := runTUI(repoRoot, worktrees); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
