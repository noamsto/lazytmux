---
name: issue-tracking
description: Use when working on a Linear/GitHub issue or PR whose branch is NOT the current tmux window's branch — orchestrating from main, spawning agents into worktrees, or driving PRs via gh/MCP. Stamps issue ids into the tmux status bar.
---

# Issue Tracking

lazytmux shows which issues this Claude Code pane is working on in the
tmux status bar (line 0) and in the session/window pickers.

## When to stamp

Only when the issue's branch is NOT the current window's branch — the
branch-derived stamp already covers the matching case.

- Picking up work on an issue/PR: `claude-status-update issue add <ID>`
- Issue merged / work finished: `claude-status-update issue done <ID>`
- Abandoning the whole batch: `claude-status-update issue clear`

Stamp every issue you orchestrate in parallel (one `issue add` per id).

## Id format

Ids must match `[A-Za-z0-9_-]+`:

- Linear: the key verbatim — `ENG-123`
- GitHub: `GH-<number>` — `GH-42` (never `#42`). PRs share the issue number space: a PR with no linked issue is stamped by its PR number, e.g. `GH-57`.

If `claude-status-update` is not on PATH (not inside a lazytmux tmux), skip
silently — do not report an error to the user.
