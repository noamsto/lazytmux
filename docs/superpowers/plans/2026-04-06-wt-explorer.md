# wt-explorer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an interactive bubbletea TUI (`wt-explorer`) for browsing, inspecting, and batch-removing git worktrees, launched via `wt clean -i`.

**Architecture:** Standalone Go binary in `wt-explorer/` directory. Does its own git/tmux queries via `exec.Command`. Bash `wt` script execs into it for `-i` mode. Picker-style list+preview layout using bubbletea/lipgloss.

**Tech Stack:** Go 1.25, bubbletea/v2, bubbles/v2 (viewport), lipgloss/v2, exec calls to git/tmux/gh.

**Spec:** `docs/superpowers/specs/2026-04-06-wt-explorer-design.md`

---

## File Structure

```
wt-explorer/
  main.go          # Entry point: parse repo root arg, load data, launch TUI
  git.go           # Worktree listing, stale detection, dirty/log queries
  git_test.go      # Tests for git parsing logic (porcelain output, stale detection)
  tmux.go          # Find and kill tmux windows by @worktree option
  tui.go           # Bubbletea model, Update, View, key handling
  go.mod           # github.com/noamsto/lazytmux/wt-explorer
  default.nix      # buildGoModule packaging

wt/
  wt.sh            # Modified: clean -i execs into wt-explorer, remove bash wt_clean_interactive
  default.nix      # Modified: add wt-explorer to runtimeInputs

flake.nix          # Modified: add packages.wt-explorer
modules/home-manager.nix  # No changes needed (wt-explorer is a runtime dep of wt)
```

---

### Task 1: Go module scaffolding and git data layer

Create the Go module, define data types, and implement git worktree listing with stale detection. This is the foundation — all data the TUI needs comes from here.

**Files:**
- Create: `wt-explorer/go.mod`
- Create: `wt-explorer/main.go`
- Create: `wt-explorer/git.go`
- Create: `wt-explorer/git_test.go`

- [ ] **Step 1: Initialize Go module**

```bash
cd wt-explorer
go mod init github.com/noamsto/lazytmux/wt-explorer
```

This creates `go.mod` with Go 1.25.

- [ ] **Step 2: Write git.go with types and worktree listing**

Create `wt-explorer/git.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Worktree represents a single git worktree.
type Worktree struct {
	Branch      string
	Path        string
	StaleReason string // empty if not stale
	DirtyFiles  string // git status --short output
	UnpushedLog string // git log --oneline @{upstream}..HEAD
	LastCommit  string // git log -1 --format="%h %s (%ar)"
}

// IsStale returns true if the worktree has been identified as stale.
func (w Worktree) IsStale() bool {
	return w.StaleReason != ""
}

// listWorktrees parses `git worktree list --porcelain` and returns all
// worktrees except the main one (repo root) and the default branch.
func listWorktrees(repoRoot, defaultBranch string) ([]Worktree, error) {
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreesPorcelain(string(out), repoRoot, defaultBranch), nil
}

// parseWorktreesPorcelain parses the porcelain output of `git worktree list`.
// Exported for testing.
func parseWorktreesPorcelain(output, repoRoot, defaultBranch string) []Worktree {
	var worktrees []Worktree
	var currentPath, currentBranch string

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			currentPath = strings.TrimPrefix(line, "worktree ")
			currentBranch = ""
		case strings.HasPrefix(line, "branch refs/heads/"):
			currentBranch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "": // end of record
			if currentPath != "" && currentPath != repoRoot && currentBranch != defaultBranch && currentBranch != "" {
				worktrees = append(worktrees, Worktree{
					Branch: currentBranch,
					Path:   currentPath,
				})
			}
			currentPath = ""
			currentBranch = ""
		}
	}
	// Handle last record (porcelain output may not end with blank line)
	if currentPath != "" && currentPath != repoRoot && currentBranch != defaultBranch && currentBranch != "" {
		worktrees = append(worktrees, Worktree{
			Branch: currentBranch,
			Path:   currentPath,
		})
	}
	return worktrees
}

// detectDefaultBranch returns "main" or "master", whichever exists.
func detectDefaultBranch(repoRoot string) (string, error) {
	for _, branch := range []string{"main", "master"} {
		cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		if cmd.Run() == nil {
			return branch, nil
		}
	}
	return "", fmt.Errorf("no main or master branch found")
}

// detectStale runs 3-strategy stale detection and sets StaleReason on each worktree.
// Mutates the slice in place.
func detectStale(repoRoot, defaultBranch string, worktrees []Worktree) {
	checked := make(map[int]bool)

	// Strategy 1: git branch --merged
	cmd := exec.Command("git", "-C", repoRoot, "branch", "--merged", defaultBranch)
	if out, err := cmd.Output(); err == nil {
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
		cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet",
			"refs/remotes/origin/"+worktrees[i].Branch)
		if cmd.Run() != nil {
			worktrees[i].StaleReason = "remote branch deleted"
			checked[i] = true
		}
	}

	// Strategy 3: GitHub PR squash-merged (parallel, optional)
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return // gh not available
	}

	var unchecked []int
	for i := range worktrees {
		if !checked[i] {
			unchecked = append(unchecked, i)
		}
	}
	if len(unchecked) == 0 {
		return
	}

	type ghResult struct {
		index int
		prNum string
	}
	results := make(chan ghResult, len(unchecked))
	var wg sync.WaitGroup

	for _, idx := range unchecked {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(ghPath, "pr", "list",
				"--head", worktrees[i].Branch,
				"--state", "merged",
				"--json", "number",
				"--jq", ".[0].number")
			cmd.Dir = repoRoot
			out, err := cmd.Output()
			if err != nil {
				return
			}
			prNum := strings.TrimSpace(string(out))
			if prNum != "" {
				results <- ghResult{index: i, prNum: prNum}
			}
		}(idx)
	}
	wg.Wait()
	close(results)

	for r := range results {
		worktrees[r.index].StaleReason = "PR #" + r.prNum + " squash-merged"
	}
}

// parseBranchList parses `git branch` output into a set of branch names.
func parseBranchList(output string) map[string]bool {
	branches := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		branch := strings.TrimLeft(scanner.Text(), " *+")
		if branch != "" {
			branches[branch] = true
		}
	}
	return branches
}

// loadWorktreeDetails populates DirtyFiles, UnpushedLog, and LastCommit for a worktree.
func loadWorktreeDetails(wt *Worktree) {
	// Dirty files
	if out, err := exec.Command("git", "-C", wt.Path, "status", "--short").Output(); err == nil {
		wt.DirtyFiles = strings.TrimRight(string(out), "\n")
	}

	// Unpushed commits
	if out, err := exec.Command("git", "-C", wt.Path, "log", "--oneline", "@{upstream}..HEAD").Output(); err == nil {
		wt.UnpushedLog = strings.TrimRight(string(out), "\n")
	}

	// Last commit
	if out, err := exec.Command("git", "-C", wt.Path, "log", "-1", "--format=%h %s (%ar)").Output(); err == nil {
		wt.LastCommit = strings.TrimRight(string(out), "\n")
	}
}

// removeWorktree removes a worktree. If force is true, uses --force.
func removeWorktree(repoRoot, path string, force bool) error {
	args := []string{"-C", repoRoot, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// fetchPrune runs git fetch --prune to get accurate remote state.
func fetchPrune(repoRoot string) {
	cmd := exec.Command("git", "-C", repoRoot, "fetch", "--prune")
	_ = cmd.Run()
}
```

- [ ] **Step 3: Write git_test.go with parsing tests**

Create `wt-explorer/git_test.go`:

```go
package main

import (
	"testing"
)

func TestParseWorktreesPorcelain(t *testing.T) {
	input := `worktree /home/user/repo
branch refs/heads/main

worktree /home/user/repo/.worktrees/feat-auth
branch refs/heads/feat-auth

worktree /home/user/repo/.worktrees/fix-cors
branch refs/heads/fix-cors

`
	got := parseWorktreesPorcelain(input, "/home/user/repo", "main")
	if len(got) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(got))
	}
	if got[0].Branch != "feat-auth" {
		t.Errorf("expected feat-auth, got %s", got[0].Branch)
	}
	if got[0].Path != "/home/user/repo/.worktrees/feat-auth" {
		t.Errorf("expected worktree path, got %s", got[0].Path)
	}
	if got[1].Branch != "fix-cors" {
		t.Errorf("expected fix-cors, got %s", got[1].Branch)
	}
}

func TestParseWorktreesPorcelainNoTrailingNewline(t *testing.T) {
	input := `worktree /repo
branch refs/heads/main

worktree /repo/.worktrees/feature
branch refs/heads/feature`

	got := parseWorktreesPorcelain(input, "/repo", "main")
	if len(got) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(got))
	}
	if got[0].Branch != "feature" {
		t.Errorf("expected feature, got %s", got[0].Branch)
	}
}

func TestParseWorktreesPorcelainSkipsDefaultBranch(t *testing.T) {
	input := `worktree /repo
branch refs/heads/main

worktree /repo/.worktrees/main
branch refs/heads/main

worktree /repo/.worktrees/feature
branch refs/heads/feature

`
	got := parseWorktreesPorcelain(input, "/repo", "main")
	if len(got) != 1 {
		t.Fatalf("expected 1 (feature only), got %d", len(got))
	}
}

func TestParseBranchList(t *testing.T) {
	input := `* main
  feat-auth
  fix-cors
+ worktree-branch
`
	got := parseBranchList(input)
	expected := []string{"main", "feat-auth", "fix-cors", "worktree-branch"}
	for _, b := range expected {
		if !got[b] {
			t.Errorf("expected branch %q in set", b)
		}
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd wt-explorer
go test -v ./...
```

Expected: all 4 tests PASS.

- [ ] **Step 5: Write main.go entry point**

Create `wt-explorer/main.go`:

```go
package main

import (
	"fmt"
	"os"
	"sort"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: wt-explorer <repo-root>\n")
		os.Exit(1)
	}
	repoRoot := os.Args[1]

	// Validate repo root
	if _, err := os.Stat(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Detect default branch
	defaultBranch, err := detectDefaultBranch(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Fetch remote state
	fmt.Fprintf(os.Stderr, "Fetching remote state...\n")
	fetchPrune(repoRoot)

	// List worktrees
	worktrees, err := listWorktrees(repoRoot, defaultBranch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(worktrees) == 0 {
		fmt.Fprintf(os.Stderr, "No worktrees found (besides %s).\n", defaultBranch)
		os.Exit(0)
	}

	// Detect stale worktrees
	fmt.Fprintf(os.Stderr, "Checking for stale branches...\n")
	detectStale(repoRoot, defaultBranch, worktrees)

	// Sort: stale first, then alphabetical
	sort.SliceStable(worktrees, func(i, j int) bool {
		si, sj := worktrees[i].IsStale(), worktrees[j].IsStale()
		if si != sj {
			return si
		}
		return worktrees[i].Branch < worktrees[j].Branch
	})

	// Load details for the first item (preview pre-load)
	loadWorktreeDetails(&worktrees[0])

	// Launch TUI
	if err := runTUI(repoRoot, worktrees); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 6: Commit**

```bash
git add wt-explorer/main.go wt-explorer/git.go wt-explorer/git_test.go wt-explorer/go.mod
git commit -m "feat(wt-explorer): scaffold Go module with git data layer"
```

---

### Task 2: Tmux integration

Implement tmux window lookup and kill for worktree removal.

**Files:**
- Create: `wt-explorer/tmux.go`

- [ ] **Step 1: Write tmux.go**

Create `wt-explorer/tmux.go`:

```go
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// killTmuxWindow finds and kills the tmux window associated with a worktree path.
// Looks in session named after the repo directory.
// No-op if tmux is not running or window not found.
func killTmuxWindow(repoRoot, worktreePath string) {
	sessionName := filepath.Base(repoRoot)

	// Check if session exists
	cmd := exec.Command("tmux", "has-session", "-t", sessionName)
	if cmd.Run() != nil {
		return
	}

	// List windows and find one matching by @worktree option
	cmd = exec.Command("tmux", "list-windows", "-t", sessionName,
		"-F", "#{window_index}\t#{@worktree}")
	out, err := cmd.Output()
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[1] == worktreePath {
			exec.Command("tmux", "kill-window", "-t", sessionName+":"+parts[0]).Run()
			return
		}
	}

	// Fallback: match by pane_current_path
	cmd = exec.Command("tmux", "list-windows", "-t", sessionName,
		"-F", "#{window_index}\t#{pane_current_path}")
	out, err = cmd.Output()
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[1] == worktreePath {
			exec.Command("tmux", "kill-window", "-t", sessionName+":"+parts[0]).Run()
			return
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add wt-explorer/tmux.go
git commit -m "feat(wt-explorer): add tmux window kill for worktree removal"
```

---

### Task 3: Bubbletea TUI — model, list rendering, and navigation

Build the core TUI: model struct, Init/Update/View, list rendering with stale markers, cursor navigation, and search filtering.

**Files:**
- Create: `wt-explorer/tui.go`

- [ ] **Step 1: Add charm dependencies**

```bash
cd wt-explorer
go get charm.land/bubbletea/v2
go get charm.land/bubbles/v2
go get charm.land/lipgloss/v2
```

- [ ] **Step 2: Write tui.go with model and list view**

Create `wt-explorer/tui.go`:

```go
package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// Styles
var (
	staleColor    = lipgloss.Color("#f38ba8") // Catppuccin red
	selectedColor = lipgloss.Color("#a6e3a1") // Catppuccin green
	dimColor      = lipgloss.Color("#6c7086") // Catppuccin overlay0
	accentColor   = lipgloss.Color("#89b4fa") // Catppuccin blue
	headerColor   = lipgloss.Color("#cdd6f4") // Catppuccin text
	warnColor     = lipgloss.Color("#fab387") // Catppuccin peach

	staleStyle    = lipgloss.NewStyle().Foreground(staleColor)
	selectedStyle = lipgloss.NewStyle().Foreground(selectedColor)
	dimStyle      = lipgloss.NewStyle().Foreground(dimColor)
	accentStyle   = lipgloss.NewStyle().Foreground(accentColor)
	headerStyle   = lipgloss.NewStyle().Foreground(headerColor).Bold(true)
	warnStyle     = lipgloss.NewStyle().Foreground(warnColor)
)

type model struct {
	repoRoot   string
	worktrees  []Worktree
	visible    []int // indices into worktrees after filtering
	cursor     int
	selected   map[int]bool // indices into worktrees
	query      string
	preview    viewport.Model
	width      int
	height     int
	ready      bool
	confirmMsg string // non-empty = showing confirmation prompt
	confirmCb  func() // called on 'y' in confirm mode
	statusMsg  string // transient status message
}

func newModel(repoRoot string, worktrees []Worktree) model {
	m := model{
		repoRoot:  repoRoot,
		worktrees: worktrees,
		selected:  make(map[int]bool),
	}
	m.refilter()
	return m
}

func runTUI(repoRoot string, worktrees []Worktree) error {
	m := newModel(repoRoot, worktrees)
	p := tea.NewProgram(&m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.preview = viewport.New(0, 0)
			m.ready = true
		}
		m.updatePreview()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Confirmation mode
	if m.confirmMsg != "" {
		switch key {
		case "y", "Y":
			m.confirmCb()
			m.confirmMsg = ""
			m.confirmCb = nil
		default:
			m.confirmMsg = ""
			m.confirmCb = nil
		}
		return m, nil
	}

	switch key {
	case "q", "esc":
		return m, tea.Quit
	case "ctrl+c":
		return m, tea.Quit

	// Navigation
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.updatePreview()
		}
	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			m.updatePreview()
		}

	// Selection
	case " ":
		if len(m.visible) > 0 {
			idx := m.visible[m.cursor]
			if m.selected[idx] {
				delete(m.selected, idx)
			} else {
				m.selected[idx] = true
			}
		}
	case "a":
		// Select all stale
		for _, idx := range m.visible {
			if m.worktrees[idx].IsStale() {
				m.selected[idx] = true
			}
		}

	// Delete
	case "d":
		m.doDelete(false)
	case "D":
		m.doDelete(true)

	// Search
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.refilter()
			m.updatePreview()
		}
	default:
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.query += key
			m.refilter()
			m.updatePreview()
		}
	}

	return m, nil
}

func (m *model) doDelete(force bool) {
	targets := m.deleteTargets()
	if len(targets) == 0 {
		return
	}

	desc := "Remove"
	if force {
		desc = "Force remove"
	}

	if len(targets) == 1 {
		m.confirmMsg = fmt.Sprintf("%s %s? (y/n)", desc, m.worktrees[targets[0]].Branch)
	} else {
		m.confirmMsg = fmt.Sprintf("%s %d worktrees? (y/n)", desc, len(targets))
	}

	m.confirmCb = func() {
		var removed []int
		for _, idx := range targets {
			wt := &m.worktrees[idx]
			err := removeWorktree(m.repoRoot, wt.Path, force)
			if err != nil {
				m.statusMsg = fmt.Sprintf("Failed: %s: %v", wt.Branch, err)
			} else {
				killTmuxWindow(m.repoRoot, wt.Path)
				removed = append(removed, idx)
			}
		}

		if len(removed) > 0 {
			m.statusMsg = fmt.Sprintf("Removed %d worktree(s)", len(removed))
			m.removeWorktrees(removed)
		}
	}
}

// deleteTargets returns worktree indices to delete: selected items, or cursor item.
func (m *model) deleteTargets() []int {
	if len(m.selected) > 0 {
		var targets []int
		for idx := range m.selected {
			targets = append(targets, idx)
		}
		return targets
	}
	if len(m.visible) > 0 {
		return []int{m.visible[m.cursor]}
	}
	return nil
}

// removeWorktrees removes worktrees by index from the model and refilters.
func (m *model) removeWorktrees(indices []int) {
	toRemove := make(map[int]bool)
	for _, idx := range indices {
		toRemove[idx] = true
		delete(m.selected, idx)
	}

	var kept []Worktree
	remap := make(map[int]int) // old index -> new index
	for i, wt := range m.worktrees {
		if !toRemove[i] {
			remap[i] = len(kept)
			kept = append(kept, wt)
		}
	}
	m.worktrees = kept

	// Remap selected
	newSelected := make(map[int]bool)
	for oldIdx := range m.selected {
		if newIdx, ok := remap[oldIdx]; ok {
			newSelected[newIdx] = true
		}
	}
	m.selected = newSelected

	m.refilter()
	if m.cursor >= len(m.visible) && m.cursor > 0 {
		m.cursor = len(m.visible) - 1
	}
	m.updatePreview()
}

// refilter rebuilds the visible list based on the current query.
func (m *model) refilter() {
	m.visible = m.visible[:0]
	q := strings.ToLower(m.query)
	for i, wt := range m.worktrees {
		if q == "" || strings.Contains(strings.ToLower(wt.Branch), q) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) && len(m.visible) > 0 {
		m.cursor = len(m.visible) - 1
	}
}

func (m *model) updatePreview() {
	if len(m.visible) == 0 {
		m.preview.SetContent("No worktrees")
		return
	}
	idx := m.visible[m.cursor]
	wt := &m.worktrees[idx]

	// Lazy-load details
	if wt.LastCommit == "" && wt.DirtyFiles == "" {
		loadWorktreeDetails(wt)
	}

	var b strings.Builder

	b.WriteString(headerStyle.Render("Branch: ") + wt.Branch + "\n")
	b.WriteString(headerStyle.Render("Path:   ") + wt.Path + "\n")

	if wt.IsStale() {
		b.WriteString(staleStyle.Render("Stale:  "+wt.StaleReason) + "\n")
	}
	b.WriteString("\n")

	if wt.DirtyFiles != "" {
		b.WriteString(warnStyle.Render("Dirty files:") + "\n")
		for _, line := range strings.Split(wt.DirtyFiles, "\n") {
			b.WriteString("  " + line + "\n")
		}
	} else {
		b.WriteString(dimStyle.Render("No dirty files") + "\n")
	}
	b.WriteString("\n")

	if wt.UnpushedLog != "" {
		b.WriteString(warnStyle.Render("Unpushed commits:") + "\n")
		for _, line := range strings.Split(wt.UnpushedLog, "\n") {
			b.WriteString("  " + line + "\n")
		}
	} else {
		b.WriteString(dimStyle.Render("No unpushed commits") + "\n")
	}
	b.WriteString("\n")

	if wt.LastCommit != "" {
		b.WriteString(headerStyle.Render("Last commit: ") + wt.LastCommit + "\n")
	}

	m.preview.SetContent(b.String())
}

func (m *model) View() string {
	if !m.ready {
		return "Loading..."
	}

	listWidth := m.width / 2
	previewWidth := m.width - listWidth - 3 // 3 for borders/divider

	// Content area height: total - search bar (3) - key hints (2) - status (1)
	contentHeight := m.height - 6
	if contentHeight < 3 {
		contentHeight = 3
	}

	// Resize viewport
	m.preview.Width = previewWidth
	m.preview.Height = contentHeight

	// Build list
	var listLines []string
	for vi, idx := range m.visible {
		wt := m.worktrees[idx]

		cursor := "  "
		if vi == m.cursor {
			cursor = accentStyle.Render("> ")
		}

		check := "  "
		if m.selected[idx] {
			check = selectedStyle.Render("✓ ")
		}

		staleMarker := "  "
		if wt.IsStale() {
			staleMarker = staleStyle.Render("● ")
		}

		branch := wt.Branch
		if vi == m.cursor {
			branch = accentStyle.Render(branch)
		}

		reason := ""
		if wt.IsStale() {
			reason = " " + dimStyle.Render("["+wt.StaleReason+"]")
		}

		line := cursor + check + staleMarker + branch + reason

		// Truncate to list width
		listLines = append(listLines, line)
	}

	// Pad list to fill height
	for len(listLines) < contentHeight {
		listLines = append(listLines, "")
	}

	listContent := strings.Join(listLines[:contentHeight], "\n")

	// Build search bar
	searchBar := accentStyle.Render(" filter: ") + m.query + dimStyle.Render("│")

	// Build key hints
	hints := dimStyle.Render("↑↓ navigate  space select  a sel stale  d delete  D force  q quit")

	// Build status line
	status := ""
	if m.confirmMsg != "" {
		status = warnStyle.Render(m.confirmMsg)
	} else if m.statusMsg != "" {
		status = m.statusMsg
	} else {
		staleCount := 0
		for _, wt := range m.worktrees {
			if wt.IsStale() {
				staleCount++
			}
		}
		status = dimStyle.Render(fmt.Sprintf("%d worktrees, %d stale, %d selected",
			len(m.worktrees), staleCount, len(m.selected)))
	}

	// Layout: list | preview
	listBox := lipgloss.NewStyle().
		Width(listWidth).
		Height(contentHeight).
		Render(listContent)

	previewBox := m.preview.View()

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		listBox,
		dimStyle.Render(" │ "),
		previewBox,
	)

	return searchBar + "\n" +
		strings.Repeat("─", m.width) + "\n" +
		body + "\n" +
		strings.Repeat("─", m.width) + "\n" +
		hints + "\n" +
		status
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd wt-explorer
go build ./...
```

Expected: compiles with no errors.

- [ ] **Step 4: Run all tests**

```bash
cd wt-explorer
go test -v ./...
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add wt-explorer/tui.go wt-explorer/go.mod wt-explorer/go.sum
git commit -m "feat(wt-explorer): bubbletea TUI with list, preview, selection, and delete"
```

---

### Task 4: Nix packaging and bash integration

Package `wt-explorer` as a Nix derivation, wire it into `wt`'s runtime, and update the bash script to exec into it.

**Files:**
- Create: `wt-explorer/default.nix`
- Modify: `wt/default.nix`
- Modify: `wt/wt.sh` (replace `wt_clean_interactive` with exec)
- Modify: `flake.nix`

- [ ] **Step 1: Create wt-explorer/default.nix**

Create `wt-explorer/default.nix`:

```nix
{pkgs}:
pkgs.buildGoModule {
  pname = "wt-explorer";
  version = "0.1.0";
  src = let
    inherit (pkgs) lib;
  in
    lib.cleanSourceWith {
      src = ./.;
      filter = path: _type:
        !builtins.elem (baseNameOf path) ["default.nix"];
    };
  vendorHash = "";
  ldflags = ["-s" "-w"];
}
```

Note: `vendorHash` will need to be updated after the first failed build — Nix will report the correct hash. This is the standard Go module Nix workflow.

- [ ] **Step 2: Update wt/default.nix to include wt-explorer**

Replace the contents of `wt/default.nix` with:

```nix
{
  pkgs,
  wt-explorer,
}:
pkgs.writeShellApplication {
  name = "wt";
  runtimeInputs = with pkgs; [git tmux gum zoxide wt-explorer];
  text = builtins.readFile ./wt.sh;
}
```

- [ ] **Step 3: Update flake.nix to build and wire both packages**

In `flake.nix`, update the `packages` section:

```nix
packages = let
  wt-explorer = import ./wt-explorer {inherit pkgs;};
in {
  default = tmuxConfig.tmux-wrapped;
  inherit wt-explorer;
  wt = import ./wt {inherit pkgs wt-explorer;};
};
```

- [ ] **Step 4: Update wt.sh — replace wt_clean_interactive with exec**

In `wt/wt.sh`:

1. Remove the entire `wt_clean_interactive` function (lines ~650-810 in the current file)
2. Update the `clean` dispatch to exec into wt-explorer:

```bash
clean | prune)
    if [[ $INTERACTIVE == "true" ]]; then
        exec wt-explorer "$(get_repo_root)"
    else
        wt_clean
    fi
    ;;
```

- [ ] **Step 5: Update home-manager module to pass wt-explorer**

In `modules/home-manager.nix`, update the `wtPkg` definition:

```nix
wt-explorer = import ../wt-explorer {inherit pkgs;};
wtPkg = import ../wt {inherit pkgs wt-explorer;};
```

- [ ] **Step 6: Build and verify**

```bash
nix build .#wt-explorer
nix build .#wt
```

The first `nix build .#wt-explorer` will likely fail with a vendor hash mismatch. Copy the correct hash from the error output and update `wt-explorer/default.nix`.

Then rebuild:

```bash
nix build .#wt-explorer
nix build .#wt
```

Expected: both build successfully. Verify the binary exists:

```bash
./result/bin/wt-explorer --help 2>&1 || true
```

Should print usage message to stderr.

- [ ] **Step 7: Test the full flow**

```bash
# Test wt clean -i launches the TUI
./result/bin/wt clean -i
```

Expected: the TUI launches showing all worktrees with stale ones at top. Navigation, selection, and deletion work.

- [ ] **Step 8: Run nix flake check**

```bash
nix flake check
```

Expected: all checks pass (shellcheck, shfmt, statix, deadnix, alejandra, typos).

- [ ] **Step 9: Commit**

```bash
git add wt-explorer/default.nix wt/default.nix wt/wt.sh flake.nix modules/home-manager.nix
git commit -m "feat(wt-explorer): Nix packaging and bash integration"
```

---

### Task 5: Clean up old bash interactive code and update help text

Remove the bash `wt_clean_interactive` function and related code that's been replaced by the Go TUI. Update help text.

**Files:**
- Modify: `wt/wt.sh`

- [ ] **Step 1: Remove wt_clean_interactive function**

In `wt/wt.sh`, delete the entire `wt_clean_interactive()` function (the one starting with `# Interactive worktree explorer for stale worktrees`).

- [ ] **Step 2: Update help text**

The help already has `wt clean -i` listed from our earlier change. Verify it reads:

```
  wt clean              Remove stale worktrees (merged, squash-merged, deleted)
  wt clean -i           Interactive explorer: inspect worktrees, force-remove
```

- [ ] **Step 3: Run shellcheck**

```bash
shellcheck wt/wt.sh
```

Expected: no warnings.

- [ ] **Step 4: Run full check**

```bash
nix flake check
```

Expected: all checks pass.

- [ ] **Step 5: Commit**

```bash
git add wt/wt.sh
git commit -m "refactor(wt): remove bash interactive mode, replaced by wt-explorer"
```

---

### Task 6: Manual integration test

End-to-end verification that the full workflow works.

- [ ] **Step 1: Build everything**

```bash
nix build .
nix build .#wt
nix build .#wt-explorer
```

- [ ] **Step 2: Test wt clean (non-interactive)**

```bash
./result/bin/wt clean
```

Expected: normal stale detection runs, shows results, tip mentions `wt clean -i`.

- [ ] **Step 3: Test wt clean -i (interactive)**

```bash
./result/bin/wt clean -i
```

Expected: TUI launches, shows all worktrees sorted (stale first), preview pane shows details. Test:
- Arrow keys navigate
- Space toggles selection
- `a` selects all stale
- `d` prompts for delete
- `D` prompts for force delete
- Typing filters the list
- `q` exits

- [ ] **Step 4: Test direct invocation**

```bash
wt-explorer "$(git rev-parse --show-toplevel)"
```

Expected: same TUI launches.

- [ ] **Step 5: Commit any fixes**

If any issues found during testing, fix and commit.
