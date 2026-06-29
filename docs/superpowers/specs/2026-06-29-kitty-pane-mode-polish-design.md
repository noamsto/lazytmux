# kitty-pane mode polish: launch-hidden follow-focus + seamless ctrl+hjkl

**Issue:** [noamsto/aeye#103](https://github.com/noamsto/aeye/issues/103)
**Status:** Design — pending review
**Repos:** aeye · lazytmux · nix-config

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

## Part 2 — seamless ctrl+hjkl (lazytmux + nix-config)

Full smart-splits. The boundary is asymmetric: tmux→kitty happens inside a tmux
pane (tmux owns it); kitty→tmux happens in a kitty window not running tmux (kitty
owns it). Both sides are required.

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

### kitty side (nix-config, generated by the lazytmux HM option)

A `pass_keys.py` kitten bound to `ctrl+hjkl`:

```
map ctrl+h kitten pass_keys.py left  ctrl+h tmux   # + l/j/k
```

The kitten checks the focused window's foreground process:
- process **is** tmux → pass the key through to the window → tmux's Part-2
  binding decides (intra-tmux move, or edge→kitty). Intra-tmux nav never changes.
- otherwise (carousel viewer) → `neighboring_window <dir>` → back into tmux.

This is the canonical kitty-FAQ pass-keys recipe; lift it verbatim rather than
reconstruct the kitten API.

### End-to-end flow

- `C-l` mid-tmux → kitten passes → tmux `select-pane -R`.
- `C-l` at tmux right edge → kitten passes → tmux `kitty @ action
  neighboring_window right` → carousel focused.
- `C-h` in carousel → kitten `neighboring_window left` → tmux focused.

## Ownership and DX

"kitty integration" is three concerns at three layers, each owned by its existing
layer in the `nix-config → lazytmux → aeye` dependency chain:

| Concern | Owner |
|---|---|
| carousel-as-kitty-window (launch, placement, follow-focus, visibility, drag-out, var tag) | **aeye** |
| tmux's side of any tmux↔kitty boundary (C-hjkl, edge handoff, `KITTY_LISTEN_ON` threading, reconcile hooks) | **lazytmux** |
| kitty's own keymaps/kittens | **kitty config** |

For the **NixOS best-DX path**, the kitty-config layer is *generated* by a single
opt-in Home Manager option in lazytmux's module (`modules/home-manager.nix`,
`options.programs.lazytmux`), since lazytmux is already the NixOS integration
layer (it consumes aeye via `carousel-toggle` / `carousel-aeye` and wires the
carousel's tmux hooks). Proposed:

```nix
programs.lazytmux.carousel.kittyNav.enable = true;   # nix-config: one line
```

When enabled the module:
- wires the tmux side (`tmux-smart-nav` on PATH + the four `C-hjkl` bindings);
- writes `pass_keys.py` via `xdg.configFile."kitty/pass_keys.py"`;
- injects the `ctrl+hjkl` maps via `programs.kitty.keybindings`.

Both nav halves are co-generated by one module, so the kitten and its maps stay
together (the "kitten lives with its maps" invariant) — materialized
declaratively.

**Keybinding ownership (not a free merge).** `programs.kitty.keybindings` is a
plain `attrsOf str`; two modules assigning the same key is a Home Manager
*evaluation conflict*, not a silent compose — it only "composes" while the key
sets are disjoint (they are today: `home/terminal/kitty/default.nix:135-165` has
no `ctrl+hjkl`). The module therefore sets the four `ctrl+hjkl` maps with
`lib.mkDefault` and documents that it owns those keys: a user who deliberately
rebinds `ctrl+h` wins cleanly (and silently loses carousel nav on that key, by
choice) instead of hitting a raw conflict.

**Prerequisite check is a soft, guarded warning — not a hard cross-module
assertion.** The option needs kitty-pane prerequisites
(`programs.kitty.settings.allow_remote_control = true`,
`programs.kitty.environment.AEYE_HOST = "kitty"`) which live in a *different*
module. To avoid coupling lazytmux to the kitty module's presence: gate first on
`config.programs.kitty.enable` (do nothing — and emit no kitty config — when
kitty isn't managed by Home Manager), and when it is enabled, emit a Home Manager
`warnings` entry (not an `assertions` entry) if `allow_remote_control` or
`AEYE_HOST` aren't set. This keeps a non-kitty lazytmux consumer who enables the
option from hitting an eval error against unset foreign options, while still
nudging a kitty user who's missing a prereq. The exact `config.programs.kitty.*`
paths read are the two named above.

Non-Nix users: **aeye's README** documents the manual `pass_keys.py` + map +
`tmux.conf` snippets, extending the existing "Enable kitty-pane mode" section.

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
- Manual checklist for live focus behavior and the kitten pass-through (focus
  movement can't be unit-tested): capture a diagram while on another window →
  carousel stays hidden; focus the owning window → it appears; `C-l` from the
  Claude pane → carousel; `C-h` in carousel → back.

## Out of scope

- Replacing kitty-pane mode with a tmux split by default (considered; rejected —
  keep the mode, fix it).
- Reworking reconcile's existing on-screen query (works from hooks; unchanged).
- Multi-client tmux (two clients viewing different windows) — inherits the
  single-attached-client assumption the carousel already makes.

## References

- aeye #90 / carousel-follows-focus design — cross-surface nav noted out-of-scope.
- kitty FAQ: "mapping key presses depending on the program running" (pass_keys).
