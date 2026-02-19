<div align="center">

# lazytmux

**Opinionated tmux configuration with Claude Code integration.**

Provides a fully configured tmux binary via a Nix flake — no dotfile management required.

`nix run github:noamsto/lazytmux` drops you into a ready-to-use tmux environment.

[![Nix Flake](https://img.shields.io/badge/nix-flake-blue?logo=nixos)](https://nixos.org)
[![tmux 3.6](https://img.shields.io/badge/tmux-3.6a-green)](https://github.com/tmux/tmux)
[![Catppuccin Mocha](https://img.shields.io/badge/theme-catppuccin%20mocha-mauve?logo=data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iMjQiIGhlaWdodD0iMjQiIHZpZXdCb3g9IjAgMCAyNCAyNCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48Y2lyY2xlIGN4PSIxMiIgY3k9IjEyIiByPSIxMiIgZmlsbD0iI2NiYTZmNyIvPjwvc3ZnPg==)](https://github.com/catppuccin/tmux)

</div>

---

<!-- TODO: Add a showcase screenshot/gif of the full tmux setup in action -->

## Quick Start

```bash
# Run directly (no install)
nix run github:noamsto/lazytmux

# If a tmux server is already running with your old config, kill it first:
tmux kill-server && nix run github:noamsto/lazytmux
```

> **First run:** Nix needs to fetch and evaluate nixpkgs on first use, which can
> download a few hundred MB. The actual package closure is only ~67 MiB
> (44 store paths). Subsequent runs use the local cache and start instantly.

## Installation

```bash
# Install to your Nix profile
nix profile install github:noamsto/lazytmux
```

This installs a `tmux` wrapper that automatically loads the configuration. Your existing
`~/.tmux.conf` is ignored — the config is baked into the wrapper.

## Features

| Feature | Description |
|---------|-------------|
| **Catppuccin Mocha theme** | Consistent colors across status bar and pane borders |
| **Multi-line status bar** | Windows auto-reflow across multiple lines when the terminal is narrow |
| **Nerd font window icons** | Per-process icons (fish, nvim, nix, Claude Code, etc.) |
| **Claude Code status** | Real-time spinner/icon in status bar and session/window pickers |
| **Git branch display** | Current branch shown in the top status line |
| **fzf pickers** | Session and window pickers with Claude status per entry |
| **vim-tmux-navigator** | Seamless `Ctrl-h/j/k/l` between vim splits and tmux panes |
| **tmux-fingers** | Smart copy with hints for URLs, hashes, file paths, JIRA tickets |
| **resurrect + continuum** | Auto-save every 10 min with restore on start |
| **Mouse + vi mode** | Mouse support, vi copy mode, pane dimming for inactive panes |

## Requirements

- **Nerd Font terminal** — any terminal with a Nerd Font renders window icons correctly (Kitty, Alacritty, WezTerm, etc.)
- **tmux 3.4+** — multi-line status bar requires 3.4; the Nix package provides a compatible version automatically

---

## `wt` — Git Worktree Manager

A separate package for managing git worktrees with tmux window integration.
Each worktree gets its own tmux window; sessions map to repositories.

```bash
# Run directly
nix run github:noamsto/lazytmux#wt -- <branch>

# Or install alongside tmux
nix profile install github:noamsto/lazytmux
wt <branch>
```

### Usage

```
wt <branch>           Smart switch/create (prompts before creating)
wt -y <branch>        Skip confirmation prompts
wt -q <branch>        Quiet mode (only output the worktree path)
wt -n <branch>        No tmux operations (skip window creation/switching)
wt -yqn <branch>      Combine flags — designed for use in Claude/scripts
wt z [query]          Fuzzy-find worktree, output path
wt main               Switch to the root repository window
wt list               List all worktrees
wt remove <branch>    Remove worktree and kill its tmux window
wt clean              Remove stale worktrees (merged, squash-merged, or remote-deleted)
wt help               Show full help
```

**Model:** one tmux session per repository, one window per worktree/branch.

**In scripts or with Claude Code:**

```bash
cd "$(wt -yqn feature-branch)"
```

**Worktrees are created at** `.worktrees/<branch-name>` inside the repo root.

---

## Claude Code Integration

The status bar and pickers show the Claude Code state for each pane, window, and session
in real time. Claude Code hooks call `claude-status-update` (bundled in the wrapper's PATH)
to write state files that the status bar reads every second.

### Status Indicators

<!-- TODO: Replace this table with a screenshot showing the actual status icons -->

| Indicator | Meaning |
|-----------|---------|
| Spinner animation | **Processing** — Claude is actively working |
| Clock icon | **Waiting** — permission prompt needs your input |
| Compress icon | **Compacting** — context compaction in progress |
| Checkmark | **Done** — Claude finished the last task |
| Sleep icon | **Idle** — waiting for your next prompt |

When multiple panes have Claude running, the window and session indicators show the
highest-priority state (waiting > compacting > processing > done > idle).

### Hooks Configuration

Paste this into `~/.claude/settings.json` (merge with existing `hooks` if present).
The commands use bare names (`claude-status-update`) because they are on PATH via the tmux wrapper.

<details>
<summary><b>Click to expand hooks JSON</b></summary>

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [
          {"type": "command", "command": "claude-status-update cleanup"},
          {"type": "command", "command": "claude-status-update idle"}
        ]
      },
      {
        "matcher": "resume",
        "hooks": [
          {"type": "command", "command": "claude-status-update cleanup"},
          {"type": "command", "command": "claude-status-update idle"}
        ]
      },
      {
        "matcher": "clear",
        "hooks": [
          {"type": "command", "command": "claude-status-update cleanup"},
          {"type": "command", "command": "claude-status-update idle"}
        ]
      },
      {
        "matcher": "compact",
        "hooks": [
          {"type": "command", "command": "claude-status-update cleanup"},
          {"type": "command", "command": "claude-status-update processing"}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {"type": "command", "command": "claude-status-update processing"}
        ]
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {"type": "command", "command": "claude-status-update processing"}
        ]
      }
    ],
    "PostToolUse": [
      {
        "hooks": [
          {"type": "command", "command": "claude-status-update processing"}
        ]
      }
    ],
    "Notification": [
      {
        "matcher": "permission_prompt",
        "hooks": [
          {"type": "command", "command": "claude-status-update waiting"}
        ]
      },
      {
        "matcher": "idle_prompt",
        "hooks": [
          {"type": "command", "command": "claude-status-update idle"}
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "claude-status-update done"}
        ]
      }
    ],
    "PreCompact": [
      {
        "hooks": [
          {"type": "command", "command": "claude-status-update compacting"}
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {"type": "command", "command": "claude-status-update clear"}
        ]
      }
    ]
  }
}
```

</details>

State files are written to `/tmp/claude-status/` and cleaned up automatically.
Stale states (e.g. a `processing` state older than 15 seconds) are resolved automatically
if a hook fails to fire.
