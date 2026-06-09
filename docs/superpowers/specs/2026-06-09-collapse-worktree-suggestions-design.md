# Collapse Worktree Suggestions in the Session Picker

**Date:** 2026-06-09
**Status:** Approved
**Builds on:** [2026-06-07 Zoxide Suggestions in the Session Picker](./2026-06-07-zoxide-session-picker-design.md)

## Summary

Teach the `prefix+s` zoxide suggestions to fold worktree directories onto their
repo root. Zoxide tracks every directory the shell visits, including worktrunk
worktrees (`<repo>/.worktrees/<branch>`), so each visited worktree currently
surfaces as its own suggestion row. That fights the picker's session model
(**Session = repo, Window = worktree**): the picker should offer one row per
repo, landing on the root session, never a row per worktree.

## Motivation

The session picker already dedupes zoxide dirs against live sessions and against
each other by derived name. But a worktree path and its repo root are *different*
paths with *different* basenames (`feat-32-claude-image-pane` vs `lazytmux`), so
the existing dedup never folds them. A repo with three checked-out worktrees you
have all `cd`'d into shows up as three unrelated suggestion rows, none of which
is the repo root — the opposite of the intended "create/attach the repo session"
flow.

## Design

### Detection — path-based, not git-based

Worktrunk lays worktrees out from a fixed template
(`worktree-path = "{{ repo_path }}/.worktrees/{{ branch | sanitize }}"`), so the
repo root is exactly the path segment before `/.worktrees/`. This is the
authoritative convention for every worktree in play, so a pure string transform
is correct and cheap. The alternative — shelling out to `git rev-parse
--git-common-dir` per candidate — would add ~30 subprocess calls per picker
open, plus error paths, and would only matter for non-worktrunk worktrees, which
do not exist in this setup. Rejected on cost and YAGNI.

### New pure function (`picker/zoxide.go`)

```go
// collapseWorktree maps a worktrunk worktree path back to its repo root.
// Worktrunk lays worktrees out as "<repo>/.worktrees/<branch>", so the segment
// before "/.worktrees/" is the root. Paths with no such segment (and subdirs of
// a worktree) pass through / collapse correctly. Non-worktree paths are
// returned unchanged.
func collapseWorktree(p string) string {
	if i := strings.Index(p, "/.worktrees/"); i != -1 {
		return p[:i]
	}
	return p
}
```

`strings.Index` finds the first occurrence, so a path nested under a worktree
(`<repo>/.worktrees/<branch>/sub/dir`) still collapses to `<repo>`.

### Call site (`picker/zoxide.go`)

Apply `collapseWorktree` at the **top of the `zoxideSuggestions` loop**, before
the session-path and name checks:

```go
for _, p := range paths {
	p = collapseWorktree(p)
	if sessionPaths[p] {
		continue
	}
	name := sessionNameFromPath(p)
	...
}
```

Placing it there lets the existing dedup machinery do all the work — no new
dedup logic:

- **Across a repo's worktrees:** every worktree collapses to the same root →
  same derived name → the existing `seen[name]` keeps the highest-ranked
  occurrence and drops the rest. One row per repo.
- **Against the root's own zoxide entry:** if `<repo>` is itself in zoxide, it
  collapses to itself and folds into the same name-dedup.
- **Against a live session:** the collapsed root is checked against
  `sessionPaths` / `sessionNames`, so if the repo session already exists the
  suggestion is suppressed — exactly the attach-don't-recreate behavior.
- **Rank order:** zoxide output is frecency-ordered and the loop iterates in
  order, so the most-recently-used worktree wins the repo's row.

### What stays unchanged

- `collectZoxide` (the impure `exec`/`os.Stat` half) is untouched. Collapse
  lives entirely in the pure, unit-tested `zoxideSuggestions` path.
- **Exclude patterns:** `isExcluded` already subtree-matches, so excluding
  `<repo>` continues to drop its worktrees — no interaction with collapse.
- **`createAndSwitch`:** a collapsed row's `path` is the repo root and `name` is
  the repo basename, so Enter creates/attaches the root session as intended. No
  change needed.
- **Window mode (`prefix+w`)** and all other picker behavior are out of scope.

## Testing

`collapseWorktree` is pure, so coverage is straightforward (`picker/zoxide_test.go`):

- `TestCollapseWorktree` table:
  - `<repo>/.worktrees/<branch>` → `<repo>`
  - `<repo>/.worktrees/<branch>/sub/dir` → `<repo>` (subdir of a worktree)
  - non-worktree path → unchanged
  - edge: `/.worktrees/x` → `""` (no crash; downstream `sessionNameFromPath`
    yields `""` and the row is filtered)
- Extend `TestZoxideSuggestions` with a worktree scenario proving:
  - two worktrees of one repo collapse to a single deduped row, and
  - a worktree whose root is already a live session is suppressed.

`buildGoModule`'s default check phase runs `go test ./...` on `nix build` /
`nix flake check`, so no harness wiring is needed. Manual verification: open
`prefix+s` in a repo with multiple checked-out worktrees and confirm a single
repo row appears (not one per worktree) and Enter lands on the root session.

## Decisions Log

| Question | Decision |
|----------|----------|
| Worktree handling | Collapse to repo root (fold worktree rows onto one repo row) |
| Detection method | Path-based (`/.worktrees/` split), not `git rev-parse` |
| Dedup mechanism | Reuse existing name/session dedup — collapse upstream of it, add nothing |
| Where collapse runs | Top of `zoxideSuggestions` loop (pure, testable); `collectZoxide` unchanged |
| Subdir of a worktree | Collapses to repo root too (first `/.worktrees/` wins) |
| Exclude interaction | None — `isExcluded` subtree match already covers worktrees |
