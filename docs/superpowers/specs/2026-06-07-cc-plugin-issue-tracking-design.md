# Claude Code Plugin: Packaging, Status Hooks, and Issue Self-Report

**Date:** 2026-06-07
**Issue:** [#8](https://github.com/noamsto/lazytmux/issues/8)
**Status:** Approved

## Summary

A CC session working from `main` on specific Linear/GitHub issue PRs
(orchestrators, subagents, hidden Agent-tool worktrees) is invisible —
enrichment is strictly branch-derived, so the window shows `main` with a
spinner. Fix: CC self-reports the issues it is touching via a new
`claude-status-update issue` subcommand; the ids live in per-pane files next
to the existing state files and display on status line 0 and in the picker. Delivery: package a
Claude Code plugin in-repo (skill + full claude-status hook set), with the
repo doubling as its own plugin marketplace. Nix users load the plugin via
`--plugin-dir`; everyone else via the marketplace.

## Motivation

- `tmux-issue-stamp` regex-matches the **window's branch**; on `main` no
  provider matches and the stamp is cleared. `@pr_*` follows the branch too.
- Only CC itself knows which issues it is working on — git-side inference
  cannot see Agent-tool temp worktrees, and the orchestrator pane's cwd never
  changes. So the data must flow from CC into tmux (self-report), not be
  detected. Hook auto-detection was considered and rejected: weak exactly
  where the workflow lives (Linear MCP, Agent tool), and false positives
  (mentioning an id ≠ working on it) are worse than no badge.
- The CC side of lazytmux today is "user wires hooks manually". A plugin
  bundles hooks + skill as one installable, versioned unit.

## Design

### 1. CLI + storage (`scripts/claude-status-update.sh`)

New subcommand (first positional arg `issue`):

```bash
claude-status-update issue add ENG-123    # append to issues= (deduped)
claude-status-update issue done ENG-123   # remove from issues=
claude-status-update issue clear          # drop the issues= line
```

- Stored as a comma list (`ENG-123,ENG-456`) in a **separate file**
  `/tmp/claude-status/issues/<pane_id>` — NOT in the pane state file. The
  state machine's hooks fire `processing` writes around the very Bash call
  that runs `issue add`, so keeping issues inside the pane file would require
  read-modify-write with locking to avoid a guaranteed lost update. A
  separate file defines the race out of existence: state writes and issue
  ops never touch each other's files.
- **Lifecycle:** the `clear` state, `cleanup_stale_panes`, and `SessionEnd`
  remove the issues file alongside the pane file. Readers (`claude-status`,
  picker) read both. Issues survive `/clear`/compaction (CC usually
  continues the same work) and die with the pane or CC session.
- **Id validation:** `[A-Za-z0-9_-]+` only. Rejecting `#` (and everything
  else) keeps ids tmux-format-safe with no escaping; GitHub issues are stamped
  as e.g. `GH-123` by the skill, not `#123`.
- Issue ids are exempt from staleness dimming: they persist until `issue
  done`/`clear` or pane death. They are work identity, not liveness.
- No titles, ids only (YAGNI — the id is the indication; titles live in the
  tracker).

### 2. Status line 0 display (`scripts/claude-status.sh`)

Line 0 already calls `claude-status --session '#{session_name}' --format
icon-color`. Extend it to append the union of issue lists across the
session's panes after the state icon: dim (`overlay_1`), space-separated, deduped,
capped at 3 ids then `+N` (e.g. ` ENG-123 ENG-456 +2`). Empty list → output
unchanged from today. No new `#()` slot, no new format machinery.

### 3. Picker display (`picker/`)

The Go picker already reads the pane state files for claude status. Window
rows (and session rows in session mode) append the same dim id list, capped
at 2 ids + `+N` (picker columns are tighter). Pane→window mapping comes from
the tmux data the picker already collects.

### 4. Plugin packaging (in-repo marketplace)

```
lazytmux/
├── .claude-plugin/marketplace.json   # name "lazytmux", owner noamsto,
│                                     # plugins: [{name "lazytmux", source "./claude-plugin"}]
└── claude-plugin/                    # NOT "plugin/" — repo root already has
    │                                 # plugins/ (opencode), avoid the near-collision
    ├── .claude-plugin/plugin.json    # name "lazytmux", version, description
    ├── skills/
    │   ├── issue-tracking/SKILL.md   # NEW (see §6)
    │   └── tmux-interactive/         # MOVED from skills/
    ├── hooks/hooks.json              # claude-status state machine (see §5)
    └── scripts/status.sh             # degrade-gracefully hook wrapper
```

- `skills/` (repo root) goes away; the home-manager `skills.enable` option
  re-points to `../claude-plugin/skills` so existing Nix users see no change.
  Users who load the plugin should set `skills.enable = false` to avoid the
  skill appearing twice (documented in README + option description).
- Skill namespace when installed: `lazytmux:issue-tracking`,
  `lazytmux:tmux-interactive`.

### 5. Hooks (`plugin/hooks/hooks.json`)

Ports the claude-status state machine (today living in the user's Nix
`--settings` overlay; personal hooks like statusline/psql-guard stay there):

| Event | Action |
|-------|--------|
| `SessionStart` (startup/resume/clear) | `cleanup` + `idle`; (compact) → `cleanup` + `processing` |
| `UserPromptSubmit` | `processing --force` |
| `PreToolUse`, `PostToolUse`, `PostCompact`, `ElicitationResult` | `processing` |
| `Notification` (permission_prompt / idle_prompt) | `waiting` / `done` |
| `Stop` / `StopFailure` | `done` / `error` |
| `PreCompact` | `compacting` |
| `PermissionDenied` / `PostToolUseFailure` | `denied` / `error` |
| `Elicitation` | `waiting` |
| `SessionEnd` | `clear` |

All commands route through `"${CLAUDE_PLUGIN_ROOT}"/scripts/status.sh <args>`,
which execs `claude-status-update "$@"` when it is on PATH and exits 0
otherwise — CC outside tmux or without lazytmux installed stays silent
instead of erroring on every hook.

### 6. Skill (`claude-plugin/skills/issue-tracking/SKILL.md`)

Short and imperative: when picking up work on a Linear/GitHub issue or PR
whose branch is **not** the current window's branch (orchestrating from main,
spawning agents into worktrees, driving PRs via `gh`/MCP), run
`claude-status-update issue add <ID>`; on merge/completion run `issue done
<ID>`; `issue clear` when abandoning the batch. GitHub issues use the `GH-`
prefix. Skip when the window's branch already matches the issue (the
branch-derived stamp covers that).

### 7. Install routes

| Audience | Mechanism | Updates |
|----------|-----------|---------|
| Nix users | `claude --plugin-dir "${inputs.lazytmux}/claude-plugin"` in their claude wrapper (the flake source is already a store path; no package output needed) | `nix flake update` — plugin and tmux scripts pinned to the same revision, can never skew |
| Non-Nix | `claude plugin marketplace add noamsto/lazytmux` → `claude plugin install lazytmux@lazytmux` | CC auto-pull / `/plugin update`; §5 wrapper covers skew |

Verified: `--plugin-dir` loads the full plugin (hooks, skills, commands,
MCP) and never writes into the plugin dir — read-only store paths are safe.

### 8. Follow-up (nix-config, separate task)

- Remove the status-hook block from `nix-settings-json` (keep statusline,
  psql-guard, skill-suggester) — avoids double-firing.
- Add `--plugin-dir` to the claude wrapper; set
  `programs.lazytmux.skills.enable = false`.

## Error handling

- Invalid issue subcommand/id → error to stderr, exit 1 (matches existing
  invalid-state handling).
- No `$TMUX_PANE` → silent exit 0 (existing behavior).
- `claude-status-update` missing → hook wrapper exits 0 silently.

## Testing

- `tests/claude-issues.bats` (runs in `nix flake check`, same pattern as
  `enrich.bats`): issue add/done/clear list manipulation, dedupe, id
  validation, and lifecycle (the `clear` state and cleanup remove the issues
  file; state writes never touch it).
- `tests/claude-issues.bats` also covers display end-to-end: fixture pane +
  issues files, assert `claude-status --session` output contains the id list
  (`test-display.sh` is the wrong surface — it diffs window names, not line 0).
- Go picker: unit test for the id-list truncation/format helper.
- Plugin smoke test: `claude --plugin-dir ./plugin` loads hooks + skills
  (manual; CC not available in CI).

## Out of scope

- Auto-detection of issues from hook payloads (revisit only if forgetting to
  stamp proves annoying; layers on without changing storage/display).
- Issue titles/URLs in the pane files.
- Changes to the branch-derived `@issue_*`/`@pr_*` pipeline — this feature is
  a separate, pane-scoped fact ("what CC is touching"), and the window-option
  invariant stands.
- The nix-config migration itself (§8 is a pointer, not part of this repo's
  diff).
