# tmux-config

Opinionated tmux configuration with Claude Code integration.

Provides a fully configured tmux binary via a Nix flake — no dotfile management required.
`nix run github:noamsto/tmux-config` drops you into a ready-to-use tmux environment.

## Quick Start

```bash
# Run directly (no install)
nix run github:noamsto/tmux-config

# If a tmux server is already running with your old config, kill it first:
tmux kill-server && nix run github:noamsto/tmux-config
```

## Installation

```bash
# Install to your Nix profile
nix profile install github:noamsto/tmux-config
```

This installs a `tmux` wrapper that automatically loads the configuration. Your existing
`~/.tmux.conf` is ignored — the config is baked into the wrapper.

## Features

- **Catppuccin Mocha theme** — consistent colors across status bar and pane borders
- **Multi-line status bar** — windows auto-reflow across multiple lines when the terminal is narrow
- **Nerd font window icons** — per-process icons (fish, nvim, nix, Claude Code, etc.)
- **Claude Code status integration** — real-time spinner/icon in status bar and session/window pickers
- **Git branch display** — current branch in the top status line
- **Session and window pickers** — fzf-powered pickers with Claude status shown per session/window
- **vim-tmux-navigator** — seamless `Ctrl-h/j/k/l` navigation between vim splits and tmux panes
- **tmux-fingers** — smart copy mode with hints for URLs, hashes, file paths, JIRA tickets
- **tmux-resurrect + continuum** — automatic session save every 10 minutes with restore on start
- **Mouse support**, vi copy mode, pane dimming for inactive panes

## Requirements

- **Nerd Font terminal** — any terminal with a Nerd Font renders window icons correctly (Kitty, Alacritty, WezTerm, etc.)
- **tmux 3.4+** — multi-line status bar requires 3.4; the Nix package provides a compatible version automatically

## wt — Git Worktree Manager

A separate package for managing git worktrees with tmux window integration.
Each worktree gets its own tmux window; sessions map to repositories.

```bash
# Run directly
nix run github:noamsto/tmux-config#wt -- <branch>

# Or install alongside tmux
nix profile install github:noamsto/tmux-config
wt <branch>
```

### wt Usage

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

## Claude Code Integration

The status bar and pickers show the Claude Code state for each pane, window, and session
in real time. Claude Code hooks call `claude-status-update` (bundled in the wrapper's PATH)
to write state files that the status bar reads every second.

### Status Indicators

| Indicator | Meaning |
|-----------|---------|
| Spinner animation (󰪞 󰪟 󰪠 …) | Processing — Claude is actively working |
| 󰔟 | Waiting — permission prompt needs your input |
|  | Compacting — context compaction in progress |
| 󰸞 | Done — Claude finished the last task |
| 󰒲 | Idle — waiting for your next prompt |

When multiple panes have Claude running, the window and session indicators show the
highest-priority state (waiting > compacting > processing > done > idle).

### Hooks Configuration

Paste this into `~/.claude/settings.json` (merge with existing `hooks` if present).
The commands use bare names (`claude-status-update`) because they are on PATH via the tmux wrapper.

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

State files are written to `/tmp/claude-status/` and cleaned up automatically.
Stale states (e.g. a `processing` state older than 15 seconds) are resolved automatically
if a hook fails to fire.
