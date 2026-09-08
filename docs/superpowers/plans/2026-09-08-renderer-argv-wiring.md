# Plan: wire renderer panes by argv, keep the crash net (#569)

Plan: required (worker-authored). Scope is decided in the task doc: argv wiring in; `remain-on-exit` / `healDeadRenderers` stay.

## Mechanism

`spawnRenderer` today wires `LZTMUX_RENDER_SOCK` / `LZTMUX_RENDER_PANE` via `-e`. Pane environment does not survive a bare `respawn-pane`; pane argv does. Pass sock path and **remote** pane id as renderer arguments so a user Respawn re-runs the same command and reconnects. A genuine crash still exits; the #547 net covers that.

Switch cleanly: drop `-e`. House style is delete-don't-preserve. Readers of those env vars are only `cmd/renderer/main.go` plus tests that assert the spawn line.

## Files

- `picker/remotebridge/cmd/renderer/main.go` — require `os.Args[1]` sock, `os.Args[2]` remote pane id; usage on stderr then exit 1.
- `picker/remotebridge/daemon/daemon.go` — `rendererSpawnArgs` becomes the trailing command argv (`bin`, sock, remote pane), not `-e`. Call sites already append `cfg.RendererBin`; fold so spawn is `respawn-pane -k -t target -- bin sock pane` / `split-window axis -t last -- bin sock pane` as needed so a sock path cannot be parsed as a tmux flag. Keep `stampMirrorWindow`'s `remain-on-exit on`. Update the comment that currently blames `-e` as the reason for the net: argv covers respawn; the net covers crashes.
- `picker/remotebridge/daemon/reconcile.go` — consume the new spawn helper (split path).
- `picker/remotebridge/daemon/reconcilesplit_test.go` — expect sock + remote pane as argv after the binary; no `LZTMUX_RENDER_*` env.
- `picker/remotebridge/daemon/daemon_test.go` (or a small spawn-args test) — assert `rendererSpawnArgs` / `spawnRenderer` argv shape.
- `tests/remote-m2-integration.bats` — retarget the #547 respawn test so recovery is **the same window** (argv reconnect, not `healDeadRenderers` rebuild). Add a sibling test that **kills the renderer process** (not respawn): session stands, `remain-on-exit` holds a corpse, then heal restores a live mirror.
- `CLAUDE.md` — renderer-exit bullet: argv wiring + why the net stays.
- `docs/superpowers/plans/2026-09-08-renderer-argv-wiring.md` — this plan, committed with the PR.

Do not touch: `healDeadRenderers`, `deadRendererStrikes`, `#{pane_dead}` + `@bridge_pane` keying, `deadrendererheal_test.go` behavior (except comments if they claim respawn is what produces the corpse).

## Assumptions

- `LocalTmux` execs without a shell; sock and pane are separate argv words.
- `cfg.SockPath` is daemon-pid-derived (`/tmp`/`/run` style) and does not start with `-`. Remote pane ids are `%N`. Still pass `--` before the binary so a future path cannot become a flag.
- Nothing keys on the renderer command-line shape: `healDeadRenderers` keys on `pane_dead` + `@bridge_pane`; `@bridge_proc` overrides `pane_current_command` for icons; agentdetect has no `renderer` match.

## Validation

- Re-verify argv survival on the pinned tmux (`respawn-pane -k` with no command reprints a marker).
- `go test` in `picker/remotebridge/daemon` and `picker/remotebridge/cmd/renderer` (if tests added).
- Existing `deadrendererheal_test.go` still green.
- Bats: respawn reconnects same window; kill-renderer still heals.
- Fast gate: scoped Go tests + `nix build .#default` / `nix flake check` / `nix build .#lint` as the repo gate allows; at minimum the changed packages' tests.

## Acceptance mapping

- [ ] `respawn-pane` (no command) brings the renderer back connected — bats demonstrates it on the same window.
- [ ] `remain-on-exit on` + `healDeadRenderers` still work for a non-respawn death — bats + existing Go tests.
- [ ] CLAUDE.md describes argv + why the net stays.
- [ ] Gate green; output in the PR.
