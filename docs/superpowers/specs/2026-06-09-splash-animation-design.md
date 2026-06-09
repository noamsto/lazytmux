# lazytmux splash / welcome buffer — design

## Goal

A Ghostty/Amp-style animated welcome buffer, shown once in a tmux popup when a
fresh, empty session is first reached: a dithered-ASCII sleepy-cat mascot with a
continuous gradient-ripple shimmer, **plus a lazyvim-style keybind cheatsheet**
beside it. Auto-dismisses after a short timeout or on any keypress, leaving the
user at a clean shell prompt. Enabled by default.

## Layout (welcome buffer)

Centered in the popup, top-to-bottom:

```
            <sleepy cat mascot — dithered ASCII, gradient ripple>
                              z z z
                       l a z y t m u x

      prefix + s   Sessions          prefix + g   LazyGit
      prefix + w   Windows           prefix + b   btop
      prefix + a   Claude windows    prefix + R   Restore snapshot
      prefix + i   Issues / PRs      prefix + u   Undo close

                  press any key to dismiss
```

- The **mascot** shimmers (gradient ripple). The **wordmark** and **cheatsheet**
  are static, rendered in flat Catppuccin colors (keys in the accent color,
  labels in subtext) so they stay readable.
- `prefix` is **interpolated from `cfg.prefix` at Nix build time** (same as other
  templated placeholders), so the tips always show the user's real prefix.
- The cheatsheet is a **curated static list** baked into the binary — it is not
  parsed from the live tmux config. Drift risk is accepted; the list lives next
  to the binds in review. (Out of scope: deriving it from the config.)

## Mascot

A curled / loafing **sleepy cat** with drifting `z z z` and the `lazytmux`
wordmark beneath it — unifies the name ("lazy") with the Catppuccin theme
(cat-themed pastel palettes) used across the scripts.

- Rendered as a **static** dithered-ASCII grid using the existing density ramp
  (`$ @ * + = x ~ ·` and spaces). The art never changes frame-to-frame — the
  ripple is purely a moving color overlay — so only one art asset per size is
  needed and there are **no runtime image dependencies**.
- **Two baked sizes** to handle small terminals: `cat.txt` (full) and
  `cat-small.txt` (compact). The renderer picks the largest art that fits the
  current viewport; if even the compact art doesn't fit, it drops the mascot and
  shows only the wordmark + cheatsheet rather than clipping the cat to mush.
- Hand-authored / pre-baked into the repo and embedded via `//go:embed`. (If a
  source image is preferred later it can be converted with `chafa` at authoring
  time and the text committed — the runtime contract is unchanged: a text file.)

## Architecture

### New binary `tmux-splash` — a subpackage of the existing Go module

To avoid a second `go.mod`/`go.sum`/`vendorHash` (the picker already vendors the
full `charm.land/bubbletea/v2` tree), `tmux-splash` is added as a **second main
package inside the existing picker module** (`github.com/noamsto/lazytmux/picker`),
at `picker/splash/` (package main) with its art under `picker/splash/assets/`.

- `picker/default.nix` builds both binaries from one module via
  `subPackages = ["." "splash"]` → one `vendorHash`, one dependency tree.
  `postInstall` keeps the existing `picker` → `tmux-picker-generate` rename and
  leaves `splash` as `tmux-splash` (or renames as needed).
- The directory living under `picker/` is a minor naming wart accepted in trade
  for shared vendoring; the module is effectively "the lazytmux Go tools."

Bubbletea is the right tool: it owns the alt-screen, runs a steady framerate for
the ripple, and reads a single keypress to dismiss — a bash loop would fight
raw-input handling and flicker.

`tmux-splash` responsibilities:

- `//go:embed assets/cat.txt assets/cat-small.txt` — load the static grids.
- Detect light/dark from `$XDG_STATE_HOME/theme-state.json` (same contract as the
  shell scripts) → Catppuccin **Latte** / **Mocha** accents. Missing file → Mocha.
- Each frame, color the **mascot** cells from a diagonal wave
  `phase = (x + y) * k - t` mapped across a small Catppuccin gradient
  (blue → sapphire → lavender → mauve). Glyphs are fixed; only color advances.
  Non-glyph cells stay uncolored.
- Render the static wordmark + cheatsheet (prefix injected at build time) below
  the mascot; center the whole block in the viewport (`WindowSizeMsg`).
- **Dismiss** on any `KeyMsg` **or** after `cfg.splash.timeout` seconds
  (`tea.Tick`), whichever first — so an unattended client is never stuck on a
  modal popup spinning forever. Use the alt-screen so exit leaves no trace.
- Truecolor gradient degrades gracefully on 256-color terminals via charm's
  `colorprofile` (already a dependency), which quantizes to the nearest palette.

Takes no arguments; reads no tmux state beyond the theme file — a pure,
independently testable renderer.

### Trigger: two hooks + a gate + popup (no shell coupling)

The splash must appear on **both** ways a user reaches a fresh session:

- `client-attached` — attaching a client to the server (`tmux` / `tmux attach`).
- `client-session-changed` — switching an already-attached client to a session,
  which is how the **picker** (`prefix + s`, `tmux switch-client -t <name>`)
  opens new sessions. Without this, picker-created sessions would never splash.

Both hooks `run-shell -b "tmux-splash-maybe"` (backgrounded). The gate dedups, so
double-binding is safe. `client-session-changed` fires on every session switch;
the gate exits in two cheap `tmux` queries when conditions don't hold.

**Gate** (`tmux-splash-maybe`, a new packaged bash script) fires the splash only
when **all** hold:

1. session option `@splash_shown` is unset. The gate **sets it to `1` first**
   (before launching the popup), minimising the TOCTOU window if two clients
   attach simultaneously; a rare double-fire is accepted, not guarded with a lock.
2. the session is fresh: `#{session_windows} == 1` **and** `#{window_panes} == 1`.
3. the active pane is at an interactive shell: `#{pane_current_command}` is one of
   the known shells (fish/bash/zsh/…). **This is what keeps the splash from ever
   covering tmux-state–restored programs/editors.** A restored *bare-shell*
   single-pane session is indistinguishable from a fresh one and may splash once
   — accepted as harmless.

On pass the gate runs:
`display-popup -E -B -w 100% -h 100% tmux-splash`
(`-B` = no border for a clean fullscreen welcome; `-E` closes the popup when the
program exits.) The underlying pane already has a clean prompt, so dismissing
drops the user straight to it. No keystroke is injected into or consumed by the
shell. **Verified:** a `client-attached` hook firing `display-popup` via
`run-shell -b` opens the popup on the attached client (probed on tmux 3.6a).

### Module options

- `programs.lazytmux.splash.enable` — **default `true`** (`mkEnableOption` with
  default override, matching `enrich`/`persist`). When false, neither hook is
  emitted; the gate/binary are simply never invoked.
- `programs.lazytmux.splash.timeout` — auto-dismiss seconds, **default `10`**
  (clamped to a sane range). Injected into the binary at build time.

The enable boolean flows from the module into the config builder the same way
`enrichEnable` does today. `prefix` is already available to the builder.

**Startup-session service & auto-restore:** the login `startupSession` session is
a genuine fresh session, so the welcome buffer is *wanted* there — no opt-out.
Auto-restore (`tmux-state restore --auto` on server start) is covered by gate
condition 3: only a restored bare-shell single-pane session could splash, once.

## Data flow

```
client attaches  ──client-attached──┐
client switches  ─client-session-changed─┤  run-shell -b
                                          ▼
                                  tmux-splash-maybe
        reads @splash_shown, session_windows, window_panes, pane_current_command
                                          │ all gates pass
                                          │ set @splash_shown 1   (set FIRST)
                                          ▼
                 display-popup -E -B -w 100% -h 100% tmux-splash
                                          │
                                          ▼
                              tmux-splash (bubbletea)
   embed cat[-small].txt → pick size for viewport → detect theme
   → ripple mascot + static cheatsheet (prefix baked in)
                                          │ any key OR timeout(cfg.splash.timeout)
                                          ▼
                        exit → popup closes → clean shell prompt
```

## Error handling / edge cases

- **Missing theme file:** default to Mocha (dark) — the scripts' fallback.
- **Viewport too small for full art:** fall back to `cat-small.txt`; if still too
  small, drop the mascot and show wordmark + cheatsheet only. Never clip the cat,
  never crash.
- **256-color terminal:** gradient quantizes via `colorprofile`; bands but works.
- **Unattended client:** `cfg.splash.timeout` guarantees the modal popup
  self-closes; CPU spin is bounded by the timeout.
- **Restored bare-shell session:** may splash once (gate condition 3 only blocks
  restored *programs*). Accepted.
- **Two simultaneous attaches:** rare double popup; flag is set before launch to
  shrink the window. Accepted, not locked.
- **Multiple attached clients (shared session):** `display-popup` from the hook
  targets the triggering client; other clients are unaffected. Edge, accepted.
- **Re-source / `prefix + r` / reattach:** no `client-attached` event and
  `@splash_shown` already set → never replays.

## Testing

- **Go unit tests** (`picker/splash/*_test.go`, run under `nix flake check` like
  the picker tests):
  - gradient phase math is deterministic per `t` and wraps correctly;
  - theme selection from a fixture theme-state file (and missing-file → Mocha);
  - art-size selection: full / compact / none for given viewport dimensions;
  - block centering for a viewport;
  - a `KeyMsg` and a timeout `tea.Msg` each produce `tea.Quit`.
- **Gate logic:** the `tmux-splash-maybe` decision (the `@splash_shown` +
  window/pane + shell-command checks) covered by a `bats` test in `tests/`,
  mocking `tmux` query output — mirroring `tests/enrich.bats`.
- **Manual:** `nix build .#default`; (a) start a fresh session → ripple + tips
  play once, dismiss on key and on timeout; (b) create a new session via
  `prefix + s` → splash shows (validates `client-session-changed`); (c) split a
  window / open a second window → no replay; (d) detach + reattach → no replay;
  (e) shrink the terminal → compact art / tips-only path.

## Out of scope

- Deriving the cheatsheet from the live tmux config (curated static list only).
- Configurable mascot / alternate art (single baked cat, two sizes).
- Configurable animation style (gradient ripple only; dissolve / typewriter /
  static-flicker not built).
- Showing the welcome buffer anywhere but a fresh empty session (no on-demand
  keybind popup, no picker header, no separate startup-service variant).
