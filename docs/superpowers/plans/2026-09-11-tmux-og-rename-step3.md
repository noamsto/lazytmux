# Plan — step 3: flip the project name to `tmux-og` (#595)

Executes step 3 of
`docs/superpowers/specs/2026-09-09-tmux-og-rename-and-packaging-design.md`: a
hard rename of `lazytmux`/`lztmux` to `tmux-og`/`og`, no aliases, no compat
window. Steps 1 and 2 (the `og` dispatcher, `og generate`) are merged as #607
and #611. Step 4 (Homebrew tap, install script, `og init`) is out of scope.

## Naming scheme

One prefix, `og`, so "did this literal get renamed?" is answerable by grep:

| today | after |
| --- | --- |
| `scripts/lztmux-*.sh`, `scripts/lazytmux-*.sh`, binaries `lztmux-*` | `scripts/og-*.sh`, binaries `og-*` |
| `LZTMUX_*`, `LAZYTMUX_*` | `OG_*` |
| `@lztmux_tick`, `@lztmux_theme_applied`, `@lztmux_carousel`, `@lztmux-*-tick` | `@og_*`, `@og-*-tick` |
| `/tmp/lztmux-*`, `/tmp/lazytmux-*`, `$XDG_STATE_HOME/lazytmux` | `/tmp/og-*`, `$XDG_STATE_HOME/og` |
| `programs.lazytmux` | `programs.tmux-og` |
| `github.com/noamsto/lazytmux/{picker,generator}` | `github.com/noamsto/tmux-og/{picker,generator}` |
| `pname = "lazytmux-go-tools"` | `"tmux-og-go-tools"` |
| marketplace `lazytmux@lazytmux` | `tmux-og@tmux-og` |

The project-free `tmux-*` and `claude-*` script families are untouched. That is
what makes the rename safe by construction rather than by enumeration: of every
name this repo owns that is sent to a remote host, exactly one carries a project
prefix (`lztmux-remote-picker`), and everything else there — `tmux`,
`tmux-startup.service`, `org.nix-community.home.tmux-startup`, `tmux-pr-enrich`
— is already project-free and must stay that way deliberately.

## Deliberate survivors

Five things keep a `lazytmux` literal on purpose. Each is allowlisted, with a
reason, and a grep over tracked files must return nothing else.

1. **cachix** — `lazytmux.cachix.org` and its cache-specific public key in
   `flake.nix` `nixConfig`, `README.md`, and `name: lazytmux` in the two
   workflows. Renaming the string does not rename the cache: it would point
   consumers at a cache that does not exist with a key that cannot match.
2. **The repo coordinate** — `noamsto/lazytmux` in install commands and in the
   five GitHub issue URLs in `docs/upstream-tmux.md`. Correct today, and correct
   after a repo rename too, by GitHub's redirect.
3. **`LZTMUX_RELAY_GRAPHICS`** — see *dual-publish the relay capability*.
4. **The four legacy `@lztmux-*-tick` names**, as clears only, in both
   `tickHookNames` and the reference's own `hookNames` — see *the monitor hooks*.
5. **The `lazytmux-managed:` codex marker spelling**, as a recognised
   alternative only — see *the home-manager namespace*.

Step numbers are deliberately absent from this list: it is the definition the
final allowlist grep is keyed to, and an earlier draft of this plan had all three
pointers off by one after a renumber.

Dated documents under `docs/superpowers/` are a record and stay as written.

## Steps

**Line numbers in these steps are locators, not addresses.** The monitor-hook
step adds four clear pairs to one `flake.nix` render and eight literals to the
other, so every citation into that file below shifts once it has run. Find things
by literal, never by line, after the first edit to a file.

Ordering follows `DECOMPOSITION.md`: step 1 fixes the basenames every other
component keys off by hand, then steps 2-11 are file-disjoint and parallel-safe,
then the gate. Nothing is independently revertable — the flip is one change set.

- [ ] **Step 1: rename the script files and their in-script literals.**
  `git mv` the nine project-prefixed scripts to `og-*.sh`
  (`remote-{open,picker,detach,auth,theme}`, `notify`, `notify-center`, and the
  two long-prefix ones, `lazytmux-debug.sh` → `og-debug.sh` and
  `lazytmux-log-event.sh` → `og-log-event.sh`). Then, across all of `scripts/`
  including the project-free families whose *names* stay, flip env vars, `/tmp`
  and `$XDG_STATE_HOME` paths, `@lztmux_*` options, self-names in usage and
  error text, the `command -v og-remote-bridge-*` fallbacks, the daemon socket
  name, the `og-pick` emit dir, and the `: og-probe;` marker. This subsumes the
  probe's own self-name and error text in `og-remote-picker.sh` — the `command -v`
  target, the per-user-profile fallback, and `remote tmux-og too old — rebuild
  <host>` — which is the one repo-owned project-prefixed name resolved *on the
  remote*, so verify it explicitly rather than trusting the sweep.
  Do not touch the bare `tmux` fallback path, the `tmux-startup` literals, or
  third-party names.
  **Carve-out:** leave the three cache-dir variables
  (`LAZYTMUX_ENRICH_CACHE_DIR`, `LAZYTMUX_AGENT_USAGE_DIR`,
  `LAZYTMUX_ENRICH_LOCK_DIR`) alone here. A blanket sweep over `scripts/` would
  flip five of their six readers and make the next step's "all sides together"
  claim false, which is the one ordering in this plan whose violation is
  destructive rather than red.

- [ ] **Step 2: the cache-dir triple, atomically, readers first.** (implement: escalated)
  **Three** variables, not two: `LAZYTMUX_ENRICH_CACHE_DIR`,
  `LAZYTMUX_AGENT_USAGE_DIR`, and `LAZYTMUX_ENRICH_LOCK_DIR`
  (`scripts/lib-enrich.sh:14`, default `/tmp/lazytmux-enrich-lock`, exported by
  `tests/issue-stamp.bats:11` and `tests/issue-backfill.bats:26`). The lock dir is
  **absent from the isolation check's `for var` list**, so its export flip has no
  guard at all — add it to that list while here, which is a small real improvement
  rather than scope creep: it is the same hazard the check already exists for.
  These have three sides that must agree, and splitting them is **destructive
  rather than red**.
  `wrapped-tmux-suite-isolation-assertions` (`flake.nix:1318-1330`) exists because
  the #603 tick hooks fire inside any server started from `tmux-wrapped` and
  reach functions that *delete* files under those trees, whose defaults are the
  developer's real `/tmp` paths.
  The three sides: the **six readers** (`scripts/lib-enrich.sh:11`,
  `scripts/tmux-agent-usage.sh:20`, its `-claude`/`-codex`/`-cursor` providers,
  and `picker/statusline/main.go:409`), **plus a seventh side that is a path
  rather than a variable**: `picker/statusline/usage.go:28`'s
  `const usageCacheDir = "/tmp/lazytmux-agent-usage"`, which is what
  `main.go:411` falls back to when the variable is unset — i.e. in production.
  Flip the variable and the shell defaults without it and the pollers write
  `/tmp/og-agent-usage` while the renderer reads `/tmp/lazytmux-agent-usage`: the
  usage segment silently disappears and nothing goes red, because
  `tests/agent-usage-gate.bats` exports the variable and so never exercises the
  const. This is the pair the spec tables under "each pair moves in one step".
  Then the bats `setup()` exports, and the check's own `for var in …` list.
  The directions are not symmetric, though neither is loud. Readers first, with
  the exports and the check lagging, is **safe but silent**: the check still
  passes, and safety comes from the reader falling back to the *fresh*
  `/tmp/og-*` path rather than from any alarm. Exports and check first, a reader
  lagging,
  makes the suites export a variable nothing reads: the readers fall back to
  `/tmp/lazytmux-pr` and `/tmp/lazytmux-agent-usage` and a local `bats` run
  deletes from the developer's real trees, while `nix flake check` stays green
  because the sandbox's `/tmp` is empty — precisely the condition that check was
  written to prevent.
  So: all sides in this one step, readers edited first, and **no bats suite
  is run outside the nix sandbox until this step has landed**. `nix flake check`
  is safe at every point; a direct `bats tests/foo.bats` is not. Escalated for
  the blast radius, not the difficulty.

- [ ] **Step 3: `LAZYTMUX_REQUIRE_TMUX`, writer and both readers together.**
  A third vacuous-pass trap, and nothing else in this plan owns it. The writer is
  `flake.nix:124`; the only readers are two **Go test** files,
  `picker/statusline/main_test.go:372` and
  `picker/remotebridge/cmd/daemon/main_test.go:35`, each doing
  `os.Getenv(...) != "" → t.Fatal`, else `t.Skip`. The flake comment at `:120-123`
  says what it is for: pruning the `nativeBuildInputs` tmux entry must break
  *loudly* rather than silently drop the #368 regression check. Flip the writer
  alone and both tests skip forever while `nix flake check` stays green, which is
  exactly the silence the flag exists to prevent. The decomposition files this
  under "flake → test harness", which misdirects to `tests/**`; the readers are in
  `picker/**`. Flip all three in one step and assert the `t.Fatal` message text
  moves with them.

- [ ] **Step 4: the Nix evaluation hub.**
  `config/tmux.conf.nix` (`scriptNames`, the four `scriptsWith*` lists,
  `ogInternal`, `ogVerbSpec.*.script`, every `script.<key>` and
  `picker-bridge-*-bin`, the `pathsToml.bin` key), `picker/default.nix` (`pname`
  and the four bridge `mv` targets — the five project-free targets and all `mv`
  *sources* stay), and `flake.nix` (store-path grep patterns, the monitor-hook
  strings, `OG_REQUIRE_TMUX`, the cache-dir hygiene loop, the `/opt/tmux-og`
  smoke prefix), **and the `og debug` verb summary at `config/tmux.conf.nix:665`
  ("Diagnose a lazytmux installation"), which is prose and easy to sweep past.**
  **Ownership:** this step does **not** touch the monitor-hook strings in either
  `flake.nix` render. *The monitor hooks* owns all of them, including
  `GUARD_JOIN`. Listing them in both steps is how the second one gets read as
  already done.
  Not escalated, despite being the evaluation hub: `ogPartitionOk` asserts set
  equality at eval time and the flake greps *are* the gate, so every mistake here
  fails loudly on the first build. Loud is the opposite of the escalation bar.
  Must not touch
  `nixConfig`, `meta.mainProgram = "tmux"`, the `$out/bin/tmux` wrapper, or the
  extraction check.

- [ ] **Step 5: the template, its oracle, and the generator, together.**
  `config/tmux.conf.tmpl` and `config/tmux.conf.reference.nix` edited in
  lockstep, plus all of `generator/` (module path and imports, `paths/paths.go`'s
  required script and bin lists, `render/status.go`, `render/keys.go`,
  `render/hooks.go`, and their tests). The extraction check diffs the render
  against the reference whole-file, so one string flipped in the template but
  not the reference is a red check with a 200-line delta.

- [ ] **Step 6: correct the reference's header.** It currently says it is
  "deleted when step 3 lands". It is not: the check it feeds is the only
  byte-identity oracle, and removing it is expressly not wanted. Rewrite the
  header to say it survives step 3 and the two-file rule survives with it.

- [ ] **Step 7: the monitor hooks — four setters under the new names, eight clears.** (implement: escalated)
  **Owns every monitor-hook literal in the repo**, across `generator/`,
  `flake.nix`, `config/`, `scripts/` and `tests/`. That deliberately spans four
  components of the decomposition, against its disjoint-boundary rule, and the
  justification is that the byte-identity check makes them one atomic string: a
  name flipped in the generator but not the reference is a red diff, and a name
  flipped in neither is an orphaned monitor. Splitting them by component is what
  creates the failure.
  Escalated because the failure modes are quiet and outlive the build: orphaned
  monitors firing every five seconds at garbage-collected store paths on the
  long-lived agent host, two known vacuous-pass traps, and a positional guard.
  `tickHookNames` (`generator/render/status.go:108`) drives **only the clears**;
  the four setters at `:144-153` carry inline literals. So: flip the four inline
  setter literals to `@og-*-tick`, and grow `tickHookNames` to eight entries —
  the four new names **first**, the four legacy ones last — rewriting its doc
  comment to say it is the clear list, not the set list. A rename reloads
  config but does not restart the tmux server, so without the legacy clears four
  orphaned monitors keep firing every five seconds at garbage-collected store
  paths, worst on the long-lived agent host. Mirror the eight clears in both
  `flake.nix` renders (`:1127-1144`, `:1266-1273`) and move `GUARD_JOIN` to
  `@og-pr-tick`, which is positional on the new ordering. Two test notes:
  `tests/tmux-next38-readiness.bats:448` must carry only the four **new** names,
  because its `grep -qF "'${name}::"` guard is setter-shaped and a legacy name
  there would `continue` every iteration and silently assert nothing; and
  `flake.nix:1228`'s `[ "$n" -eq 4 ]` is unaffected because `n` counts
  `\"`-wrapped `run-shell` payloads and a clear carries none. Emit the legacy
  clears **inside** the same `if-shell` body, not after it, or
  `tick-floor-conf-assertions`' `clear_max < setter_min` check fails — correctly
  but confusingly. The features-disabled render (`:1266-1273`, loop at `:1286`)
  needs three things: **sixteen** clear literals rather than eight, since the
  count is eight names times two forms (`CLEAR_B` and `CLEAR_OPT`), with its
  comment moved off "eight" too; its `PR_SETTER`/`BACKFILL_SETTER`/`USAGE_SETTER`
  literals renamed, because they are asserted **absent** and a legacy name there
  can never match, making the check unreachable — the same vacuous-pass defect as
  the next38 loop, only harder to see behind negative assertions; and
  `LZTMUX_TICK_SWEEP=1` flipped in lockstep across `render/status.go:153`,
  `scripts/tmux-update-icons.sh`, both flake tick checks, the reference, and
  `tests/agent-detect-arm.bats` plus `tests/agent-liveness.bats`. **Concretely, in `config/tmux.conf.reference.nix`** — the file the extraction
  check diffs against, so it mirrors the generator exactly and must move with it:
  the `hookNames` list at line 670 holds four entries feeding `clears` through
  `lib.concatMap`, and the four setters are separate inline `setHook` calls at
  lines 687-699. So `hookNames` grows to **eight** entries with the four legacy
  names **last**, and those four `setHook` literals flip to `@og-*-tick`,
  including the `LZTMUX_TICK_SWEEP=1` inside the sweep one. Leaving it at four
  against the generator's eight is a red extraction diff with a large delta — cheap
  to detect and cheap to get right, but only if it is instructed rather than left
  implied by "the reference is also a pinning site".
  `tests/tick-floor.bats` pins the four names too; it is the live-runtime proof,
  and being a bare `[[ $output == *"$name::"* ]]` with no `|| continue`, a stale
  name there fails **loudly** rather than vacuously.
  Flip the pre-3.8 fallback text too: `display-message 'lazytmux: tmux predates
  3.8 …'` at `render/status.go:156`, pinned at `status_test.go:67`. Criterion 1
  forces it and it is easy to miss, since it is prose rather than a hook name.

- [ ] **Step 8: the Go tools.**
  `picker/go.mod` module path and every import under `picker/**`; the `OG_*` env
  literals the daemon, renderer, ctl, picker and enrich card read;
  `@og_carousel`; the socket, cache and paste path literals; the bare-name
  binary defaults; both copies of the ctl error prefix
  (`cmd/ctl/main.go` `errorPrefix` and `enrichcard/model.go` `ctlErrorPrefix`,
  which no compiler relates); the `@@og-wall-` capture marker. The
  `OG_BRIDGE_TMUX` flag's default stays the bare string `tmux`, and the
  hard-coded per-user-profile `tmux` fallbacks stay.

- [ ] **Step 9: the home-manager namespace.** (implement: escalated)
  `cfg = config.programs.tmux-og` and `options.programs.tmux-og`; **both** the
  from- and to-paths of all five `mk{Renamed,Removed}OptionModule` entries, since
  an alias pointing at a namespace that no longer exists fails evaluation;
  assertion messages; `lazytmux:` stderr prefixes; the remux GC unit and timer
  names; `/tmp/og-startup.log`. Then the two codex markers: each twin greps for
  **both** spellings and acts on whichever it finds, a fresh append stamps
  `tmux-og-managed:`, an existing `lazytmux-managed:` block is recognised, left
  in place, and used as the `sed` anchor where one applies. The file is never
  rewritten to change a marker — the status-line twin's own comment records that
  codex hashes the config for hook trust, so a rewrite re-prompts on every host.
  Escalated because this step edits an activation script that writes a file the
  project explicitly does not own, and the failure mode is a duplicated hook
  block plus a trust re-prompt on every machine. Must not touch the
  `tmux-startup` service name or the `org.nix-community.home.tmux-startup` label.

- [ ] **Step 10: dual-publish the relay capability, atomically.** (implement: escalated)
  Measured: nothing in this repo reads `LZTMUX_RELAY_GRAPHICS`. The reader is the
  `aeye` input at the `flake.lock` rev `7504c3a`, whose `gallery.go:1149` calls
  `os.Getenv("LZTMUX_RELAY_GRAPHICS")`. A rename confined to this repo therefore
  breaks sixel relay over the bridge silently — no build or test fails. So
  `relayenv.go` publishes **both** names from the same `graphics.RelaySource`
  cell, and teardown unsets both. `RelayEnvCmd` and `RelayEnvUnsetCmd` each
  return both commands and `send` becomes variadic. That is wider than it sounds
  and the compiler catches all of it: `(*stream).send` (`daemon.go:489`) and
  `(*connHolder).send` (`conn.go:113`) widen, and because Go will not assign
  `func(string) bool` to `func(...string) bool`, so do the four production
  signatures that take `send` as a **parameter** — `watchLocalClient`
  (`daemon.go:302`), `submit` (`ctl.go:578`), `handleCtl` (`ctl.go:635`), `poll`
  (`carouselprobe.go:124`) — plus their test fakes (`daemon_test.go:311,544,615,659,715`,
  `setupwindow_test.go:154`, `seedfailure_test.go:105`, `deadrendererheal_test.go:30`)
  and the `RelayEnvCmd` call sites, which become spreads (`daemon.go:341,635,1150`,
  and `:782` for the unset). Reading one cell guarantees
  the two values agree, but only one batch guarantees both *landed*, and
  `bufio.Writer` latches its first write error, so two independent sends could
  leave a fresh new name beside a stale legacy one — the exact regression this
  exists to prevent. Escalated for that reason: it is the daemon's send path and
  the failure is invisible. Comment the removal condition: drop the legacy write
  once an `aeye` release reads `OG_RELAY_GRAPHICS` and `flake.lock` is bumped.

- [ ] **Step 11: the one Go fixture that cannot be renamed mechanically.**
  `TestFuzzyScore` (`picker/tui_test.go:370-383`) uses `"lazytmux"` as the
  *haystack* and draws its query literals from that word's letters: `ltx` must be
  a subsequence, `xyz` must not, and `lazy` (consecutive prefix) must outscore
  `lzyu` (scattered). Rename the haystack to `tmux-og` and `ltx`, `lazy` and
  `lzyu` are no longer subsequences at all — three assertions fail, so criterion 1
  and the "never loosen an assertion" rule collide here.
  Fix it by substituting a **project-neutral** haystack that preserves all four
  relationships, not by loosening the assertions. `"workspace"` is a candidate
  (`wkp` a subsequence, `xyz` not, `work` a consecutive prefix, `wrsc` scattered),
  but the prefix-beats-scatter inequality depends on `fuzzyScore`'s internals —
  **run the test, do not reason about it.**
  The remaining occurrences of the name in Go fixtures are ordinary renames with
  no semantics attached: `picker/zoxide_test.go:50,85,92,156,194`,
  `picker/tui_test.go:34-243`, `picker/wall_test.go:196`, and a comment at
  `picker/tui.go:755`. Note also `picker/agentdetect/manifest/testdata/*.txt`,
  where `lazytmux` is a *scraped repo name inside a captured window title* rather
  than a project reference — no manifest rule matches on it, so renaming is
  harmless and leaving it is correct; prefer leaving it and say so.

- [ ] **Step 12: the bats suites.** One trap first: `tests/picker-launcher.bats:113`
  asserts `run ! grep -Eq -- '(^| )-e LZTMUX_PICKER_CURRENT_SESSION'` — a
  **negated** grep, so left on the old name the pattern can never match and the
  negation always passes. That is the fourth vacuous shape in this rename, after
  the next38 guard, the disabled render's absent setters, and
  `LAZYTMUX_REQUIRE_TMUX`. Sweep for negated and absence-shaped assertions
  deliberately, not just for literals.
  **A fifth shape, found during execution and the subtlest yet.**
  `tests/codex-status-hooks.bats:55` asserts
  `[[ $output == *"MARKER='# lazytmux-managed: codex status-line hooks'"* ]]`.
  The home-manager module now carries
  `LEGACY_MARKER='# lazytmux-managed: codex status-line hooks'` for D10's
  both-spellings grep — and `LEGACY_MARKER=` **ends with** `MARKER=`, so that
  assertion still passes against the legacy line. Retyping the literal is not
  enough: **anchor** it so it matches the `MARKER=` assignment and not
  `LEGACY_MARKER=` — e.g. `grep -qE "^[[:space:]]*MARKER='# tmux-og-managed: ...'$"`
  over the extracted block rather than a substring glob. Unanchored, it pins the
  legacy spelling forever and stops guarding the real marker.
  Then: source paths, fake-bin names, env var names,
  option names, path fixtures, the `-tmux-og-go-tools-` store-path regex, the
  module-text greps, the error-text assertions, and the relay assertions (which
  gain the new variable beside the old). Every assertion that pins old text is
  updated to pin the new text, never loosened to a wildcard.

- [ ] **Step 13: plugin identity and the living documents.** Both manifests'
  `name` → `tmux-og`; `claude-plugin/README.md`'s install lines and skill
  names; `plugins/opencode-status.ts`; and the `programs.lazytmux.splash.*`
  header in the checked-in `picker/splash/tips_generated.go:3`, which
  `picker/default.nix` regenerates — both copies must agree.
  Then `README.md`, `CLAUDE.md` and
  `docs/upstream-tmux.md`, flipping `${inputs.lazytmux}` to `${inputs.tmux-og}`
  (a flake-input attribute the consumer names, so an example should show what a
  fresh flake would call it) while leaving the cachix lines and the repo
  coordinates alone.

- [ ] **Step 14: the gate, looped to green.** `nix build .#default`,
  `nix flake check`, `nix build .#lint` — none subsumes another, and
  `nix flake check` runs neither the formatter nor the other pre-commit hooks.

- [ ] **Step 15: the allowlist grep.**
  `git ls-files -z | xargs -0 rg -i 'lazytmux|lztmux'` returns only the five
  deliberate survivors above plus `docs/superpowers/`. Scoped to tracked files
  because the worker's own uncommitted artifacts would otherwise bury the signal.

## Acceptance criteria

- [ ] The probe resolves `og-remote-picker` on the remote and its error text
      names `tmux-og`.
- [ ] Both relay variables are published in one batch with equal values, and
      teardown unsets both.
- [ ] The wrapped binary is still `$out/bin/tmux` with
      `meta.mainProgram = "tmux"`.
- [ ] `tmux-startup.service` and `org.nix-community.home.tmux-startup` are
      unchanged.
- [ ] `programs.tmux-og` is the only option namespace.
- [ ] The generated `tmux.conf` carries four `@og-*-tick` setters and eight
      clears, inside one `if-shell` body; the disabled render carries sixteen
      clear literals and renamed setter literals; the next38 loop and
      `tests/tick-floor.bats` carry the four new names.
- [ ] All seven sides of the agent-usage path moved together, `usage.go:28`'s
      fallback const included, so the renderer reads where the pollers write.
- [ ] The reference's `hookNames` carries eight entries and its four setters are
      renamed, so the extraction diff is clean.
- [ ] The cache-dir triple landed in one step with the readers first, and no bats
      suite ran outside the nix sandbox before it did. The no-local-bats rule is
      what protects the developer's trees; the ordering only bounds the worst
      intermediate state.
- [ ] `tmux-conf-extraction-assertions` is green and still strict.
- [ ] All three gate commands green.
- [ ] `LAZYTMUX_REQUIRE_TMUX`'s writer and both Go-test readers moved together,
      so neither test silently skips.
- [ ] No assertion passes vacuously: the next38 guard, the disabled render's
      absent setters, and `tests/picker-launcher.bats:113`'s negated grep all
      still test what they name.
- [ ] The allowlist grep returns only the deliberate survivors, and the reference
      oracle's legacy tick names are among them.

## Out of scope, and named as follow-ups in the PR body

- Rebuilding any host. All three must be rebuilt in one sitting; until the last
  one lands, the bridge reports the capability probe as failing.
- `nix-config` needs a matching commit for `programs.tmux-og`, or its next
  rebuild fails to evaluate.
- An `aeye` release reading `OG_RELAY_GRAPHICS`, then a `flake.lock` bump, then
  dropping the legacy write.
- Renaming the GitHub repo and the cachix cache.
- Re-pinning `tests/verify-extraction.sh`'s `BASE` past this PR's merge. It is a
  manual cross-revision instrument, not in `nix flake check`, and a rename makes
  its diff non-empty by construction, so the pin cannot be advanced from inside
  the commit that causes it.
