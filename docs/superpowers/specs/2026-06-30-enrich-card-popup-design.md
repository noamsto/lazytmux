# Enrich Card Popup (`tmux-enrich-card`)

**Date:** 2026-06-30
**Status:** LOCKED — passed adversarial spec-critic review (REVISE → all 4 blocking
issues B1–B4 and concerns N1–N4 resolved); ready for implementation planning.

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

It also adds a shared library sub-package `picker/enrichstate/` (PR-state
precedence → semantic role; see "Shared PR-state precedence" below), imported by
both `enrichcard` and the existing `statusline`. As a library (no `main`) it
needs no entry in `subPackages` — only the binary packages do.

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

1. **Stable, passed once as CLI flags at launch:**
   - Theme colors, threaded from tmux's already-resolved `@thm_*` options (so
     light/dark needs no special handling — the flags carry the active palette).
     The tmux options are underscored; the flags map them to non-underscored
     names, matching how `tmux-statusline` is invoked at
     `config/tmux.conf.nix:609`: `--thm-bg` (`@thm_bg`), `--thm-fg` (`@thm_fg`),
     `--thm-red`, `--thm-green`, `--thm-peach`, `--thm-mauve`, `--thm-blue`,
     `--thm-overlay0` (`@thm_overlay_0`), `--thm-subtext0` (`@thm_subtext_0`),
     and `--thm-lavender` (`@thm_lavender`). Note `@thm_lavender` is **new Go
     wiring** — no existing Go binary consumes it yet, so the keybind must add
     it to the flag list (it is a real option, used today only by the reflow
     script).
   - Enrich icon glyphs: `--icon-linear`, `--icon-github`, `--icon-pending`,
     `--icon-success`, `--icon-failure`, `--icon-merged`, `--icon-closed`,
     `--icon-conflict`. **These MUST use `enrichIconSetRaw.*`
     (`config/tmux.conf.nix:146`), NOT the `##`-escaped `enrichIconSet.*` that
     `tmux-statusline` receives.** statusline gets the escaped form only because
     its stdout is re-parsed by tmux as a format string, where `##` collapses
     back to `#`. The card is a raw TTY program under `display-popup -E`; its
     output is never re-parsed, so a user-overridden glyph containing `#` (via
     `enrich.icons`) would render as a literal `##`. The default glyphs contain
     no `#`, so the escaped set would pass a naive smoke test and ship broken
     only for users who customize.

2. **Live window state, polled** via a single `tmux show-options -w -t <target>`
   at startup and on every ~1s `tea.Tick`. Parsed into the model:
   - Issue: `@issue_provider`, `@issue_id`, `@issue_title`, `@issue_url`.
   - PR: `@pr_number`, `@pr_title`, `@pr_state`, `@pr_check_state`, `@pr_url`,
     `@pr_mergeable`.
   - Branch/worktree: `@branch`, `@worktree`, `@git_root`.
   - Claude: `@window_task`, `@window_ai_name`, `@window_claude_ago`,
     `@active_pane_icon`.

   **Verified mechanism:** a `display-popup -E` child inherits the server's
   `$TMUX` env, so the popup process can call `tmux show-options -w -t <target>`
   back against the same server (confirmed against a running server — the popup
   child read `@thm_mauve` successfully). Every `show-options` call passes
   `-t <target>`, and `--target` is `session_id:window_id` (not name — sidesteps
   the documented numeric-session-name ambiguity gotcha) so the popup tracks the
   window it was launched from regardless of focus changes.

The card never re-implements the enrich or claude-status logic; it reflects the
window options those pipelines already write.

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

**Small-terminal behavior:** `display-popup` clamps to the client size, so on a
narrow or short terminal the fixed `-w 64 -h 18` request shrinks. The card reads
its actual size from the bubbletea `WindowSizeMsg` and degrades rather than
clipping blindly: below a width floor it drops the worktree-path line and
ellipsizes titles harder; below a height floor it collapses the blank spacer
rows first, then the Claude block (lowest-priority), always preserving the
footer hint row (the keybinds are the point) and the issue/PR identity lines.

## Shared PR-state precedence

The PR badge's color/glyph precedence (merged→mauve, closed→dim overlay,
`conflicting`→red and wins, pending→peach, failure→red, else success→green) is
**already implemented** in `prBadge()` at `picker/statusline/main.go:94-134`,
and is the exact logic this repo has had to fix across 3–5 renderers at once
(see the "merged PR badge stuck" and "closed PR renders as live" regressions).
Re-implementing it a sixth time in `enrichcard` would guarantee the next fix is
a six-renderer change.

To avoid that, this work **extracts the decision into a small shared package in
the picker module** (e.g. `picker/enrichstate/`) exposing a pure function:

```
state + check + mergeable  →  (ColorRole, GlyphRole)
```

It returns semantic *roles* (an enum: `Merged`, `Closed`, `Conflict`, `Pending`,
`Failure`, `Success`), not output strings — because the two consumers emit
different formats: `statusline` maps roles to tmux `#[fg=...]` strings, the card
maps them to lipgloss styles + the `--icon-*` glyph. `statusline.prBadge()` is
refactored to call this shared function (preserving its exact current output,
guarded by its existing tests), and `enrichcard` calls the same function. This
keeps the two Go renderers on one codepath; the three bash renderers remain
separate (different language) and out of scope here.

## Interactions

| Key | Action |
|-----|--------|
| Key | Action |
|-----|--------|
| `o` | `xdg-open @issue_url` (no-op if empty); brief "opened ↗" flash in footer |
| `p` | `xdg-open @pr_url` (no-op if empty); same flash |
| `r` | force-refresh — see below (disabled when `@branch` is empty) |
| `q` / `Esc` / `Ctrl-c` | `tea.Quit` → popup closes |

**Refresh (`r`)** runs the same command the old `-T enrich r` keybind ran:
`tmux-pr-enrich --target <t> --branch <@branch> --dir <@worktree|@git_root|pane_path> --force`.
It is wired as a bubbletea `tea.Cmd`: the handler exec's the poller, **waits for
that process to exit** (the `--force` single-target path is a synchronous `gh`
call, ~1–2s), then returns a `refreshDoneMsg`. `Update` on that message triggers
one immediate `show-options` re-read and clears the `⧗ refreshing…` spinner. This
converges deterministically — it does **not** key the spinner-clear on detecting
a *changed* `@pr_*` value, because the poller always re-writes the same `@pr_*`
values on a no-op refresh (`tmux-pr-enrich.sh:103` only skips the reflow, not the
option write), so a value-diff would never fire on an unchanged-state refresh and
an unrelated background full-pass could clear it prematurely.

**Empty-branch guard (B3):** the single-target force path is gated on a non-empty
branch (`[[ -n $target && -n $branch ]]`, `tmux-pr-enrich.sh:314`); an empty
`@branch` makes the script exit 0 having done nothing. The old keybind hit this
dead path invisibly; the card must not arm a 1–2s phantom spinner for a command
that structurally cannot run. So when `@branch` is empty the `r` action is
**disabled**: the footer shows `r` greyed with a "no branch — nothing to refresh"
hint instead of starting the spinner.

**Platform note (N4):** `o`/`p` use `xdg-open`, which is Linux-only — this is
parity with the existing keybind (`config/tmux.conf.nix:544`), not a new
regression, but it is a known gap on the macOS target (which would need `open`).
Out of scope here; recorded so it is not mistaken for verified cross-platform
support.

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
  closed, conflicting/mergeable, the refreshing-spinner state, the
  empty-branch-disabled-`r` state, and a sub-floor `WindowSizeMsg` (degraded
  layout). Assert the expected glyph/color tokens and labels appear.
- **Shared `enrichstate` package:** table-driven tests for the
  `state+check+mergeable → (ColorRole, GlyphRole)` precedence, covering the
  cases the statusline regressions hit (merged-wins-over-pending,
  closed-vs-merged, conflicting-wins). The refactored `statusline.prBadge()`
  keeps its existing tests green, proving output is unchanged.

No new entry in `nix flake check` beyond the Go test run already covered by the
module build; manual smoke test via `nix build .#default` + opening the popup.

## Approaches considered and rejected

- **Static bash `display-popup` printf card** — no event loop, so no live
  refresh; fails the in-place-feedback goal.
- **A `--card` mode on the existing `picker` binary** — couples an unrelated
  concern into the ~25k-line `main.go`; a focused sibling package matches the
  `splash` precedent and keeps each binary single-purpose.
