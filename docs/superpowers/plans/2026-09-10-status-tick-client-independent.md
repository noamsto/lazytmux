# Plan: client-independent tick floor for the status-format side effects (#603)

Design spec: `docs/superpowers/specs/2026-09-10-status-tick-client-independent-design.md`.
Read it first. Every choice below, and the measurements behind it, is justified
there — in particular the empty-target monitor spec (`@name::`), the string-form
`if-shell` guard, and why `arm_agent_detect` is in scope.

## What code review changed, after this plan was written

The steps below are the plan as written. Three things changed during review; the
spec carries the reasoning, and the code follows the spec, not these steps.

- **The sweep hook arms only.** `claude_reap_dead_panes` and
  `claude_prune_stale_state` were dropped from it and keep their existing
  per-session call sites. `CLAUDE_STATUS_DIR` is a bare `/tmp` path shared by every
  tmux server and isolated by neither `TMUX_TMPDIR` nor `-L`, and both functions
  delete against the caller's own view, so a client-independent 5-second timer
  would let a scratch server continuously wipe the real server's agent state.
- **The sweep is dispatched by `LZTMUX_TICK_SWEEP` in the hook's environment, not
  by a `--sweep` argv flag.** tmux accepts a session named `--sweep`, and
  `#{qs:session_name}` does not change a value, so the flag was forgeable — that
  session would have silently stopped rendering its own icons.
- **No hook command carries a tmux format.** With no prune on the hook there is no
  start time to pass, so `#{q:start_time}` is gone and the build check asserts a
  hook command contains no `#{` at all. `-B` hooks expand their action string and
  then re-lex it with tmux's own parser, which strips the backslashes `#{q:}`
  inserts, so that form is not the protection it appears to be.

One step was also planned in error and reverted: adding `(` to the quoting guard's
boundary set. tmux has no `( )` grouping, so the construct is unreachable, and
covering it required a bespoke recursion guard in a security scanner.

One sentence: drive the four server-wide side effects that `status-format[0]`
currently smuggles through `#()` jobs from tmux 3.8 `-B` monitor hooks instead,
which fire on the server's own clock rather than on a status redraw.

## Step 1 (do this FIRST): isolate the state dirs in the wrapped-tmux suites

`tests/conf-shell-quoting-integration.bats`, `tests/rename-bind-integration.bats`,
`tests/tmux-next38-readiness.bats` — the only three suites that start a server from
`tmuxConfig.tmux-wrapped` and therefore load the generated config.

This step is first because without it the later steps make the local test workflow
**destructive**. It is not a tidiness step.

- [ ] Export `CLAUDE_STATUS_DIR`, `LAZYTMUX_ENRICH_CACHE_DIR` and
      `LAZYTMUX_AGENT_USAGE_DIR` into `$BATS_TEST_TMPDIR` in all three `setup`
      functions. Only `tmux-next38-readiness.bats:14` sets the first today; none of
      the three sets the other two.
- [ ] Why it is destructive, verified: once the hooks are live they fire inside
      these test servers, and the sweep reaches two functions that delete files
      under `CLAUDE_STATUS_DIR`, which defaults to the developer's real
      `/tmp/claude-status`.
      - `claude_reap_dead_panes` unlinks every file in `panes/`, `screen/`,
        `interrupt/` and `watchers/` whose id is absent from the `list-panes -a`
        rows it was handed. Rows from a one-window test server are perfectly well
        formed, so the #373 fail-closed guard does not fire — it only catches
        *malformed* rows.
      - `claude_prune_stale_state`, which Step 2 adds to the same hook, is worse:
        it deletes every file with `mtime < server_start` across **eight**
        directories. A test server starting "now" means nearly the whole real tree,
        not just ids missing from a pane list.
- [ ] `nix flake check` **cannot** catch this: the sandbox `/tmp/claude-status` is
      empty, so CI stays green and silent while the damage lands only on a developer
      running bats directly in a worktree, which is the documented local workflow.
      Do not treat the gate as coverage for it.
- [ ] `pipe-pane` arming is not part of the hazard: it is gated on the pane's
      normalized command appearing in `AGENT_COMMANDS`, and every pane in these
      suites runs the default shell. Confirm rather than assume.
- [ ] Add a check asserting that every bats suite referencing `TMUX_BIN` exports all
      three variables, so a future wrapped-tmux suite cannot be added without
      isolation.

Verify: with the later steps in place, run all three suites locally from a worktree
and confirm `/tmp/claude-status` is untouched — compare a file listing before and
after.

## Step 2: a `--sweep` entry point for the agent sweep

`scripts/tmux-update-icons.sh`.

- [ ] Give `arm_agent_detect` an optional first parameter that skips the
      `((CLAUDE_NOW % 5)) && return 0` gate when non-empty. Leave the gate in the
      function, not at the call site: `tests/agent-detect-arm.bats:70` and
      `tests/agent-liveness.bats:365` both pin the gate by calling
      `arm_agent_detect` with no argument under `CLAUDE_NOW=101`, so a no-argument
      call must behave exactly as today.
- [ ] Add a `--sweep` branch at the very top of `main()`, **before**
      `SESSION=${1:-…}` (otherwise `--sweep` is read as a session name). It calls
      `arm_agent_detect force` and returns. It must not touch the per-session
      rendering body.
- [ ] Leave the existing `arm_agent_detect` call in `main()` untouched. The spec
      explains why this one keeps its call site while the pollers lose theirs.
- [ ] `CLAUDE_NOW`, `AGENT_DETECT_BIN`, `AGENT_COMMANDS`, `CLAUDE_LIVE_DIR` and
      `claude_reap_dead_panes` all come from the sourced libs, so `--sweep` needs
      no extra setup. Confirm by reading the top of the script rather than
      assuming.

Verify: `bats tests/agent-detect-arm.bats tests/agent-liveness.bats` still pass
unchanged, and `CLAUDE_NOW=101 bash scripts/tmux-update-icons.sh --sweep` arms
(the gate is skipped) while `CLAUDE_NOW=101 … arm_agent_detect` with no argument
does not.

## Step 3: emit the guarded monitor hooks

`config/tmux.conf.nix`, immediately after `set -g status-format[4] ""`.

- [ ] `set -g @lztmux_tick '%s'` — the shared clock. `#{T:<option>}` has no
      inline form, so the option is required.
- [ ] Build a list of `set-hook` command strings, one per side effect, gated by
      the same flags that gate the `#()` jobs they replace:

      | hook name | command | gate |
      | --- | --- | --- |
      | `@lztmux-pr-tick` | `tmux-pr-enrich --tick` | `enrichEnable` |
      | `@lztmux-backfill-tick` | `tmux-issue-stamp --backfill` | `enrichEnable` |
      | `@lztmux-usage-tick` | `tmux-agent-usage --tick` | `agentUsageEnable` |
      | `@lztmux-sweep-tick` | `tmux-update-icons --sweep` | unconditional |

      Each spec is `'<name>::#{e|/|:#{T:@lztmux_tick},5}'` — **empty** middle
      field, never `:session:`. Each command is
      `'run-shell -b "<store path> <args>"'`. Verified: `#{q:start_time}` reaches
      the sweep as a single argument (`argc=2`, an epoch integer matching
      `display-message -p '#{start_time}'`).

- [ ] Chain FOUR unconditional `set-hook -g -u -B '@lztmux-<name>-tick'` clears
      **first**, ahead of the conditional setters, regardless of `enrichEnable` /
      `agentUsageEnable`. This is load-bearing and not cosmetic: `hooks_monitor_add`
      keys on the name so a reload replaces, but a *disable* does not — rebuild with
      `enrich.enable = false`, reload, and the old generation's monitor keeps firing
      at a store path GC will remove, every 5s, for the life of the server. The
      `#()` jobs have no such failure mode because `status-format[0]` is set
      unconditionally. Verified: a `-u -B` clear on a bare name removes the
      subscription and stops the firing, and on a never-set name it is silent with
      status 0. The clears go inside the guard, since `-u -B` is still `-B`.
- [ ] Pair each `-u -B` clear with a `set -gu '@lztmux-<name>-tick'`. The `-u -B`
      form removes the monitor but leaves the `@name` option string, so
      `show-options -g` keeps printing the old command and its store path. Nothing
      fires without a monitor, so this is auditability rather than liveness: someone
      inspecting a disabled feature should not find a dead store path with no way to
      tell it is inert. Measured: after `-u -B` the subscription is gone from
      `show-hooks -g -B` while `show-options -gv` still prints the command; a
      following `set -gu` removes it, and `set -gu` on a never-set name is silent
      with status 0.

      Order each pair `-u -B` first, then `set -gu`. Review raised the concern that
      the reverse order would defeat the clear, since `hooks_monitor_remove` reaches
      the monitor through `options_get_only` and would find nothing. That hazard does
      **not** reproduce: measured against a live firing hook, `set -gu` first still
      removed the subscription and the firing stopped. `options.c:415-416` is why —
      freeing an option entry calls `hooks_monitor_free` on its monitor data, so
      unsetting the option destroys the monitor on its own. Keep `-u -B` first anyway,
      because it is the documented path and costs nothing, but do not write the
      reverse order up as a bug.
- [ ] Emit them as ONE `if-shell`, string form, chaining the clears and then the
      enabled hooks with ` \; `, with a `display-message` in the else-branch. Every
      clear must precede every setter — a check asserts that ordering. Model the escaping on
      `floatNewPaneGuard` (`config/tmux.conf.nix:691-695`) — its `esc` helper and
      its `\;` chaining are the same problem already solved. Do not use a brace
      block: tmux parses every branch at source time, so `-B` is rejected even
      when the condition is false.
- [ ] Probe: `tmux list-commands set-hook | grep -q -- -B`. This asks the LIVE
      server, which is the point (#407).
- [ ] Else-branch message must be accurate: on a pre-3.8 server the sweep still
      runs through `tmux-update-icons`' per-session body while a real client is
      attached, so say the floor is missing, not that the work is dead.
- [ ] The emitted shape below is already verified on the pinned binary: all four
      hooks registered and each fired twice in 12s with **zero** clients, and both
      `tests/check-tmux-format-delimiters.sh` and `tests/conf-shell-quoting.bats`
      are clean on it. Reproduce this text, do not reinvent the escaping.

      ```
      if-shell "tmux list-commands set-hook | grep -q -- -B" "set-hook -g -B '@lztmux-pr-tick::#{e|/|:#{T:@lztmux_tick},5}' 'run-shell -b \"<store>/bin/tmux-pr-enrich --tick\"' \; set-hook -g -B '@lztmux-backfill-tick::#{e|/|:#{T:@lztmux_tick},5}' 'run-shell -b \"<store>/bin/tmux-issue-stamp --backfill\"' \; set-hook -g -B '@lztmux-usage-tick::#{e|/|:#{T:@lztmux_tick},5}' 'run-shell -b \"<store>/bin/tmux-agent-usage --tick\"' \; set-hook -g -B '@lztmux-sweep-tick::#{e|/|:#{T:@lztmux_tick},5}' 'run-shell -b \"<store>/bin/tmux-update-icons --sweep #{q:start_time}\"'" "display-message 'lazytmux: ...'"
      ```

      In the Nix source that means `\\;` for each separator and `\\\"` for each
      nested quote, the same doubling `floatNewPaneGuard`'s `esc` performs.

- [ ] Comment the three non-obvious things: why `-g` (leaves the monitor session
      NULL, making the hook server-lifetime), why the empty target field (upstream
      `557967c3`), and why the divisor is 5 (`arm_agent_detect`'s own cadence).
      Add a fourth: the hook command string is format-expanded before it is parsed
      (`hooks_parse` via `hooks_monitor_hook_cb`), which is safe only because Nix
      store paths contain no `#`.

Verify: `nix build .#default`, then grep the built conf for the four
`@lztmux-…-tick::` specs, assert no `:session:` appears, and source the built
conf on a private server to confirm all four hooks register and fire.

## Step 4: take the three poller jobs out of `status-format[0]`

`config/tmux.conf.nix`, the `set -g status-format[0]` line.

- [ ] Delete the `lib.optionalString enrichEnable "#(echo; …--tick)#(echo; …--backfill)"`
      interpolation and the `lib.optionalString agentUsageEnable "#(echo; …--tick)"`
      interpolation that carry the three poller jobs.
- [ ] Leave everything else byte-identical: the `tmux-update-icons` job, the
      `tmux-statusline` job, and the `agentUsageEnable`-guarded
      `--icon-usage-*` / `--agent-usage-monthly-threshold` arguments that live
      **inside** the `tmux-statusline` invocation. Only the three standalone
      `#(echo; …)` poller jobs go. Read the line carefully before editing — there
      are two `agentUsageEnable` interpolations on it and only one is a job.
- [ ] Do not remove any `script.*` binding; all three scripts are still reached
      from hooks and keybinds.

Verify: `nix build .#default`, then
`grep 'set -g status-format\[0\]' "$CONF" | grep -c -- '--tick'` must be `0`
(it is `1` before this step — confirm that first, so the assertion is known to be
able to fail).

## Step 5: widen the quoting guard so it can see these hooks

`tests/conf-shell-quoting.bats`, the `walk_tokens` recursion predicate.

- [ ] The predicate is
      `(^|[[:space:]\;{])(run-shell|if-shell)([[:space:]]|$)`. It requires `^`,
      whitespace, `;` or `{` before the command word, so a `run-shell` preceded by a
      quote is never recursed into and its shell string is never scanned. These
      hooks are exactly that shape.
- [ ] This is what makes the earlier "both scanners are clean" result **vacuous**
      for the nested `run-shell`. Measured: a bare `#{session_name}` inside
      `if-shell "true" "run-shell -b \"/bin/x #{session_name}\""` is flagged, and
      the same format inside
      `if-shell "true" "set-hook -g -B '@a::1' 'run-shell -b \"/bin/x #{session_name}\"'"`
      is **not**. Do not cite the clean run as evidence anywhere.
- [ ] Widen the class to admit a quote:
      `(^|[[:space:]\;{\'\"])(run-shell|if-shell)([[:space:]]|$)`. Verified: the
      widened predicate flags the blind-spot fixture and leaves the real emitted
      config clean at 18/18, so it introduces no false positive.
- [ ] Fix this rather than merely documenting it, because this change is the first
      in the conf to nest a `run-shell` behind a quote inside a string-form branch,
      and the sweep hook walks straight into the blind spot by carrying
      `#{q:start_time}`. A guard that exists because an injection class recurred
      twice should not go blind on the construct that makes it reachable.
- [ ] Add the blind-spot fixture to the scanner's own "prove it can fail" cases, the
      way it already pins its other rules, so a future narrowing is caught.

Verify: `nix build .#checks.x86_64-linux.conf-shell-quoting-tests`, and confirm the
new fixture fails under the old predicate and passes under the new one.

## Step 6: repair the existing hook-name extractor it would otherwise break

`tests/tmux-next38-readiness.bats`, the test "every hook the config registers with
set-hook -g is actually stored by tmux" (around line 404).

- [ ] Its extractor is
      `grep -oE 'set-hook -g ([A-Za-z-]+(\[[0-9]+\])?)' | awk '{print $3}'`. The
      character class accepts a leading hyphen, so `set-hook -g -B '@…'` yields the
      literal hook name `-B` and `set-hook -g -u -B '@…'` yields `-u`. The test then
      searches `show-hooks -g` output for those names and fails. Verified against
      the exact text Step 2 emits: the extractor returns `-B`, `-u` and
      `client-attached[20]`.
- [ ] Fix by anchoring the name to a letter:
      `([A-Za-z][A-Za-z-]*(\[[0-9]+\])?)`. Verified: the same input then returns
      only `client-attached[20]`.
- [ ] That fix makes the test skip monitor hooks entirely, which would silently
      shrink its coverage. Restore it by asserting the monitor hooks separately
      through `show-hooks -g -B` — the only listing form that prints a
      subscription (`show-hooks -g` prints the command alone; both measured). Keep
      the test's stated purpose intact: every hook the config registers must
      actually be stored.

Verify: `nix build .#checks.x86_64-linux.tmux-next38-readiness-tests`. Confirm it
is red before this step and green after, so the repair is known to be the thing
that fixed it.

## Step 7: a build-time assertion for the wiring

`flake.nix`, a new `tick-floor-conf-assertions`, modelled on
`default-size-conf-assertions` (`pkgs.runCommand` + `CONF = tmuxConfig.tmuxConf`).

- [ ] Assert each of the four `set-hook -g -B '@lztmux-…-tick::` specs is present
      and carries the matching store-path binary.
- [ ] Assert `set -g @lztmux_tick` is present.
- [ ] Assert **no** `-B '@` spec in the conf uses `:session:`. This is the guard
      against the `557967c3` regression and it is the highest-value line in the
      check.
- [ ] Assert the `status-format[0]` line carries no `--tick` and no `--backfill`.
      Isolate that one line first — a whole-file grep matches the hooks
      themselves.
- [ ] Assert the guard is present and is the string form, i.e. the `set-hook -g -B`
      text sits inside an `if-shell "…" "…"` and not inside `{ }`. A brace-block
      regression is invisible until someone reloads on an old server, which is
      exactly the kind of thing a build check should hold.
- [ ] Assert that every `#{` inside a `@lztmux-*-tick` hook **command** is a
      `#{q:` form. The command string is format-expanded twice before a shell sees
      it — once by `hooks_parse` (`hooks_monitor_hook_cb` passes `expand = 1`) and
      again by `run-shell` — so a bare format there is the injection class the
      quoting guard exists for, and Step 5 notwithstanding, a check that reads the
      emitted text directly cannot be blinded by a predicate. The sweep hook's
      `#{q:start_time}` is the only format any command carries, so this assertion
      is tight today and stays tight.
- [ ] Assert each hook has its `-u -B` clear, and that every clear precedes every
      setter in the emitted text — the ordering is what makes a disable take
      effect, so assert the order, not just the presence.
- [ ] Register the check beside its neighbours.

Verify: `nix build .#checks.x86_64-linux.tick-floor-conf-assertions`, then break
each assertion locally in turn and confirm it fails — the discipline
`tmux-format-delimiter-assertions` already follows.

## Step 8: a live test showing the freeze and the recovery

New `tests/tick-floor.bats`, plus a `tick-floor-tests` check in `flake.nix`
modelled on `tmux-next38-readiness-tests` (`TMUX_BIN = tmuxConfig.tmux-wrapped`,
isolated `HOME`/`XDG_*`).

- [ ] `nativeBuildInputs` must include `(mkTmux pkgs)` and `pkgs.gnugrep`: the
      `-B` guard shells out to `tmux list-commands set-hook | grep`, and
      `tmux-next38-readiness-tests` deliberately has no `tmux` on PATH, so
      copying that derivation verbatim would make the guard fail closed and the
      test would pass or fail for the wrong reason.
- [ ] Export `LAZYTMUX_ENRICH_CACHE_DIR` and `LAZYTMUX_AGENT_USAGE_DIR` into the
      test tmpdir **before** starting the server, so nothing touches the real
      `/tmp/lazytmux-*`. The hook's `run-shell` child inherits the server
      environment, which is what makes this work.
- [ ] **Recovery, zero clients:** start the wrapped server detached, attach
      nothing, and assert `.last-tick` and `.last-backfill-tick` both appear
      within a bounded wait. Both stamp unconditionally in tick mode
      (`scripts/tmux-pr-enrich.sh:474-486`, `scripts/tmux-issue-stamp.sh:62-71`),
      so the stamp is a sound assertion. `tmux-agent-usage` gates on an agent pane
      before stamping, so cover it by its hook registration instead, or give the
      server a pane whose `pane_current_command` is in the manifest.
- [ ] **Recovery, control-mode client:** the same with a control-mode client
      attached, which is the exact production shape on `halo`.
- [ ] **Sweep recovery:** assert the sweep actually ran with no client — the
      cheapest witness is `pipe-pane` arming on a fake agent pane, the way
      `tests/agent-detect-arm.bats` fakes one. Assert the artifact keeps advancing
      across a window long enough that a phase slip would show, not just that it
      happened once: a server whose ticks happen to land on epoch ≡ 0 mod 5 passes a
      single-shot assertion even with the `% 5` gate left in place, which is the
      defect Step 1 removes.
- [ ] **Upstream-assumption leg:** on a scratch server with a hand-written config
      carrying a marker `#()` job in `status-format[0]` and only a control-mode
      client, assert the marker never fires. Label it for what it is. It does **not**
      go red if this fix is reverted, so it is not a regression net for the change —
      the recovery legs above are. What it pins is the tmux behaviour the whole
      design rests on, so a future tmux that started expanding status formats for
      control clients would be noticed here rather than silently making the hooks
      redundant. Do not describe it as the net for the root cause.
- [ ] **That leg needs a positive control, or it is worthless.** "The marker never
      fired" is equally true if the marker path is wrong, the scratch config never
      loaded, the marker script is not executable, or the control client died on
      startup — it goes green against an empty file. So assert the same marker on the
      same scratch server **does** fire with a real client attached. That is the only
      thing separating "tmux does not expand the format for a control client" from
      "my marker never worked", and both halves are rows in the spec's own measured
      table. In-repo patterns: hold the control client open with `coproc` as
      `tests/tmux-next38-readiness.bats:128` does, rather than letting stdin EOF
      close it; give the real client a pty with `util-linux`'s `script`, as
      `tests/remote-auth.bats` does and the `remote-tests` derivation already
      provisions for.
- [ ] Apply the same discipline to the sweep leg: prove the artifact is writable in
      that environment before asserting it advances, or an unwritable directory reads
      as "never advanced" and fails for the wrong reason.
- [ ] The two legs must share one marker invocation, not two copies. Same scratch
      server, same config, same marker file, with the real client attaching and
      detaching between the two observations. If the positive leg writes its own
      marker path or its own config they stop being a control pair, and the negative
      assertion is back to proving only that its own setup worked.
- [ ] Assert all four hooks are registered, via `show-hooks -g -B` so the
      assertion covers the subscription and not merely the command string.

Verify: `nix build .#checks.x86_64-linux.tick-floor-tests`.

## Step 9: update the architecture docs

`CLAUDE.md`.

- [ ] Script Roles: change the Invocation cell for `tmux-pr-enrich`,
      `tmux-agent-usage` and `tmux-issue-stamp`'s `--backfill` from `#()` in
      status-format[0] to the monitor hook. In `tmux-update-icons`' row, note that
      the every-5th-tick sweep also runs from its own monitor hook so it survives
      a client-less server.
- [ ] "PR + Issue Enrichment" and "Agent Usage Limits": replace "background tick
      in status-format[0]" with the monitor hook, and give the one-line reason —
      a control-mode client renders no status line, so a status-driven poller
      never runs on a host whose only clients are bridges.
- [ ] Add a Key Conventions entry carrying the rule and both traps: a side effect
      must not be driven from `status-format` (`status_line_size` returns 0 for a
      control client, so the format is never expanded); a monitor spec's target
      field is **empty** for a session monitor, never the word `session`
      (upstream `557967c3`); and a version guard around `-B` must pass its body as
      a string, because a brace block is parsed at source time.
- [ ] Do not "fix" the unrelated stale rows in that table (`claude-status`,
      `tmux-branch-display`, `tmux-dir-display` still claim `#()` slots the Go
      statusline took over). Out of scope.

## Step 10: gate, demonstrate, ship

- [ ] `nix build .#default`
- [ ] `nix flake check` — read this one carefully rather than just checking the
      exit code. The floor now runs inside **every** wrapped-tmux test server: a
      headless `new-session -d` with no client has never expanded
      `status-format[0]`, which is the root cause itself, so these pollers have
      never executed under test before. The sweep hook in particular is
      unconditional. Named hazards to check against any new failure or flake:
      `pipe-pane` arming perturbing a suite that asserts on pane state (test panes
      run shells, so the manifest match should reject them — confirm rather than
      assume); `claude_reap_dead_panes` writing under `CLAUDE_STATUS_DIR`, which
      not every suite overrides; poller cache writes under the sandbox `/tmp`; and
      added wall-clock. If a suite does break, fix it as part of this change and
      say which — do not paper over it as a flake.
- [ ] `nix build .#lint`
- [ ] Run the before/after demo on private servers — isolated `TMUX_TMPDIR`,
      `XDG_DATA_HOME`, `HOME` and poller cache dirs, control-mode client only,
      never the user's server. The "before" leg is already captured: zero stamps
      in 25s. Capture the "after" leg and put both in the PR body.
- [ ] Rewrite the stale reason in `picker/remotebridge/daemon/ctl.go:330-333`.
      It currently justifies the `enrich-refresh` verb with "the remote's own
      tmux-pr-enrich has never run for a bridged session", which this change makes
      false. The verb stays useful for immediacy; only its reason changes.
- [ ] PR body: `Closes #603`, the falsified hypotheses, and three follow-ups —
      a defensive guard in `claude_reap_dead_panes` / `claude_prune_stale_state` so
      neither can delete state belonging to a server other than the caller's (Step 1
      isolates the known callers, but the functions stay trusting, and the blast
      radius is the whole local state tree);
      `tmux-remux`'s `@remux-save:session:` spelling (a separate repo), and
      whether `tmux-statusline`'s own staleness on a control-only host deserves
      anything at all.
- [ ] Commit the spec and this plan alongside the code (repo rule).

## Out of scope

- The pollers' own gate intervals and internal logic.
- Patching `tmux-remux`; report it instead.
