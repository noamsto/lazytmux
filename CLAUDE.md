# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Lazytmux is an opinionated tmux configuration delivered as a Nix flake. It wraps the tmux binary with a baked-in config so there's no dotfile management. Key features: Catppuccin theme, multi-line status bar with auto-reflow, per-window Nerd Font process icons, real-time Claude Code status integration, and Go bubbletea session/window pickers.

## Build and Test

```bash
nix build .              # Build wrapped tmux (output: ./result/bin/tmux)
nix flake check          # Run all flake checks (includes pre-commit hooks)
```

After building, reload the running tmux with `prefix + r` (which sources `~/.config/tmux/tmux.conf`, a symlink managed by the home-manager module).

There are no unit tests. CI runs `nix build .#default` and `nix flake check`.

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
| `tmux-session-picker` | `prefix + s` | Launches the Go bubbletea picker (`tmux-picker-generate --tui`) in a popup: sessions sorted by activity, then top-15 zoxide dir suggestions (Enter on a suggestion creates a session there and switches). |
| `tmux-window-picker` | `prefix + w` | Same TUI in window mode (`--tui --windows`), grouped by session. |
| `tmux-branch-display` | `#()` in status-format[0] | Shows git branch name from `@branch` or fallback to `git branch --show-current`. |
| `tmux-dir-display` | `#()` in status-format[0] | Shows pane path relative to git root (e.g., `./src`). |
| `tmux-set-pane-border` | `run-shell` at config load | Interpolates `@thm_*` color variables into `pane-border-format` (needed because nested `#{@thm_*}` inside `#[]` don't expand at render time). |
| `tmux-issue-stamp` | worktrunk `post-switch` hook (one-shot, backgrounded) | Detects the Linear/GitHub issue for the new window's branch via provider priority; writes `@issue_provider`/`@issue_id`/`@issue_title`/`@issue_url`, then kicks an immediate PR fetch. |
| `tmux-issue-stamp-linear` / `-github` | called by the dispatcher | Provider impls: branch regex (+ `linear`/`gh` CLI) → `id\ntitle\nurl`. First provider with a non-empty id wins. |
| `tmux-pr-enrich` | `#()` in status-format[0] (`--tick`); `prefix + i` `r` (`--force`) | Background PR poller; `gh pr list` per branch (cached at `/tmp/lazytmux-pr/`, 60s TTL, flock-guarded), writes `@pr_number`/`@pr_title`/`@pr_state`/`@pr_check_state`/`@pr_url`. Self-gating tick daemonizes a full pass every `prRefreshSeconds`. |

### Shared Libraries

- **`lib-icons.sh`** — `ICON_MAP` associative array, `build_proc_icons`, `measure_display_width`, `strip_tmux_colors`, `pad_to_width`. Sourced by update-icons, reflow, session-picker, window-picker.
- **`lib-claude.sh`** — `CLAUDE_PANES_DIR`, spinner frames, `read_pane_state` (with staleness), `claude_state_icon`, `setup_claude_colors`, `claude_colored_icon`, `claude_priority_state`. Sourced by update-icons, session-picker, window-picker, claude-status.
- **`lib-enrich.sh`** — `branch_to_linear_key`, `branch_to_gh_issue_number`, `sanitize_title`, `truncate_ellipsis`, `branch_sha1`, `collapse_check_rollup`, `provider_priority_list`. Sourced by `tmux-issue-stamp*` + `tmux-pr-enrich`. Pure logic is unit-tested in `tests/enrich.bats` (run via `nix flake check`).

Functions use the `REPLY` variable pattern (set `REPLY` instead of echoing) to avoid subshell forks in hot paths.

### Two Icon Variables

- `@window_icon_display` — unpadded, used in `automatic-rename-format` (window tab names) and top-right status
- `@window_icon_padded` — fixed-width padded to `MAX_ICONS * 3 + 2` cells, used in status-format ENTRY for `|` separator alignment. Set by both `tmux-update-icons` (every 1s) and `tmux-reflow-windows` (on layout events)

Icon display width is computed per-icon from Unicode codepoint: nerd font PUA (U+E000-F8FF, U+F0000+) = 1 cell, emoji/other = 2 cells, plus 1 space each. `wc -L` is unreliable for nerd font glyphs (reports 0 for PUA range) and emoji with variant selectors.

### Status Bar Layout

- **Line 0** (status-format[0]): Global — session name, git branch, directory, claude status (left); active pane icon + command (right)
- **Lines 1-3** (status-format[1-3]): Window list, dynamically reflowed. Single-line mode unsets session overrides to fall back to global format. Multi-line mode sets per-session overrides with `├─`/`╰─` tree prefixes.

tmux treats session-level `status-format` as all-or-nothing: setting any index at session level overrides ALL indices. That's why reflow must copy `FMT0` from global when setting session-level formats.

### Session Targeting Gotcha

Numeric session names (e.g., "10") cause ambiguity with `tmux set -t '10'` when piped through `tmux source -`. The Go TUI targets sessions by name (`tmux switch-client -t <name>`; zoxide suggestions use `=name` exact-match when creating a new session). Direct `tmux set` calls (as in update-icons) work fine with session names.

### Home-Manager Module

`modules/home-manager.nix` provides `programs.lazytmux` with options for `enable`, `worktrunk.enable`, `skills.enable`, and `startupSession` (systemd service). The activation script reloads tmux config and reflows all sessions after `home-manager switch`.

### Worktree Management

Git worktree management is handled by the third-party `worktrunk` tool, configured via the home-manager module's `worktrunk.enable` option.

### Persist (tmux-state)

The [tmux-state](https://github.com/noamsto/tmux-state) Go binary is the persistence
layer (replaces tmux-resurrect/tmux-continuum). Enabled by default via
`programs.lazytmux.persist.enable`; set to `false` to opt out.

- tmux hooks fire `tmux-state save` on structural change and
  `tmux-state capture-event` on close.
- systemd user timer runs `tmux-state save --reason=timer` every 60s; weekly GC
  drops orphan scrollback files.
- Keybindings: `prefix + u` (undo pop), `prefix + U` (close-event picker),
  `prefix + R` (snapshot picker), `prefix + Ctrl-s` (immediate save).
- Storage: `$XDG_DATA_HOME/tmux-state/state.db` + scrollbacks dir.
- `restoreMode` defaults to `"off"` (manual `prefix + R` only). Set to `"auto"`
  to apply the smart filter on tmux server start.

### PR + Issue Enrichment

Per-worktree Linear/GitHub issue identity + PR check-state shown in the status
line. Enabled by default via `programs.lazytmux.enrich.enable`.

- **Window options are the source of truth.** `tmux-issue-stamp` (backgrounded
  from worktrunk `post-switch` hook) writes `@issue_*`; `tmux-pr-enrich`
  (background tick in status-format[0]) writes `@pr_*`. Display formats and
  keybinds only read them.
- **Providers** (`enrich.providers`, default `["linear" "github"]`) are tried in
  priority order; first non-empty issue id wins. Both CLIs are optional and
  degrade gracefully: `gh` (inherited from PATH) provides PR data and GitHub
  issue titles, `linear` provides Linear titles/URLs. Without a CLI, only the
  branch-regex-derived issue id is shown (no titles, no PR state).
- **Keybindings:** `prefix + i` enters the enrich table — `i` open issue URL,
  `p` open PR URL, `r` force-refresh the current window.
- **Refresh:** `prRefreshSeconds` (default 30, clamped 10-300) gates the
  background poll. PR state cached at `/tmp/lazytmux-pr/` (60s TTL).
- **Icons:** override the 6 glyphs (linear/github/pending/success/failure/merged)
  via `enrich.icons`; defaults are ASCII sentinels (`L`, `GH`, `*`, `OK`, `X`,
  `M`). The `#` escape: Nix replaces `#` with `##` in icon values for tmux
  format safety.
- **Display test:** `./tests/test-display.sh` after `nix build .#default`
  (manual; not in `nix flake check`).

## Key Conventions

- **Shell scripts are bash**, not fish (they run inside tmux's environment). User's interactive shell is fish.
- **Placeholders** (`@ICON_MAP@`, `@FALLBACK_ICON@`, etc.) in scripts are replaced at Nix build time. Don't use these patterns in non-placeholder contexts.
- **Process icon mapping** lives in `config/process-icons.nix` — a plain Nix attrset of `"process-name" = "icon"`.
- **Claude status state files** at `/tmp/claude-status/panes/<pane_id>` use simple `key=value` format (state, timestamp, session).
- **Staleness thresholds**: waiting > 30s becomes processing; processing > 15s becomes done.
- **Theme support**: Scripts detect light/dark from `$XDG_STATE_HOME/theme-state.json` and use Catppuccin Latte/Mocha colors accordingly.
- **shfmt** uses tabs for indentation (project default).
- **Enrichment window options** (`@issue_*`, `@pr_*`) are the single source of truth for issue/PR state — display formats and keybinds read them; only the stamp/enrich scripts write them.
