# lazytmux splash animation — design

## Goal

A Ghostty/Amp-style animated splash: a dithered-ASCII sleepy-cat mascot with a
continuous gradient-ripple shimmer, shown once in a tmux popup when a fresh,
empty session is first attached. Dismisses on any keypress, leaving the user at
a clean shell prompt. Enabled by default.

## Mascot

A curled / loafing **sleepy cat** with drifting `z z z` and a `lazytmux`
wordmark beneath it. The cat unifies the name ("lazy") with the Catppuccin
theme (cat-themed pastel palettes) already used across the scripts.

- Rendered as a **static** dithered-ASCII text file using the existing density
  ramp (`$ @ * + = x ~ · ` and spaces). The art never changes frame-to-frame —
  the ripple is purely a moving color overlay — so exactly one art asset is
  needed and there are no runtime image dependencies.
- The art is hand-authored / pre-baked into the repo as `splash/assets/cat.txt`
  and embedded into the Go binary via `//go:embed`. (If a source image is
  preferred later, it can be converted with `chafa` at authoring time and the
  resulting text committed — the runtime contract is unchanged: a text file.)

## Architecture

### New Go binary: `tmux-splash`

A bubbletea program living in a sibling `splash/` directory, packaged exactly
like the existing `picker/` (its own `splash/default.nix` calling
`buildGoModule`, imported from `config/tmux.conf.nix` the same way
`picker-generate` is). Bubbletea is chosen because it owns the alt-screen, runs
a steady framerate for the ripple, and reads a single keypress to dismiss —
a bash loop would fight raw-input handling and flicker.

Responsibilities:

- `//go:embed assets/cat.txt` — load the static mascot glyph grid.
- Detect light/dark from `$XDG_STATE_HOME/theme-state.json` (same contract as
  the shell scripts) and pick Catppuccin **Latte** or **Mocha** accents.
- Each frame, compute a per-cell color from a diagonal wave:
  `phase = (x + y) * k - t`, mapped across a small Catppuccin gradient
  (e.g. blue → sapphire → lavender → mauve) by lightness/hue. Non-glyph cells
  (spaces) stay uncolored. The glyphs are fixed; only their color advances.
- Center the grid in the popup viewport (handle `WindowSizeMsg`).
- Quit on **any** `KeyMsg`. Use the alt-screen so the popup leaves no trace
  when it exits.

The binary takes no arguments and reads no tmux state beyond the theme file —
keeping it a pure, independently testable renderer.

### Trigger: hook + popup (no shell coupling)

A global tmux hook fires a tiny bash gate, which conditionally opens the splash
as a full-size popup. This is shell-agnostic (your shell is fish, but nothing
touches shell rc) and matches the existing picker idiom (`display-popup`).

1. **Hook:** `set-hook -g client-attached` runs `tmux-splash-maybe` (backgrounded
   with `run-shell -b`). `client-attached` fires *after* the client is attached,
   so the popup always has a client to draw on (avoids the race a
   `session-created` hook would have).
2. **Gate** (`tmux-splash-maybe`, a new packaged bash script) shows the splash
   **only when all hold**:
   - session option `@splash_shown` is unset — it is set to `1` immediately
     after firing, so re-attach never replays it;
   - the session is genuinely fresh: `#{session_windows} == 1` **and**
     `#{window_panes} == 1`. This scopes the splash to "new empty session" and
     keeps it off window-splits, new windows, and busy/reattached sessions.
3. On pass, the gate runs:
   `display-popup -E -w 100% -h 100% -b none tmux-splash`.
   `-E` closes the popup when the program exits; the underlying pane already has
   a clean prompt, so dismissing the splash drops the user straight to it. No
   keystroke is injected into or consumed by the shell.

The `tmux-splash` binary is added to the wrapped-tmux PATH (alongside the other
scripts) so the hook can reference it by bare name.

### Module option

`programs.lazytmux.splash.enable`, **default `true`** (`lib.mkEnableOption`
with default override, matching the `enrich`/`persist` option style). When
false:

- the `client-attached` hook is not emitted, and
- the `tmux-splash` binary / `tmux-splash-maybe` gate are still built but never
  invoked (or omitted from PATH — implementation detail for the plan).

The enable boolean flows from the module into the config builder the same way
`enrichEnable` does today.

## Data flow

```
client attaches to session
        │  client-attached hook
        ▼
tmux-splash-maybe  ──reads──▶ @splash_shown, session_windows, window_panes
        │ (all gates pass)
        │ set @splash_shown 1
        ▼
display-popup -E -w 100% -h 100% -b none tmux-splash
        │
        ▼
tmux-splash (bubbletea)
   embed cat.txt → detect theme → animate diagonal gradient ripple
        │ any keypress
        ▼
exit → popup closes → clean shell prompt
```

## Error handling / edge cases

- **Missing theme file:** default to Mocha (dark) — same fallback the scripts use.
- **Popup smaller than art:** center and clip; never crash. If the viewport is
  too small to hold the grid, render what fits (top-left anchored) rather than
  erroring.
- **No client yet / detached:** impossible by construction — the hook is
  `client-attached`. If `display-popup` still fails (e.g. nested popup), the
  backgrounded `run-shell -b` swallows it; worst case the splash is skipped.
- **Re-source of config / `prefix + r`:** does not re-fire (no client-attached
  event; and `@splash_shown` is already set on the session).
- **Reattach to an existing single-pane session that never showed the splash:**
  will show on first attach — acceptable, it's still a "fresh, empty" session by
  the pane/window test. The `@splash_shown` flag prevents any repeat.

## Testing

- **Go unit tests** (`splash/*_test.go`, run under `nix flake check` like the
  picker tests): gradient phase math (wave wraps correctly, deterministic per
  `t`), theme selection from a fixture theme-state file, grid centering for a
  given viewport size, and that a `KeyMsg` produces `tea.Quit`.
- **Gate logic:** the `tmux-splash-maybe` decision (the `@splash_shown` +
  window/pane checks) is pure enough to cover with a `bats` test in the existing
  `tests/` harness, mocking `tmux` query output — mirroring `tests/enrich.bats`.
- **Manual:** `nix build .#default`, start a fresh session, confirm the ripple
  plays once and dismisses on a key; split a window / open a second window and
  confirm no replay; detach + reattach and confirm no replay.

## Out of scope

- Configurable mascot / alternate art (single baked cat for now).
- Configurable animation style (gradient ripple only; the other styles
  considered — dissolve, typewriter, static flicker — are not built).
- Showing the splash anywhere but a fresh empty session (no keybind popup, no
  picker header, no startup-service variant).
