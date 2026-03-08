# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Lazytmux is an opinionated tmux configuration delivered as a Nix flake. It wraps the tmux binary with a baked-in config so there's no dotfile management. Key features: Catppuccin theme, multi-line status bar with auto-reflow, per-window Nerd Font process icons, real-time Claude Code status integration, fzf session/window pickers, and a `wt` git worktree manager.

## Build and Test

```bash
nix build .              # Build wrapped tmux (output: ./result/bin/tmux)
nix build .#wt           # Build wt worktree manager separately
nix flake check          # Run all flake checks (includes pre-commit hooks)
```

After building, reload the running tmux with `prefix + r` (which sources `~/.config/tmux/tmux.conf`, a symlink managed by the home-manager module).

There are no unit tests. CI runs `nix build .#default`, `nix build .#wt`, and `nix flake check`.

## Pre-commit Hooks

Entering the dev shell (`nix develop`) installs these hooks: `statix`, `deadnix`, `alejandra` (Nix); `shellcheck`, `shfmt` (shell); `typos`, `check-merge-conflicts`, `trim-trailing-whitespace` (general).

## Architecture

### Nix Build Pipeline

`flake.nix` imports `config/tmux.conf.nix` which is the core of the build:

1. **Shared libraries**: `scripts/lib-icons.sh` and `scripts/lib-claude.sh` are built via `writeShellScript` with placeholder substitution, then sourced by scripts at runtime via `source @lib_icons@` (replaced with store paths at build time).
1. **Script packaging**: Each `scripts/*.sh` file becomes a Nix store binary via `writeShellScriptBin`. Scripts in `scriptsWithIcons` get Nix-time string substitution for `@lib_icons@`, `@lib_claude@`, `@ICON_MAP@`, `@FALLBACK_ICON@`, `@MAX_ICONS@`, `@MAX_ICONS_PICKER@`, and `@claude_status_bin@` placeholders.
2. **Plugin pinning**: Catppuccin and which-key are pinned with `mkTmuxPlugin`; others come from nixpkgs `tmuxPlugins`.
3. **Config generation**: The full tmux.conf is generated as a Nix store text file with interpolated store paths to all scripts and plugins.
4. **Binary wrapping**: `symlinkJoin` + `wrapProgram` creates a `tmux` wrapper that loads the config via `-f` and adds all scripts to PATH.

### Script Roles

| Script | Invocation | Purpose |
|--------|-----------|---------|
| `tmux-update-icons` | `#()` every 1s (status-interval) | Sets `@window_icon_display` (unpadded), `@window_icon_padded` (fixed-width), `@active_pane_icon` per window. Reads claude status files. |
| `tmux-reflow-windows` | tmux hooks (window add/remove/resize) | Computes multi-line window layout split points, sets `status-format[1-3]` and `status` line count (2-4). Caches by window-count:width key to skip no-ops. |
| `claude-status` | `#()` in status-format[0] | Reads `/tmp/claude-status/panes/*` files, aggregates per-pane/window/session with priority (waiting > compacting > processing > done > idle). Handles staleness. |
| `claude-status-update` | Claude Code hooks (external) | Writes state files to `/tmp/claude-status/panes/<pane_id>`. Called by user's Claude Code hook config. |
| `tmux-session-picker` | `prefix + s` | Pre-computes claude status + process icons, then opens `choose-tree -Zs`. Pads icon and name columns for alignment. |
| `tmux-window-picker` | `prefix + w` | Same as session picker but for windows (`choose-tree -Zw`). |
| `tmux-branch-display` | `#()` in status-format[0] | Shows git branch name from `@branch` or fallback to `git branch --show-current`. |
| `tmux-dir-display` | `#()` in status-format[0] | Shows pane path relative to git root (e.g., `./src`). |
| `tmux-set-pane-border` | `run-shell` at config load | Interpolates `@thm_*` color variables into `pane-border-format` (needed because nested `#{@thm_*}` inside `#[]` don't expand at render time). |

### Shared Libraries

- **`lib-icons.sh`** — `ICON_MAP` associative array, `build_proc_icons`, `measure_display_width`, `strip_tmux_colors`, `pad_to_width`. Sourced by update-icons, reflow, session-picker, window-picker.
- **`lib-claude.sh`** — `CLAUDE_PANES_DIR`, spinner frames, `read_pane_state` (with staleness), `claude_state_icon`, `setup_claude_colors`, `claude_colored_icon`, `claude_priority_state`. Sourced by update-icons, session-picker, window-picker, claude-status.

Functions use the `REPLY` variable pattern (set `REPLY` instead of echoing) to avoid subshell forks in hot paths.

### Two Icon Variables

- `@window_icon_display` — unpadded, used in `automatic-rename-format` (window tab names) and top-right status
- `@window_icon_padded` — fixed-width padded to `(MAX_ICONS + 1) * 3` cells, used in status-format ENTRY for `|` separator alignment. Set by both `tmux-update-icons` (every 1s) and `tmux-reflow-windows` (on layout events)

Icon display width is computed from icon count (each icon = 2 display cells + 1 space = 3 cells per icon). `wc -L` is unreliable for nerd font glyphs (reports 0 for PUA range) and emoji with variant selectors.

### Status Bar Layout

- **Line 0** (status-format[0]): Global — session name, git branch, directory, claude status (left); active pane icon + command (right)
- **Lines 1-3** (status-format[1-3]): Window list, dynamically reflowed. Single-line mode unsets session overrides to fall back to global format. Multi-line mode sets per-session overrides with `├─`/`╰─` tree prefixes.

tmux treats session-level `status-format` as all-or-nothing: setting any index at session level overrides ALL indices. That's why reflow must copy `FMT0` from global when setting session-level formats.

### Session Targeting Gotcha

Numeric session names (e.g., "10") cause ambiguity with `tmux set -t '10'` when piped through `tmux source -`. The pickers use `#{session_id}` (`$N` format) instead to avoid this. Direct `tmux set` calls (as in update-icons) work fine with session names.

### Home-Manager Module

`modules/home-manager.nix` provides `programs.lazytmux` with options for `enable`, `wt.enable`, `skills.enable`, and `startupSession` (systemd service). The activation script reloads tmux config and reflows all sessions after `home-manager switch`.

### `wt` — Git Worktree Manager

Separate Nix package (`wt/default.nix` → `wt/wt.sh`). Model: one tmux session per repo, one window per worktree. Runtime deps: git, tmux, gum, zoxide. Worktrees created at `.worktrees/<branch>` inside repo root.

## Key Conventions

- **Shell scripts are bash**, not fish (they run inside tmux's environment). User's interactive shell is fish.
- **Placeholders** (`@ICON_MAP@`, `@FALLBACK_ICON@`, etc.) in scripts are replaced at Nix build time. Don't use these patterns in non-placeholder contexts.
- **Process icon mapping** lives in `config/process-icons.nix` — a plain Nix attrset of `"process-name" = "icon"`.
- **Claude status state files** at `/tmp/claude-status/panes/<pane_id>` use simple `key=value` format (state, timestamp, session).
- **Staleness thresholds**: waiting > 30s becomes processing; processing > 15s becomes done.
- **Theme support**: Scripts detect light/dark from `$XDG_STATE_HOME/theme-state.json` and use Catppuccin Latte/Mocha colors accordingly.
- **shfmt** uses tabs for indentation (project default).
