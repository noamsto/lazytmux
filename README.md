<div align="center">

# lazytmux

**Opinionated tmux configuration with Claude Code & OpenCode integration.**

Provides a fully configured tmux binary via a Nix flake — no dotfile management required.

`nix run github:noamsto/lazytmux` drops you into a ready-to-use tmux environment.

[![Nix Flake](https://img.shields.io/badge/nix-flake-blue?logo=nixos)](https://nixos.org)
[![tmux 3.6](https://img.shields.io/badge/tmux-3.6a-green)](https://github.com/tmux/tmux)
[![Catppuccin Mocha](https://img.shields.io/badge/theme-catppuccin%20mocha-mauve?logo=data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iMjQiIGhlaWdodD0iMjQiIHZpZXdCb3g9IjAgMCAyNCAyNCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48Y2lyY2xlIGN4PSIxMiIgY3k9IjEyIiByPSIxMiIgZmlsbD0iI2NiYTZmNyIvPjwvc3ZnPg==)](https://github.com/catppuccin/tmux)

</div>

---

<div align="center">

https://github.com/user-attachments/assets/8c6381fc-1eb8-4942-bc5b-521ad7fbf464

</div>

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
| **Catppuccin theme** | Consistent Mocha/Latte colors across status bar and pane borders, following your light/dark theme |
| **Multi-line status bar** | Windows auto-reflow across multiple lines when the terminal is narrow |
| **Nerd font window icons** | Per-process icons (fish, nvim, nix, Claude Code, OpenCode, etc.) |
| **AI agent status** | Real-time spinner/icon in status bar for Claude Code and OpenCode |
| **Bubbletea pickers** | Go session/window pickers with AI status per entry, zoxide suggestions, and issue/PR badges |
| **Issue / PR enrichment** | Per-worktree Linear/GitHub issue identity and PR check-state in the status line (`prefix + i`) |
| **Git branch display** | Current branch shown in the top status line |
| **Smart pane navigation** | Seamless `Ctrl-h/j/k/l` between vim splits and tmux panes (zoom-aware) |
| **tmux-fingers** | Smart copy with hints for URLs, hashes, file paths, JIRA tickets |
| **tmux-state persistence** | Periodic snapshots, undo for closed windows, optional smart auto-restore |
| **Welcome splash** | Animated braille-cat welcome buffer with a keybind cheatsheet, once per server |
| **Image carousel** | View a Claude session's images/diagrams in a split (`prefix + I`) |
| **Mouse + vi mode** | Mouse support, vi copy mode, pane dimming for inactive panes |

## Requirements

- **Nerd Font terminal** — any terminal with a Nerd Font renders window icons correctly (Kitty, Alacritty, WezTerm, etc.)
- Nothing else — the Nix package bundles tmux 3.6a; your own `~/.tmux.conf` and system tmux are not used

---

## Keybindings

The prefix defaults to <kbd>`</kbd> (backtick); set it via `programs.lazytmux.prefix`.
Press <kbd>prefix</kbd> then <kbd>C-Space</kbd> for the in-terminal cheatsheet.

### Prefix bindings

| Key | Action |
|-----|--------|
| <kbd>r</kbd> | Reload config |
| <kbd>\|</kbd> / <kbd>_</kbd> | Split pane horizontal / vertical |
| <kbd>c</kbd> | New window |
| <kbd>N</kbd> | New session (prompts for name) |
| <kbd>x</kbd> | Kill pane — instant on an idle shell, confirm otherwise |
| <kbd>&</kbd> | Kill window (confirm) |
| <kbd>M-Up/Down/Left/Right</kbd> | Resize pane (repeatable) |
| <kbd>s</kbd> | Session picker |
| <kbd>w</kbd> | Window picker |
| <kbd>a</kbd> | Claude-window picker (only windows with a running agent) |
| <kbd>i</kbd> | Issue / PR enrich card (Linear/GitHub + PR state) |
| <kbd>g</kbd> | LazyGit popup |
| <kbd>G</kbd> | gh-dash popup |
| <kbd>b</kbd> | btop popup |
| <kbd>y</kbd> | yazi (new window) |
| <kbd>p</kbd> | Scratchpad |
| <kbd>Y</kbd> | Yank pane's cwd to clipboard |
| <kbd>I</kbd> | Toggle the image/diagram carousel |
| <kbd>C-Space</kbd> | Welcome splash + cheatsheet |
| <kbd>u</kbd> / <kbd>U</kbd> | Undo close / close-event picker |
| <kbd>R</kbd> | Snapshot picker |
| <kbd>C-s</kbd> | Save snapshot now |
| <kbd>F</kbd> / <kbd>J</kbd> | tmux-fingers copy / jump mode |
| <kbd>D</kbd> | Toggle debug logging |

<kbd>u</kbd>/<kbd>U</kbd>/<kbd>R</kbd>/<kbd>C-s</kbd> require `persist.enable` (on by default);
<kbd>i</kbd> requires `enrich.enable`; <kbd>I</kbd> requires the image carousel.

### No-prefix bindings

| Key | Action |
|-----|--------|
| <kbd>Ctrl-h/j/k/l</kbd> | Navigate panes, falling through to vim splits (zoom-aware) |
| <kbd>M-H</kbd> / <kbd>M-L</kbd> | Previous / next window |
| <kbd>M-J</kbd> / <kbd>M-K</kbd> | Move down / up a row in the reflowed window grid |
| <kbd>M-l</kbd> | Clear screen |
| <kbd>S-Enter</kbd> | Newline in Claude Code / Amp / OpenCode |

### Copy mode (vi)

| Key | Action |
|-----|--------|
| <kbd>v</kbd> | Begin selection |
| <kbd>C-v</kbd> | Toggle rectangle selection |
| <kbd>y</kbd> | Copy selection to clipboard |

---

## Git Worktree Integration

Lazytmux integrates with [worktrunk](https://worktrunk.dev/) (`wt`) so
each git worktree maps to its own tmux window, and each repository to a session. Enable
it and the `post-switch` navigation hook via the home-manager module:

```nix
programs.lazytmux.worktrunk.enable = true;
```

```bash
wt switch <branch>      # switch to a worktree (creates it if the branch exists)
wt switch -c <branch>   # create branch + worktree, then switch
wt switch               # interactive picker
wt list                 # list worktrees
wt remove               # remove the current worktree
wt merge                # merge the current branch into its target
```

**Model:** one tmux session per repository, one window per worktree/branch. See the
[worktrunk docs](https://worktrunk.dev/) for the full command set.

---

## AI Agent Status Integration

The status bar and pickers show the AI agent state for each pane, window, and session
in real time. Both Claude Code and OpenCode are supported via `claude-status-update`
(bundled in the wrapper's PATH), which writes state files the status bar reads every second.

### Status Indicators

<!-- TODO: Replace this table with a screenshot showing the actual status icons -->

| Indicator | Meaning |
|-----------|---------|
| Spinner animation | **Processing** — agent is actively working |
| Clock icon (orange) | **Waiting** — permission prompt needs your input |
| Clock icon (yellow) | **Denied** — auto mode denied a command |
| Compress icon | **Compacting** — context compaction in progress |
| Checkmark | **Done** — agent finished the last task |
| X icon (red) | **Error** — tool or stop failure |
| Sleep icon | **Idle** — waiting for your next prompt |

When multiple panes have agents running, the window and session indicators show the
highest-priority state (waiting > denied > compacting > processing > done > idle).

### Claude Code Hooks

The easiest path is the [Claude Code plugin](#claude-code-plugin) — install it and these
hooks register automatically, with no `settings.json` editing.

To wire them up manually (e.g. you use the tmux integration without the plugin), mirror
the canonical definitions in
[`claude-plugin/hooks/hooks.json`](claude-plugin/hooks/hooks.json) into
`~/.claude/settings.json`, replacing each
`"${CLAUDE_PLUGIN_ROOT}"/scripts/status.sh <state>` with `claude-status-update <state>`
(the bare name is on PATH via the tmux wrapper). The full event → state mapping:

| Hook event (matcher)                        | Status                 |
| ------------------------------------------- | ---------------------- |
| `SessionStart` (`startup`/`resume`/`clear`) | cleanup + idle         |
| `SessionStart` (`compact`)                  | cleanup + processing   |
| `UserPromptSubmit`                          | processing (`--force`) |
| `PreToolUse` / `PostToolUse`                | processing             |
| `PostToolUseFailure`                        | processing             |
| `Notification` (`permission_prompt`)        | waiting                |
| `Notification` (`idle_prompt`)              | idle                   |
| `Stop`                                      | done                   |
| `StopFailure`                               | error                  |
| `PreCompact`                                | compacting             |
| `PostCompact`                               | processing             |
| `PermissionDenied`                          | denied                 |
| `Elicitation`                               | waiting                |
| `ElicitationResult`                         | processing             |
| `SessionEnd`                                | clear                  |

### OpenCode Plugin

OpenCode uses a [plugin system](https://opencode.ai/docs/plugins/) instead of JSON hooks.
Lazytmux ships a plugin at `plugins/opencode-status.ts` that maps OpenCode events to
`claude-status-update` calls.

**With home-manager** (automatic): the plugin is installed to `~/.config/opencode/plugin/`
by default. Disable with `programs.lazytmux.opencode.enable = false`.

**Manual install**: symlink or copy the plugin file:

```bash
mkdir -p ~/.config/opencode/plugin
cp plugins/opencode-status.ts ~/.config/opencode/plugin/
```

The plugin maps OpenCode events as follows:

| OpenCode Event | Status |
|----------------|--------|
| `session.created` | cleanup + idle |
| `session.idle` | done |
| `session.error` | error |
| `session.deleted` | clear |
| `session.compacted` | processing |
| `tool.execute.before/after` | processing |
| `permission.asked` | waiting |
| `permission.replied` | processing |
| `message.updated` | processing |

### State Files

State files are written to `/tmp/claude-status/` and cleaned up automatically.
Stale states (e.g. a `processing` state older than 15 seconds) are resolved automatically
if a hook fails to fire.

## Claude Code plugin

The CC-side integration (status-bar hooks + issue-tracking skill) ships as a
Claude Code plugin in this repo. For an agent-oriented setup walkthrough
(install, verify, troubleshoot), see
[`claude-plugin/README.md`](claude-plugin/README.md).

Nix (recommended — pins plugin and tmux scripts to the same revision):

```nix
# in your claude wrapper
claude --plugin-dir "${inputs.lazytmux}/claude-plugin"
```

Marketplace:

```bash
claude plugin marketplace add noamsto/lazytmux
claude plugin install lazytmux@lazytmux
```

With the plugin installed, the tmux status bar tracks Claude state with zero
manual hook wiring, and Claude can stamp the issues it works on
(`claude-status-update issue add ENG-123`) so orchestrator sessions on `main`
show what they're actually doing.
