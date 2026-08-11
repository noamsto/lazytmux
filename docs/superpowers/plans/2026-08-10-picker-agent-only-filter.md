# Picker `^a` Filters All Agent Windows, Not Just Claude

**Issue:** [#342](https://github.com/noamsto/lazytmux/issues/342) — the picker's `^a` toggle
(footer label `claude`) filters to hook-written Claude panes only, so codex/cursor-agent panes
detected by the screen-scraper are invisible to it.

## Cause

`collectClaudePanes()` (`picker/main.go`) read exactly one directory,
`/tmp/claude-status/panes/` — the hook-written state. The screen-scraper's output for codex /
cursor-agent lives in `/tmp/claude-status/screen/`, written by `agentdetect`, and nothing under
`picker/` outside `agentdetect` read it. The shell side already merges both sources correctly:
`read_pane_state` (`scripts/lib-claude.sh`) reads `panes/` and `screen/` together, hook-first,
with `waiting`/`error`/`denied` protected from a screen override. The Go picker was the side that
never got the second source — `screen/` only started populating on a nix host once #333 (shell
arming) and #340 (Go `ForCommand` match) both landed, so this went unnoticed until now.

## Fix

- `collectAgentPanes()` (renamed from `collectClaudePanes`) now reads both `panes/` and `screen/`
  and merges them per pane id, mirroring `read_pane_state`'s **state** precedence exactly:
  - A hook state wins outright, **unless** it is `compacting`/`processing`/`done` (the only
    states a missed completion hook can leave stuck) **and** it is stale past its own
    `CLAUDE_STALE_*`-equivalent threshold **and** a screen reading exists — then the screen state
    (and its timestamp) wins, and `unseen` clears, matching the shell's override branch.
  - `waiting`/`error`/`denied` are never overridden by screen, at any age — they look identical
    to `idle` on screen and the shell protects them for the same reason.
  - One deliberate divergence: `read_pane_state` also blanks its `session` field on a screen
    override, which drops the pane from the status bar's per-session tally while its
    live-enumerated per-window tally still counts it — the shell has two independent counting
    paths. The Go picker has one: `aggregateAgentBySession`/`aggregateAgentByWindow` both key off
    the same `agentPaneInfo.session`, so blanking it would drop the pane from both, silently
    undoing the fix for exactly the panes it's meant to surface. Instead, an override re-resolves
    `session`/`winIdx` from the live pane map (same as the scraper-only path below) rather than
    trusting the stale hook write or blanking it.
  - A screen-only pane (no hook file at all — codex/cursor) uses the screen state directly. Since
    a screen file carries no `session` field, its session/window index are resolved from the live
    tmux pane map instead (a hook pane keeps using its own self-reported `session`, unchanged).
  - The collection function is split into `collectAgentPanesFrom(hookDir, screenDir, issuesDir,
    paneMap, now)` with `collectAgentPanes()` as a thin wrapper over the real paths/clock/tmux —
    so the merge precedence is testable as pure Go with no `/tmp` state or tmux process involved.
- Renamed the Claude-specific identifiers that implied the old behavior: `claudeOnly` →
  `agentOnly`, `toggleClaudeOnly` → `toggleAgentOnly`, `collectClaudePanes` →
  `collectAgentPanes`, `claudePaneInfo` → `agentPaneInfo`, `claudeCounts` → `agentCounts`,
  `claudePriority` → `agentPriority`, `hasActiveClaude` → `hasActiveAgent`,
  `claudeStateOrder`/`claudeStateLabel`/`claudeStateCount` → `agentState*`, plus the
  `sessionData`/`windowData` `claude` field → `agent`, and the footer label `claude` → `agents`.
  `^a` stays the key. The `bind a` tmux keybinding and its `tmux-window-picker` wrapper script
  carried the same `--claude` CLI flag/title, both renamed to `--agent`/"Agent Windows".
- Swept `groupWindowsByState` (#229/#334, the state-grouped `ctrl+g` mode): it already grouped by
  `agentStateOrder`, which now naturally includes scraper-detected states through the same merged
  `collectAgentPanes()` — no separate priority list was introduced, since the issue's constraint
  was exactly to avoid a second, hand-copied ordering.
- Renamed `appendClaudeIcon` → `appendAgentIcon` (it renders the state dot for any agent, not a
  per-agent glyph) but left `claudeColors`/`claudeStateIcon` (the underlying color/icon tables,
  build-time generated from `config/process-icons.nix`) and `icons_generated.go`'s `"claude"`
  process-icon entry untouched — per-agent glyphs (showing *which* agent a row is) are an
  explicit, deliberate follow-up per the issue, not part of this change; the state dot itself was
  already agent-neutral.

## Testing

`picker/agent_panes_test.go` (new) exercises `collectAgentPanesFrom` directly with `t.TempDir()`
fixtures and an injected pane map — pure Go, no tmux:

- `TestCollectAgentPanesMergesHookAndScreen` — a hook-written pane, a scraper-only pane, and a
  pane with neither file all resolve correctly (present/present/absent).
- `TestCollectAgentPanesHookFirstPrecedence` — a `waiting` hook state with a much newer, fresher
  screen `idle` reading still wins as `waiting`. Verified this catches a divergent
  implementation: temporarily swapping the merge for "most recent timestamp wins" fails this test
  (screen's newer timestamp wins the case wrongly), confirming it pins the hook-first rule rather
  than passing by construction.
- `TestCollectAgentPanesScreenOverridesStaleHook` — a `processing` hook state stale past its
  threshold, with a live screen reading available, is corrected to the screen's state and clears
  `unseen`.
- `TestCollectAgentPanesOverrideRefreshesSessionFromPaneMap` — pins the session-refresh-on-override
  behavior above: a stale hook's self-reported session is discarded in favor of the live pane
  map's session/window index once the screen reading takes over.

`picker/main_test.go` and `picker/tui_test.go`'s existing `agentPriority`/`agentStateOrder`/
toggle-visibility fixtures were renamed in place (`TestClaudePriority` → `TestAgentPriority`,
`"claude only"` subtests → `"agent only"`, etc.) but their assertions are unchanged.
