# Bridge window names via `@window_bridge_name` (#196)

**Status:** design approved 2026-07-22
**Issue:** [#196](https://github.com/noamsto/lazytmux/issues/196) — part of #167 (M2.2)

## Problem

On a real lazytmux server, a mirrored remote-bridge window never shows the
**remote window's name**. It shows the pane cwd basename (literally `lazytmux`)
instead of e.g. `shell` / `clock`.

Two independent causes, confirmed on a g5 → tp-g6 hardware drive (both
`tmux next-3.8`):

1. **The daemon never captures remote names on seed/add.** The seed and
   window-add `list-windows` calls
   (`picker/remotebridge/daemon/daemon.go:121`, `:363`) request only
   `#{window_index} #{window_id}` — the name field is never fetched. Names
   arrive *only* via live `%window-renamed` notifications.

2. **A circular clobber overwrites any name that is set.** For a `@bridge_win`
   window, `tmux-reflow-windows.sh` (lines 123–137) sources the label from
   `#{window_name}` and writes it to `@window_label_short`;
   `automatic-rename-format` (`config/tmux.conf.nix:779`) renders
   `@window_label_short` back into `window_name`. So
   `window_name → @window_label_short → automatic-rename → window_name` is a
   closed loop, and `tmux-update-icons.sh:329-335` re-asserts
   `automatic-rename on` every 1s tick. The remote name never enters the loop;
   the seed's cwd-fallback (`#{b:pane_current_path}` → `lazytmux`) gets locked
   in. Even a live `%window-renamed` name is re-derived away within a tick.

**Why CI misses it:** the M2.2 integration test
(`tests/remote-m2-integration.bats`) asserts a remote rename propagates to the
local `window_name`, but runs a vanilla `tmux -L` server (the `--test-local`
seam) with no `automatic-rename` and no `tmux-update-icons`. The clobber only
exists under the real wrapped config.

## Approach

Introduce **`@window_bridge_name`**, a daemon-owned window option holding the
remote window's name for a `@bridge_win` window. It joins the existing
daemon/hook-owned label inputs (`@window_ai_name`, `@window_task`): **only the
daemon writes it; only reflow reads it.**

reflow's existing `@bridge_win` branch reads `@window_bridge_name` instead of
the volatile `#{window_name}`. This breaks the circular clobber — the source of
truth becomes a stable, daemon-owned option, and `window_name` is reduced to
pure derived output. `automatic-rename` stays on and simply renders the option.

Rejected alternative (B): exempt `@bridge_win` windows from the update-icons
`automatic-rename on` re-assertion and from `build_window_label`, set
`automatic-rename off`, and let the daemon own `window_name` via
`rename-window`. Rejected — it fights the naming pipeline, adds a special case
to the hot 1s tick, and duplicates state.

## Data flow

```
remote name
  → daemon (seed/add list-windows name field, or %window-renamed)
  → set-option -w @window_bridge_name <sanitized name>
  → tmux-reflow-windows.sh reads @window_bridge_name
  → @window_label_short
  → automatic-rename-format
  → window_name (display only)
```

The source of truth is `@window_bridge_name`. `window_name` is now derived
output, never an input — so the icon-append in `automatic-rename-format` no
longer accretes across ticks.

## Changes

### Daemon (`picker/remotebridge/daemon/`)

- **Seed enumeration** (`daemon.go:121`) and **window-add** (`:363`): add
  `#{window_name}` to the `list-windows` format. `parseWindowList` gains a
  `name` field on its window struct.
- **Seed loop** (~186–204) and **`addWindow`**: after creating each local
  window, `set-option -w -t <localWin> @window_bridge_name <name>`.
- **`%window-renamed`** (`translate.go`): change the emitted command from
  `rename-window -t <localWin> <name>` to
  `set-option -w -t <localWin> @window_bridge_name <name>`.
- **Sanitize** the remote name before writing: strip/replace `|` (the reflow
  FMT delimiter) and any control chars. Defines the delimiter-break out of
  existence rather than relying on field position alone.
- **Instant-floor rename:** at seed (and window-add), also emit one
  `rename-window -t <localWin> <name>` so there is no ≤1s flash of the cwd name
  before the first reflow tick. This mirrors the AI-naming "seed + persist"
  pattern; the durable value is still `@window_bridge_name`, and reflow
  self-heals `window_name` on the next tick regardless.

### reflow (`scripts/tmux-reflow-windows.sh`)

- Add `@window_bridge_name` to `FMT` (line 105) **at the very end** (same
  `|`-safety posture the trailing `window_name`/`@window_task` fields have
  today — belt-and-suspenders alongside the daemon-side sanitize).
- In the `@bridge_win == 1` branch (lines 123–137): use `@window_bridge_name`
  as the label source, falling back to `window_name` when it is empty (covers
  the brief interval before the daemon's first write, and any non-daemon path).

## Edge cases

- **`|` in remote name:** sanitized daemon-side *and* the field lives at
  end-of-FMT. Either alone would suffice; both together make it robust.
- **Empty remote name:** reflow falls back to `window_name`; if that is also
  empty tmux shows its own default. No new failure mode.
- **Icon accretion:** eliminated — reflow reads the clean option, so
  `window_name` (= option + icon) is never fed back as the label source.
- **Non-bridge windows:** unaffected — the `@bridge_win` branch is the only
  reader of `@window_bridge_name`.

## Testing (closes the CI blind spot)

1. **Reflow bats** (existing reflow test harness): a `@bridge_win=1` window with
   `@window_bridge_name=foo` (and `window_name=lazytmux`) → assert the computed
   `@window_label_short == "foo"`, i.e. the option wins over `window_name`.
   Directly exercises the fix without needing a full `automatic-rename`
   simulation.
2. **Daemon integration** (`tests/remote-m2-integration.bats`): assert
   `@window_bridge_name` is set on seed and updated after a remote
   `rename-window` — asserting the *option*, not local `window_name` (the
   latter is exactly what the vanilla-tmux server can't validate).
3. **Parse unit test** (`daemon/translate_test.go` or the parse test): confirm
   `parseWindowList` carries the new name field, including a name containing a
   space and a `|`.

Scope is proportionate: three focused test additions, no new test
infrastructure.
