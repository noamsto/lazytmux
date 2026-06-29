# kitty-pane mode polish: launch-hidden follow-focus + seamless ctrl+hjkl

**Issue:** [noamsto/aeye#103](https://github.com/noamsto/aeye/issues/103)
**Status:** Design — pending review
**Repos:** aeye · lazytmux (nix-config: flake-input bumps only)

## Goal

Close two rough edges in kitty-pane mode (`AEYE_HOST=kitty`), where the aeye
carousel opens as a kitty vsplit beside the tmux host instead of a tmux split:

1. **Auto-open ignores tmux focus** — a carousel auto-opened for an off-screen
   pane appears over whatever window the user is currently viewing.
2. **`ctrl+hjkl` can't cross the tmux/kitty boundary** — navigation into and out
   of the kitty carousel was never wired.

Both were known gaps: cross-surface navigation was explicitly punted as
out-of-scope in aeye #90 and the carousel-follows-focus design.

## Background

In kitty-pane mode there is one kitty OS window hosting the tmux client (window
WIN-host) plus a kitty carousel window (tagged `claude_img_src=<pane>`) placed as
a vsplit beside it. The carousel "follows tmux focus" via a reconcile step
(`tmux-claude-images --reconcile`) fired from tmux focus hooks
(`client-session-changed` / `session-window-changed` / `client-attached`): it
stashes off-screen carousels into a hidden tab and unstashes the on-screen one.

Verified building blocks: kitty 0.47.4 (`kitty @ action`), tmux 3.6a
(`#{pane_at_right}` / `#{pane_at_left}` / `#{pane_at_top}` / `#{pane_at_bottom}`).
`kitty @` is reachable from a tmux `run-shell` / key-binding context because
`update-environment KITTY_LISTEN_ON` puts the socket in the session environment.

## Part 1 — launch-hidden follow-focus (aeye)

### Problem

`scripts/tmux-claude-images.sh` `launch_kitty()` always creates the carousel
*visible* (`kitty @ launch … --location=vsplit --next-to … --keep-focus`) with
no check of whether the owning pane (`$KEY`) is on the currently-visible tmux
window. The capture hooks fire `tmux-claude-images --ensure-open`
(`adapters/.../diagrams.sh:112`, image capture) from the *capturing* pane,
regardless of where the user is looking. Reconcile only stashes off-screen
carousels on tmux *focus-change* hooks — never as a consequence of the launch.
So a diagram captured in pane A while the user views window B pops the carousel
over B and leaves it there until the next focus change.

### Fix

On an `--ensure-open` launch, when `$KEY` is **not** on a visible window, create
the carousel **stashed** instead of as a visible vsplit; the existing reconcile
reveals it when the user focuses the owning window.

Insert the new branch **immediately after** the existing already-open
short-circuit (`tmux-claude-images.sh:126-130`) and **before** the visible
`kitty_place_args` launch:

```sh
# launch_kitty(), AFTER the line 126-130 `var:claude_img_src=$KEY` toggle/
# already-open guard (so it only runs when no carousel window for $KEY exists
# yet — visible or stashed), and BEFORE the visible-vsplit launch:
if [[ -n $ENSURE_OPEN ]] && ! _key_on_screen; then
    _ensure_stash_tab
    kitty @ launch --type=window --match "var:aeye_stash=1" --keep-focus \
        --var claude_img_src="$KEY" \
        --env AEYE_DIR="$STATE_DIR" --env CLAUDE_STATUS_DIR="$STATE_DIR" \
        "$VIEWER_BIN" "$KEY" >/dev/null
    return
fi
# else: existing visible-vsplit launch (kitty_place_args)
```

Two interactions this relies on:
- **Already-open is still a no-op for a stashed carousel.** The line 126 guard
  (`kitty @ ls --match "var:claude_img_src=$KEY"`) matches windows in *any* tab,
  including the stash tab, so a second `--ensure-open` for an off-screen pane
  whose carousel is already stashed returns at line 127 before reaching the new
  branch. The branch only ever fires on the *first* open for that pane.
- **The stash launch deliberately bypasses `kitty_place_args`.** It does not run
  the host-window `goto-layout splits` side effect (lines 102/105); placement is
  deferred to `_carousel_unstash`, which re-runs `goto-layout … splits` on the
  host tab (line 331) before detaching the window back — so the carousel still
  lands as a vsplit beside the host when revealed.

The manual toggle path (`prefix+I`) is unaffected: it passes no `--ensure-open`
(`config/tmux.conf.nix:367`), and there `$KEY` is the user's current pane,
always on-screen → still opens visible.

### Env-independent on-screen check

The launch runs in the *capturing* pane's tmux context, so reconcile's
context-dependent `tmux list-panes` would always count its own pane as current.
Use an `-a` query filtered to the active window of an attached session:

```sh
_key_on_screen() {
    tmux list-panes -a -F '#{pane_id} #{window_active} #{session_attached}' 2>/dev/null \
        | awk -v k="$KEY" '$1==k && $2==1 && $3>=1 {f=1} END{exit !f}'
}
```

Reconcile's own query is left as-is (it runs from focus hooks where the context
is already the visible window, and it ships working today).

This `-a` query is more exposed to multiple *attached* sessions than reconcile's
client-local query: it treats `$KEY` as on-screen if `$KEY`'s window is active in
*any* attached session, so with two attached clients viewing different sessions a
carousel could still launch visible over the wrong one. That is acceptable under
the single-attached-client assumption the carousel already makes (see Out of
scope), but is a strictly wider blast radius than the reconcile path it sits
beside — called out here so it's a known limit, not a surprise. If it bites,
scope the query to the session/window of the client attached to `$KEY`'s session
rather than all attached sessions.

### Opening / revealing must never steal focus

Two paths must keep the user's focus on their tmux pane:

- **Launch** — every `kitty @ launch` already passes `--keep-focus` (the visible
  vsplit via `kitty_place_args`, the new stash-launch, and `_ensure_stash_tab`),
  so creating the window never moves focus. This is now an invariant: assert
  `--keep-focus` is present in the launch argv.
- **Reveal (reconcile unstash)** — **this currently steals focus** (verified):
  `kitty @ detach-window --target-tab` makes the moved carousel the *active*
  window in the host tab, and `_reconcile_apply`'s `focus-tab --match id:host_tab`
  only focuses the tab, not the prior window — so after a reveal, focus lands on
  the carousel, not the tmux pane. Fix: after any reconcile move, focus the tmux
  **host window** explicitly — the window in the host tab whose `user_vars` has
  neither `claude_img_src` nor `aeye_stash` — via `kitty @ focus-window --match
  id:<host_window>`, falling back to `focus-tab` only if no such window is found.
  The host window id can be read from the same `kitty @ ls` snapshot
  `_reconcile_apply` already captured (the tmux host window doesn't move).

## Part 2 — seamless ctrl+hjkl (lazytmux + aeye)

The boundary is asymmetric, and each side is owned by the layer that controls it:
tmux→carousel happens inside a tmux pane (tmux owns it → lazytmux); carousel→tmux
happens inside the carousel window, which runs **our** viewer (aeye owns it). No
kitty config is needed on either side — because the only non-tmux kitty window in
kitty-pane mode is the aeye viewer, and the viewer can handle its own keys.

### tmux side (lazytmux)

A small packaged helper keeps the binding readable and pulls the branching out of
tmux's quoting:

```sh
# tmux-smart-nav  <select-flag> <kitty-dir> <zoomed> <at_edge>
flag=$1 dir=$2 zoomed=$3 edge=$4
[ "$zoomed" = 1 ] && exit 0
if [ "$edge" = 1 ] && [ -n "$KITTY_LISTEN_ON" ] && command -v kitty >/dev/null 2>&1; then
    kitty @ action neighboring_window "$dir" 2>/dev/null && exit 0
fi
tmux select-pane -"$flag"
```

The four bindings in `config/tmux.conf.nix` (currently `select-pane -{L,D,U,R}`)
call it, passing the edge format var per direction:

```
bind -n C-l if-shell "$is_vim" "send-keys C-l" \
    "run-shell 'tmux-smart-nav R right #{window_zoomed_flag} #{pane_at_right}'"
# C-h → L left pane_at_left ; C-j → D down pane_at_bottom ; C-k → U up pane_at_top
```

Self-gates on `KITTY_LISTEN_ON`, so non-kitty tmux users and tmux-split-mode
users keep exactly today's behavior; intra-tmux moves (non-edge) stay plain
`select-pane`.

**Known limit — vim/fzf panes at the edge don't hand off.** The `if-shell
"$is_vim"` branch sends the key straight to vim/fzf and never reaches
`tmux-smart-nav` (`config/tmux.conf.nix:559-563`; `is_vim` also matches `fzf`).
So a vim pane sitting at the tmux edge cannot cross into the kitty carousel — that
would require vim's own vim-tmux-navigator to learn the kitty handoff. In
practice the boundary pane is Claude Code (not vim), so this doesn't bite the
carousel workflow; it's documented rather than solved.

### carousel-viewer side (aeye)

The viewer is a bubbletea TUI already switching on `"l"/"h"/"j"/"k"` and
`"ctrl+c"` (`gallery.go:232-262`). Add `ctrl+hjkl` cases that shell out to kitty's
remote control to move focus to the neighboring kitty window:

```go
case "ctrl+h": kittyNeighbor("left")
case "ctrl+j": kittyNeighbor("down")
case "ctrl+k": kittyNeighbor("up")
case "ctrl+l": kittyNeighbor("right")
```
```go
// kittyNeighbor moves kitty focus to the neighbour in dir. No-op off-kitty.
func kittyNeighbor(dir string) {
    if os.Getenv("KITTY_LISTEN_ON") == "" { return }
    _ = exec.Command("kitty", "@", "action", "neighboring_window", dir).Run()
}
```

`KITTY_LISTEN_ON` is present in the viewer's env (kitty sets it for windows it
launches with remote control on), so the guard makes this inert anywhere else
(e.g. the tmux-split mode viewer). The viewer uses bare `h/l/j/k` for gallery
nav, so `ctrl+hjkl` are free and steal nothing. Why kitty needs **no** ctrl+hjkl
map: in the tmux window kitty has no such map so the keys pass through to tmux
(Part-2 tmux side); in the carousel window they reach the viewer, which handles
them. The kitty-config layer disappears entirely.

### End-to-end flow

- `C-l` mid-tmux → tmux `select-pane -R`.
- `C-l` at tmux right edge → tmux `tmux-smart-nav` → `kitty @ action
  neighboring_window right` → carousel focused.
- `ctrl+h` in carousel → viewer `kittyNeighbor("left")` → `kitty @ action
  neighboring_window left` → tmux focused.

## Ownership and DX

Each concern is owned by the layer that controls it, with **no kitty-config
layer** — that's the simplification over the kitten approach:

| Concern | Owner |
|---|---|
| carousel-as-kitty-window: launch, placement, follow-focus, visibility, drag-out, var tag, **and carousel→tmux nav (viewer ctrl+hjkl → `kitty @ action`)** | **aeye** |
| tmux→carousel nav (C-hjkl edge handoff), `KITTY_LISTEN_ON` threading, reconcile hooks | **lazytmux** |
| kitty config | **nothing** — the viewer handles its own keys; kitty needs no ctrl+hjkl map |

This is the best DX precisely because there is **nothing to configure on the
kitty side**: enable kitty-pane mode and nav works. `tmux-smart-nav` self-gates
on `KITTY_LISTEN_ON` at runtime and goes straight into `config/tmux.conf.nix`, so
it needs no opt-in Home Manager option either — non-kitty/tmux-split users are
unaffected because the gate is false for them. nix-config changes reduce to
bumping the aeye + lazytmux flake inputs.

Why no HM option / no `programs.kitty` reach-in: the carousel→tmux direction
moved into the viewer (aeye), so there is no kitten to ship and no kitty
keybindings to inject — which also dissolves the cross-module keybinding-merge and
prerequisite-assertion concerns the kitten approach carried.

Non-Nix users get this for free too (the viewer ships the behavior); aeye's
README only needs the `tmux.conf` `tmux-smart-nav` snippet for non-lazytmux tmux
configs, extending the existing "Enable kitty-pane mode" section.

## Testing

- `bats` for `tmux-smart-nav`: zoomed → no-op; non-edge → `select-pane`; edge +
  no `KITTY_LISTEN_ON` → `select-pane`; edge + kitty → `kitty @ action` (stub
  `kitty`/`tmux`, assert argv).
- `bats` for the aeye on-screen-launch branch: the `tmux` stub must emit
  correctly-shaped `list-panes -a` rows — `<pane-id> <window_active>
  <session_attached>`, with the pane id keeping its `%` prefix (`$KEY` retains it,
  `tmux-claude-images.sh:42`) — so the test exercises the awk filter, not a
  trivially-true stub. Off-screen row for `$KEY` (`window_active=0`) → assert the
  launch targets `var:aeye_stash=1`; on-screen row (`%KEY 1 1`) → assert the
  visible-vsplit (`kitty_place_args`) launch.
- Go test for the viewer's `ctrl+hjkl` handling: assert each key maps to
  `kittyNeighbor(<dir>)` and that `kittyNeighbor` no-ops when `KITTY_LISTEN_ON` is
  unset (inject the command runner / assert the `kitty @ action …` argv via a stub
  on PATH, rather than spawning real kitty).
- Manual checklist for live focus behavior (focus movement can't be
  unit-tested): capture a diagram while on another window → carousel stays hidden;
  focus the owning window → it appears; `C-l` from the Claude pane → carousel;
  `ctrl+h` in carousel → back.

## Out of scope

- Replacing kitty-pane mode with a tmux split by default (considered; rejected —
  keep the mode, fix it).
- Reworking reconcile's on-screen *query* (works from hooks; unchanged) — note
  the reveal *focus* restoration IS changed (see "Opening / revealing must never
  steal focus").
- Multi-client tmux (two clients viewing different windows) — inherits the
  single-attached-client assumption the carousel already makes.
- General smart-splits for an arbitrary *third* kitty window — the viewer-handled
  approach crosses only the tmux↔carousel boundary (the only windows in kitty-pane
  mode). A `pass_keys.py` kitten could generalize it later if ever wanted (YAGNI).

## References

- aeye #90 / carousel-follows-focus design — cross-surface nav noted out-of-scope.
- kitty remote control: `kitty @ action neighboring_window` (used by both the tmux
  helper and the viewer).
