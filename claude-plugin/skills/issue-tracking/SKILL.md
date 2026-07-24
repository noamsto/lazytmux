---
name: issue-tracking
description: Use when working on a Linear/GitHub issue or PR whose branch is NOT the current tmux window's branch — orchestrating from main, spawning agents into worktrees, or driving PRs via gh/MCP — or when you just created an issue/PR on the CURRENT window's branch mid-session. Stamps issue ids into the tmux status bar.
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

## After creating an issue or PR mid-session

`issue add`/`done`/`clear` above are for a self-reported *badge* — tracking
issues you're orchestrating on OTHER windows/branches. This is different: when
you create a new issue or PR on the branch you're **currently sitting in**
(`gh issue create`, `gh pr create`, or the Linear MCP), the window's real
`@issue_*` identity stamp doesn't know about it yet — the branch may predate
the issue, so nothing re-derives it until the next branch change. Re-stamp
this window immediately:

```
claude-status-update enrich <ID>
```

Run this right after the `gh`/Linear MCP call that returns the new id — you
already have it in hand, so there's no need to re-fetch it. Omit `<ID>` to
re-derive from the branch instead. Skip silently if `claude-status-update` is
not on PATH (same as above).

## Id format

Same convention for `issue add`/`done` and `enrich`. Ids must match
`[A-Za-z0-9_-]+`:

- Linear: the key verbatim — `ENG-123`
- GitHub: `GH-<number>` — `GH-42` (never `#42`). PRs share the issue number space: a PR with no linked issue is stamped by its PR number, e.g. `GH-57`.

If `claude-status-update` is not on PATH (not inside a lazytmux tmux), skip
silently — do not report an error to the user.
