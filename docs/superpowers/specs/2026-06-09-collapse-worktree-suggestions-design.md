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

Apply `collapseWorktree` inside `collectZoxide`, **immediately after
`normalizePath`, before `isExcluded` and `os.Stat`**:

```go
for _, l := range lines {
	p := normalizePath(l)
	p = collapseWorktree(p)
	if isExcluded(p, exclude) {
		continue
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		continue
	}
	paths = append(paths, p)
}
```

Collapsing here (not in `zoxideSuggestions`) is the deliberate choice — it puts
**both** the exclude check and the stat on the repo root, which fixes two bugs
that collapsing later would leave:

- **Stale worktree → lost repo.** A worktree removed from disk but still ranked
  in zoxide would fail `os.Stat` on its own path and be dropped entirely.
  Collapsing first stats the repo root (still present), so the repo correctly
  surfaces as a suggestion instead of vanishing.
- **Basename exclude.** `isExcluded` matches a pattern against the path's
  basename; on a worktree path that basename is the branch (`feat-x`), not the
  repo. Collapsing first means a bare-basename exclude of the repo
  (`lazytmux`) hides its worktree-derived rows too, matching intent. (Absolute
  and subtree excludes already worked either way.)

`zoxideSuggestions` then receives already-collapsed paths and its existing dedup
does the rest, unchanged:

- **Across a repo's worktrees:** every worktree collapses to the same root →
  same derived name → the existing `seen[name]` keeps the highest-ranked
  occurrence and drops the rest. One row per repo.
- **Against the root's own zoxide entry:** if `<repo>` is itself ranked, it
  collapses to itself and folds into the same name-dedup.
- **Against a live session:** the collapsed root is checked against
  `sessionPaths` / `sessionNames`. `#{session_path}` is the session's
  creation directory — under the session=repo model that is the repo root
  (verified live: a `lazytmux` session whose active pane sits in a worktree
  still reports the repo root). So an existing repo session suppresses the
  suggestion — the attach-don't-recreate behavior.
- **Rank order:** zoxide output is frecency-ordered and the loop iterates in
  order, so the most-recently-used worktree wins the repo's row.

### Known limitations (accepted, not bugs)

- **Name-collision amplification.** Pre-collapse, worktree rows carried unique
  branch basenames that never collided. Post-collapse they become repo-name
  rows, so they now participate in the existing basename dedup — e.g. a
  `factify/lazytmux` worktree is suppressed when a `noamsto/lazytmux` session is
  live. This is inherent to the session=repo model and the pre-existing
  name-dedup tradeoff, not a regression of this change.
- **Coincidental `.worktrees` dir.** A non-worktrunk directory literally named
  `.worktrees` (e.g. `~/notes/.worktrees/foo`) would collapse to `~/notes`.
  Worktrunk owns this path convention, so this is treated as a non-case — no
  guard added.

### What stays unchanged

- **`zoxideSuggestions`** keeps its current body; it just receives collapsed
  paths. `collapseWorktree` is independently pure and unit-tested, so moving the
  call into the impure `collectZoxide` costs no meaningful test coverage.
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
- A `zoxideSuggestions`-level test feeding **already-collapsed** paths (mirroring
  what `collectZoxide` now produces) proving:
  - two worktrees of one repo, both collapsed to the same root, yield a single
    deduped row — use a repo name **absent** from the session maps so this
    exercises collapse-dedup, not session suppression (the existing
    `TestZoxideSuggestions` fixture already has `lazytmux` in `sessionNames`, so
    reusing it would only prove suppression);
  - a collapsed root that matches a live session's path is suppressed.

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
| Where collapse runs | In `collectZoxide`, after `normalizePath`, before `isExcluded`/`Stat` — so exclude + stat see the root (fixes stale-worktree and basename-exclude cases) |
| Subdir of a worktree | Collapses to repo root too (first `/.worktrees/` wins) |
| Stale worktree in zoxide | Collapses to live repo root and surfaces, instead of being dropped by a failing stat |
| Exclude interaction | Exclude runs on the collapsed root; absolute/subtree/basename excludes of the repo all hit |
| Name collision | Accepted: worktree rows now fold into repo-name dedup (see Known limitations) |
