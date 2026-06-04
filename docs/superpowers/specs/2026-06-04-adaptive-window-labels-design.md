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

## Architecture audit — does the current impl hinder us?

No, but it dictates the seams. Three writers touch per-window status state today:

| writer | cadence | owns |
|---|---|---|
| `tmux-update-icons` | every 1s (`#()`) | `@branch`, `@window_icon_display`, `@window_icon_padded` (process + **claude** icons), `@active_pane_icon` |
| `tmux-reflow-windows` | on structural/resize hooks (cached) | line count, split points, `@window_icon_padded`, status-format[1-3] |
| `tmux-issue-stamp` / `tmux-pr-enrich` | post-switch / background poll | `@issue_*`, `@pr_*` |

**The load-bearing constraint:** process/claude icons live in `@window_icon_*`
and must stay 1s-fresh (the claude spinner animates). Therefore the adaptive
**label must be text-only** — provider/id/pr-glyph/title — and the dynamic icons
must continue to be appended *separately* by the template from `@window_icon_*`.
Baking icons into a reflow-built label would freeze the spinner between events.
This is the decision that reshapes the original "one resolved `@window_label`".

Given that, the current split is actually a clean fit: reflow already owns the
global view (all windows + width) needed for budgeting; update-icons stays the
hot path for icons and is **left untouched** (no per-1s label work).

### Architecture decisions

- **A1 — Label is text-only**, never includes process/claude icons. Template
  renders `{idx}: {label} {@window_icon_padded}`; icons stay 1s-fresh.
- **A2 — Store both variants + a mode flag, resolve in the template.** Reflow
  sets per-window `@window_label_short` and `@window_label_long`, plus a session
  `@labels_mode` ∈ {`long`,`active`}. The template picks:
  `#{?#{==:#{@labels_mode},long},LONG,#{?window_active,LONG,SHORT}}`.
  Because `window_active` is evaluated live every render, **single-line focus
  changes need no reflow** and are never stale.
- **A3 — Global budget collapses to one flag.** Reflow's existing width math
  decides `@labels_mode`: `long` if all-long fits the available lines, else
  `active`. No per-window budget negotiation.
- **A4 — Label building is a pure, testable lib function** (extend
  `lib-enrich.sh`, already bats-tested): compose short/long from the `@issue_*`/
  `@pr_*`/branch inputs; reflow calls it and measures with
  `measure_display_width`. Keeps the 224-line reflow script from growing a
  second brain.
- **A5 — `automatic-rename-format` reuses the short-label logic** so
  `window_name` (pickers/titles) and the tab short form can't drift apart again.

## Label model (one source of truth)

For each window, two candidate labels are derived from the same data:

| | enriched (`@issue_id` set) | plain (branch/dir only) |
|---|---|---|
| **short** | `<provider> <id> <pr-glyph>` e.g. `󰰍 ENG-1957 ` | branch basename, e.g. `fix-login` |
| **long**  | `<provider> <id> <pr-glyph> <issue_title>` | full branch |

- Text + glyphs only, **no color and no process/claude icons** (see A1). The
  template applies active/inactive color to the whole tab and appends the icon
  column; `measure_display_width` therefore needs no color stripping.
- `<provider>` = `enrichIconSet.linear`/`.github`; `<pr-glyph>` = the
  check-state glyph (no `#number`) when a PR exists, else nothing.
- The **short enriched** form keeps the PR-state glyph — one cell, high-value
  at-a-glance PR health.
- `window_name` (via `automatic-rename-format`) stays the **short** form so
  `choose-tree` pickers and window titles remain compact (A5).

## Allocation policy (two-tier → one flag)

`tmux-reflow-windows` already knows window count + client width and measures
display widths (`measure_display_width`, handles nerd/emoji cells). It will:

1. Compute every window's short and long display width (label width + the
   fixed icon column it already accounts for).
2. **If all-long fits** on the available window lines (≤3) → `@labels_mode=long`.
3. **Else** → `@labels_mode=active` (active window long, the rest short).

Two-tier (not per-window greedy) is deliberate: greedy "fit as many long as
possible" looks ragged and reshuffles every time focus moves. Two-tier is
predictable. Revisitable if it feels too coarse.

## Components & data flow

- **`lib-enrich.sh`** gains a pure `build_window_label` helper (short/long from
  `@issue_*`/`@pr_*`/branch) — unit-tested in `tests/enrich.bats` (A4).
- **`tmux-reflow-windows`** (event-driven) calls it per window, measures both
  variants, decides `@labels_mode`, and in its existing batched `tmux source -`
  pass sets `@window_label_short` + `@window_label_long` per window and
  `@labels_mode` per session. Splits are packed using each window's **chosen**
  width (active→long when `@labels_mode=active`).
- **Templates select live (A2)** and append the dynamic icon (A1):
  - single-line (`config/tmux.conf.nix` status-format[1]) and multi-line `ENTRY`
    both render `{idx}: {SELECT} {@window_icon_padded}`, where
    `SELECT = #{?#{==:#{@labels_mode},long},#{@window_label_long},#{?window_active,#{@window_label_long},#{@window_label_short}}}`,
    wrapped in the existing active/inactive color.
- Multi-line switches from padded columns (`#{p${P}:…}`) to **natural widths**:
  once labels vary in length, column alignment is impossible; tree prefixes
  (`├─`/`╰─`) still align line starts.
- `automatic-rename-format` reuses the short-label logic (A5).

## Recompute triggers & caching

Reflow today fires on window add/remove/resize, cached by
`@reflow_key = "<window-count>:<width>"`. Add:

- **focus change** (`window-changed` hook) — needed only to re-pack **multi-line
  `active` mode**, where the active window's long width shifts the split points.
  Single-line and `long` mode don't need it (the template handles focus live,
  A2); reflow early-exits via the cache when the key is unchanged.
- **after enrichment writes** — `tmux-issue-stamp` and `tmux-pr-enrich` invoke
  reflow with a `--force` flag (cache bypass) once they've written
  `@issue_title`/`@pr_*`, so new titles/PR states recompute labels + mode.

`@reflow_key` gains the **active window id** (so focus changes bust it). Enrich
freshness is handled by the `--force` path rather than a stamp in the key.

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
