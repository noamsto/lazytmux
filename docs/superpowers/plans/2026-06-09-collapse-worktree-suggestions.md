# Collapse Worktree Suggestions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fold worktrunk worktree directories onto their repo root in the `prefix+s` zoxide suggestions, so the picker offers one row per repo (landing on the root session) instead of one row per visited worktree.

**Architecture:** Add one pure helper, `collapseWorktree`, that maps `<repo>/.worktrees/<branch>[/…]` to `<repo>`. Call it inside `collectZoxide` right after `normalizePath`, before `isExcluded`/`os.Stat`, so exclude and existence checks operate on the repo root. The existing name/session dedup in `zoxideSuggestions` then folds the collapsed duplicates with no new dedup logic.

**Tech Stack:** Go (bubbletea picker under `picker/`), `go test`, built via `buildGoModule` (`nix build` / `nix flake check` run `go test ./...`).

**Spec:** `docs/superpowers/specs/2026-06-09-collapse-worktree-suggestions-design.md`

---

## Prerequisite: isolated worktree

Before Task 1, create an isolated worktree for this work (per superpowers:using-git-worktrees). The repo uses worktrunk:

```bash
wt switch -c feat/collapse-worktree-suggestions
```

All tasks run inside that worktree. Do not implement on `main` (it currently carries an unrelated uncommitted change in `modules/home-manager.nix`).

---

## File Structure

- **`picker/zoxide.go`** — add `collapseWorktree`; add one line to `collectZoxide`. (Currently holds all zoxide-suggestion logic; this is the right home.)
- **`picker/zoxide_test.go`** — add `TestCollapseWorktree` and `TestCollapseThenSuggest`.

No new files. No other files touched.

---

### Task 1: `collapseWorktree` helper

**Files:**
- Modify: `picker/zoxide.go` (add function; near the other path helpers, e.g. after `normalizePath`)
- Test: `picker/zoxide_test.go` (add `TestCollapseWorktree`)

- [ ] **Step 1: Write the failing test**

Add to `picker/zoxide_test.go`:

```go
func TestCollapseWorktree(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/n/git/lazytmux/.worktrees/feat-x", "/home/n/git/lazytmux"}, // worktree -> root
		{"/home/n/git/lazytmux/.worktrees/feat-x/sub/dir", "/home/n/git/lazytmux"}, // subdir of worktree
		{"/home/n/git/lazytmux", "/home/n/git/lazytmux"}, // non-worktree unchanged
		{"/home/n/notes/.worktrees-backup/x", "/home/n/notes/.worktrees-backup/x"}, // not the ".worktrees/" segment
		{"/.worktrees/x", ""}, // degenerate: no root segment
	}
	for _, c := range cases {
		if got := collapseWorktree(c.in); got != c.want {
			t.Errorf("collapseWorktree(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test -run TestCollapseWorktree ./...`
Expected: FAIL — `undefined: collapseWorktree`.

- [ ] **Step 3: Write minimal implementation**

Add to `picker/zoxide.go` (after `normalizePath`):

```go
// collapseWorktree maps a worktrunk worktree path back to its repo root.
// Worktrunk lays worktrees out as "<repo>/.worktrees/<branch>", so the segment
// before "/.worktrees/" is the root; a subdir of a worktree collapses too
// (first occurrence wins). Paths without that segment are returned unchanged.
func collapseWorktree(p string) string {
	if i := strings.Index(p, "/.worktrees/"); i != -1 {
		return p[:i]
	}
	return p
}
```

(`strings` is already imported in `zoxide.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test -run TestCollapseWorktree ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/zoxide.go picker/zoxide_test.go
git commit -m "feat(picker): add collapseWorktree path helper"
```

---

### Task 2: Pipeline characterization test (collapse → dedup → session suppression)

**Files:**
- Test: `picker/zoxide_test.go` (add `TestCollapseThenSuggest`)

This test mirrors `collectZoxide`'s pipeline purely (collapse each path, then run `zoxideSuggestions`) so the intended end-to-end behavior is pinned without exec'ing `zoxide`. It uses a repo name (`delta`) **absent** from the session maps so it exercises collapse-dedup, plus a second repo (`epsilon`) whose root **is** a live session to exercise suppression.

- [ ] **Step 1: Write the test**

Add to `picker/zoxide_test.go`:

```go
func TestCollapseThenSuggest(t *testing.T) {
	// Mirror collectZoxide: collapse every path, then suggest.
	raw := []string{
		"/home/n/git/delta/.worktrees/feat-a",   // -> /home/n/git/delta
		"/home/n/git/delta/.worktrees/feat-b",   // -> same root, deduped away
		"/home/n/git/epsilon/.worktrees/wip",    // -> /home/n/git/epsilon, suppressed (live session)
	}
	var paths []string
	for _, p := range raw {
		paths = append(paths, collapseWorktree(p))
	}
	sessionPaths := map[string]bool{"/home/n/git/epsilon": true}

	got := zoxideSuggestions(paths, sessionPaths, nil, 15)
	want := []suggestion{{path: "/home/n/git/delta", name: "delta"}}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run the test**

Run: `cd picker && go test -run TestCollapseThenSuggest ./...`
Expected: PASS (both `collapseWorktree` and `zoxideSuggestions` already exist; this characterizes their composition).

- [ ] **Step 3: Commit**

```bash
git add picker/zoxide_test.go
git commit -m "test(picker): pin collapse->dedup->session-suppression pipeline"
```

---

### Task 3: Wire `collapseWorktree` into `collectZoxide`

**Files:**
- Modify: `picker/zoxide.go` — inside `collectZoxide`'s line loop

This is the production behavior change. The filtering loop in `collectZoxide` is not unit-testable (it execs `zoxide` and stats the real filesystem), so it is verified by reading the diff plus the manual smoke test in Task 4. The placement — after `normalizePath`, before `isExcluded`/`os.Stat` — is load-bearing (see spec: stale-worktree and basename-exclude correctness).

- [ ] **Step 1: Make the edit**

In `collectZoxide`, the current loop body begins:

```go
		p := normalizePath(l)
		if isExcluded(p, exclude) {
			continue
		}
```

Insert the collapse call immediately after `normalizePath`:

```go
		p := normalizePath(l)
		p = collapseWorktree(p)
		if isExcluded(p, exclude) {
			continue
		}
```

- [ ] **Step 2: Run the full suite + build the picker**

Run: `cd picker && go test ./...`
Expected: PASS (all existing tests plus the two new ones).

Run: `cd picker && go build ./...`
Expected: builds cleanly, no errors.

- [ ] **Step 3: Commit**

```bash
git add picker/zoxide.go
git commit -m "feat(picker): collapse worktree dirs to repo root in zoxide suggestions"
```

---

### Task 4: Build via Nix and manual verification

**Files:** none (verification only)

- [ ] **Step 1: Nix check runs the Go tests**

Run: `nix flake check` (or `nix build .#<picker-package>` — see `picker/default.nix`)
Expected: `buildGoModule`'s check phase runs `go test ./...` and passes.

- [ ] **Step 2: Manual smoke test**

In a repo that has at least one checked-out worktree you've `cd`'d into (so the worktree is in zoxide), rebuild the picker into your environment and open the session picker:

- Press `prefix+s`.
- Confirm the repo shows as a **single** suggestion row at its root (basename = repo name), not one row per worktree, and that no `feat-…` / branch-named rows appear.
- Press Enter on it and confirm you land on a session at the repo root (creating it if absent, attaching if present).
- Confirm a repo whose root session is already live does **not** appear as a suggestion.

- [ ] **Step 3: Finish the branch**

Use superpowers:finishing-a-development-branch to merge or open a PR for `feat/collapse-worktree-suggestions`.

---

## Self-Review

**Spec coverage:**
- Detection (path-based `/.worktrees/` split) → Task 1. ✓
- Call site in `collectZoxide` after `normalizePath`, before exclude/stat → Task 3. ✓
- Reuse existing dedup; across-worktrees, root's-own-entry, live-session suppression → Task 2 (delta dedup + epsilon suppression). ✓
- Subdir-of-worktree collapse → Task 1 case. ✓
- Degenerate `/.worktrees/x` → `""` edge → Task 1 case. ✓
- Coincidental non-`/.worktrees/` segment not collapsed → Task 1 `.worktrees-backup` case. ✓
- Stale-worktree / basename-exclude correctness → consequence of Task 3 placement; called out, verified by reading + Task 4 smoke test (not unit-testable across the exec boundary). ✓
- Nix check runs tests; manual TUI verification → Task 4. ✓
- Out of scope (window mode, `createAndSwitch`) → untouched, no task. ✓

**Placeholder scan:** none.

**Type consistency:** `collapseWorktree(string) string`, `suggestion{path, name string}`, `zoxideSuggestions(paths []string, sessionPaths, sessionNames map[string]bool, limit int) []suggestion` — all match the existing signatures in `picker/zoxide.go`.
