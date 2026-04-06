package main

import (
	"testing"
)

func TestParseWorktreesPorcelain(t *testing.T) {
	t.Run("normal multi-worktree output", func(t *testing.T) {
		output := `worktree /home/user/repo
HEAD abc1234567890
branch refs/heads/main
bare

worktree /home/user/repo/.worktrees/feature-a
HEAD def4567890123
branch refs/heads/feature-a

worktree /home/user/repo/.worktrees/feature-b
HEAD 789abc0123456
branch refs/heads/feature-b

`
		wts := parseWorktreesPorcelain(output, "/home/user/repo", "main")
		if len(wts) != 2 {
			t.Fatalf("expected 2 worktrees, got %d", len(wts))
		}
		if wts[0].Branch != "feature-a" {
			t.Errorf("expected branch feature-a, got %s", wts[0].Branch)
		}
		if wts[0].Path != "/home/user/repo/.worktrees/feature-a" {
			t.Errorf("expected path .worktrees/feature-a, got %s", wts[0].Path)
		}
		if wts[1].Branch != "feature-b" {
			t.Errorf("expected branch feature-b, got %s", wts[1].Branch)
		}
	})

	t.Run("no trailing newline", func(t *testing.T) {
		output := "worktree /repo\nHEAD abc123\nbranch refs/heads/main\n\nworktree /repo/.worktrees/fix\nHEAD def456\nbranch refs/heads/fix"
		wts := parseWorktreesPorcelain(output, "/repo", "main")
		if len(wts) != 1 {
			t.Fatalf("expected 1 worktree, got %d", len(wts))
		}
		if wts[0].Branch != "fix" {
			t.Errorf("expected branch fix, got %s", wts[0].Branch)
		}
	})

	t.Run("skips default branch", func(t *testing.T) {
		output := `worktree /repo
HEAD abc123
branch refs/heads/main

worktree /repo/.worktrees/main
HEAD abc123
branch refs/heads/main

worktree /repo/.worktrees/feature
HEAD def456
branch refs/heads/feature
`
		wts := parseWorktreesPorcelain(output, "/repo", "main")
		if len(wts) != 1 {
			t.Fatalf("expected 1 worktree (only feature), got %d", len(wts))
		}
		if wts[0].Branch != "feature" {
			t.Errorf("expected branch feature, got %s", wts[0].Branch)
		}
	})

	t.Run("skips detached HEAD", func(t *testing.T) {
		output := `worktree /repo
HEAD abc123
branch refs/heads/main

worktree /repo/.worktrees/detached
HEAD def456
detached

worktree /repo/.worktrees/feature
HEAD ghi789
branch refs/heads/feature
`
		wts := parseWorktreesPorcelain(output, "/repo", "main")
		if len(wts) != 1 {
			t.Fatalf("expected 1 worktree, got %d", len(wts))
		}
		if wts[0].Branch != "feature" {
			t.Errorf("expected branch feature, got %s", wts[0].Branch)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		wts := parseWorktreesPorcelain("", "/repo", "main")
		if len(wts) != 0 {
			t.Fatalf("expected 0 worktrees, got %d", len(wts))
		}
	})
}

func TestParseBranchList(t *testing.T) {
	t.Run("mixed prefixes", func(t *testing.T) {
		output := "* main\n  feature-a\n+ feature-b\n  feature-c\n"
		branches := parseBranchList(output)
		expected := []string{"main", "feature-a", "feature-b", "feature-c"}
		for _, b := range expected {
			if !branches[b] {
				t.Errorf("expected branch %q to be present", b)
			}
		}
		if len(branches) != len(expected) {
			t.Errorf("expected %d branches, got %d", len(expected), len(branches))
		}
	})

	t.Run("empty output", func(t *testing.T) {
		branches := parseBranchList("")
		if len(branches) != 0 {
			t.Errorf("expected 0 branches, got %d", len(branches))
		}
	})

	t.Run("trailing newline only", func(t *testing.T) {
		branches := parseBranchList("\n")
		if len(branches) != 0 {
			t.Errorf("expected 0 branches, got %d", len(branches))
		}
	})
}

func TestWorktreeIsStale(t *testing.T) {
	wt := Worktree{Branch: "feature", Path: "/repo/.worktrees/feature"}
	if wt.IsStale() {
		t.Error("expected not stale initially")
	}
	wt.StaleReason = "merged into main"
	if !wt.IsStale() {
		t.Error("expected stale after setting reason")
	}
}
