# wt Go Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the `wt` git worktree manager as a standalone Go binary in a new repo at `~/Data/git/noamsto/wt`, absorbing the bash script and wt-explorer TUI into one tool.

**Architecture:** Single Go module with `cmd/wt/main.go` entry point and `internal/` packages for git operations, optional integrations (tmux/zoxide/gh), and TUI components (explorer + prompts). External tools are called via `exec.Command` — no go-git.

**Tech Stack:** Go 1.25+, Bubble Tea v2, Bubbles v2, Lipgloss v2, Huh v2

**Spec:** `docs/superpowers/specs/2026-04-06-wt-go-rewrite-design.md` (in lazytmux repo)

**Reference code:** The current implementation lives in lazytmux:
- `wt/wt.sh` — bash script being replaced
- `wt-explorer/` — Go Bubble Tea TUI being absorbed (git.go, tui.go, tmux.go, main.go)
- `modules/home-manager.nix` — fish completions at lines 125-190

---

## File Structure

```
~/Data/git/noamsto/wt/
├── cmd/wt/main.go                  — CLI entry point, flag parsing, command dispatch
├── internal/
│   ├── git/
│   │   ├── repo.go                 — repo root detection, default branch detection
│   │   ├── worktree.go             — worktree CRUD (list, create, remove, prune)
│   │   ├── branch.go               — branch existence checks, merged detection
│   │   ├── stale.go                — stale worktree detection (3 strategies)
│   │   ├── details.go              — worktree detail loading (dirty files, unpushed, last commit)
│   │   ├── worktree_test.go        — tests for worktree parsing
│   │   ├── branch_test.go          — tests for branch list parsing
│   │   └── stale_test.go           — tests for stale detection
│   ├── tmux/
│   │   └── tmux.go                 — session/window management, no-ops when unavailable
│   ├── zoxide/
│   │   └── zoxide.go               — add/remove paths, no-ops when unavailable
│   ├── gh/
│   │   └── gh.go                   — squash-merge PR detection, no-ops when unavailable
│   ├── runtime/
│   │   └── runtime.go              — runtime detection (which tools are available)
│   ├── tui/
│   │   ├── explorer/
│   │   │   └── explorer.go         — Bubble Tea TUI for wt clean -i (port of wt-explorer)
│   │   └── prompt/
│   │       └── prompt.go           — confirm/filter using huh v2
│   └── cmd/
│       ├── smart.go                — wt <branch> smart handler
│       ├── list.go                 — wt list
│       ├── remove.go               — wt remove <branch>
│       ├── clean.go                — wt clean / wt clean -i
│       ├── find.go                 — wt z [query]
│       └── main_switch.go          — wt main
├── completions/
│   └── wt.fish                     — fish shell completions
├── go.mod
├── go.sum
├── flake.nix                       — nix flake with buildGoModule + home-manager module
├── flake.lock
└── CLAUDE.md
```

---

## Task 1: Scaffold Repo and Go Module

**Files:**
- Create: `~/Data/git/noamsto/wt/go.mod`
- Create: `~/Data/git/noamsto/wt/cmd/wt/main.go`
- Create: `~/Data/git/noamsto/wt/CLAUDE.md`

- [ ] **Step 1: Create repo directory and initialize git**

```bash
mkdir -p ~/Data/git/noamsto/wt
cd ~/Data/git/noamsto/wt
git init
```

- [ ] **Step 2: Initialize Go module**

```bash
cd ~/Data/git/noamsto/wt
go mod init github.com/noamsto/wt
```

- [ ] **Step 3: Create minimal main.go with help text**

Create `cmd/wt/main.go`:

```go
package main

import (
	"fmt"
	"os"
	"strings"
)

const helpText = `Git Worktree Manager

Usage:
  wt <branch>           Smart switch/create (prompts before creating)
  wt -y <branch>        Skip prompts
  wt -q <branch>        Quiet mode (only output path)
  wt -n <branch>        No tmux (skip window creation/switching)
  wt -yqn <branch>      Combine flags (for Claude/scripts)
  wt z [query]          Fuzzy find worktree, output path (cd "$(wt z)")
  wt main               Switch to root repository window
  wt list               List all worktrees
  wt remove <branch>    Remove worktree + kill window
  wt clean              Remove stale worktrees (merged, squash-merged, deleted)
  wt clean -i           Interactive explorer: inspect worktrees, force-remove
  wt help               Show this help

Model: Session = Project, Window = Worktree

Smart mode:
  Worktree exists     → switch to window (unless -n)
  Branch exists       → prompt to create worktree
  Branch not found    → prompt to create new branch

Worktree location: .worktrees/<branch-name>`

type flags struct {
	yes         bool
	quiet       bool
	noSwitch    bool
	interactive bool
}

func parseArgs(args []string) (flags, []string) {
	var f flags
	var rest []string

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && arg != "-" {
			chars := arg[1:]
			allShort := true
			for _, c := range chars {
				switch c {
				case 'y':
					f.yes = true
				case 'q':
					f.quiet = true
				case 'n':
					f.noSwitch = true
				case 'i':
					f.interactive = true
				default:
					allShort = false
				}
			}
			if !allShort {
				rest = append(rest, arg)
			}
			continue
		}

		switch arg {
		case "--yes":
			f.yes = true
		case "--quiet":
			f.quiet = true
		case "--no-switch":
			f.noSwitch = true
		case "--interactive":
			f.interactive = true
		default:
			rest = append(rest, arg)
		}
	}

	return f, rest
}

func main() {
	f, args := parseArgs(os.Args[1:])
	_ = f

	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "help", "-h", "--help", "":
		fmt.Println(helpText)
	case "list", "ls":
		fmt.Fprintln(os.Stderr, "not implemented: list")
		os.Exit(1)
	case "remove", "rm":
		fmt.Fprintln(os.Stderr, "not implemented: remove")
		os.Exit(1)
	case "clean", "prune":
		fmt.Fprintln(os.Stderr, "not implemented: clean")
		os.Exit(1)
	case "z":
		fmt.Fprintln(os.Stderr, "not implemented: z")
		os.Exit(1)
	case "main":
		fmt.Fprintln(os.Stderr, "not implemented: main")
		os.Exit(1)
	default:
		// Smart mode: treat as branch name
		fmt.Fprintln(os.Stderr, "not implemented: smart")
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Create CLAUDE.md**

Create `CLAUDE.md`:

```markdown
# CLAUDE.md

## Project Overview

`wt` is a standalone git worktree manager. Single Go binary, no required deps beyond `git`. Optional auto-detected integrations: `tmux` (window management), `zoxide` (frecency tracking), `gh` (squash-merge detection).

## Build and Test

\`\`\`bash
go build ./cmd/wt          # Build binary
go test ./...              # Run all tests
nix build .                # Build via Nix
nix flake check            # Run flake checks
\`\`\`

## Architecture

- `cmd/wt/main.go` — CLI entry point, flag parsing, command dispatch
- `internal/git/` — all git operations via exec.Command (no go-git)
- `internal/tmux/` — tmux window/session management (no-op when unavailable)
- `internal/zoxide/` — zoxide add/remove (no-op when unavailable)
- `internal/gh/` — GitHub PR squash-merge detection (no-op when unavailable)
- `internal/runtime/` — detect which optional tools are available
- `internal/tui/explorer/` — Bubble Tea TUI for interactive worktree cleanup
- `internal/tui/prompt/` — confirm/filter prompts using huh v2
- `internal/cmd/` — command implementations

## Conventions

- Shell out to git/tmux/zoxide/gh via exec.Command
- Optional integrations auto-detect at startup and silently no-op when unavailable
- Tests use temp git repos — never touch the real filesystem
```

- [ ] **Step 5: Verify it compiles and runs**

```bash
cd ~/Data/git/noamsto/wt
go build ./cmd/wt
./wt help
./wt list  # should print "not implemented" and exit 1
```

- [ ] **Step 6: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add .
git commit -m "feat: scaffold wt Go project with CLI entry point and flag parsing"
```

---

## Task 2: Runtime Detection

**Files:**
- Create: `internal/runtime/runtime.go`

- [ ] **Step 1: Create the runtime package**

Create `internal/runtime/runtime.go`:

```go
package runtime

import (
	"os"
	"os/exec"
)

// Runtime holds which optional tools are available.
type Runtime struct {
	HasTmux   bool
	HasZoxide bool
	HasGh     bool
	InTmux    bool
	NoSwitch  bool
	Quiet     bool
	Yes       bool
}

// Detect probes the environment for available tools.
func Detect() Runtime {
	_, hasTmux := exec.LookPath("tmux")
	_, hasZoxide := exec.LookPath("zoxide")
	_, hasGh := exec.LookPath("gh")

	return Runtime{
		HasTmux:   hasTmux == nil,
		HasZoxide: hasZoxide == nil,
		HasGh:     hasGh == nil,
		InTmux:    os.Getenv("TMUX") != "",
	}
}

// TmuxActive returns true if tmux integration should be used.
func (r Runtime) TmuxActive() bool {
	return r.HasTmux && !r.NoSwitch && r.InTmux
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 3: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/runtime/
git commit -m "feat: add runtime detection for optional tools (tmux, zoxide, gh)"
```

---

## Task 3: Git Package — Repo and Branch Operations

**Files:**
- Create: `internal/git/repo.go`
- Create: `internal/git/branch.go`
- Create: `internal/git/branch_test.go`

- [ ] **Step 1: Write test for ParseBranchList**

Create `internal/git/branch_test.go`:

```go
package git

import "testing"

func TestParseBranchList(t *testing.T) {
	t.Run("mixed prefixes", func(t *testing.T) {
		output := "* main\n  feature-a\n+ feature-b\n  feature-c\n"
		branches := ParseBranchList(output)
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
		branches := ParseBranchList("")
		if len(branches) != 0 {
			t.Errorf("expected 0 branches, got %d", len(branches))
		}
	})

	t.Run("trailing newline only", func(t *testing.T) {
		branches := ParseBranchList("\n")
		if len(branches) != 0 {
			t.Errorf("expected 0 branches, got %d", len(branches))
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd ~/Data/git/noamsto/wt
go test ./internal/git/ -run TestParseBranchList -v
```

Expected: FAIL — `ParseBranchList` not defined.

- [ ] **Step 3: Write repo.go and branch.go**

Create `internal/git/repo.go`:

```go
package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoRoot finds the true repository root (works from worktrees too).
// It resolves via git-common-dir to always return the main repo root.
func RepoRoot() (string, error) {
	commonDir, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository")
	}
	cd := strings.TrimSpace(string(commonDir))

	if cd == ".git" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", fmt.Errorf("git show-toplevel: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}

	abs, err := filepath.Abs(cd)
	if err != nil {
		return "", err
	}
	// Remove /.git suffix to get repo root
	return strings.TrimSuffix(abs, "/.git"), nil
}

// DefaultBranch detects whether the repo uses "main" or "master".
func DefaultBranch(repoRoot string) (string, error) {
	for _, branch := range []string{"main", "master"} {
		if exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
			return branch, nil
		}
	}
	return "", fmt.Errorf("could not find main or master branch")
}
```

Create `internal/git/branch.go`:

```go
package git

import (
	"os/exec"
	"strings"
)

// ParseBranchList parses output from `git branch --merged` into a set of branch names.
func ParseBranchList(output string) map[string]bool {
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

// BranchExists checks if a branch exists locally.
func BranchExists(repoRoot, branch string) bool {
	return exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// RemoteBranchExists checks if a branch exists on origin.
func RemoteBranchExists(repoRoot, branch string) bool {
	return exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch).Run() == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd ~/Data/git/noamsto/wt
go test ./internal/git/ -run TestParseBranchList -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/git/
git commit -m "feat: add git repo root detection and branch operations"
```

---

## Task 4: Git Package — Worktree Operations

**Files:**
- Create: `internal/git/worktree.go`
- Create: `internal/git/worktree_test.go`

- [ ] **Step 1: Write tests for worktree parsing**

Create `internal/git/worktree_test.go`:

```go
package git

import "testing"

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
		wts := ParseWorktreesPorcelain(output, "/home/user/repo", "main")
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
		wts := ParseWorktreesPorcelain(output, "/repo", "main")
		if len(wts) != 1 {
			t.Fatalf("expected 1 worktree, got %d", len(wts))
		}
		if wts[0].Branch != "fix" {
			t.Errorf("expected branch fix, got %s", wts[0].Branch)
		}
	})

	t.Run("skips default branch worktrees", func(t *testing.T) {
		output := "worktree /repo\nHEAD abc123\nbranch refs/heads/main\n\nworktree /repo/.worktrees/main\nHEAD abc123\nbranch refs/heads/main\n\nworktree /repo/.worktrees/feature\nHEAD def456\nbranch refs/heads/feature\n"
		wts := ParseWorktreesPorcelain(output, "/repo", "main")
		if len(wts) != 1 {
			t.Fatalf("expected 1 worktree (only feature), got %d", len(wts))
		}
		if wts[0].Branch != "feature" {
			t.Errorf("expected branch feature, got %s", wts[0].Branch)
		}
	})

	t.Run("skips detached HEAD", func(t *testing.T) {
		output := "worktree /repo\nHEAD abc123\nbranch refs/heads/main\n\nworktree /repo/.worktrees/detached\nHEAD def456\ndetached\n\nworktree /repo/.worktrees/feature\nHEAD ghi789\nbranch refs/heads/feature\n"
		wts := ParseWorktreesPorcelain(output, "/repo", "main")
		if len(wts) != 1 {
			t.Fatalf("expected 1 worktree, got %d", len(wts))
		}
		if wts[0].Branch != "feature" {
			t.Errorf("expected branch feature, got %s", wts[0].Branch)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		wts := ParseWorktreesPorcelain("", "/repo", "main")
		if len(wts) != 0 {
			t.Fatalf("expected 0 worktrees, got %d", len(wts))
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd ~/Data/git/noamsto/wt
go test ./internal/git/ -run "TestParseWorktrees|TestWorktreeIsStale" -v
```

Expected: FAIL — types not defined.

- [ ] **Step 3: Write worktree.go**

Create `internal/git/worktree.go`:

```go
package git

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree represents a git worktree with its metadata.
type Worktree struct {
	Branch        string
	Path          string
	StaleReason   string
	DirtyFiles    int
	UnpushedLog   []string
	LastCommit    string
	DetailsLoaded bool
}

// IsStale returns true if the worktree has been marked stale.
func (w *Worktree) IsStale() bool {
	return w.StaleReason != ""
}

// ListWorktrees returns all worktrees excluding the repo root and default branch.
func ListWorktrees(repoRoot, defaultBranch string) ([]Worktree, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return ParseWorktreesPorcelain(string(out), repoRoot, defaultBranch), nil
}

// ParseWorktreesPorcelain parses `git worktree list --porcelain` output.
func ParseWorktreesPorcelain(output, repoRoot, defaultBranch string) []Worktree {
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

// ListWorktreesRaw returns raw `git worktree list` output (for `wt list` command).
func ListWorktreesRaw(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list").Output()
	if err != nil {
		return "", fmt.Errorf("git worktree list: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateWorktree creates a new worktree. If createBranch is true, creates a new branch.
// If trackRemote is true, tracks origin/<branch>.
func CreateWorktree(repoRoot, branch, path string, createBranch, trackRemote bool) error {
	var args []string
	switch {
	case createBranch:
		args = []string{"-C", repoRoot, "worktree", "add", "-b", branch, path}
	case trackRemote:
		args = []string{"-C", repoRoot, "worktree", "add", "--track", "-b", branch, path, "origin/" + branch}
	default:
		args = []string{"-C", repoRoot, "worktree", "add", path, branch}
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveWorktree removes a worktree by path. If force is true, uses --force.
func RemoveWorktree(repoRoot, path string, force bool) error {
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

// PruneWorktrees removes stale worktree references.
func PruneWorktrees(repoRoot string) error {
	return exec.Command("git", "-C", repoRoot, "worktree", "prune").Run()
}

// FetchPrune runs git fetch --prune with a 30s timeout.
func FetchPrune(repoRoot string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "fetch", "--prune")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// FindWorktreeByBranch returns the path of the worktree for the given branch, or empty string.
func FindWorktreeByBranch(repoRoot, branch string) string {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return ""
	}
	var currentPath string
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			currentPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			b := strings.TrimPrefix(line, "branch refs/heads/")
			if b == branch {
				return currentPath
			}
			currentPath = ""
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd ~/Data/git/noamsto/wt
go test ./internal/git/ -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/git/
git commit -m "feat: add worktree CRUD operations and parsing"
```

---

## Task 5: Git Package — Stale Detection and Details

**Files:**
- Create: `internal/git/stale.go`
- Create: `internal/git/details.go`
- Create: `internal/git/stale_test.go`

- [ ] **Step 1: Write stale detection test (for the merged-branch parsing, reusing ParseBranchList)**

The stale detection functions that call git are integration-tested later. The pure parsing is already tested in `branch_test.go`. Create a test for the DetectStale orchestration in a real temp repo.

Create `internal/git/stale_test.go`:

```go
package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectStale_MergedBranch(t *testing.T) {
	// Create a temp repo with a merged branch
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	// Init repo with a commit on main
	os.MkdirAll(repo, 0o755)
	run("git", "init", "-b", "main")
	run("git", "commit", "--allow-empty", "-m", "init")

	// Create and merge a feature branch
	run("git", "checkout", "-b", "feature-merged")
	run("git", "commit", "--allow-empty", "-m", "feature work")
	run("git", "checkout", "main")
	run("git", "merge", "feature-merged")

	// Create a worktree for the merged branch
	wtPath := filepath.Join(repo, ".worktrees", "feature-merged")
	run("git", "worktree", "add", wtPath, "feature-merged")

	// Create an unmerged branch worktree
	run("git", "checkout", "-b", "feature-active")
	run("git", "commit", "--allow-empty", "-m", "active work")
	run("git", "checkout", "main")
	wtPath2 := filepath.Join(repo, ".worktrees", "feature-active")
	run("git", "worktree", "add", wtPath2, "feature-active")

	worktrees := []Worktree{
		{Branch: "feature-merged", Path: wtPath},
		{Branch: "feature-active", Path: wtPath2},
	}

	DetectStale(repo, "main", worktrees)

	if !worktrees[0].IsStale() {
		t.Error("expected feature-merged to be stale")
	}
	if worktrees[0].StaleReason != "merged into main" {
		t.Errorf("expected stale reason 'merged into main', got %q", worktrees[0].StaleReason)
	}
	if worktrees[1].IsStale() {
		t.Error("expected feature-active to not be stale")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd ~/Data/git/noamsto/wt
go test ./internal/git/ -run TestDetectStale -v
```

Expected: FAIL — `DetectStale` not defined.

- [ ] **Step 3: Write stale.go**

Create `internal/git/stale.go`:

```go
package git

import (
	"os/exec"
	"strings"
	"sync"
)

// DetectStale marks worktrees as stale using three strategies, ordered cheapest first:
// merged branches (local), deleted remote branches (local refs), GitHub PR state (network).
// The gh strategy is handled separately via DetectStaleGh so gh can be optional.
func DetectStale(repoRoot, defaultBranch string, worktrees []Worktree) {
	// Strategy 1: git branch --merged
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--merged", defaultBranch).Output()
	if err == nil {
		merged := ParseBranchList(string(out))
		for i := range worktrees {
			if merged[worktrees[i].Branch] {
				worktrees[i].StaleReason = "merged into " + defaultBranch
			}
		}
	}

	// Strategy 2: remote branch deleted
	for i := range worktrees {
		if worktrees[i].IsStale() {
			continue
		}
		ref := "refs/remotes/origin/" + worktrees[i].Branch
		if exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", ref).Run() != nil {
			worktrees[i].StaleReason = "remote branch deleted"
		}
	}
}

// DetectStaleGh checks unchecked worktrees against GitHub PRs for squash-merges.
// This runs gh in parallel for each unchecked branch.
func DetectStaleGh(repoRoot string, worktrees []Worktree) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return
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
```

- [ ] **Step 4: Write details.go**

Create `internal/git/details.go`:

```go
package git

import (
	"os/exec"
	"strings"
)

// LoadDetails populates a worktree's detail fields (dirty files, unpushed commits, last commit).
func LoadDetails(wt *Worktree) {
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
```

- [ ] **Step 5: Run all tests**

```bash
cd ~/Data/git/noamsto/wt
go test ./internal/git/ -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/git/
git commit -m "feat: add stale worktree detection (merged, remote-deleted, gh) and detail loading"
```

---

## Task 6: Optional Integrations — tmux, zoxide, gh packages

**Files:**
- Create: `internal/tmux/tmux.go`
- Create: `internal/zoxide/zoxide.go`
- Create: `internal/gh/gh.go`

- [ ] **Step 1: Write tmux package**

Create `internal/tmux/tmux.go`:

```go
package tmux

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Client manages tmux operations. All methods are no-ops if tmux is unavailable.
type Client struct {
	active bool
}

// New creates a Client. If active is false, all operations are no-ops.
func New(active bool) *Client {
	return &Client{active: active}
}

// FindWindowByWorktree returns the window index for a worktree path, or empty string.
func (c *Client) FindWindowByWorktree(session, worktreePath string) string {
	if !c.active {
		return ""
	}

	out, err := exec.Command("tmux", "list-windows", "-t", session,
		"-F", "#{window_index}\t#{@worktree}\t#{pane_current_path}").Output()
	if err != nil {
		return ""
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[1] == worktreePath || parts[2] == worktreePath {
			return parts[0]
		}
	}
	return ""
}

// SwitchToWorktree handles the full tmux session/window lifecycle for a worktree.
// Returns true if a tmux operation was performed.
func (c *Client) SwitchToWorktree(repoRoot, branch, worktreePath string) bool {
	if !c.active {
		return false
	}

	repoName := filepath.Base(repoRoot)
	inTmux := isInsideTmux()

	if inTmux {
		return c.switchInsideTmux(repoName, branch, worktreePath)
	}
	return c.switchOutsideTmux(repoName, branch, worktreePath)
}

func (c *Client) switchInsideTmux(repoName, branch, worktreePath string) bool {
	currentSession := tmuxCmd("display-message", "-p", "#{session_name}")

	inRepoSession := currentSession == repoName || strings.HasPrefix(currentSession, repoName+"/")

	if inRepoSession {
		targetSession := currentSession
		windowIdx := c.FindWindowByWorktree(targetSession, worktreePath)
		if windowIdx != "" {
			_ = exec.Command("tmux", "select-window", "-t", targetSession+":"+windowIdx).Run()
		} else {
			_ = exec.Command("tmux", "new-window", "-a", "-t", targetSession, "-c", worktreePath).Run()
			c.setWindowOptions(targetSession, branch, worktreePath)
		}
		return true
	}

	// Different session — switch to repo session
	if !hasSession(repoName) {
		_ = exec.Command("tmux", "new-session", "-d", "-s", repoName, "-c", worktreePath).Run()
		c.setWindowOptions(repoName, branch, worktreePath)
	} else if c.FindWindowByWorktree(repoName, worktreePath) == "" {
		_ = exec.Command("tmux", "new-window", "-a", "-t", repoName, "-c", worktreePath).Run()
		c.setWindowOptions(repoName, branch, worktreePath)
	}

	windowIdx := c.FindWindowByWorktree(repoName, worktreePath)
	if windowIdx != "" {
		_ = exec.Command("tmux", "switch-client", "-t", repoName+":"+windowIdx).Run()
	} else {
		_ = exec.Command("tmux", "switch-client", "-t", repoName).Run()
	}
	return true
}

func (c *Client) switchOutsideTmux(repoName, branch, worktreePath string) bool {
	if !hasSession(repoName) {
		_ = exec.Command("tmux", "new-session", "-d", "-s", repoName, "-c", worktreePath).Run()
		c.setWindowOptions(repoName, branch, worktreePath)
	} else if c.FindWindowByWorktree(repoName, worktreePath) == "" {
		_ = exec.Command("tmux", "new-window", "-a", "-t", repoName, "-c", worktreePath).Run()
		c.setWindowOptions(repoName, branch, worktreePath)
	}

	windowIdx := c.FindWindowByWorktree(repoName, worktreePath)
	if windowIdx != "" {
		_ = exec.Command("tmux", "attach-session", "-t", repoName+":"+windowIdx).Run()
	} else {
		_ = exec.Command("tmux", "attach-session", "-t", repoName).Run()
	}
	return true
}

func (c *Client) setWindowOptions(session, branch, worktreePath string) {
	_ = exec.Command("tmux", "set-option", "-t", session, "-w", "@worktree", worktreePath).Run()
	_ = exec.Command("tmux", "set-option", "-t", session, "-w", "@branch", branch).Run()
}

// KillWindow kills the tmux window associated with a worktree path.
func (c *Client) KillWindow(repoRoot, worktreePath string) {
	if !c.active {
		return
	}
	sessionName := filepath.Base(repoRoot)
	if !hasSession(sessionName) {
		return
	}
	windowIdx := c.FindWindowByWorktree(sessionName, worktreePath)
	if windowIdx != "" {
		_ = exec.Command("tmux", "kill-window", "-t", sessionName+":"+windowIdx).Run()
	}
}

// UpdateWindowMetadata sets @worktree and @branch on the current window.
func (c *Client) UpdateWindowMetadata(worktreePath, branch string) {
	if !c.active {
		return
	}
	_ = exec.Command("tmux", "set-option", "-w", "@worktree", worktreePath).Run()
	_ = exec.Command("tmux", "set-option", "-w", "@branch", branch).Run()
}

func isInsideTmux() bool {
	return strings.TrimSpace(tmuxCmd("display-message", "-p", "#{pid}")) != ""
}

func hasSession(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

func tmuxCmd(args ...string) string {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

- [ ] **Step 2: Write zoxide package**

Create `internal/zoxide/zoxide.go`:

```go
package zoxide

import "os/exec"

// Client manages zoxide operations. All methods are no-ops if zoxide is unavailable.
type Client struct {
	active bool
}

// New creates a Client. If active is false, all operations are no-ops.
func New(active bool) *Client {
	return &Client{active: active}
}

// Add registers a path with zoxide.
func (c *Client) Add(path string) {
	if !c.active {
		return
	}
	_ = exec.Command("zoxide", "add", path).Run()
}

// Remove unregisters a path from zoxide.
func (c *Client) Remove(path string) {
	if !c.active {
		return
	}
	_ = exec.Command("zoxide", "remove", path).Run()
}
```

- [ ] **Step 3: Write gh package**

The gh squash-merge detection is already in `internal/git/stale.go` as `DetectStaleGh`. The `gh` package just provides availability checking.

Create `internal/gh/gh.go`:

```go
package gh

import "os/exec"

// Available returns true if the gh CLI is installed.
func Available() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}
```

- [ ] **Step 4: Verify everything compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 5: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/tmux/ internal/zoxide/ internal/gh/
git commit -m "feat: add tmux, zoxide, and gh integration packages"
```

---

## Task 7: TUI Prompts — Confirm and Filter

**Files:**
- Create: `internal/tui/prompt/prompt.go`

- [ ] **Step 1: Add huh v2 dependency**

```bash
cd ~/Data/git/noamsto/wt
go get charm.land/huh/v2@latest
```

- [ ] **Step 2: Write prompt package**

Create `internal/tui/prompt/prompt.go`:

```go
package prompt

import (
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"
	"golang.org/x/term"
)

// Confirm asks the user a yes/no question. Returns true for yes.
// If skipPrompt is true, returns true without asking.
func Confirm(message string, skipPrompt bool) bool {
	if skipPrompt {
		return true
	}

	var confirmed bool
	err := huh.NewConfirm().
		Title(message).
		Value(&confirmed).
		Run()
	if err != nil {
		return false
	}
	return confirmed
}

// Filter shows a fuzzy filter over items and returns the selected item.
// If stdin is not a terminal, returns the best substring match for query.
func Filter(items []string, placeholder string, query string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return bestMatch(items, query), nil
	}

	var selected string
	opts := make([]huh.Option[string], len(items))
	for i, item := range items {
		opts[i] = huh.NewOption(item, item)
	}

	err := huh.NewSelect[string]().
		Title(placeholder).
		Options(opts...).
		Value(&selected).
		Run()
	if err != nil {
		return "", err
	}
	return selected, nil
}

func bestMatch(items []string, query string) string {
	if query == "" && len(items) > 0 {
		return items[0]
	}
	q := strings.ToLower(query)
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), q) {
			return item
		}
	}
	if len(items) > 0 {
		return items[0]
	}
	return ""
}

// Log prints a message to stderr (not captured by quiet mode output piping).
func Log(quiet bool, format string, args ...any) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// LogError prints an error to stderr.
func LogError(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 4: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/tui/prompt/
git commit -m "feat: add confirm and filter prompts using huh v2"
```

---

## Task 8: Command Implementations — list, main, remove

**Files:**
- Create: `internal/cmd/list.go`
- Create: `internal/cmd/main_switch.go`
- Create: `internal/cmd/remove.go`
- Modify: `cmd/wt/main.go`

- [ ] **Step 1: Write the command implementations**

Create `internal/cmd/list.go`:

```go
package cmd

import (
	"fmt"

	"github.com/noamsto/wt/internal/git"
)

// List prints all worktrees.
func List(repoRoot string) error {
	output, err := git.ListWorktreesRaw(repoRoot)
	if err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}
```

Create `internal/cmd/main_switch.go`:

```go
package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
)

// MainSwitch switches to the repo root worktree.
func MainSwitch(repoRoot string, rt runtime.Runtime, tmuxClient *tmux.Client) error {
	if rt.Quiet {
		fmt.Println(repoRoot)
		return nil
	}

	tmuxClient.SwitchToWorktree(repoRoot, filepath.Base(repoRoot), repoRoot)

	if !rt.Quiet {
		fmt.Println()
		fmt.Println("Worktree:", repoRoot)
	}
	return nil
}
```

Create `internal/cmd/remove.go`:

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/tui/prompt"
	"github.com/noamsto/wt/internal/zoxide"
)

// Remove removes a worktree and its associated tmux window.
func Remove(repoRoot, branch string, rt runtime.Runtime, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	if branch == "" {
		return fmt.Errorf("branch name required\nUsage: wt remove <branch-name>")
	}

	worktreePath := git.FindWorktreeByBranch(repoRoot, branch)
	if worktreePath == "" {
		fmt.Fprintf(os.Stderr, "No worktree found for branch '%s'\n\nAvailable worktrees:\n", branch)
		output, err := git.ListWorktreesRaw(repoRoot)
		if err == nil {
			fmt.Fprintln(os.Stderr, output)
		}
		return fmt.Errorf("no worktree found for branch '%s'", branch)
	}

	tmuxClient.KillWindow(repoRoot, worktreePath)

	prompt.Log(rt.Quiet, "Removing worktree: %s", worktreePath)
	if err := git.RemoveWorktree(repoRoot, worktreePath, false); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	zoxideClient.Remove(worktreePath)
	prompt.Log(rt.Quiet, "✓ Worktree removed")

	return nil
}
```

- [ ] **Step 2: Wire commands into main.go**

Replace the content of `cmd/wt/main.go` with the full version that wires everything together:

```go
package main

import (
	"fmt"
	"os"
	"strings"

	wcmd "github.com/noamsto/wt/internal/cmd"
	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/tui/prompt"
	"github.com/noamsto/wt/internal/zoxide"
)

const helpText = `Git Worktree Manager

Usage:
  wt <branch>           Smart switch/create (prompts before creating)
  wt -y <branch>        Skip prompts
  wt -q <branch>        Quiet mode (only output path)
  wt -n <branch>        No tmux (skip window creation/switching)
  wt -yqn <branch>      Combine flags (for Claude/scripts)
  wt z [query]          Fuzzy find worktree, output path (cd "$(wt z)")
  wt main               Switch to root repository window
  wt list               List all worktrees
  wt remove <branch>    Remove worktree + kill window
  wt clean              Remove stale worktrees (merged, squash-merged, deleted)
  wt clean -i           Interactive explorer: inspect worktrees, force-remove
  wt help               Show this help

Model: Session = Project, Window = Worktree

Smart mode:
  Worktree exists     → switch to window (unless -n)
  Branch exists       → prompt to create worktree
  Branch not found    → prompt to create new branch

Worktree location: .worktrees/<branch-name>`

type flags struct {
	yes         bool
	quiet       bool
	noSwitch    bool
	interactive bool
}

func parseArgs(args []string) (flags, []string) {
	var f flags
	var rest []string

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && arg != "-" {
			chars := arg[1:]
			allShort := true
			for _, c := range chars {
				switch c {
				case 'y':
					f.yes = true
				case 'q':
					f.quiet = true
				case 'n':
					f.noSwitch = true
				case 'i':
					f.interactive = true
				default:
					allShort = false
				}
			}
			if !allShort {
				rest = append(rest, arg)
			}
			continue
		}

		switch arg {
		case "--yes":
			f.yes = true
		case "--quiet":
			f.quiet = true
		case "--no-switch":
			f.noSwitch = true
		case "--interactive":
			f.interactive = true
		default:
			rest = append(rest, arg)
		}
	}

	return f, rest
}

func main() {
	f, args := parseArgs(os.Args[1:])

	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	// Help doesn't need a git repo
	switch sub {
	case "help", "-h", "--help", "":
		fmt.Println(helpText)
		return
	}

	// Everything else requires a git repo
	repoRoot, err := git.RepoRoot()
	if err != nil {
		prompt.LogError("Not in a git repository")
		os.Exit(1)
	}

	rt := runtime.Detect()
	rt.NoSwitch = f.noSwitch
	rt.Quiet = f.quiet
	rt.Yes = f.yes

	tmuxClient := tmux.New(rt.TmuxActive())
	zoxideClient := zoxide.New(rt.HasZoxide)

	switch sub {
	case "list", "ls":
		if err := wcmd.List(repoRoot); err != nil {
			prompt.LogError("%v", err)
			os.Exit(1)
		}

	case "remove", "rm":
		branch := ""
		if len(args) > 1 {
			branch = args[1]
		}
		if err := wcmd.Remove(repoRoot, branch, rt, tmuxClient, zoxideClient); err != nil {
			prompt.LogError("%v", err)
			os.Exit(1)
		}

	case "clean", "prune":
		if err := wcmd.Clean(repoRoot, f.interactive, rt, tmuxClient, zoxideClient); err != nil {
			prompt.LogError("%v", err)
			os.Exit(1)
		}

	case "z":
		query := ""
		if len(args) > 1 {
			query = args[1]
		}
		if err := wcmd.Find(repoRoot, query, rt, tmuxClient); err != nil {
			prompt.LogError("%v", err)
			os.Exit(1)
		}

	case "main":
		if err := wcmd.MainSwitch(repoRoot, rt, tmuxClient); err != nil {
			prompt.LogError("%v", err)
			os.Exit(1)
		}

	default:
		// Smart mode: treat as branch name
		if err := wcmd.Smart(repoRoot, sub, rt, tmuxClient, zoxideClient); err != nil {
			prompt.LogError("%v", err)
			os.Exit(1)
		}
	}
}
```

- [ ] **Step 3: Verify list and remove compile (clean, find, smart are stubs for now)**

Add stub files so the build passes. Create `internal/cmd/clean.go`:

```go
package cmd

import (
	"fmt"

	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/zoxide"
)

// Clean removes stale worktrees.
func Clean(repoRoot string, interactive bool, rt runtime.Runtime, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	return fmt.Errorf("not implemented: clean")
}
```

Create `internal/cmd/find.go`:

```go
package cmd

import (
	"fmt"

	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
)

// Find fuzzy-finds a worktree and outputs its path.
func Find(repoRoot, query string, rt runtime.Runtime, tmuxClient *tmux.Client) error {
	return fmt.Errorf("not implemented: find")
}
```

Create `internal/cmd/smart.go`:

```go
package cmd

import (
	"fmt"

	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/zoxide"
)

// Smart handles the default wt <branch> command.
func Smart(repoRoot, branch string, rt runtime.Runtime, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	return fmt.Errorf("not implemented: smart")
}
```

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 4: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/cmd/ cmd/wt/main.go
git commit -m "feat: add list, main, and remove commands; wire CLI dispatch"
```

---

## Task 9: Command Implementation — Smart (wt \<branch\>)

**Files:**
- Modify: `internal/cmd/smart.go`

- [ ] **Step 1: Implement the smart command**

Replace `internal/cmd/smart.go`:

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/tui/prompt"
	"github.com/noamsto/wt/internal/zoxide"
)

// Smart handles the default wt <branch> command.
// It detects whether the worktree/branch exists and takes the appropriate action.
func Smart(repoRoot, branch string, rt runtime.Runtime, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	worktreePath := filepath.Join(repoRoot, ".worktrees", branch)
	existingPath := git.FindWorktreeByBranch(repoRoot, branch)

	// Case 1: Worktree already exists
	if existingPath != "" {
		if _, err := os.Stat(existingPath); os.IsNotExist(err) {
			// Directory missing — offer to prune
			prompt.Log(rt.Quiet, "Worktree directory missing: %s", existingPath)
			prompt.Log(rt.Quiet, "Git thinks branch '%s' has a worktree, but directory doesn't exist.", branch)
			if prompt.Confirm("Run 'git worktree prune' to fix stale references?", rt.Yes) {
				if err := git.PruneWorktrees(repoRoot); err != nil {
					return fmt.Errorf("git worktree prune: %w", err)
				}
				prompt.Log(rt.Quiet, "✓ Pruned stale worktree references")
				// Fall through to create
			} else {
				return fmt.Errorf("cancelled")
			}
		} else {
			// Worktree exists and directory is present — switch to it
			return switchTo(existingPath, branch, repoRoot, rt, tmuxClient)
		}
	}

	// Case 2: Branch exists but no worktree
	isRemote := false
	branchExists := git.BranchExists(repoRoot, branch)
	if !branchExists {
		if git.RemoteBranchExists(repoRoot, branch) {
			branchExists = true
			isRemote = true
		}
	}

	if branchExists {
		sourceDesc := "local branch"
		if isRemote {
			sourceDesc = "remote branch origin/" + branch
		}
		prompt.Log(rt.Quiet, "Branch '%s' exists (%s) but has no worktree.", branch, sourceDesc)
		if !prompt.Confirm(fmt.Sprintf("Create worktree at .worktrees/%s?", branch), rt.Yes) {
			return fmt.Errorf("cancelled")
		}
		return createAndSwitch(repoRoot, branch, worktreePath, false, isRemote, rt, tmuxClient, zoxideClient)
	}

	// Case 3: Branch doesn't exist — create new
	prompt.Log(rt.Quiet, "Branch '%s' does not exist.", branch)
	if !prompt.Confirm("Create new branch + worktree?", rt.Yes) {
		return fmt.Errorf("cancelled")
	}
	return createAndSwitch(repoRoot, branch, worktreePath, true, false, rt, tmuxClient, zoxideClient)
}

func createAndSwitch(repoRoot, branch, worktreePath string, createBranch, trackRemote bool, rt runtime.Runtime, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	prompt.Log(rt.Quiet, "Creating worktree...")
	if err := git.CreateWorktree(repoRoot, branch, worktreePath, createBranch, trackRemote); err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}
	prompt.Log(rt.Quiet, "✓ Worktree created: %s", worktreePath)
	zoxideClient.Add(worktreePath)
	return switchTo(worktreePath, branch, repoRoot, rt, tmuxClient)
}

func switchTo(worktreePath, branch, repoRoot string, rt runtime.Runtime, tmuxClient *tmux.Client) error {
	tmuxClient.SwitchToWorktree(repoRoot, branch, worktreePath)

	if rt.Quiet {
		fmt.Println(worktreePath)
	} else {
		fmt.Println()
		fmt.Println("Worktree:", worktreePath)
	}
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 3: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/cmd/smart.go
git commit -m "feat: add smart worktree switch/create command"
```

---

## Task 10: Command Implementation — Clean

**Files:**
- Modify: `internal/cmd/clean.go`

- [ ] **Step 1: Implement the clean command**

Replace `internal/cmd/clean.go`:

```go
package cmd

import (
	"fmt"
	"sort"

	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/tui/explorer"
	"github.com/noamsto/wt/internal/tui/prompt"
	"github.com/noamsto/wt/internal/zoxide"
)

// Clean removes stale worktrees. If interactive is true, launches the TUI explorer.
func Clean(repoRoot string, interactive bool, rt runtime.Runtime, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	defaultBranch, err := git.DefaultBranch(repoRoot)
	if err != nil {
		return err
	}

	prompt.Log(rt.Quiet, "Fetching latest remote state...")
	_ = git.FetchPrune(repoRoot)

	worktrees, err := git.ListWorktrees(repoRoot, defaultBranch)
	if err != nil {
		return err
	}

	if len(worktrees) == 0 {
		prompt.Log(rt.Quiet, "No worktrees to clean (besides main).")
		return nil
	}

	git.DetectStale(repoRoot, defaultBranch, worktrees)
	if rt.HasGh {
		git.DetectStaleGh(repoRoot, worktrees)
	}

	if interactive {
		sort.Slice(worktrees, func(i, j int) bool {
			si := worktrees[i].IsStale()
			sj := worktrees[j].IsStale()
			if si != sj {
				return si
			}
			return worktrees[i].Branch < worktrees[j].Branch
		})
		return explorer.Run(repoRoot, worktrees, tmuxClient, zoxideClient)
	}

	// Non-interactive: find stale and prompt
	var stale []git.Worktree
	for _, wt := range worktrees {
		if wt.IsStale() {
			stale = append(stale, wt)
		}
	}

	if len(stale) == 0 {
		prompt.Log(rt.Quiet, "No stale worktrees found.")
		return nil
	}

	prompt.Log(rt.Quiet, "Found %d stale worktree(s):", len(stale))
	for _, wt := range stale {
		prompt.Log(rt.Quiet, "  • %s (%s)", wt.Branch, wt.StaleReason)
		prompt.Log(rt.Quiet, "    %s", wt.Path)
	}

	if !prompt.Confirm(fmt.Sprintf("Remove all %d stale worktrees?", len(stale)), rt.Yes) {
		return fmt.Errorf("cancelled")
	}

	var failed int
	for _, wt := range stale {
		prompt.Log(rt.Quiet, "Removing: %s", wt.Branch)
		tmuxClient.KillWindow(repoRoot, wt.Path)
		if err := git.RemoveWorktree(repoRoot, wt.Path, false); err != nil {
			prompt.Log(rt.Quiet, "  ❌ Failed: %v", err)
			failed++
		} else {
			zoxideClient.Remove(wt.Path)
			prompt.Log(rt.Quiet, "  ✓ Removed worktree")
		}
	}

	cleaned := len(stale) - failed
	if failed == 0 {
		prompt.Log(rt.Quiet, "✓ Cleaned %d worktree(s)", cleaned)
	} else {
		prompt.Log(rt.Quiet, "⚠ Cleaned %d worktree(s), %d failed", cleaned, failed)
	}
	return nil
}
```

- [ ] **Step 2: This references `explorer.Run` which doesn't exist yet — create a stub**

Create `internal/tui/explorer/explorer.go`:

```go
package explorer

import (
	"fmt"

	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/zoxide"
)

// Run launches the interactive TUI explorer for worktree management.
func Run(repoRoot string, worktrees []git.Worktree, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	return fmt.Errorf("not implemented: explorer TUI")
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 4: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/cmd/clean.go internal/tui/explorer/
git commit -m "feat: add clean command with stale detection and batch removal"
```

---

## Task 11: Command Implementation — Find (wt z)

**Files:**
- Modify: `internal/cmd/find.go`

- [ ] **Step 1: Implement the find command**

Replace `internal/cmd/find.go`:

```go
package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/runtime"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/tui/prompt"
)

// Find fuzzy-finds a worktree and outputs its path.
func Find(repoRoot, query string, rt runtime.Runtime, tmuxClient *tmux.Client) error {
	defaultBranch, err := git.DefaultBranch(repoRoot)
	if err != nil {
		return err
	}

	worktrees, err := git.ListWorktrees(repoRoot, defaultBranch)
	if err != nil {
		return err
	}

	if len(worktrees) == 0 {
		return fmt.Errorf("no worktrees found (besides main)")
	}

	var paths []string
	for _, wt := range worktrees {
		paths = append(paths, wt.Path)
	}

	var result string
	if query != "" {
		// Filter paths matching query
		var matches []string
		for _, p := range paths {
			if strings.Contains(p, query) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("no worktree matching '%s'", query)
		}
		if len(matches) == 1 {
			result = matches[0]
		} else {
			result, err = prompt.Filter(matches, "Select worktree...", query)
			if err != nil {
				return err
			}
		}
	} else {
		result, err = prompt.Filter(paths, "Select worktree...", "")
		if err != nil {
			return err
		}
	}

	if result == "" {
		return fmt.Errorf("no worktree selected")
	}

	// Update tmux window metadata
	branch := filepath.Base(result)
	tmuxClient.UpdateWindowMetadata(result, branch)

	fmt.Println(result)
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 3: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/cmd/find.go
git commit -m "feat: add fuzzy find command (wt z)"
```

---

## Task 12: TUI Explorer — Port wt-explorer

This is the largest task — porting the Bubble Tea TUI from wt-explorer. The code is a direct port from `lazytmux/wt-explorer/tui.go` adapted to use the new internal packages.

**Files:**
- Modify: `internal/tui/explorer/explorer.go`

- [ ] **Step 1: Add Bubble Tea dependencies**

```bash
cd ~/Data/git/noamsto/wt
go get charm.land/bubbletea/v2@latest
go get charm.land/bubbles/v2@latest
go get charm.land/lipgloss/v2@latest
```

- [ ] **Step 2: Write the full explorer TUI**

Replace `internal/tui/explorer/explorer.go` with the full port. This is based on `lazytmux/wt-explorer/tui.go` but uses `internal/git` and `internal/tmux` packages:

```go
package explorer

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/noamsto/wt/internal/git"
	"github.com/noamsto/wt/internal/tmux"
	"github.com/noamsto/wt/internal/zoxide"
)

var (
	colorRed   = lipgloss.Color("#f38ba8")
	colorGreen = lipgloss.Color("#a6e3a1")
	colorBlue  = lipgloss.Color("#89b4fa")
	colorDim   = lipgloss.Color("#6c7086")
	colorText  = lipgloss.Color("#cdd6f4")
	colorPeach = lipgloss.Color("#fab387")
)

var (
	staleStyle     = lipgloss.NewStyle().Foreground(colorRed)
	selectedStyle  = lipgloss.NewStyle().Foreground(colorGreen)
	cursorStyle    = lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	dimStyle       = lipgloss.NewStyle().Foreground(colorDim)
	headerStyle    = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	warnStyle      = lipgloss.NewStyle().Foreground(colorPeach)
	borderStyle    = lipgloss.NewStyle().Foreground(colorDim)
	statusBarStyle = lipgloss.NewStyle().Foreground(colorDim)
)

type model struct {
	repoRoot     string
	worktrees    []git.Worktree
	tmuxClient   *tmux.Client
	zoxideClient *zoxide.Client
	visible      []int
	cursor       int
	selected     map[int]bool
	query        string
	searching    bool
	preview      viewport.Model
	width        int
	height       int
	ready        bool
	confirmMsg   string
	confirmForce bool
	statusMsg    string
	staleCount   int
}

type detailsLoadedMsg struct {
	index int
}

// Run launches the interactive TUI explorer.
func Run(repoRoot string, worktrees []git.Worktree, tmuxClient *tmux.Client, zoxideClient *zoxide.Client) error {
	m := model{
		repoRoot:     repoRoot,
		worktrees:    worktrees,
		tmuxClient:   tmuxClient,
		zoxideClient: zoxideClient,
		selected:     make(map[int]bool),
		preview:      viewport.New(),
	}
	m.filterVisible()
	m.recomputeStaleCount()

	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	if len(m.visible) > 0 {
		idx := m.visible[0]
		if !m.worktrees[idx].DetailsLoaded {
			return m.loadDetailsCmd(idx)
		}
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.updatePreviewSize()
		return m, nil
	case detailsLoadedMsg:
		m.worktrees[msg.index].DetailsLoaded = true
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirmMsg != "" {
		switch msg.String() {
		case "y", "Y":
			m.executeDelete()
		default:
			m.statusMsg = "Cancelled."
		}
		m.confirmMsg = ""
		return m, nil
	}

	if m.searching {
		return m.handleSearchKey(msg)
	}
	return m.handleNormalKey(msg)
}

func (m model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "enter":
		m.searching = false
		return m, nil
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.filterVisible()
			return m, m.ensureDetailsLoaded()
		}
		return m, nil
	default:
		key := msg.Key()
		if key.Text != "" && key.Mod == 0 {
			m.query += key.Text
			m.filterVisible()
			return m, m.ensureDetailsLoaded()
		}
		return m, nil
	}
}

func (m model) handleNormalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q", "esc":
		if m.query != "" {
			m.query = ""
			m.filterVisible()
			return m, m.ensureDetailsLoaded()
		}
		return m, tea.Quit
	case "/":
		m.searching = true
		return m, nil
	case "up", "k":
		return m, m.moveCursor(-1)
	case "down", "j":
		return m, m.moveCursor(1)
	case "space", " ":
		if len(m.visible) > 0 {
			idx := m.visible[m.cursor]
			if m.selected[idx] {
				delete(m.selected, idx)
			} else {
				m.selected[idx] = true
			}
		}
		return m, nil
	case "a":
		for _, idx := range m.visible {
			if m.worktrees[idx].IsStale() {
				m.selected[idx] = true
			}
		}
		return m, nil
	case "d":
		m.startDelete(false)
		return m, nil
	case "D":
		m.startDelete(true)
		return m, nil
	}
	return m, nil
}

func (m *model) moveCursor(delta int) tea.Cmd {
	if len(m.visible) == 0 {
		return nil
	}
	m.cursor += delta
	m.cursor = max(0, min(m.cursor, len(m.visible)-1))
	return m.ensureDetailsLoaded()
}

func (m *model) ensureDetailsLoaded() tea.Cmd {
	if len(m.visible) == 0 {
		return nil
	}
	idx := m.visible[m.cursor]
	if !m.worktrees[idx].DetailsLoaded {
		return m.loadDetailsCmd(idx)
	}
	return nil
}

func (m *model) loadDetailsCmd(idx int) tea.Cmd {
	wt := &m.worktrees[idx]
	return func() tea.Msg {
		git.LoadDetails(wt)
		return detailsLoadedMsg{index: idx}
	}
}

func (m *model) filterVisible() {
	m.visible = m.visible[:0]
	q := strings.ToLower(m.query)
	for i := range m.worktrees {
		if q == "" || strings.Contains(strings.ToLower(m.worktrees[i].Branch), q) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

func (m *model) startDelete(force bool) {
	targets := m.deleteTargets()
	if len(targets) == 0 {
		return
	}
	var names []string
	for _, idx := range targets {
		names = append(names, m.worktrees[idx].Branch)
	}
	verb := "Remove"
	if force {
		verb = "Force remove"
	}
	m.confirmMsg = fmt.Sprintf("%s %d worktree(s) [%s]? y/n", verb, len(targets), strings.Join(names, ", "))
	m.confirmForce = force
}

func (m *model) deleteTargets() []int {
	var targets []int
	for _, idx := range m.visible {
		if m.selected[idx] {
			targets = append(targets, idx)
		}
	}
	if len(targets) == 0 && len(m.visible) > 0 {
		targets = []int{m.visible[m.cursor]}
	}
	return targets
}

func (m *model) executeDelete() {
	targets := m.deleteTargets()
	if len(targets) == 0 {
		return
	}

	removedSet := make(map[int]bool, len(targets))
	var removed, failed int
	var lastErr string
	for _, idx := range targets {
		wt := m.worktrees[idx]
		err := git.RemoveWorktree(m.repoRoot, wt.Path, m.confirmForce)
		if err != nil {
			failed++
			lastErr = fmt.Sprintf("Error removing %s: %v", wt.Branch, err)
		} else {
			m.tmuxClient.KillWindow(m.repoRoot, wt.Path)
			m.zoxideClient.Remove(wt.Path)
			removedSet[idx] = true
			removed++
		}
	}

	if removed > 0 {
		var newWorktrees []git.Worktree
		indexMap := make(map[int]int)
		for i, wt := range m.worktrees {
			if !removedSet[i] {
				indexMap[i] = len(newWorktrees)
				newWorktrees = append(newWorktrees, wt)
			}
		}

		newSelected := make(map[int]bool)
		for oldIdx := range m.selected {
			if newIdx, ok := indexMap[oldIdx]; ok {
				newSelected[newIdx] = true
			}
		}

		m.worktrees = newWorktrees
		m.selected = newSelected
		m.filterVisible()
		m.recomputeStaleCount()
	}

	switch {
	case failed == 0:
		m.statusMsg = fmt.Sprintf("Removed %d worktree(s).", removed)
	case removed == 0:
		m.statusMsg = lastErr
	default:
		m.statusMsg = fmt.Sprintf("Removed %d, failed %d. %s", removed, failed, lastErr)
	}
}

func (m *model) recomputeStaleCount() {
	m.staleCount = 0
	for i := range m.worktrees {
		if m.worktrees[i].IsStale() {
			m.staleCount++
		}
	}
}

func (m *model) updatePreviewSize() {
	previewW, previewH := m.previewDimensions()
	m.preview = viewport.New(
		viewport.WithWidth(previewW),
		viewport.WithHeight(previewH),
	)
}

func (m *model) listWidth() int {
	w := m.width * 3 / 5
	return max(30, min(w, m.width-20))
}

func (m *model) previewDimensions() (int, int) {
	lw := m.listWidth()
	pw := max(10, m.width-lw-3)
	ph := max(3, m.height-6)
	return pw, ph
}

func (m model) View() tea.View {
	var content string
	if !m.ready {
		content = "Loading..."
	} else {
		content = m.renderFull()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m *model) renderFull() string {
	var b strings.Builder

	var searchLine string
	if m.searching {
		searchLine = cursorStyle.Render("/") + m.query + cursorStyle.Render("│")
	} else if m.query != "" {
		searchLine = dimStyle.Render("/") + m.query
	} else {
		searchLine = dimStyle.Render("/ to search")
	}
	b.WriteString(borderStyle.Render("── Search "+strings.Repeat("─", max(0, m.width-10))) + "\n")
	b.WriteString(searchLine + "\n")

	lw := m.listWidth()
	_, ph := m.previewDimensions()

	b.WriteString(padRight(headerStyle.Render(" Worktrees"), lw) + borderStyle.Render(" │ ") + headerStyle.Render(" Details") + "\n")

	listLines := m.renderListLines(lw, ph)

	previewContent := m.renderPreview()
	m.preview.SetContent(previewContent)
	previewRendered := m.preview.View()
	previewLines := strings.Split(previewRendered, "\n")

	for i := range ph {
		var left, right string
		if i < len(listLines) {
			left = listLines[i]
		}
		left = padRight(left, lw)
		if i < len(previewLines) {
			right = previewLines[i]
		}
		b.WriteString(left + borderStyle.Render(" │ ") + right + "\n")
	}

	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width)) + "\n")
	if m.searching {
		b.WriteString(dimStyle.Render("type to filter  enter/esc accept  q clear+quit") + "\n")
	} else {
		b.WriteString(dimStyle.Render("j/k navigate  space select  a sel stale  d/D delete  / search  q quit") + "\n")
	}

	if m.confirmMsg != "" {
		b.WriteString(warnStyle.Render(m.confirmMsg))
	} else if m.statusMsg != "" {
		b.WriteString(statusBarStyle.Render(m.statusMsg))
	} else {
		b.WriteString(statusBarStyle.Render(fmt.Sprintf("%d worktrees, %d stale, %d selected",
			len(m.worktrees), m.staleCount, len(m.selected))))
	}

	return b.String()
}

func (m *model) renderListLines(width, height int) []string {
	lines := make([]string, 0, height)

	start := 0
	if m.cursor >= height {
		start = m.cursor - height + 1
	}

	for i, idx := range m.visible {
		if i < start {
			continue
		}
		if len(lines) >= height {
			break
		}

		wt := m.worktrees[idx]
		var line strings.Builder

		if i == m.cursor {
			line.WriteString(cursorStyle.Render("> "))
		} else {
			line.WriteString("  ")
		}

		if m.selected[idx] {
			line.WriteString(selectedStyle.Render("✓"))
		} else {
			line.WriteString(" ")
		}

		if wt.IsStale() {
			line.WriteString(staleStyle.Render("●"))
		} else {
			line.WriteString(" ")
		}

		line.WriteString(" ")
		if i == m.cursor {
			line.WriteString(cursorStyle.Render(wt.Branch))
		} else {
			line.WriteString(wt.Branch)
		}

		if wt.IsStale() {
			line.WriteString(" ")
			line.WriteString(staleStyle.Render("[" + wt.StaleReason + "]"))
		}

		lines = append(lines, truncateToWidth(line.String(), width))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (m *model) renderPreview() string {
	if len(m.visible) == 0 {
		return dimStyle.Render("No worktrees to display.")
	}

	idx := m.visible[m.cursor]
	wt := m.worktrees[idx]
	pw, _ := m.previewDimensions()
	sep := dimStyle.Render(strings.Repeat("─", max(0, pw-1)))

	var b strings.Builder

	b.WriteString(headerStyle.Render("  "+wt.Branch) + "\n")
	b.WriteString(dimStyle.Render("  "+wt.Path) + "\n")

	if wt.IsStale() {
		b.WriteString(staleStyle.Render("  ● "+wt.StaleReason) + "\n")
	}

	if !wt.DetailsLoaded {
		b.WriteString("\n" + dimStyle.Render("  Loading..."))
		return b.String()
	}

	b.WriteString(sep + "\n")

	dirtyLabel := dimStyle.Render("  clean")
	if wt.DirtyFiles > 0 {
		dirtyLabel = warnStyle.Render(fmt.Sprintf("  %d dirty file(s)", wt.DirtyFiles))
	}
	b.WriteString(dirtyLabel + "\n")

	unpushedLabel := dimStyle.Render("  pushed")
	if len(wt.UnpushedLog) > 0 {
		unpushedLabel = warnStyle.Render(fmt.Sprintf("  %d unpushed commit(s)", len(wt.UnpushedLog)))
	}
	b.WriteString(unpushedLabel + "\n")

	if len(wt.UnpushedLog) > 0 {
		b.WriteString(sep + "\n")
		b.WriteString(headerStyle.Render("  Unpushed") + "\n")
		for _, line := range wt.UnpushedLog {
			b.WriteString(dimStyle.Render("  ") + line + "\n")
		}
	}

	if wt.LastCommit != "" {
		b.WriteString(sep + "\n")
		b.WriteString(headerStyle.Render("  Last commit") + "\n")
		b.WriteString(dimStyle.Render("  ") + wt.LastCommit + "\n")
	}

	return b.String()
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	var result []rune
	visW := 0
	inEscape := false
	for _, r := range runes {
		if r == '\x1b' {
			inEscape = true
			result = append(result, r)
			continue
		}
		if inEscape {
			result = append(result, r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		rw := lipgloss.Width(string(r))
		if visW+rw > width {
			break
		}
		visW += rw
		result = append(result, r)
	}
	return string(result)
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd ~/Data/git/noamsto/wt
go build ./...
```

- [ ] **Step 4: Run all tests**

```bash
cd ~/Data/git/noamsto/wt
go test ./... -v
```

- [ ] **Step 5: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add internal/tui/explorer/
git commit -m "feat: add interactive TUI explorer for worktree cleanup"
```

---

## Task 13: Fish Completions

**Files:**
- Create: `completions/wt.fish`

- [ ] **Step 1: Write fish completions**

Create `completions/wt.fish` — ported from lazytmux `modules/home-manager.nix` lines 125-190:

```fish
# Completions for wt (worktree manager)

# Flags
complete -c wt -f -s y -l yes -d 'Skip confirmation prompts'
complete -c wt -f -s q -l quiet -d 'Quiet mode (only output path)'
complete -c wt -f -s n -l no-switch -d 'Skip tmux window operations'
complete -c wt -f -s i -l interactive -d 'Interactive TUI mode (for clean)'

# Subcommands
complete -c wt -f -n '__fish_use_subcommand' -a 'list' -d 'List worktrees'
complete -c wt -f -n '__fish_use_subcommand' -a 'ls' -d 'List worktrees (alias)'
complete -c wt -f -n '__fish_use_subcommand' -a 'remove' -d 'Remove worktree + window'
complete -c wt -f -n '__fish_use_subcommand' -a 'rm' -d 'Remove worktree + window (alias)'
complete -c wt -f -n '__fish_use_subcommand' -a 'clean' -d 'Remove merged worktrees'
complete -c wt -f -n '__fish_use_subcommand' -a 'prune' -d 'Remove merged worktrees (alias)'
complete -c wt -f -n '__fish_use_subcommand' -a 'z' -d 'Fuzzy find worktree'
complete -c wt -f -n '__fish_use_subcommand' -a 'main' -d 'Switch to main worktree'
complete -c wt -f -n '__fish_use_subcommand' -a 'help' -d 'Show help'

# Complete existing worktree branches
function __wt_list_worktree_branches
    if git rev-parse --git-dir >/dev/null 2>&1
        set -l repo_root (git rev-parse --show-toplevel)
        git -C "$repo_root" worktree list 2>/dev/null | while read -l line
            if string match -rq '\[(.+)\]$' -- $line
                set -l branch (string match -r '\[(.+)\]$' -- $line)[2]
                echo $branch
            end
        end
    end
end

# Complete all branches (for smart mode)
function __wt_list_all_branches
    if git rev-parse --git-dir >/dev/null 2>&1
        set -l repo_root (git rev-parse --show-toplevel)

        __wt_list_worktree_branches

        set -l used_branches (__wt_list_worktree_branches)

        for branch in (git -C "$repo_root" branch --format='%(refname:short)' 2>/dev/null)
            if not contains $branch $used_branches
                echo $branch
            end
        end

        for branch in (git -C "$repo_root" branch -r --format='%(refname:short)' 2>/dev/null)
            set -l short_name (string replace -r '^origin/' "" -- $branch)
            if test "$short_name" != "HEAD"; and not contains $short_name $used_branches
                echo $short_name
            end
        end | sort -u
    end
end

# Branch completions for remove and z (only existing worktrees)
complete -c wt -f -n '__fish_seen_subcommand_from remove rm z' -a '(__wt_list_worktree_branches)'

# Branch completions for smart mode (all branches)
complete -c wt -f -n '__fish_use_subcommand' -a '(__wt_list_all_branches)'
```

- [ ] **Step 2: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add completions/
git commit -m "feat: add fish shell completions"
```

---

## Task 14: Nix Flake with buildGoModule and Home-Manager Module

**Files:**
- Create: `flake.nix`

- [ ] **Step 1: Create the flake**

Create `flake.nix`:

```nix
{
  description = "wt - Git worktree manager";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs @ {flake-parts, ...}:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = ["x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin"];

      perSystem = {pkgs, ...}: {
        packages.default = pkgs.buildGoModule {
          pname = "wt";
          version = "0.1.0";
          src = ./.;
          vendorHash = null; # Will be set after first build
          ldflags = ["-s" "-w"];
          meta = {
            description = "Git worktree manager with optional tmux integration";
            mainProgram = "wt";
          };
        };
      };

      flake = {
        homeManagerModules.default = {
          config,
          lib,
          pkgs,
          ...
        }: let
          cfg = config.programs.wt;
          wtPkg = inputs.self.packages.${pkgs.system}.default;
        in {
          options.programs.wt = {
            enable = lib.mkEnableOption "wt - git worktree manager";
          };

          config = lib.mkIf cfg.enable {
            home.packages = [wtPkg];

            xdg.configFile."fish/completions/wt.fish".source = ./completions/wt.fish;
          };
        };
      };
    };
}
```

- [ ] **Step 2: Vendor Go dependencies and build**

```bash
cd ~/Data/git/noamsto/wt
go mod tidy
go mod vendor
```

Then try building with nix. The `vendorHash` will need to be determined:

```bash
cd ~/Data/git/noamsto/wt
nix build . 2>&1
```

If the build fails with a hash mismatch, copy the expected hash from the error message and update `vendorHash` in `flake.nix`. Alternatively, use `lib.fakeHash` first to get the real hash.

- [ ] **Step 3: Verify the nix build produces a working binary**

```bash
cd ~/Data/git/noamsto/wt
./result/bin/wt help
./result/bin/wt list
```

- [ ] **Step 4: Commit**

```bash
cd ~/Data/git/noamsto/wt
git add flake.nix flake.lock go.mod go.sum
git commit -m "feat: add nix flake with buildGoModule and home-manager module"
```

---

## Task 15: End-to-End Manual Testing

**Files:** None (testing only)

- [ ] **Step 1: Build and test help**

```bash
cd ~/Data/git/noamsto/wt
go build -o wt ./cmd/wt
./wt help
```

Expected: help text prints.

- [ ] **Step 2: Test list in a git repo**

```bash
cd ~/Data/git/noamsto/wt
./wt list
```

Expected: shows worktree list (likely just the main worktree).

- [ ] **Step 3: Test smart create with -yqn**

```bash
cd ~/Data/git/noamsto/wt
./wt -yqn test-branch
```

Expected: creates worktree at `.worktrees/test-branch`, prints the path.

- [ ] **Step 4: Test list again**

```bash
cd ~/Data/git/noamsto/wt
./wt list
```

Expected: shows main + test-branch worktrees.

- [ ] **Step 5: Test remove**

```bash
cd ~/Data/git/noamsto/wt
./wt remove test-branch
```

Expected: removes the worktree.

- [ ] **Step 6: Test clean (should find nothing)**

```bash
cd ~/Data/git/noamsto/wt
./wt -y clean
```

Expected: "No stale worktrees found." or "No worktrees to clean."

- [ ] **Step 7: Run all unit tests**

```bash
cd ~/Data/git/noamsto/wt
go test ./... -v
```

Expected: all pass.

- [ ] **Step 8: Clean up test artifacts**

```bash
cd ~/Data/git/noamsto/wt
git branch -D test-branch 2>/dev/null || true
```

---

## Task 16: Push to GitHub

**Files:** None

- [ ] **Step 1: Create GitHub repo**

```bash
cd ~/Data/git/noamsto/wt
gh repo create noamsto/wt --public --source=. --push
```

- [ ] **Step 2: Verify remote**

```bash
cd ~/Data/git/noamsto/wt
git remote -v
git log --oneline
```
