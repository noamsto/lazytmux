# lazytmux — Claude Code plugin

The Claude Code side of [lazytmux](https://github.com/noamsto/lazytmux): lifecycle
hooks that drive the tmux status bar, plus three skills. This README is written
for an **agent setting the plugin up on its own** — the steps are copy-pasteable
and each command is non-interactive.

> Human installing lazytmux's tmux itself (Nix / home-manager)? See the
> [top-level README](../README.md). This file only covers the Claude Code plugin.

## What this plugin is

| Component | What it does |
|-----------|--------------|
| **Hooks** (`hooks/hooks.json`) | A state machine over the CC lifecycle (`SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `Notification`, `PreCompact`, …). Each event routes through `scripts/status.sh <state>`, which writes the pane's Claude state (`processing`/`waiting`/`done`/`idle`/…) so the tmux status bar reflects it live. |
| **Skills** (`skills/*/SKILL.md`) | `lazytmux:issue-tracking`, `lazytmux:tmux-interactive` — see [Skills](#skills). |

**Safe to install anywhere.** `status.sh` `exit 0` silently when the
`claude-status-update` binary isn't on `PATH` (i.e. you're not in a lazytmux tmux
pane). The plugin never errors on a hook; the status-bar effects simply appear
once Claude is running inside lazytmux's wrapped tmux.

## Prerequisites

- Claude Code with plugin support: `claude --version`.
- For the **status-bar effects to be visible**: Claude must be running inside a
  pane of lazytmux's wrapped tmux (the `claude-status-update` binary on `PATH`).
  Installing the plugin without that is harmless — hooks no-op.
- The skills assume a tmux session; `tmux-interactive` needs a tmux pane to act
  on.

## Install

Pick the path that matches the environment.

### A. Marketplace (most setups)

```bash
claude plugin marketplace add noamsto/lazytmux
claude plugin install lazytmux@lazytmux
```

`lazytmux@lazytmux` is `<plugin>@<marketplace>` — both are named `lazytmux`
(`.claude-plugin/marketplace.json`).

### B. Local plugin dir (development, or pinned via Nix)

Point Claude at the plugin directory directly — no marketplace, no install step.
The Nix flake exposes the plugin at a read-only store path, which pins the plugin
and the tmux scripts to one revision:

```nix
# in your claude wrapper
claude --plugin-dir "${inputs.lazytmux}/claude-plugin"
```

Or against a checkout:

```bash
claude --plugin-dir /path/to/lazytmux/claude-plugin
```

### C. Skills only (plugin already wired another way)

If lazytmux's home-manager module manages the hooks and you only want the skills,
`programs.lazytmux.skills.enable` symlinks `claude-plugin/skills/` into
`~/.claude/skills`. Disable it when the full plugin is installed — otherwise the
skills load twice.

## Verify

```bash
claude plugin list                 # lazytmux present + enabled?
claude plugin list --enabled       # enabled only
```

In an interactive session the slash-command equivalents are `/plugin list` and
`/plugin` (the manager UI); `/reload-plugins` picks up hook/skill changes without
restarting.

What to expect:

- **Skills loaded** — `lazytmux:issue-tracking`,
  `lazytmux:tmux-interactive` appear in the skill list immediately.
- **Hooks active** — fire on the next lifecycle event. To confirm they reach the
  status writer (only meaningful inside a lazytmux tmux pane):

  ```bash
  command -v claude-status-update && cat /tmp/claude-status/panes/* 2>/dev/null
  ```

  A `state=…` line for the current pane means the hook chain works end to end.

## Skills

| Skill | Use it when |
|-------|-------------|
| `lazytmux:issue-tracking` | Working a Linear/GitHub issue or PR whose branch is **not** the current tmux window's branch — orchestrating from `main`, spawning agents into worktrees, driving PRs. Stamps issue ids into the status bar. |
| `lazytmux:tmux-interactive` | Driving an interactive CLI (Python REPL, gdb, psql, node, lldb) that needs keystroke-level control, output scraping, or waiting on prompts inside a tmux pane. |

Skills auto-invoke from their descriptions; no manual step beyond having the
plugin installed.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Skills don't appear | Run `/reload-plugins`; confirm `claude plugin list` shows lazytmux enabled. Check each `skills/*/SKILL.md` has valid frontmatter (`name`, `description`). |
| Status bar shows nothing | Expected unless Claude runs inside lazytmux's wrapped tmux. Check `command -v claude-status-update` — if absent, the hooks are no-opping by design. Install/run lazytmux's tmux (see [top README](../README.md)). |
| Hooks seem dead even in tmux | `cat /tmp/claude-status/panes/*` after a tool call — empty means the writer isn't on `PATH`. The tmux server may predate the lazytmux deploy; restart it so panes inherit the new `PATH`. |
| Skills loaded twice | The plugin and `programs.lazytmux.skills.enable` are both active. Pick one (see [Install §C](#c-skills-only-plugin-already-wired-another-way)). |

## Quick reference

```bash
# Install + verify, start to finish
claude plugin marketplace add noamsto/lazytmux
claude plugin install lazytmux@lazytmux
claude plugin list --enabled
# (inside a lazytmux tmux pane, after one tool call:)
cat /tmp/claude-status/panes/*
```
