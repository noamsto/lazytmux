# Honeymux-Borrowed Features (Deferred)

**Date:** 2026-04-26
**Status:** Deferred — captured for later prioritization
**Repo:** `~/Data/git/lazytmux`
**Source:** Comparison against [honeymux/honeymux](https://github.com/honeymux/honeymux) (Apache 2.0 TUI wrapper for tmux)

## Overview

Honeymux is a TUI host built on OpenTUI + libghostty-vt that owns its own rendering (sidebar, toolbar, dialogs) and runs tmux underneath. Most of its surface area (sidebar, SSH pane stitching, in-app config dialogs) is out of scope for lazytmux, which is a pure tmux-config-as-Nix-package. A small set of its agent-monitoring ideas are tractable inside our existing architecture and would meaningfully improve the multi-agent multitasking story.

This spec captures those ideas for later. None are committed.

## Architectural Constraint

Anything adopted here must fit lazytmux's invariants:

- Rendered through tmux's own status bar / pickers — no custom VT layer.
- State files keep the existing `key=value` format under `/tmp/claude-status/panes/<pane_id>` so all aggregation in `claude-status` keeps working unchanged.
- Hooks are opt-in and user-installed (no auto-overwriting on upgrade, unlike honeymux's first-run consent flow).
- Hot paths stay subshell-free (the `REPLY`/`REPLY_DW` pattern in `lib-icons.sh` / `lib-claude.sh`).

## Candidate Features

### 1. Multi-agent adapters (Codex, Gemini, OpenCode)

**What honeymux does:** Supports Claude Code, Codex CLI, Gemini CLI, and OpenCode through hook scripts the host installs into each agent's config.

**What we'd do:** Ship per-agent hook recipes that all write the same `state=` / `timestamp=` / `session=` file format `claude-status-update` already uses. Per-agent icon mapping in `lib-claude.sh` (`claude_state_icon` → `agent_state_icon` keyed on an `agent=` field).

**Effort:** S–M. Aggregation in `claude-status` is already agent-agnostic by construction; the work is mostly recipes + an extra dispatch in the icon function. Codex first (closest hook model to Claude); Gemini and OpenCode after.

**Open questions:**
- Do we add an `agent=` field, or infer agent from icon mapping per pane?
- Codex lacks a permission-response hook (per honeymux docs) — we'd inherit that limitation.

### 2. Distinct "unanswered" vs "processing" glyphs

**What honeymux does:** Three visually distinct states — `●` unanswered (waiting on user), `·` alive, sine-synced spinner while producing output.

**What we'd do:** Stop decaying `waiting` into `processing` after 30s. Instead, keep `waiting` as a sticky "needs input" state with its own glyph (e.g. a colored dot) on the window tab, distinct from the active spinner. The current decay was a workaround for stale state files; replace it with a "stale" terminal state (different glyph again — e.g. dim dot) so the user can tell `agent paused for input` from `hook script crashed`.

**Effort:** S. Logic lives in `read_pane_state` and `claude_priority_state` in `lib-claude.sh`. Mostly a state-machine rewrite of the existing staleness rules.

**Why it matters:** Honeymux explicitly pitches itself at the "trouble keeping up with coding agents" use case. Today, a window where Claude wants permission and a window where Claude is mid-edit look the same once 30s elapse.

### 3. Global unanswered counter in status bar

**What honeymux does:** "Mux-o-Tron" displays `total agents / unanswered agents` as a persistent counter.

**What we'd do:** Add a `claude-status --aggregate` mode (or equivalent) that scans `/tmp/claude-status/panes/` once and emits `N waiting` / `M total`. Place in `status-format[0]` next to the existing claude status block. Cheap because the directory scan already happens.

**Effort:** S. Same scan, new output format. Care needed to avoid double-scan when both per-pane and aggregate displays are active in the same status update — cache via a single invocation.

**Layout cost:** Adds ~6–10 cells to `status-format[0]`. Width budget needs a re-check; may need a compact form (`!3` for "3 waiting") on narrow terminals.

### 4. Desktop notification on transition into `waiting`

**What honeymux does:** Fires notifications on agent activity.

**What we'd do:** Add an opt-in (`programs.lazytmux.notifyOnWaiting = true;`) hook in `claude-status-update` that, on a transition `*  → waiting`, fires `notify-send` (Linux) / `terminal-notifier` (macOS). Debounce per-pane: at most one notification per pane per state-change.

**Effort:** S. Single conditional in the update script + a home-manager option.

**Default:** Off. Lazytmux's ethos is silent-by-default; users who want it enable it.

### 5. Team grouping in pickers

**What honeymux does:** Sub-agents with a shared `teamName` nest under a team-lead row in the agent dialog with a member count.

**What we'd do:** If a hook writes `team=foo` alongside `state=`, the session/window pickers (`tmux-session-picker`, `tmux-window-picker`) group panes by team and prefix with `└─` / `├─` like the existing reflow tree. Sessions/windows without a team render unchanged.

**Effort:** M. Pickers currently sort by session/window; adding a grouping pass means re-sorting and computing prefix glyphs. Alignment math (already complex) gets one more column.

**Use case:** Fan-out parallel agents (one orchestrator + N workers). Currently they're a flat list of identical-looking icons.

## Explicitly Not Adopted

These are out of scope and should not be revisited without a fundamental architecture change:

- **SSH pane stitching.** Requires owning the VT layer.
- **Sidebar / toolbar / in-app dialogs.** Same.
- **Zero-config in-app mutability.** Fights the Nix-package value prop.
- **Auto-overwriting hook scripts on upgrade.** Surprising for users who customized them; we keep hooks user-installed.

## Sequencing (when un-deferred)

Roughly cheapest-first, each independently shippable:

1. Distinct `waiting` glyph (#2) — pure refactor of existing state machine, no new surface.
2. Global unanswered counter (#3) — small status-bar addition, builds confidence in the aggregate path.
3. Desktop notifications (#4) — opt-in flag; isolated.
4. Codex adapter (#1, first agent) — exercises the multi-agent abstraction.
5. Gemini + OpenCode adapters (#1 cont.) — same template, more recipes.
6. Team grouping (#5) — last; touches picker layout, the most fragile code.

## Open Decisions to Make Before Implementation

- Whether to add an `agent=` field to the state file format (touches the `claude-status-update` contract — breaking change for users who customized their hooks).
- Whether to keep the `claude-` prefix on shared library / scripts or rename to `agent-` once multi-agent lands. Renaming is a one-time cost; staying creates a misleading name.
- Width budget on `status-format[0]` once both per-pane status, branch, dir, and an aggregate counter coexist. May need a "compact" mode triggered by terminal width.
