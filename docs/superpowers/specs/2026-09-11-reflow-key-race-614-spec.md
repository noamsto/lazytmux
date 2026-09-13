# #614: mirror status line stops reconciling after crew reap — spec

Line references (`:N`) point at `scripts/tmux-reflow-windows.sh` as of `HEAD`
(`a178f41`) unless another file is named. The fix shifts them.

## Reported symptom

> after crew reap on remote mirrored session, the windows status line isn't
> reconciling and being garbled

`WORKER_TASK.md` lists three readings. What the investigation found for each:

| Reading | Disposition |
|---|---|
| 1. Status line shows stale content (a window that is gone, a neighbour's label) | Reachable via D1 and D2 below: the layout is frozen for a window set that no longer exists. |
| 2. Corrupt bytes / broken layout | No literal byte corruption was observed or found in the code. The "garble" is a layout computed for a different window count (see *What the garble looks like*). |
| 3. Reflow stops running for the session | In effect, yes. A poisoned or torn `@reflow_key` makes later reflows cache-hit and exit before recomputing. |

## The trigger

`crew reap` kills several windows in one burst. On the laptop, the bridge
daemon's `closeWindow` runs `kill-window` on the local mirror session for each
one, and every kill fires tmux's `window-unlinked` hook. That hook runs
`run-shell -b "tmux-reflow-windows …"` (`config/tmux.conf.reference.nix:766`),
so N kills fork N concurrent invocations of `scripts/tmux-reflow-windows.sh`
against one session. A dispatcher fan-out does the same through
`after-new-window` (`:765`, also `-b`).

The script already serializes its compute-and-write section behind
`acquire_lock` (#150). Two defects remain in how it uses that lock.

## D1 — `@reflow_key` is stamped from a pre-lock snapshot

- `win_count` is read before the lock (`:80`), and `cache_key` is built from it
  (`:86`).
- The layout is computed from `list-windows` after the lock (`:237`), so the
  render reflects the true window set.
- `@reflow_key` is then stamped from the pre-lock `$cache_key` (`:444`), not
  from what was rendered.

An invocation can read `win_count` mid-burst and still win the lock last. It
then renders correctly but stamps a count that doesn't match, so the key no
longer describes what is on screen. This becomes visible as soon as a later
reflow's `session_windows` equals the poisoned number and the fast path (`:87`)
cache-hits:

- **Off by one:** the next ordinary single `new-window` collides. A kill burst
  commonly stamps `2:…` with 1 window left. That was 5 of the 9 frozen trials
  below.
- **Further off:** walking back up one window at a time clears the poison,
  since each intermediate reflow stamps the truth. Jumping onto the poisoned
  value inside a single burst does not.

Both happen routinely on a dispatcher host. A kill burst poisons with a higher
number, an add burst with a lower one, and the session's window count cycles up
and down in bursts all day.

**Confidence:** confirmed end to end. The real hooks poisoned the key in 8/10
burst trials, and a follow-up return to the poisoned count froze the rendered
layout in 9/10 (see *Evidence*).

## D2 — exhausting the lock budget writes without the lock

The retry loop (`:103–106`) tries 40 × 50ms and then, per its own comment,
"proceed[s] unlocked rather than wedge — a later reflow still settles it."
That fails in two ways:

- **A reap-sized burst exhausts the budget.** An instrumented copy logged
  `got_lock`/`retries` after the loop, and N concurrent invocations were fired
  after invalidating the cache. With N=10, 0/10 exhausted it (at most 20
  retries). With N=25, **7 of 26 exhausted it** and ran unlocked. One solo
  invocation takes 0.6–1.0s of wall time here.
- **The writes are not one batch.** `@reflow_key`, `@window_split*`,
  `@window_per` and `status` go out in the `tmux_cmds` batch (`:468–471`).
  The `status-format[0..4]` strings are written afterwards as separate
  `tmux set` calls (`:528–557`). Two unlocked invocations can each win a
  different phase. That leaves `status-format` built for one window count
  while the key and split points describe another, and every later reflow
  cache-hits on it.

"A later reflow still settles it" does not hold after a reap. Once the burst
ends, nothing fires another reflow until the next structural event, and that
event's fast path compares against the torn key.

**Confidence:** the exhaustion was measured, using synthetic simultaneous
firing. The torn write is inferred from the write ordering and was never
observed end to end.

## What the garble looks like

`status-format` iterates the *live* window set through `#{W:…}` and reads
`#{session_windows}` live (`:541,545,549`). In a frozen state, though,
`@window_split*`, `status` and each window's `@window_label_disp` padding
still hold what the stale render computed. The window grid a viewer sees
therefore no longer matches the windows they have. *Evidence* shows a
concrete frame from a real-hook trial.

## Fix

In `scripts/tmux-reflow-windows.sh` only:

1. **D1.** Stamp `@reflow_key` as `"${total}:${WIDTH}:${HEIGHT}"`, where `total`
   is the number of windows the post-lock `list-windows` actually rendered.
   `cache_key` remains only for the pre-lock fast-path compare. The `recompute`
   log event reports `$total`.
2. **D2: never write unlocked, and never drop the owed render.**
   - **The foreground stays bounded.** It waits at most 2s, timed by the clock (`EPOCHREALTIME`) rather than HEAD's
     40 × 50ms retry count. Every failed acquire spawns processes (`mkdir`,
     `date`, `stat`, `sleep`), and on macOS CI those forks stretched 40 retries
     past 5s.
     Several callers run the script synchronously and block tmux's command
     queue while they do: `session-window-changed`, `after-new-session` and
     `client-session-changed` (`config/tmux.conf.reference.nix:767,773,774`),
     config load (`:901`), and home-manager activation
     (`modules/home-manager.nix:1121`). A long foreground wait would freeze
     keystrokes or `home-manager switch`.
   - **Exhaustion hands off to a waiter.** The script runs
     `detach "$0" --await-lock --force "$SESSION" "$WIDTH"` (`detach` comes
     from `lib-log.sh`) and exits 0.
   - **The waiter polls, then renders or gives up.** It polls every 0.5s. If it
     acquires the lock, it renders. If `SECONDS + OG_LOCK_STALE_SECONDS + 5`
     passes first, it exits 0 without rendering. That is safe: `acquire_lock`
     steals any lock dir older than the stale window, so hitting the deadline
     means every holder acquired after the waiter started and read state newer
     than the trigger. A SIGKILLed holder's lock is therefore stolen by the
     waiter.
   - **`--force` guarantees the render.** The waiter's fast path can't
     cache-hit on a count that matches only by coincidence, which would skip
     the render the waiter exists to do.
   - **`--await-lock` prevents recursion.** A waiter never spawns another
     waiter.

All production callers run the script by absolute store path, so re-execing
`$0` works. In the bats harness, `setup()` gives the sed-built script a
`#!$BASH` shebang and marks it executable for the same reason.

### Limits

Each of these predates this change or is a cost it accepts. None writes
unlocked in the normal case.

- **A steal breaks mutual exclusion for a live holder that outlasts the stale
  window.** That holder's EXIT trap then removes the thief's lock dir. This is
  `acquire_lock`'s existing behaviour, shared by every caller.
- **Two stealers can both win.** When two processes both see a stale dir, B's
  `rmdir` can remove A's freshly made dir, so both proceed. The race is
  pre-existing, but after a SIGKILLed holder every waiter polls on the same
  0.5s tick, which makes it likelier. The result is D2's torn write, and only
  after a dead holder.
- **A waiter can apply a width that is no longer current.** The waiter renders
  at the `WIDTH` its trigger passed. If the client resizes while it waits, the
  resize's debounced reflow can take the lock first. The waiter then renders
  and stamps the old width, and that persists until the next event. HEAD's
  lock queue had the same ordering race inside a ~2s window; the waiter
  stretches that window to the stale deadline. It needs lock exhaustion and a
  resize close together. A width re-read inside the waiter can't be tested in
  the bats harness, which has no attached client, so it is left out.
- **Waiters pile up after a large burst.** Each invocation that exhausts the
  budget detaches one waiter (7 of 26 at N=25). Once the burst ends, each
  forces a full recompute in turn. That is redundant work, but it is serialized
  and every render is correct.
- **The cache key encodes only count, width and height.** If a window is killed
  and another created before the next reflow's pre-lock read, that read still
  matches the key and skips the render. The fix adds no in-lock cache
  re-check, because one would widen this same hole to every queued invocation.

### Assumption: `total` equals `session_windows`

`total` counts parsed `list-windows` lines, while the fast path compares against
`session_windows`. The two diverge only if one row spans two lines.
`@window_task` is the last, unsanitized field, but `claude-status-update task`
writes a single line, and a split row would already misparse the loop. If it
did happen, the cache would never hit: an extra recompute, never a wrong
render. `tmux-next38-readiness.bats` asserts a literal `10:36:0` from a single
invocation, where the two are equal.

## Evidence

### Live, real hooks and a real client

This ran on an isolated server from `nix build .#default`, with the
`window-unlinked` hooks doing all the reflowing. The `script(1)` pty client is
80x24, and `window-size latest` sizes the session to it. Pre-fix, 10 windows
killed down to 1:

```
baseline @reflow_key: 10:80:24
t=100ms   session_windows=1 reflow_key=7:80:24
t=200ms   session_windows=1 reflow_key=3:80:24
...unchanged through t=10000ms
```

The race depends on timing, and other pre-fix runs converged.

### Bats, `tests/reflow-fanout.bats`

Plain nixpkgs tmux 3.7c comes first on PATH, as in the `reflow-fanout-tests`
check. Each row runs a copy of the tree carrying the final test file with that
row's script:

| Script under test | D1 test | Foreground budget | Dead holder | Other 9 |
|---|---|---|---|---|
| `HEAD` | fail (`3:200:0`) | fail (wrote unlocked) | fail (wrote unlocked) | pass |
| round-0 skip-on-exhaustion | pass | fail (never rendered) | fail (never rendered) | pass |
| final | pass | pass | pass | pass |

### End-to-end, `tests/reflow-race-e2e.sh`

Each trial runs on an isolated server with a real attached client, and the
real hooks do every reflow:

1. Kill N windows down to 1.
2. Wait until no reflow is running and the lock is free.
3. Snapshot `@reflow_key`, `@window_split*`, `@window_per`, `status`, the
   session `status-format` and per-window label padding.
4. Run one from-scratch `--force` reflow and snapshot again.

A mismatch means the burst left a state a clean reflow would not produce, and
the script then exits non-zero. Freeze mode adds a step: after the burst it
jumps back to the count the burst stamped (5 if the key was truthful), in one
chained `new-window` command. `--jump-to M` forces that target.

**Pre-fix build**, 5 trials per cell:

| Mode | N | Key poisoned / wrong | Render wrong |
|---|---|---|---|
| burst | 10 | 4/5 | 0/5 |
| burst | 25 | 4/5 | 0/5 |
| freeze | 10 | 4/5 | 4/5 |
| freeze | 25 | 5/5 | 5/5 |

Every freeze trial whose key was poisoned froze visibly (9/10), and the one
unpoisoned trial rendered correctly. One of them, with fields
`session_windows|@reflow_key|split1|split2|split3|@window_per|status|status-format hash|label hash`:

```
before: 10|10:80:24|4|8|999|4|status=4|sf=ab835703|labels=f20347b5
after kill burst: key 8:80:24 with 1 window left; jump to 8
after:   8|8:80:24|999|999|999|1|status=2|sf=d41d8cd9|labels=82878c78
fresh:   8|8:80:24|3|6|999|3|status=4|sf=6b6741d9|labels=cad0ed84
```

Eight windows are open, but the status line is still the 1-window layout. It
has no split points, one window per row, two status lines, and the single-line
`status-format`. A clean reflow of the same session gives three rows split at 3
and 6. Every reflow the chained add forked read 8, matched the poisoned
`8:80:24`, and exited on the fast path. That is "isn't reconciling" and
"garbled" in one frame.

**Final build.** Every configuration exits 0.

| Mode | N | Trials | Key wrong | Render wrong |
|---|---|---|---|---|
| burst | 10 | 5 | 0 | 0 |
| burst | 25 | 5 | 0 | 0 |
| freeze | 10 | 5 | 0 (never poisoned) | 0 |
| freeze | 25 | 5 | 0 (never poisoned) | 0 |
| freeze `--jump-to 2` | 10 | 3 | 0 | 0 |
| freeze `--jump-to 8` | 10 | 3 | 0 | 0 |
| freeze `--jump-to 13` | 25 | 3 | 0 | 0 |

The final build never poisoned the key, so plain freeze mode jumped to 5. The
`--jump-to` rows are the like-for-like control: they jump to counts the
pre-fix runs had poisoned.

## Non-causes

- **Bridge daemon ghost windows (a swallowed `%window-close`).** The top-level
  path is closed. `efba842` (#283, "match control-mode replies by command
  ordinal") added `asyncQueue`/`settle` beside the ordinal matching, which
  queues notifications met during a round-trip for later dispatch
  (`picker/remotebridge/daemon/daemon.go:954–982`, `1533–1542`).
  Burst-killing 4–9 remote windows through a `--test-local` daemon converged
  within ~0.5s in 5/5 trials. That is a judgment, not a measurement of the halo
  case. The `--test-local` "remote" is a bare local tmux running at local-pipe
  latency, without the `after-new-window` hooks #283 blames for the
  interleaving. `tests/remote-m2-integration.bats:290–296` still carries the
  `select-window` workaround.
- **Body-lifting in `picker/remotebridge/controlmode/parse.go` `readBlock`.**
  This is a separate hazard, unrelated to this repro. `readBlock` lifts any body
  line that parses as a known verb, `%window-close` included. M2.3 design D5
  rejected exactly that, because pane content can reach a block body
  (`capture-pane -e -p`, `seed.go`). It is left alone here and named in the PR.
- **`tmux-update-icons` cwd reconcile (#596).** It skips `@bridge_win` windows
  and is untouched.
- **Worktree removal.** It isn't needed to reproduce. A mirror's label state is
  the daemon-owned `@bridge_*`, not derived from the mirror pane's cwd.

## Acceptance criteria

1. **D1 regression test** in `tests/reflow-fanout.bats`.
   1. With 3 windows, hold `$TMPDIR/og-reflow.lock.S`.
   2. Start `tmux-reflow-windows S 200 --force`.
   3. Kill the other two windows by window id, then release the lock.
   4. Poll up to 5s for `@reflow_key == "1:200:0"`.

   It fails against `HEAD`'s script.
2. **D2 regression tests** in the same file.
   - **Foreground budget.** With the lock held and `@reflow_key=sentinel`,
     `timeout 15 tmux-reflow-windows S 200 --force` exits 0 and leaves
     `sentinel`. After the lock is released, `@reflow_key` becomes `1:200:0`
     within 5s.
   - **Dead holder.** With `OG_LOCK_STALE_SECONDS=10` and a lock dir that is
     never released, the foreground run exits 0 and leaves `sentinel`, and
     `@reflow_key` becomes `1:200:0` within 25s. The window is 10s because staleness is whole-second and a
     slow runner's startup adds to the 2s foreground budget: at 5, macOS CI's
     foreground stole the lock itself.

   Both fail against `HEAD`, which writes unlocked, and against the round-0
   skip, which never renders.
3. **CI coverage.** All three run through the existing `reflow-fanout-tests`
   check (`flake.nix`), which runs `bats tests/reflow-fanout.bats`. No new
   derivation is needed.
4. **Symptom level, runnable on demand.** `tests/reflow-race-e2e.sh` is manual
   and Linux-only, and not part of `nix flake check`, in the same spirit as
   `tests/test-display.sh`. Run it after `nix build .#default`:

   ```sh
   tests/reflow-race-e2e.sh --mode burst  --windows 10 --trials 5 ./result/bin/tmux
   tests/reflow-race-e2e.sh --mode freeze --windows 25 --trials 5 ./result/bin/tmux
   tests/reflow-race-e2e.sh --mode freeze --windows 25 --trials 3 --jump-to 13 ./result/bin/tmux
   ```

   On the fix it must exit 0 in both modes at N=10 and N=25. Against `HEAD`'s
   build, freeze mode exits 1.
5. **Gate.** `nix build .#default`, `nix build .#lint` and `nix flake check`
   are green on the tree rebased onto `origin/main`.

## Confirming on halo

This was reproduced on an isolated local server, not on the reporter's laptop
and halo pair. To confirm there, wait until reflows have settled after a reap
and compare:

```sh
tmux show -t <mirror-session> -v @reflow_key
tmux display -t <mirror-session> -p '#{session_windows}'
```

If the count in the key differs from `session_windows`, this is the bug.

## Out of scope

- The bridge daemon, including `readBlock` body-lifting.
- The lock primitive, the debounce path, and the `WIDTH`/`HEIGHT` dimensions of
  the cache key.
