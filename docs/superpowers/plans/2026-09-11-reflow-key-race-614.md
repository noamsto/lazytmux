# #614 reflow race — plan

Spec: `docs/superpowers/specs/2026-09-11-reflow-key-race-614-spec.md`.

The code change touches two files: `scripts/tmux-reflow-windows.sh` and
`tests/reflow-fanout.bats`. The on-demand reproduction is a new manual script,
`tests/reflow-race-e2e.sh`. The bats file already runs in CI as
`checks.<system>.reflow-fanout-tests`: `flake.nix` copies `./scripts` and
`./tests` and runs `bats tests/reflow-fanout.bats`, so no new derivation is
needed.

This is plan revision 2 of 2.

- **Round 1 (plan-critic).** Reordered the finish steps. Replaced
  skip-on-exhaustion after the shell reviewer's HIGH finding.
- **Round 2 (plan-critic).** Replaced the round-1 design, for two reasons:
  - The long foreground wait froze synchronous callers.
  - The in-lock cache re-check could skip a needed render.

  Its final pass also asked for two changes: the D1 test polls rather than
  asserting straight after `wait`, and the docs match the code.

## Steps

- [x] **1. Reproduce.** Isolated server, a real attached client, and the real
  `window-unlinked` hooks. `@reflow_key` stuck at `3:80:24` with 1 window left,
  for the full 10s.
- [x] **2. Measure the lock budget.** Fired N concurrent invocations at an
  instrumented copy. With N=10, 0/10 exhausted. With N=25, 7/26 exhausted and
  ran unlocked.
- [x] **3. Stamp the rendered count (D1).** Stamp
  `applied_key="${total}:${WIDTH}:${HEIGHT}"` after the post-lock
  `list-windows` loop. The `recompute` log reports `$total`.
- [x] **4. D2: bounded foreground, detached waiter** (implement: escalated).
  - **Bounded foreground.** Cap the foreground at 2s of wall time (`EPOCHREALTIME`), so
    synchronous callers stay capped at ~2s. A retry count is not a time bound:
    each failed acquire spawns processes, and macOS CI stretched 40 retries
    past 5s. Those callers are `session-window-changed`,
    `after-new-session` and `client-session-changed`
    (`config/tmux.conf.reference.nix:767,773,774`), config load (`:901`), and
    home-manager activation (`modules/home-manager.nix:1121`).
  - **Hand off on exhaustion.** Never write unlocked: run
    `detach "$0" --await-lock --force "$SESSION" "$WIDTH"` and exit 0.
  - **Waiter.** `--await-lock` polls every 0.5s until
    `SECONDS + OG_LOCK_STALE_SECONDS + 5`. If it acquires, it renders. If it
    hits the deadline, it exits 0 without rendering. Reaching the deadline
    means every holder acquired after the waiter started (an older lock dir
    would have aged out and been stolen), so each holder read state after the
    trigger.
  - **`--force`.** The waiter's fast path must not cache-hit on a coincidental
    count.
  - **No in-lock cache re-check.**
- [x] **5. Tests** in `tests/reflow-fanout.bats`. `setup()` gives the
  sed-built `$REFLOW` a `#!$BASH` shebang and `chmod +x`, so the waiter can
  re-exec `$0`.
  - [x] **Foreground budget.** Hold the lock and run `timeout 15 … --force`.
    Assert exit 0 and `sentinel`. Release the lock, then poll up to 5s for
    `1:200:0`.
  - [x] **Dead holder.** Set `OG_LOCK_STALE_SECONDS=10` and never release the
    lock dir. Assert exit 0 and `sentinel`, then poll up to 25s for
    `1:200:0`. Staleness is whole-second and slow-runner startup adds to the
    budget; at 5, macOS CI's foreground stole the lock itself.
  - [x] **D1.** Hold the lock and start `--force`. After `sleep 1`, kill the
    other 2 windows by window id, release, and `wait`. Poll up to 5s for
    `1:200:0` instead of asserting straight away: on a slow runner the
    foreground can overrun its budget and hand off to the waiter.
  - [x] **Red checks.** Plain nixpkgs tmux first on PATH:
    - Against `git archive HEAD`: all three fail, the other 9 pass.
    - Against the round-0 skip: the foreground-budget and dead-holder tests
      fail, the D1 test passes.
    - Final: 12/12.
- [x] **6. On-demand reproduction.** `tests/reflow-race-e2e.sh` takes
  `--mode burst|freeze`, `--windows N`, `--trials T`, `--jump-to M` and the
  tmux binary. It exits non-zero if any trial diverges from a from-scratch
  forced reflow.
- [x] **7. End-to-end on the final build.** Burst and freeze mode at N=10 and
  N=25, 5 trials each, plus like-for-like freeze controls with
  `--jump-to 2`, `8` and `13`. Every run must exit 0. Re-run if step 11 or 12
  changes `scripts/tmux-reflow-windows.sh`. Done: all 7 configurations exited
  0, and 29 of 29 trials matched a from-scratch reflow.
- [x] **8. Spec.** Match the code: the waiter exits 0 at its deadline, AC2 uses
  `OG_LOCK_STALE_SECONDS=10`, and the D1 test polls. Record the final e2e table.
- [x] **9. Final critic round.** Spec-critic and plan-critic reviewed the docs
  and the realized diff. Any finding that is still unresolved goes under
  `## Escalated` in the PR body.
- [x] **10. Commit.** From inside `nix develop`/direnv, so the pre-commit hooks
  run. Never use `PRE_COMMIT_ALLOW_NO_CONFIG=1`. Stage exactly:
  - `scripts/tmux-reflow-windows.sh`
  - `tests/reflow-fanout.bats`
  - `tests/reflow-race-e2e.sh` (executable)
  - the spec
  - this plan

  Never stage `WORKER_TASK.md`.
- [x] **11. Rebase onto `origin/main`**, then run the gate on the rebased tree:
  `nix build .#default`, `nix build .#lint`, `nix flake check`.
- [x] **12. `/deslop`.** If it changes anything, re-run the gate.
- [x] **13. Push and open the PR** with `gh pr create --assignee @me`. The body
  contains:
  - `Closes #614`
  - the cause (D1, D2) and the fix
  - the bats red/green table and the e2e tables, re-run on the rebased tree
  - how to run `tests/reflow-race-e2e.sh`
  - the halo confirmation check (`@reflow_key` vs `#{session_windows}`)
  - a "Not addressed" note on `controlmode/parse.go` `readBlock`
  - `## Review notes`, including that the waiter keeps its trigger's `WIDTH`

- [x] **14. macOS CI fix (PR #620).** `aarch64-darwin` failed the
  foreground-budget test (`timeout 5` exited 124) and the dead-holder test (the
  foreground stole the lock). Cause: the foreground budget counted 40 retries,
  and every failed acquire spawns processes, so macOS fork cost stretched it
  past 5s. This was reproduced on Linux by shimming `mkdir`, `date`, `stat` and
  `sleep` with 30ms of latency: the pushed tree failed tests 5 and 6 the same
  way. Fix: a wall-clock 2s deadline, `timeout 15` in test 5, and a 10s stale
  window in test 6. The fixed tree passes 12/12 under 30ms latency, tests 5–6
  pass under 80ms, and plain Linux passes 12/12.

## Validation commands

```sh
nix build .#default
nix build .#lint
nix build .#checks.x86_64-linux.reflow-fanout-tests -L
nix flake check -L
tests/reflow-race-e2e.sh --mode freeze --windows 25 --trials 5 ./result/bin/tmux
```
