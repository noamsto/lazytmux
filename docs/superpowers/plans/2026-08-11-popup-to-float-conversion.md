# Move the eligible tool popups to floating panes

**Issue:** [#353](https://github.com/noamsto/lazytmux/issues/353)

**Stacked on:** [#352](https://github.com/noamsto/lazytmux/pull/352) (`fix(popup): pin
display-popup to its invoking client`, unmerged at the time this branch was cut) — this
PR must merge after #352.

## Why

A `display-popup` is a client-side overlay drawn into the client's tty. A floating pane
(`new-pane -x -y -X -Y`, tmux 3.7+) is a real pane: it has a pane id, appears in
`list-panes`, and emits `%output` in control mode. Three consequences:

1. Popups cannot cross the remote bridge — control mode has no `%popup` event. A
   floating pane is mirrorable in principle (mirroring the float itself is future work,
   not part of this change).
2. Floating panes get full escape-sequence passthrough, which is why yazi already runs
   on `new-pane` (`config/tmux.conf.nix:800`, pre-existing).
3. Popups are what produced the #346 server SEGV (PR #352). Fewer popup call sites is
   strictly less exposure.

## Converted (window-scoped, matching yazi/prdash's existing pattern)

- `prefix + g` lazygit — `-c '#{pane_current_path}'`
- `prefix + b` btop
- `prefix + G` gh-dash — `-c '#{pane_current_path}'`
- `prefix + k` k9s — PATH-only fallback message preserved verbatim
- `prefix + i` enrich card — reads the current window's `@issue_*`/`@pr_*`, so window
  scope is correct

Each conversion follows the yazi/prdash reference exactly: `-B heavy` border, explicit
`-X`/`-Y` position (floats cascade from the top-left if omitted — see the prdash
comment at `config/tmux.conf.nix:567`), and `set -p @pane_label <name>` immediately
after via `\;` chaining, so the theme script's existing float-aware border rendering
(`scripts/tmux-apply-theme-colors.sh:54`) picks it up unchanged.

`g`/`b`/`G`/`k` use `-x 90% -y 90% -X 5% -Y 5%` (centered). `i`'s enrich card keeps its
original fixed cell size (`-x 64 -y 18`, per the card's own graceful-degradation design
at `docs/superpowers/specs/2026-06-30-enrich-card-popup-design.md`) with an approximate
centering offset (`-X 20% -Y 15%`) rather than the percentage sizing used by the other
four, since the card was deliberately sized in cells, not window percentage.

## Left as popups (not converted)

- `prefix + s` / `w` / `W` (session/window/wall pickers) — `switch-client` away; a float
  would be stranded in the window just left.
- `lztmux-notify` toasts — already settled at `scripts/lztmux-notify.sh:16`.
- The splash (`prefix + C-Space`) — inherently per-attach, i.e. client-scoped.
- `prefix + n` notify-center — judged and left as a popup. Its notification history is
  session/host-wide, not tied to the current window's data (unlike the enrich card), and
  it's adjacent to the toast decision above: a float would die if the user switches
  windows mid-browse, same failure mode the toast decision explicitly avoided.

## #346 regression check

Per the design doc (`docs/superpowers/specs/2026-08-10-popup-control-mode-guard-design.md`,
"Non-goals"): the direct `display-popup` binds `prefix + C-Space|i|n|g|b|G|k` were
explicitly **not** part of #352's guard — they execute on the pressing client's own
command queue (`cmd_find_current_client`), never `cmd_find_best_client`, so no control
client can ever be their target. None of `g`/`b`/`G`/`k`/`i` carried `-c` pinning before
this change, so converting them to `new-pane` orphans no guard code. Verified no bats
case in `tests/picker-launcher.bats` / `tests/splash.bats` references these five binds.

## Verification performed

- `nix build .#default`, then ran the built tmux against a throwaway socket:
  `new-pane` for `g`/`b`/`i` opens a real pane with the expected `@pane_label`, does not
  change `window_panes`/window count in a way that would confuse reflow, and the pane
  vanishes on command exit under this repo's default `remain-on-exit off` (no popup `-E`
  equivalent needed — floats already behave this way, matching the pre-existing
  yazi/prdash behavior).
- `tests/tmux-next38-readiness.bats`'s `"popup bindings and picker tmux data path are
  present"` test still passes: it only asserts substring presence of `display-popup`
  (still true — `C-Space`/`n` remain) and `tmux-enrich-card` (still true — the binary
  path is unchanged, only its launch mechanism changed).
