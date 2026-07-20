# Picker responsive layout — bottom preview + inline-identity window rows

Issue: [#175](https://github.com/noamsto/lazytmux/issues/175)
Branch: `feat/175-picker-responsive`

## Problem

The bubbletea pickers (`prefix + s` sessions, `prefix + w` windows) don't adapt
well to terminal size:

- The live pane-content preview (`^/`, on by default) renders **side-by-side**
  on wide terminals (`portrait()` = `width < 2*height`), squeezing the list to
  45–60% and truncating window labels + the trailing identity column off the
  right edge.
- prefix+w window rows put the worker (crew) name in a **leading reserved
  column** (padded even when a window is untagged), show the branch as the name,
  and push the Linear/GH ticket into a **trailing identity column** that clips
  first on narrow terminals — burying the highest-value signal.
- Truncation caps are fixed (`name` ≤ 40, identity ≤ 32) regardless of terminal
  width.

## Goals

1. Preview always renders **below** the list; the list always gets full width.
2. prefix+w rows: worker after the index (no reserved space when absent); ticket
   folded **inline as the name**; PR badge stays trailing.
3. Name/identity truncation **adapts to terminal width**.

Non-goals: no changes to shell scripts, the status bar, or window options. The
`@window_*` / `@crew_*` / `@pr_*` options remain the single source of truth.

## Design

All changes are in `picker/tui.go` (+ `picker/tui_test.go`).

### 1. Preview always at the bottom

Remove the landscape (side-by-side) code path entirely; the body always stacks
vertically.

- `View()` — drop the `portrait()` branch; always
  `JoinVertical(list, sep, preview)` when `showPreview`.
- `renderSeparator()` — always the horizontal rule (`─ × innerWidth`).
- `listWidth()` — always `innerWidth()` (delete the 45/60% split).
- `previewWidth()` — always `innerWidth()`.
- `listHeight()` / `previewHeight()` — always the top/bottom vertical split
  (current portrait math becomes unconditional).
- `inPreview()` / `listIndexAt()` — delete the `x >= listWidth()` side
  hit-testing; preview is the region below the list rows.
- `moveCursor` mouse path unaffected.
- Delete `portrait()` and its now-dead callers.

Preview stays **on by default** (`showPreview: true`) and `^/`-toggleable, for
both pickers. On a wide terminal the list gets full width but ~50% height; the
user toggles preview off for a full-height list.

### 2. Window row redesign (`renderWindowItems`)

New row shape:

```
{tree} {marker} {index}: {crew?} {inline-identity}   {icons} {prBadge}
```

Concretely (matches the approved mockup):

```
├─ 2: rust  ENG-7290 fix-and-lock…   🧠 z^z ✔
╰─ 4: coral ENG-7402 narrow-mode…    🧠 ◐
   1: lazytmux                       🧠 ✔
```

- **Worker after the index, no reserved column.** Remove the leading `crewCell`
  padding (`maxCrewDW`). Crew renders right after `N: `, colored by
  `@crew_color`, only when `crewName != ""`. Untagged rows get no gap.
- **Inline identity as the name.** The label text uses status-bar priority
  (mirroring `build_window_label`):
  1. `labelID + labelRest` — issue glyph + id (mauve accent) + title (dim).
     `labelID` (`@window_label_id`) already carries the provider glyph.
  2. non-default `branch` (faint) — when set, not echoing the name, not
     `main`/`master`.
  3. `w.name` — repo basename (plain), the existing fallback.
- **Trailing identity column removed**, along with the `anyPR` / `identityCol`
  reserve logic. The PR badge (`prBadge`) still trails after the icons.
- **Alignment.** The full label part (`N: {crew?} {identity}`) pads to a shared
  max width so the `icons` column lines up across rows (as the mockup shows).

`plain` / `searchText` are rebuilt from the same parts. Search still covers
session, name, identity text, PR number, and crew name.

### 3. Adaptive name width

`renderWindowItems` (and `buildWindowItems`) take a `width int` param (the list
width in cells). The inline-identity cap is computed from it:

```
identityCap = listWidth
            - leadingOverhead   // widest "{tree} {marker} N: {crew?} " prefix
            - trailingReserve   // iconCol + PR badge + gaps
```

clamped to `[minIdentityCap, maxIdentityCap]` (e.g. `[12, 48]`). Wider terminal
→ longer titles; narrow → shorter, with icons + PR always pinned visible.

Because the label column pads to a shared width, the cap is computed once for
the whole list from the widest leading prefix, then applied to every row's
identity before measuring column maxima.

**Threading width into the model:**

- `runTUI` builds initial items with `width = 0` → a sensible default cap (so
  first paint before `WindowSizeMsg` is reasonable).
- `refreshDataCmd` closes over `m.width` and passes it to `buildWindowItems`, so
  the 1s tick keeps the adaptive cap.
- On `WindowSizeMsg`, when the width **changed** and `windowMode`, the model
  also kicks `refreshDataCmd()` for an immediate width-aware rebuild. Guarded on
  width change so it fires ~once (tmux popups are fixed-size); avoids re-forking
  `collectClaudePanes` on resize storms.

`width = 0` (unknown) is the sentinel for "use default cap, don't compute
adaptively".

## Testing

- `picker/tui_test.go` `renderWindowItems` cases updated for:
  - new column order (crew after index, identity inline, no trailing column);
  - untagged rows having no crew gap;
  - the new `width` parameter (assert adaptive cap at a couple of widths, and the
    `width = 0` default).
- `nix build .#default` + `nix flake check` green.
- Manual: `prefix + w` on a wide terminal (bottom preview, full-width list,
  inline `ENG-…` names, aligned icons) and a narrow one (shorter titles, icons +
  PR still visible). `prefix + s` preview also bottom.

## Rollout

Standard lazytmux deploy: input bump in nix-config → HM switch. The popup
resolves `tmux-window-picker` via the tmux server's PATH, which goes stale after
a bump — so a **tmux server restart** (or new server) is needed to pick up the
new picker binary. `prefix + r` alone won't refresh the popup's binary path.
