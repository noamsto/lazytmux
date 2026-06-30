# Enrich Card Popup (`tmux-enrich-card`)

**Date:** 2026-06-30
**Status:** Approved design, pre-implementation

## Problem

`prefix + i` currently runs `switch-client -T enrich`, arming an invisible tmux
key-table. The follow-up keys shell out silently — `i`/`p` open a URL in the
browser, `r` force-refreshes the PR poller with no tmux-side output. There is no
on-screen feedback and no hint of which keys are available, so the binding reads
as "does nothing." The enrichment data itself (issue id/title, PR number/state,
branch) only lives in the ambient status line, where the title is truncated and
there is no single place to see a window's full identity or act on it.

## Goal

Replace the silent key-table with a single, self-documenting floating popup that
shows the current window's full issue/PR/branch/Claude identity and hosts the
open-issue / open-PR / refresh actions as labeled, visible keybinds — with live
in-place updates so a force-refresh visibly changes the card.

Non-goals: a multi-window navigator (that is already `prefix + w`, the window
picker); editing issue/PR data; any change to the enrich/claude data pipelines.

## Architecture

A fourth Go binary in the existing `picker/` module, as sibling sub-package
`picker/enrichcard/` (mirrors `picker/splash/` and `picker/statusline/`):

- `picker/enrichcard/main.go` — flag parsing + bubbletea program entry.
- `picker/enrichcard/model.go` — the bubbletea `Model` (state, `Update`, `View`).
- Tests alongside (`*_test.go`), matching the existing table-driven style.

Build wiring in `picker/default.nix`:

- Add `"enrichcard"` to `subPackages`.
- Add `mv $out/bin/enrichcard $out/bin/tmux-enrich-card` to `postInstall`.
- No new dependencies (bubbletea v2 / bubbles v2 / lipgloss v2 are already
  vendored for the picker and splash), so `vendorHash` is unchanged.

The binary is referenced from `config/tmux.conf.nix` like the other picker
binaries (its store path interpolated into the keybind).

### Keybind change (`config/tmux.conf.nix`)

`prefix + i` is rebound from the key-table to a popup launch:

```
bind-key i display-popup -E -w 64 -h 18 \
  "${script.tmux-enrich-card}/bin/tmux-enrich-card \
     --target '#{session_id}:#{window_id}' \
     <theme flags> <icon flags>"
```

The entire `-T enrich` key-table (the old `bind-key -T enrich i|p|r`) is
**deleted** — those actions now live inside the popup.

## Data flow

Two classes of input, by how often they change:

1. **Stable, passed once as CLI flags at launch** (same pattern as
   `tmux-statusline`):
   - Theme colors: `--thm-bg`, `--thm-fg`, `--thm-red`, `--thm-green`,
     `--thm-peach`, `--thm-mauve`, `--thm-blue`, `--thm-lavender`,
     `--thm-overlay0`, `--thm-subtext0` (exact set finalized during render
     implementation; superset of what the four blocks need).
   - Enrich icon glyphs: `--icon-linear`, `--icon-github`, `--icon-pending`,
     `--icon-success`, `--icon-failure`, `--icon-merged`, `--icon-closed`,
     `--icon-conflict`.
   - Light/dark needs no special handling: tmux's `@thm_*` values are already
     resolved for the active theme, so the flags carry the right palette.

2. **Live window state, polled** via a single `tmux show-options -w -t <target>`
   at startup and on every ~1s `tea.Tick`. Parsed into the model:
   - Issue: `@issue_provider`, `@issue_id`, `@issue_title`, `@issue_url`.
   - PR: `@pr_number`, `@pr_title`, `@pr_state`, `@pr_check_state`, `@pr_url`,
     `@pr_mergeable`.
   - Branch/worktree: `@branch`, `@worktree`, `@git_root`.
   - Claude: `@window_task`, `@window_ai_name`, `@window_claude_ago`,
     `@active_pane_icon`.

The card never re-implements the enrich or claude-status logic; it reflects the
window options those pipelines already write. `--target` is
`session_id:window_id` so the popup tracks the window it was launched from
regardless of focus changes.

## Card layout

Four stacked blocks plus a footer hint row, in a rounded lipgloss border, sized
to the popup (`-w 64 -h 18`):

```
╭─ window 3 ──────────────────────────────────╮
│   Linear  ENG-6794                          │
│  Seamless ctrl+hjkl into the kitty carousel  │
│                                              │
│   PR #103   ✓ checks pass  ·  mergeable     │
│  feat/103-kitty-nav  →  main                 │
│  ~/Data/git/.../wt/kitty-nav                 │
│                                              │
│  󰪡 processing  ·  reflowing the grid  · 4m   │
│                                              │
│  [o] issue   [p] PR   [r] refresh   [q] close │
╰──────────────────────────────────────────────╯
```

Color-coding reuses the status-line palette and precedence:

- PR check state: green = success, peach = pending, red = failure.
- PR state: mauve = merged, dim overlay = closed; `@pr_mergeable == conflicting`
  forces red and wins the badge (same precedence as the status line).
- Issue provider glyph tinted per provider (Linear/GitHub).

Graceful degradation: any missing block collapses to a single dim line rather
than empty space (e.g. no issue stamped → "no issue", no PR → "no PR"). Long
titles wrap or ellipsize to the card width; the issue/PR title is shown in full
where it fits (the status-line truncation is the thing being fixed).

## Interactions

| Key | Action |
|-----|--------|
| `o` | `xdg-open @issue_url` (no-op if empty); brief "opened ↗" flash in footer |
| `p` | `xdg-open @pr_url` (no-op if empty); same flash |
| `r` | spawn `tmux-pr-enrich --target <t> --branch <@branch> --dir <@worktree\|@git_root\|pane_path> --force`; set a `⧗ refreshing…` spinner on the PR line |
| `q` / `Esc` / `Ctrl-c` | `tea.Quit` → popup closes |

The `r` command is exactly what the old `-T enrich r` keybind ran. The spinner
clears when a subsequent poll tick observes a changed `@pr_*` value, with a ~5s
fallback timeout so a refresh that yields no change does not spin forever.

## Error handling

- A failed or empty `tmux show-options` (e.g. the window closed underneath the
  popup) leaves the model at its last good state; the card never crashes on
  missing options.
- `Ctrl-c` / `q` always exit cleanly via `tea.Quit`.
- `xdg-open` failures are swallowed (the URL may be empty or no opener present);
  the action is best-effort, matching the current keybind behavior.

## Testing

Go unit tests (table-driven, matching `picker/enrich_test.go` and
`picker/statusline/*_test.go`):

- **Option parsing:** raw `show-options -w` output lines → model struct, including
  absent options and quoted values.
- **Pure `View()` rendering** over fixed model states (no tmux needed, since
  render is pure over the model): full issue+PR, no-issue, no-PR, merged,
  closed, conflicting/mergeable, and the refreshing-spinner state. Assert the
  expected glyph/color tokens and labels appear.

No new entry in `nix flake check` beyond the Go test run already covered by the
module build; manual smoke test via `nix build .#default` + opening the popup.

## Approaches considered and rejected

- **Static bash `display-popup` printf card** — no event loop, so no live
  refresh; fails the in-place-feedback goal.
- **A `--card` mode on the existing `picker` binary** — couples an unrelated
  concern into the ~25k-line `main.go`; a focused sibling package matches the
  `splash` precedent and keeps each binary single-purpose.
