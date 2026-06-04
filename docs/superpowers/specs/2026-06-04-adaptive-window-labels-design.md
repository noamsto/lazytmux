# Adaptive window labels — design

## Problem

Window tabs show only the issue **id** (e.g. `󰰍 ENG-1957`) — never the ticket
title — so you can't tell what a window is for without switching to it. Worse,
the label is computed in three disconnected places with **different content**:

- single-line list (`config/tmux.conf.nix` status-format[1]) renders
  `#{window_name}` → the enriched `id` form;
- multi-line list (`tmux-reflow-windows` `ENTRY`) renders `#{=30:@branch}` → the
  **branch**, not the id;
- `automatic-rename-format` is a third variant.

So crossing from single- to multi-line silently changes what each tab says.

## Goals

- One label source of truth; single- and multi-line modes show identical text.
- Tabs adapt to **real available width**: full ticket/branch titles when there's
  room, shrinking to the bare id when windows pile up.
- The **active** window is prioritized for its full title.
- Updates follow focus changes and enrichment writes, not just structural events.

## Non-goals

- Row 0 full title — already shipped (issue/PR titles no longer truncated to 25).
- Nerd Font enrichment glyphs — already shipped.
- Per-window "some long / some short" mixing (see Policy).
- Touching `scratch-*` sessions (they manage their own bar; still skipped).

## Label model (one source of truth)

For each window, two candidate labels are derived from the same data:

| | enriched (`@issue_id` set) | plain (branch/dir only) |
|---|---|---|
| **short** | `<provider> <id> <pr-glyph>` e.g. `󰰍 ENG-1957 ` | branch basename, e.g. `fix-login` |
| **long**  | `<provider> <id> <pr-glyph> <issue_title>` | full branch |

- `<provider>` = `enrichIconSet.linear`/`.github`; `<pr-glyph>` = the colored
  check-state glyph (no `#number`) when a PR exists, else nothing.
- The **short enriched** form keeps the PR-state glyph — one cell, high-value
  at-a-glance PR health.
- `window_name` (via `automatic-rename-format`) stays the **short** form so
  `choose-tree` pickers and window titles remain compact. Only the status bar
  reads the adaptive label.

## Allocation policy (two-tier)

`tmux-reflow-windows` already knows window count + client width and measures
display widths (`measure_display_width`, handles nerd/emoji cells). It will:

1. Compute every window's short and long display width.
2. **If all-long fits** on the available window lines (≤3) → every window long.
3. **Else** → active window long, all others short; greedily pack onto lines;
   anything past the last line's edge truncates (tmux line truncation).

Two-tier (not per-window greedy) is deliberate: greedy "fit as many long as
possible" looks ragged and reshuffles every time focus moves. Two-tier is
predictable. Revisitable if it feels too coarse.

## Components & data flow

- **`tmux-reflow-windows`** (existing, event-driven) gains label computation:
  per window it builds short/long labels, runs the two-tier allocation, and in
  its existing batched `tmux source -` pass sets `@window_label` per window.
- **Templates collapse to reading the var:**
  - single-line global template (`config/tmux.conf.nix` status-format[1]) →
    `#{@window_label}` (active branch keeps the green/bold styling).
  - multi-line `ENTRY` → `#{@window_label}`.
- Multi-line switches from padded columns (`#{p${P}:…}`) to **natural widths**:
  once labels vary in length, column alignment is impossible; tree prefixes
  (`├─`/`╰─`) still align line starts.
- `automatic-rename-format` is simplified to emit only the **short** label
  (shared logic with the reflow short form).

## Recompute triggers & caching

Reflow today fires on window add/remove/resize, cached by
`@reflow_key = "<window-count>:<width>"`. Add:

- **focus change** (`window-changed` hook) so the long label follows the active
  window;
- **after enrichment writes** — `tmux-issue-stamp` and `tmux-pr-enrich` call
  reflow once they've written `@issue_title`/`@pr_*`, so titles appear without
  waiting for a structural event.

`@reflow_key` gains the **active window id** and an **enrich-version stamp** (a
cheap hash/counter of the enrich vars) so focus and enrichment changes actually
bust the cache.

## Edge cases

- **No enrich CLI / no title:** long == short for that window (nothing to add);
  it simply never grows.
- **Plain window, long==short** (short branch): no change at any width.
- **Very long active title:** truncates at the screen edge; never forces a new
  line by itself (allocation is computed on measured widths first).
- **`scratch-*`:** unchanged, skipped.

## Testing

- `tests/test-display.sh` (manual, after `nix build .#default`) for visual
  verification across window counts and widths.
- Detached-session capture harness (`tmux new-session -d -x W -y H … ;
  capture-pane`) to assert short vs long at chosen widths.
- `nix flake check` for shellcheck/shfmt on the reworked script.

## Rollout

Behind the existing `programs.lazytmux.enrich.enable` / persist machinery; no new
option. Default behavior changes (tabs now show titles when space allows), which
is the requested improvement.
