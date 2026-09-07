# Report a missing theme-toggle, fix the premature applied-stamp (#545)

## Problem

Two defects in the bridge's theme fan-out (`scripts/lztmux-remote-theme.sh` +
`picker/remotebridge/daemon/ctl.go`'s `theme` verb):

1. A remote without `theme-toggle` (every headless remote — it ships from the
   desktop profile) silently no-ops: `command -v theme-toggle >/dev/null 2>&1
   && exec theme-toggle apply %s` succeeds by omission, so a missing binary
   and a correct apply are indistinguishable. Nothing tells the user.
2. `@lztmux_theme_applied` (global) is stamped *before* the fan-out runs, so a
   fan-out that reached nobody still records success — the next reload
   short-circuits on the unchanged flavor and never retries.

## Decisions

- **Defect 1**: decouple *detection* from *apply*. The apply verb (`ctl.go`'s
  `"theme"` entry, `run-shell -b`) is fire-and-forget by design — the daemon
  acks the local ctl caller as soon as it has queued the command, never
  waiting on the remote, so there is no synchronous channel to learn the
  remote's exit status per toggle. Instead, probe **once**, at daemon
  connect — `Run()` in `daemon.go` only executes once per bridge (a
  reconnect goes through `repair`, not `Run`), which gives "once per host"
  for free with no new state to track.
- **Defect 2**: keep `@lztmux_theme_applied` exactly as it is today — a pure
  local-reload guard (its only real job, per CLAUDE.md: making an
  unchanged-flavor `prefix + r` free). The issue names the tension itself
  (global stamp, many hosts, no way to express partial success in one
  option) and offers reporting per-host failure through defect 1 as the
  alternative to a per-session stamp. That's what defect 1's one-shot
  connect-time notice already does — a host missing `theme-toggle` is
  reported once, independent of the stamp, without ever making the reload
  cost anything. **No code change to `scripts/lztmux-remote-theme.sh` or
  `tests/remote-theme.bats`.**

## Steps (as implemented, revised after plan-critic pass 1 — see below)

- [x] **Step 1: probe command, colocated with the apply verb**
  (`picker/remotebridge/daemon/ctl.go`, beside `themeApplyScript`)
  - `func themeProbeCmd(sess string) string` builds the `run-shell` line.
    **Critic finding (blocking):** `run-shell` executes under the remote's
    `default-shell` (fish, not POSIX) — the apply verb already wraps its body
    in `exec /bin/sh -c` for exactly this reason (`themeApplyScript`'s
    contract). The probe body (`command -v theme-toggle >/dev/null 2>&1 &&
    echo yes || echo no`) must be wrapped the same way, not sent as raw
    `&&`/`||` directly to `run-shell`. Targets the bridged session (`-t sess`)
    since no pane is resolved yet at the call site.

- [x] **Step 2: probe + notify functions** (new file
  `picker/remotebridge/daemon/themeprobe.go`)
  - `func themeToggleAvailable(rt roundTrip, sess string) (available, checked bool)`:
    round-trips `themeProbeCmd(sess)` via the existing `one(rt, cmd)` helper
    (same pattern as `readIdentity`/`readLayout`). `!ok || l.Kind ==
    controlmode.Error` → `(false, false)` (inconclusive — link dropped or
    command errored, never treated as "missing", no notify).
  - `func notifyThemeMissing(cfg Config)`: fires `notifyLocal` in a goroutine
    via `retryNotifyLocal` (see below), so it never blocks `Run()`.
  - `func retryNotifyLocal(cfg Config, msg string, retries int, interval
    time.Duration, sleep func(time.Duration))`: retries up to `retries` times.
    **Critic finding (blocking):** `scripts/lztmux-remote-open.sh` backgrounds
    the daemon and only *then* runs `switch-client` — the mirror session can
    have no client yet the instant `Run()` reaches the probe, and
    `notifyLocal` silently drops with no client. A bare one-shot call would
    reproduce the exact "nothing tells the user" failure this issue is about.
    Sleep is injected (mirrors `Backoff` in this same package) so a test
    drives the loop without a real wait.
  - `notifyLocal` (`paste.go`) changed to return `bool` (sent), and gained a
    nil-guard on `cfg.LocalTmuxOut`/`cfg.LocalTmux` — its only caller before
    this change was the paste path (off with no ssh transport); `Run()` is
    now an unconditional caller.

- [x] **Step 3: wire into `Run()`** (`daemon.go`, right after the existing
  `list-windows` check succeeds — first place `rt` is known good and nothing
  else in setup has started, so this is one extra round-trip at connect, not
  a poll):
  ```go
  if available, checked := themeToggleAvailable(rt, cfg.RemoteSession); checked && !available {
      notifyThemeMissing(cfg)
  }
  ```

- [x] **Step 4: Go unit tests** (`picker/remotebridge/daemon/themeprobe_test.go`,
  using the `scriptedRT`/`newTestReader` pattern from `sessionpin_test.go`)
  - `themeProbeCmd`'s exact wrapped command string.
  - `themeToggleAvailable`: present, absent, connection-dropped
    (inconclusive), `%error` reply (inconclusive) — none of the inconclusive
    cases notify.
  - `retryNotifyLocal`: stops as soon as a client attaches; gives up (no
    infinite sleep) once retries are exhausted. Both deterministic — no real
    `time.Sleep`.
  - `notifyLocal`'s nil-guard.

- [x] **Step 5: CLAUDE.md updates**
  - "Script Roles" table, `lztmux-remote-theme` row: notes the stamp stays a
    pure local-reload guard with no per-host success/failure, and points at
    the daemon's separate one-shot probe.
  - "What the Remote Host Needs on PATH" table, `Theme fan-out` row: absence
    is now reported once per bridge connect via `display-message`, not
    silent.

- [x] **Step 6: PR body** carries the Decisions section's defect-2 paragraph
  verbatim (global stamp stays a pure reload guard; per-host failure is
  reported through defect 1; an unchanged-flavor `prefix + r` stays free) —
  the dispatcher notes require this justification to be explicit, not just
  implied by an unmodified script.

- [x] **Step 7: verify**
  - `go test ./picker/remotebridge/...` (new tests + no regressions —
    note: `TestCarouselResolveScriptManifestCheck/present_manifest_...` fails
    on `main` already, unrelated to this change; confirmed via `git stash`).
  - `nix flake check` (bats, including unmodified `tests/remote-theme.bats`
    and `tests/remote-m2-integration.bats` — the latter runs real tmux
    servers with no `theme-toggle` on PATH, so the new probe fires
    `notifyThemeMissing` once per test's daemon startup; harmless extra
    `display-message`, asserts nothing that would conflict).
  - `nix build .#lint`.

## Out of scope

- No change to the `theme` ctl verb's own apply behavior (`ctl.go`
  `themeApplyScript`) — it stays fire-and-forget, silent per call. That
  silence is intentional (dispatcher notes: "not once per reload").
- No per-session `@bridge_theme_*` stamp — rejected direction, see Decisions.

## Plan-critic history

- **Pass 1**: `revise`, 3 blocking findings (probe shell-wrapping mismatch;
  notify-before-client-attaches race with no verification; missing PR-body
  justification step) + several non-blocking notes (deadline rationale,
  stale comment, CLAUDE.md row naming, `notifyLocal` nil-guard, ambiguous
  test file name). All incorporated above.
- **Pass 2**: `accept`. All three blocking findings verified fixed against
  the real diff (not just the plan doc). Non-blocking notes: verify steps
  6/7 must still actually run (tracked below); the probe's `yes`/`no` reply
  parsing has no live-tmux assertion (bats coverage would close this, judged
  non-blocking — `tests/remote-m2-integration.bats` already runs the daemon
  against a real remote with no `theme-toggle` on PATH, exercising the code
  path even without a dedicated assertion); no deadline on the probe
  round-trip (matches the adjacent list-windows round-trip's existing
  posture); `--test-local`'s empty `RemoteHost` renders a cosmetic double
  space in the notice.

## Post-acceptance correction: the probe mechanism itself

Running `tests/remote-m2-integration.bats` (deferred past pass 2, per the
non-blocking note above) surfaced a defect the plan-critic pass could not
have caught without a live tmux server: the accepted Step 1 mechanism — a
blocking (`run-shell -t sess ...`, no `-b`) command sent via the shared
round-tripper — hangs the mirror's live-content painting forever.

Root cause, confirmed by an A/B test (baseline daemon vs. this branch's
daemon, both driving the same two real tmux servers): `run-shell` without
`-b` displays its output ON the target pane via a transient view-mode
overlay — the exact behavior every *other* `run-shell` call in `ctl.go`
uses `-b` to avoid (`themeApplyScript`'s own comment: "nothing should
appear on screen"). The probe's `-t sess` target resolves to the same pane
this daemon is about to mirror, so the overlay left that pane wedged: its
`%pause`/`%continue` re-seed — the mechanism the "pause-after default"
regression test (#183) exists to cover — never fires again. Reproduced
with `theme-toggle` both present and absent, so the hang is the overlay,
not the notify path.

Fix: `themeProbeCmd` now sends `display-message -p -t sess "#(...)"` —
tmux's job-substitution idiom (`remoteClockSkew`, `newSessionPin` already
use `display-message -p` for a synchronous, side-effect-free read; a
`#()` job runs the same POSIX body without ever touching a pane). The
tradeoff: a `#()` job is cached and populated asynchronously, so the
first read of a freshly-issued job routinely comes back empty — measured
via a raw control-mode client, ~300ms before a second identical read
returns the job's result. `themeToggleAvailable` (renamed loop body
`themeToggleAvailableRetry`) retries up to 6 times at 300ms, mirroring
`retryNotifyLocal`'s injectable-sleep shape for the same reason: a test
must drive the loop without a real wait.

Re-verified after the fix: three consecutive runs of the exact
`remote-m2-integration.bats` "pause-after default" scenario (headless —
`theme-toggle` genuinely absent from the remote's `PATH`) all painted live
content on the first poll. `go test ./picker/remotebridge/...` re-run
clean (the pre-existing `TestCarouselResolveScriptManifestCheck` flake
aside). Steps 1, 2 and 4 above are revised accordingly; steps 3, 5 and 6
needed no change — `Run()`'s call site and the notify path were never the
defect.
