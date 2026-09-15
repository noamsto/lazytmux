# Scripted README visuals (#630)

The README demo clip went stale and was removed in #628. Replace hand-recorded
visuals with `vhs` tapes that one command re-renders.

## Shape

- `docs/media/tapes/*.tape` — one tape per clip. Tapes hold only keystrokes
  and timing; every bit of state they show is seeded by the runner.
- `docs/media/demo.sh` — the runner, packaged as `apps.<system>.demo` with
  `writeShellApplication` (so shellcheck runs at build). `nix run .#demo` from
  the repo root regenerates every `docs/media/*.gif`.
- README: the hero GIF under the badges, a short Screenshots section after
  Features for the rest.

## Runner

1. Private scratch dir (short path — socket paths cap near 108 bytes) holding
   `TMUX_TMPDIR`, `CLAUDE_STATUS_DIR`, `HOME` and `XDG_*`. A private
   `TMUX_TMPDIR` alone does not isolate agent state, and a private `HOME` keeps
   the user's shell rc, zoxide db and agent credentials out of the frame.
2. Start the flake's wrapped tmux on its own `-L` socket; a `tmux` shim on
   `PATH` pins every call (including the ones typed inside tapes) to it.
3. Seed: a few git repos with issue-shaped branches, sessions/windows in them,
   `@issue_*`/`@pr_*` window options (no `gh`/`linear` on `PATH`, so the
   pollers cannot overwrite them), fake agent panes (`exec -a claude sleep`
   after a mock transcript) and matching `panes/<pane_id>` state files
   (`processing`, `waiting`, `done`) keyed by pane ids read back from the
   server. `@splash_shown 1` so no splash covers the first frame.
4. `vhs` each tape with a Nerd Font pinned through `FONTCONFIG_FILE`.
5. Trap: kill the demo servers, remove the scratch dir.

Status-bar reflow needs the client to narrow mid-clip, which `vhs` cannot do.
The reflow tape attaches through an outer, config-less tmux and a background
driver shrinks the outer pane step by step.

## Clips

| GIF | Shows |
|-----|-------|
| `hero.gif` | status bar with agent states, window switching, session picker |
| `reflow.gif` | window list reflowing onto more lines as the client narrows |
| `pickers.gif` | session picker, then window picker |
| `wall.gif` | window wall (`prefix + W`) |
| `agents.gif` | agent status across windows |

Not in this change: the which-key popup (#629 not merged), the remote bridge
(needs a second host), and the enrich card — `prefix + i` launches its float
with an unexpanded `#{session_id}:#{window_id}` target, so the card opens
empty. That bind bug is tracked separately; its clip lands with the fix.

## Done when

- `nix run .#demo` regenerates every GIF; each is under ~2 MB.
- README shows them.
- `nix build .#lint`, `nix flake check`, `nix build .#default` pass.
