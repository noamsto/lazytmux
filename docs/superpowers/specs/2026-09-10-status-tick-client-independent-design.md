# Client-independent tick floor for the status-format side effects (#603)

## Symptom

On `halo`, `/tmp/lazytmux-pr/.last-tick` and `/tmp/lazytmux-agent-usage/.last-tick`
both froze at the same second (`08:59:25`) and did not move for 82 minutes, with
`status-interval 1` and `REFRESH_SECONDS=120`. A merged PR kept rendering as
open. `tmux refresh-client -S` did not revive it; a manual `tmux-pr-enrich
--tick` worked but the cadence did not resume; no lingering poller process
existed; the per-session `/tmp/lazytmux-statusline/` caches were stale too.

## Root cause

`status_line_size()` returns `0` for any client carrying `CLIENT_CONTROL`
(tmux `status.c:117`), and `status_redraw()` returns before it creates the format
tree when that size is `0` (`status.c:234-237`). A control-mode client therefore
never expands `status-format[0]`, and `status_redraw` is the only caller that
ever expands it, so none of the `#()` jobs embedded in that string is spawned at
all.

`halo`'s clients are all control-mode. It is the machine the agents run on, and
the laptops view it through the remote bridge, whose transport is
`tmux -C attach-session`. The freeze second is the second the last real client
detached. Nothing recovers it except a real client attaching.

Measured on a private server running the same `next-3.8` binary the live server
was built from, with a marker job in `status-format[0]`:

| clients attached | marker runs in 4s |
| --- | --- |
| none | 0 |
| control-mode only | 0 |
| control-mode + real | 4 |
| real detached again | 0 |
| still control-only | 0 |

This is upstream behaviour, not a dev-build regression: the `CLIENT_CONTROL`
branch in `status_line_size` dates from tmux commit `ad27b7de` (2019-05-11).
There is nothing to report upstream — a control client has no status line, and
drawing one is the only thing that expands the format.

### This was already known here, one symptom at a time

The repo has written this root cause down twice without generalizing it:

- `CLAUDE.md`, "Remote Agent Status": a control-mode client renders no status
  line, so the remote's 1s `#()` pollers never run for a session whose only
  client is the bridge. That is why agent state is *pushed* at write time rather
  than polled.
- `picker/remotebridge/daemon/ctl.go:330-333`, explaining why the
  `enrich-refresh` ctl verb exists (#598): "the remote's own tmux-pr-enrich has
  never run for a bridged session (its poller is a status-line `#()`, and a
  control client renders no status line)".

So #603 is not a new discovery. It is the same fact, finally hitting a host whose
*own* pollers are the ones starved rather than a mirror's. Each previous
encounter was patched at the symptom — push the state, add a manual refresh verb
— and this change fixes the driver instead.

That second comment goes **stale** with this change: once the floor lands, the
remote's `tmux-pr-enrich` does run for a bridged session. The verb stays useful
for immediacy, so the comment needs its reason rewritten in the same pull
request.

### Hypotheses falsified

- **"Byte-identical `#()` strings collapse into one shared job, and a shared job
  is never re-run."** They are not shared. `format_job_get` selects
  `ft->client->jobs` whenever the format tree carries a client, and
  `status_redraw` always passes one, so every client keeps its own job tree.
- **"Only the byte-identical jobs are affected, so there are two faults."** One
  fault. Measured with two sessions, each holding one control-mode client, and a
  `status-format[0]` carrying two jobs — one byte-identical across sessions, one
  embedding `#{qs:session_name}` and therefore distinct per session:

  | clients | shared job | per-session job (`a`) | per-session job (`b`) |
  | --- | --- | --- | --- |
  | 2 control-mode only | 0 | 0 | 0 |
  | + a real client on `a` | 5 | 5 | 0 |
  | real client gone again | 5 | 5 | 0 |

  Job identity is irrelevant. The per-session jobs freeze exactly as the shared
  ones do, which is the discriminator the issue was missing.
- **"`detach()` leaves stdin inherited, so tmux never reaps the job and never
  re-runs it."** Measured under a real client against both spellings of
  `detach` (stdin inherited, stdin redirected): 12 runs in 12 seconds either
  way. Inheriting the job's stdin does not keep the job alive.

## The organizing rule

`status-format[0]` carries five `#()` jobs. The axis that matters is **who reads
what the job produces**:

- If the output is consumed *only* by the status line currently being drawn, the
  status format is the right driver. Nothing is lost when nobody draws.
- If the output outlives the draw, or anyone else reads it, it needs a driver
  that does not depend on who is watching.

The tempting shorter rule — "a client that renders no status line needs no text"
— is **false on a bridge host**, and this repo has already been bitten by it. A
bridge is a consumer that is not a status client: the remote's "text" is the
laptop's data, which is exactly why `@bridge_proc`, #589 and #590 exist. So the
test is artifact privacy, not whether a human is looking.

### Side effects — moving to hooks

| work | driver today | gate interval |
| --- | --- | --- |
| `tmux-pr-enrich --tick` | its own `#()` job | `prRefreshSeconds`, default 120, clamped 10-300 |
| `tmux-issue-stamp --backfill` | its own `#()` job | `BACKFILL_TICK_SECONDS`, 30 |
| `tmux-agent-usage --tick` | its own `#()` job | `agentUsage.refreshSeconds`, default 120, clamped 10-300 |
| `arm_agent_detect`'s `agent-detect` arming and `live/` stamps | rides the per-session `tmux-update-icons` job | every 5th tick, `CLAUDE_NOW % 5` |

Two more side effects sit in the same function and **stay** where they are — see
"What the sweep hook must not carry" below: `claude_reap_dead_panes` (#341) and
`claude_prune_stale_state`.

`arm_agent_detect` is the one the first draft wrongly excluded, on the premise
that a `-B` session monitor evaluates one session and so cannot drive
per-session work. The premise is true — `set-hook -g` leaves the monitor's
session NULL (`cmd-set-option.c:203-209`) and `monitor_get_session` falls back to
`RB_MIN(sessions)` — but it does not apply here. `arm_agent_detect` takes **no
session argument** and drives a single server-wide `tmux list-panes -a`, so it is
server-wide in exactly the way the three pollers are, and the hook shape is
identical. It carries three jobs:

- `claude_reap_dead_panes` (#341)
- the dead-agent-floor `live/` stamps, when `claudeStatus.assumeDeadAfter` is
  non-zero
- **arming `agent-detect` through `pipe-pane`**

`config/tmux.conf.nix:1247-1249` makes the dependency explicit: `pane-exited` is
a silent no-op on the pinned tmux, "so per-pane claude-status cleanup instead
rides the every-5th-tick full-server sweep in tmux-update-icons.sh (issue #341)".
That is the config stating outright that it uses `status-format[0]` as a
side-effect channel, which settles whether the sweep is load-bearing.

The third job is the most consequential on the host in the report, and the first
draft did not list it at all.

`claude_prune_stale_state` is the fifth side effect, and earlier drafts missed it
too. Its only caller in the repo is the same `main()`, so on a control-only host a
previous tmux server's pane-keyed files are never purged: `watchers/` entries a
supersession left behind stay, and a restored pane that reuses a dead pane's id
inherits its name and task. After a reboot or a `tmux-remux` restore on `halo`,
nothing prunes at all. It is marker-gated on the server's start time, idempotent,
and needs only `#{start_time}`, so it rides the same hook for free.
 `/tmp/claude-status/screen/` is the only source of
state for non-Claude agents, and it is written only by an `agent-detect` watcher
that `arm_agent_detect` arms. On a control-only host nothing has armed one since
the last real client detached, so codex and cursor panes there have had no state
at all — a larger hole than the stale PR badge that found the bug.

### Text — staying put

- `tmux-statusline` passes the privacy test. It has exactly one write — its own
  last-good-frame cache at `picker/statusline/main.go:113` — and that file is read
  only by `main.go:383`, the same program on its next run. The artifact is
  genuinely private, so a frozen renderer costs nothing.
- `tmux-update-icons`' per-session body **fails** the privacy test, and the rule
  says so rather than excusing it. `@window_icon_display` feeds
  `automatic-rename-format` (`config/tmux.conf.nix:1284`), so on a control-only
  host window names permanently lose their icon suffix, and that name reaches
  every mirror through the `@window_bridge_name` fallback. It is narrow — reflow
  stamps `@window_label_*` from hooks and a mirror prefers those — but it is not
  nothing.

  It is nonetheless a **follow-up**, for a reason that is true here and was not
  true of `arm_agent_detect`: this work is genuinely per-session, and a session
  monitor evaluates one session only, so the mechanism cannot express it. Reviving
  it needs a server-wide mode in the script, which is a real change with its own
  blast radius (`tests/update-icons-all-windows.bats` is the #580 regression test
  keyed on that script's invocation). Deferred on mechanism, not on need.

### Who consumes this on a machine nobody looks at

The point is not freshness on `halo`'s own screen, which nobody reads. It is the
chain the reported symptom travelled:

`halo`'s `tmux-pr-enrich` writes the `@pr_*` window options -> the bridge
daemon's label shipper carries them across as the `@bridge_*` copies -> the
laptop's window list renders them. A poller that never runs on `halo` is exactly
"a merged PR still reading as open" on the laptop.

`tmux-issue-stamp --backfill` and `arm_agent_detect` have the same shape: their
output is read elsewhere. `tmux-agent-usage` is the honest exception — its cache
is read by `tmux-statusline` behind a *local* liveness scan, so `halo`'s copy
feeds nothing until someone really attaches. Reviving it is cheap, harmless and
helps the next attach, but it does not carry the weight the other three do.

## The mechanism

A monitor hook's timer is a plain 1s libevent timer owned by the monitor set
(`monitor.c:monitor_timer`), not by a client, so a `set-hook -g -B` hook fires
with **zero** clients attached.

Measured on a private `next-3.8` server:

| condition | fires |
| --- | --- |
| no clients, format changing every 1s | 10 in 12s |
| no clients, format changing every 10s | 1 in 12s |
| no clients, three such hooks, config sourced twice | 2 each in 22s |
| origin session killed, a second session remaining | keeps firing |

Two limits, so "server-lifetime" is not overstated. With **zero sessions**
`monitor_get_session` yields NULL and `monitor_timer` returns early, so the hooks
are inert — harmless, since `exit-empty` ends such a server anyway. And the hook
*command* string is format-expanded before it is parsed (`hooks_parse`, reached
from `hooks_monitor_hook_cb`), which is safe here only because Nix store paths
contain no `#`; that deserves a comment in a repo this sensitive to `#` in tmux
formats.

`show-hooks -g` prints a monitor hook's **command** only. `show-hooks -g -B` is
the one listing form that prints the subscription itself (measured:
`@lztmux-pr-tick::#{e|/|:#{T:@lztmux_tick},5}`), so any assertion about a
monitor's spec must read the generated config or use `-B`.

The hook's `run-shell -b` child inherits the server's environment and its
`$TMUX`, so the work resolves the same server and honours
`LAZYTMUX_ENRICH_CACHE_DIR` / `LAZYTMUX_AGENT_USAGE_DIR` exactly as it does
today (measured).

### The monitor spec must use an EMPTY target field

Write `@lztmux-pr-tick::<format>`, **not** `@lztmux-pr-tick:session:<format>`.

`monitor_parse` (`monitor.c`) derives the type from the middle field, and a
session monitor is the **empty** string — `%*`, `%N`, `@*` and `@N` are the other
four. Upstream commit `557967c3` (2026-09-07, "Do not silently make a session
monitor if the target is unknown", GitHub issue 5575) turned the permissive
fallthrough into a hard failure:

```
-	else
+	else if (*what == '\0')
 		*type = MONITOR_SESSION;
+	else
+		goto fail;
```

| binary | carries `557967c3` | `:session:` | `::` |
| --- | --- | --- | --- |
| flake pin `578e07fc` | no | accepted, fires | accepted, fires |
| upstream `5f9e6255` | yes | `invalid subscription`, hook never installed | accepted |

`::` is correct on both, so it is the only spelling that survives a routine
`flake.lock` bump. Getting this wrong would reintroduce #603 through a dependency
bump, with no driver left at all.

**Corollary to report, not to fix here:** `tmux-remux` 0.4.0 emits
`@remux-save:session:…`, so the same bump silently kills its periodic-save
floor. `tmuxRemuxWireScript`'s pre-3.8 warning will not catch it, because that
warning greps the emitted text for `@remux-save`, which is still present — the
line is emitted and then rejected at parse. Raise it on noamsto/tmux-remux.

### What the sweep hook must not carry

Only the arming and the `live/` stamps go on the hook. `claude_reap_dead_panes`
and `claude_prune_stale_state` keep their existing per-session call sites.

`CLAUDE_STATUS_DIR` defaults to a bare `/tmp/claude-status` — one path shared by
every tmux server on the machine, which neither `TMUX_TMPDIR` nor `-L` isolates.
The prune deletes by mtime across eight directories; the reaper deletes every pane
file whose id is absent from the *caller's own* `list-panes -a`. Neither can tell
whose state it is deleting, so any second wrapped-tmux server started to test a
build deletes the real server's live agent state.

That hazard is pre-existing: the config-load `run-shell` already prunes once,
client-free, and reaps on a one-in-five chance. But a client-independent 5-second
timer would make it continuous, and a destructive operation that cannot identify
its owner does not belong on a timer. Arming is kept because it is
non-destructive, and because it is the one side effect here whose absence loses
state outright rather than only freshness.

Scoping those two by server identity is the follow-up that would let them join the
hook.

### The sweep is dispatched by environment, not argv

`#{qs:session_name}` quotes a session name for the shell but does not change its
value, so a flag in `$1` can be forged by naming a session after it. Measured:
tmux accepts `new-session -d -s '--sweep'`. A session so named would have taken
the sweep branch and silently stopped rendering its own icons, branch and task —
the very failure this change exists to fix, reintroduced in the same file. Both
call sites are exposed: the per-session status tick and the config-load
`run-shell`.

So the hook sets `LZTMUX_TICK_SWEEP=1` in its command's environment, which a
session name cannot forge, and `main()` dispatches on that.

### No hook command carries a tmux format

With the prune gone from the hook, the sweep needs no server start time, so all
four commands are pure store paths. A build check asserts a hook command contains
no `#{` at all.

That is deliberate, not incidental. `-B` monitor hooks are the first hook path
that format-expands its action string (`hooks_parse`, reached from
`hooks_monitor_hook_cb` with expansion on) and then re-lexes the result with
tmux's own command parser — which strips exactly the backslashes
`format_quote_shell` inserts — before `run-shell` expands a second time. A
`#{q:…}` in that position is therefore not the protection it looks like. It was
inert here only because a start time is pure digits. Keeping formats out
altogether is the invariant worth enforcing.

### Tick granularity

One global option holds the clock and one divisor serves all four hooks:

```
set -g @lztmux_tick '%s'
set-hook -g -B '@lztmux-pr-tick::#{e|/|:#{T:@lztmux_tick},5}' 'run-shell -b "<store>/bin/tmux-pr-enrich --tick"'
```

`#{T:<option>}` expands an option's value through `strftime` and has no inline
form — `#{T:%s}` expands to the empty string (measured) — so the option is
required. Integer-dividing epoch seconds by 5 gives a value that changes every
five seconds.

Five, not ten, because `arm_agent_detect` runs every 5th tick today and the
floor should not make a new agent pane wait longer to be armed. The three
pollers gate on their own mtime first, so the extra ticks cost a short-circuiting
fork and nothing else: four hooks at 0.8 forks/second, against the four jobs per
second per attached client the status path pays today.

### The `-B` guard is required, and must use the string form of `if-shell`

`-B` needs tmux 3.8+, and the repo's documented decision is to gate it on the
**live** server's version, not on the store build:
`modules/home-manager.nix:80-94` passes `tmux-remux triggers` the live
`#{version}` for exactly this reason (#407 — a server predating a nix switch
keeps its old binary resident), and warns on the pane when the floor is
unavailable. Measured: `tmux-remux triggers --tmux-version=3.7c` emits no `-B`
line, `--tmux-version=next-3.8` emits one.

So these hooks are guarded too, and the guard's else-branch warns on the pane the
way the remux wiring does. Unguarded, a pre-3.8 live server would print
`command set-hook: unknown flag -B` on every config load and leave the work dead
with no warning, which is worse than today.

The guard must pass its body as a **string**, not a brace block. tmux parses
every branch of a brace block at source time, so the flag is rejected even when
the condition is false. Measured on the pinned binary:

```
if-shell 'false' { set-hook -g -Q '@nope::x' 'run-shell -b true' }
  -> c.conf:1: command set-hook: unknown flag -Q     (exit 1)
if-shell 'false' "set-hook -g -Q '@nope::x' 'run-shell -b true'"
  -> exit 0
```

`config/tmux.conf.nix:691` already records this, and `floatNewPaneGuard` is the
in-repo precedent for the shape.

### The sweep must bypass its own `% 5` gate

`arm_agent_detect` opens with `((CLAUDE_NOW % 5)) && return 0`
(`scripts/tmux-update-icons.sh:57`), and `CLAUDE_NOW` is sampled at script start
(`scripts/lib-claude.sh:34`). Under the 1-second status driver that is a throttle.
Under a 5-second driver it is a **phase filter on a clock nobody aligns**, and it
fails in the worst available shape: the hook fires, the script returns 0
immediately, nothing is logged, and reaping, the `live/` stamps and `agent-detect`
arming all stop with no artifact to attribute it to. That is #603's failure mode
relocated into the fix.

Measured idle on the pinned binary, divisor 5 over 47 seconds: 9 fires, every one
at an epoch congruent to 0 mod 5. The happy path works, which is exactly what
makes it dangerous — the alignment is incidental, not guaranteed. `monitor_timer`
re-arms with a 1-second *relative* timeout from inside its own callback, so
intervals are at least a second and drift forward; the measurement table above
shows that drift directly ("format changing every 1s | 10 in 12s" skipped two
epoch values on an idle server). One skip across a multiple of 5 shifts the
residue permanently, and `run-shell -b` forking bash across a second boundary does
the same thing independently.

So `--sweep` bypasses the gate: the hook **is** the cadence, and keeping the gate
under both drivers is double-throttling. An acceptance test must assert the
sweep's artifact advances across a window long enough to catch a slip, not merely
that it ran once — a test on an aligned server passes either way.

A caveat this creates, worth stating: with the gate kept in the `#()` path and a
divisor of 5, the hook and every attached session's `#()` sweep land on the same
second, so a watched host with several sessions runs several passes at once rather
than staggered. That already happens across sessions today, since they share the
gate, so the hook adds one pass rather than a new class of behaviour. Freeing the
hook's phase is what would let it be offset deliberately.

### Every hook needs an unconditional `-u` clear

`hooks_monitor_add` keys on the hook name, so re-setting one replaces it and a
reload is idempotent. **Disabling** one is not. Build with `enrich.enable = false`
and reload, and the previous generation's monitor stays registered on the live
server, firing `run-shell -b` at a store path garbage collection will remove,
every five seconds, for the life of that server. The `#()` jobs have no such
failure mode because `status-format[0]` is set unconditionally, so the string
simply no longer contains them.

So all four hooks are cleared unconditionally, above the conditional setters, the
way the conf already clears its event hooks — and for the reason it already
states for its notification hooks, that a rebuild with a feature off must not
leave a hook pointing at a dead store path. Measured: `set-hook -g -u -B '@name'`
on a bare name removes the subscription and stops the firing, and on a name that
was never set it is silent with status 0, so the clears are safe on a fresh
server. They live **inside** the version guard, since `-u -B` is still `-B`.

One residue, so it is not a surprise later: `-u -B` removes the monitor but leaves
the `@name` option string, so `show-options -g` keeps printing the old command and
its store path. Nothing fires without a monitor, so this is an auditability problem
rather than a liveness one — someone inspecting a disabled feature should not find a
dead store path with no way to tell it is inert. Each clear is therefore paired with
a `set -gu`, which removes the string and is silent on a name that was never set.

### Why the three poller `#()` jobs go, and the sweep's call site stays

The pollers' `#()` jobs exist **only** as drivers. They contribute nothing to the
rendered line, the hook fires in every case they do and more, and no poller has a
gate shorter than 30 seconds, so the 1-second resolution they offer buys nothing
observable. They are removed.

`arm_agent_detect`'s call in `tmux-update-icons`' per-session body is different
and stays. Two drivers for one cadence is safe here for three specific reasons,
and they are worth writing down because the general argument against two drivers
otherwise applies verbatim: arming is gated on `#{pane_pipe}` being 0 and then
issued as `pipe-pane -o`, a no-op on an already-piped pane; both passes write the
same `CLAUDE_NOW` into the per-pane stamps and `.sweep`, so the "a fresh `.sweep`
implies every agent pane of that pass was stamped" invariant survives interleaving;
and `claude_reap_dead_panes` is idempotent. It rides an invocation that has to happen anyway to render the icons,
so the hook is a floor under it rather than a competing driver; the sweep is
idempotent; and keeping it means no configuration loses `agent-detect` arming if
the `-B` guard ever fails closed — arming is the one side effect here whose
absence loses state outright rather than only freshness.

## The quoting guard does not see this construct

`tests/conf-shell-quoting.bats` exists because a command-injection class recurred
twice here: `run-shell`/`if-shell` format-expand their whole argument before `sh
-c` sees it, so a bare `#{…}` in that argument is injection the moment the value
carries a shell metacharacter.

Its recursion predicate requires `^`, whitespace, `;` or `{` before the command
word. In these hooks the byte before `run-shell` is a single quote, so the branch
never recurses and the nested shell string is never scanned. Measured: a bare
`#{session_name}` inside `if-shell "true" "run-shell -b \"/bin/x #{session_name}\""`
is flagged, while the same format inside
`if-shell "true" "set-hook -g -B '@a::1' 'run-shell -b \"/bin/x #{session_name}\"'"`
is not.

So an earlier "both scanners are clean on the emitted text" result was true and
**vacuous** for the nested `run-shell`. This change is the first in the conf to
nest a `run-shell` behind a quote inside a string-form branch, so it is what makes
the blind spot reachable — and the sweep hook immediately walks into it by
carrying `#{q:start_time}`. The predicate is therefore widened to admit a quote,
in this change. Measured: the widened predicate flags the fixture above and leaves
the real emitted config clean at 18/18, so it adds no false positive.

## Acceptance criteria

- [ ] All three pollers and the sweep run on a ~5s cadence on a server with
      **zero** clients.
- [ ] The same holds with only a control-mode client attached.
- [ ] A real client attached changes nothing: the pollers still run, the sweep
      still runs, and the status line still renders.
- [ ] Every monitor spec uses the empty target field (`@name::`), and a check
      fails on `:session:`.
- [ ] Every hook is cleared unconditionally with `-u -B` above the conditional
      setters, so disabling a feature cannot leave a live monitor firing a
      garbage-collected store path.
- [ ] `--sweep` bypasses the `CLAUDE_NOW % 5` gate, and a test asserts the sweep's
      artifact keeps advancing across a window long enough that a phase slip would
      show, not merely that it ran once.
- [ ] `claude_prune_stale_state` runs from the same hook, receiving the server
      start time.
- [ ] The quoting guard's recursion predicate admits a quote before the command
      word, it flags a bare format nested the way these hooks nest one, and the
      real emitted config stays clean.
- [ ] `tests/tmux-next38-readiness.bats`' hook-name extractor no longer reads the
      `-B` / `-u` flag tokens as hook names.
- [ ] The hooks are guarded by a string-form `if-shell`, and the else-branch
      warns on the pane.
- [ ] Each hook is emitted only when its feature is enabled (`enrich.enable` /
      `agentUsage.enable`); the sweep hook is unconditional, as its call site is.
- [ ] `status-format[0]` no longer carries any `--tick` / `--backfill` job.
- [ ] A build-time check fails if a poller invocation is reintroduced into
      `status-format[0]`, or if one of the hooks is dropped.
- [ ] A live test demonstrates the freeze and the recovery side by side: a `#()`
      job in `status-format[0]` stays frozen under a control-only client while
      the monitor hooks on the same server fire.
- [ ] `picker/remotebridge/daemon/ctl.go:330-333`'s comment no longer claims the
      remote's poller has never run for a bridged session.
- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` all pass — with
      the new floor live inside every wrapped-tmux test server, which is a
      behaviour change for those suites and not only for production.

## Non-goals

- Any change to the pollers' own gate intervals or internal logic.
- `tmux-remux`'s `:session:` spelling — report upstream, do not patch here.
- Anything upstream in tmux.
