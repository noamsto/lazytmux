# Plan — restore aeye carousel panes with their images (#577)

Spec of record: `docs/superpowers/specs/2026-09-08-carousel-remux-resume-design.md`
(revision 4). Fact numbers below refer to that document's "Verified facts".

Order matters: the script (step 1) is what step 2 stamps, and steps 3-4 are what
gate step 2 at all. Tests land with the code they cover, not in a trailing lump.

---

## Step 1: `scripts/tmux-carousel-restore.sh`

New script. Runs as the restored viewer pane's own command, so `$TMUX_PANE` is
the viewer's new id.

- `#!/usr/bin/env bash`, `set -euo pipefail`, following the header-comment style
  of `scripts/codex-relaunch-stamp.sh` (what it is, when it fires, why).
- No-op guards first, mirroring the stamp scripts: `[[ -n ${TMUX_PANE:-} ]]` and
  `command -v tmux`.
- **Resolve the viewer binary BEFORE anything is stamped, and exit 0 on a miss.**
  Ordering is load-bearing, not stylistic. Under `set -euo pipefail` a failed
  resolution aborts the script; if that abort lands *after* the
  `@claude_img_src` stamp, the pane is left running a bare shell while **marked
  as a viewer** — so `tmux-update-icons` re-stamps `@remux_relaunch` on the next
  tick and every later restore faithfully reproduces the same fake viewer. That
  is a self-perpetuating wrong state, strictly worse than today's bare shell.
  Resolution is `[[ -x ${AEYE_BIN:-@carousel_aeye@} ]]` (an absolute substituted
  path, so no `command -v` fork and no `-e` abort), and a miss exits 0 having
  stamped nothing — the same all-or-nothing rule as host discovery below.
  Also treat an *unsubstituted* `@carousel_aeye@` as a miss, matching
  `lib-claude.sh:77-79`'s "an unsubstituted placeholder disables it".
- **Host discovery.** One `tmux list-panes` in its own window,
  `-F '#{pane_index}|#{pane_id}|#{pane_current_command}'` (`|` delimiter — the
  repo rule; a tab collapses, see CLAUDE.md). Exclude `$TMUX_PANE`. Keep rows
  whose command is in the agent set. Pick the **lowest `pane_index`** among
  matches (spec condition 2 — a stated rule, so it is reproducible; sort on the
  index, do not rely on `list-panes` order).
- **Bounded retry, and the bound is load-bearing — two mechanisms make a
  command-name match legitimately miss for a while.** Neither is in the spec's
  fact base and both must be added there:

  1. **Scrollback replay masks the agent's command.** The
     `scrollback=yes relaunch=yes` startup form is
     `'<self>' cat-scrollback <sha>; <override>; exec <shell>`
     (`tmux-remux/internal/restore/startup.go:42-47`) — the prefix runs *first*,
     so while the host pane replays its stored scrollback its
     `pane_current_command` reads **`tmux-remux`**, not `claude`. Fact 7 quotes
     only the `<override>; exec <shell>` form and misses this.
  2. **A pane-0 viewer is briefly alone in its window.** `CreateWindow` is built
     from `firstPane` and the rest arrive as `SplitPane`s
     (`plan.go:160-199`), so if the carousel was pane 0 the window legitimately
     holds exactly one pane at the first tick.

  So: retry for an agent-command match on a **quantified** bound — poll every
  250ms up to ~15s, generous enough to outlast a scrollback replay — rather than
  "a small fixed number of attempts".

  **On expiry, fall back before giving up.** If exactly **one** non-self pane
  exists in the window, use it: during a replay or a pane-0 birth that pane *is*
  the host, and matching "not me" is immune to whatever command it currently
  shows. Only when the window holds several non-self panes and none matches an
  agent does the script exit 0 unstamped, leaving today's bare shell. Never a
  partial stamp, per the resolution-ordering rule above.
- **Key computation.** `key="<server pid>-<host pane id sans %>"`. Read the
  server pid from `$TMUX` (`<socket>,<pid>,<session>`) exactly as aeye's
  `resolve_target` does — no `tmux` fork needed. **Carry the condition-1 comment
  here** naming `aeye main.go:37` as the contract source, and say what breaks if
  it drifts (silent empty carousel).
- **Stamp its own pane**: `@claude_img_src="$key"`, and `@claude_img_axis=side`.
  Stamp the literal `side` — do **not** re-derive it. Mirroring aeye's
  `resolve_axis` (`tmux-claude-images.sh:26-44`) would also mean honouring
  `AEYE_SPLIT` and its `CELL_ASPECT` constant, i.e. a *fourth* duplication the
  spec's cost accounting never budgeted for. `side` is aeye's own documented
  fallback for empty/non-positive dims, so it is a value aeye already treats as
  valid, and the user's next `s` toggle corrects it.
- **Exec**: `export AEYE_HOST_PANE="$host"`, then `exec` the binary resolved in
  the first step, as `exec "$viewer" "$key"`.
  `AEYE_BIN` is a **test seam, not a user override** — document it that way
  (the `lib-claude.sh:77-79` / `AGENT_DETECT_BIN` phrasing). `update-environment`
  (`config/tmux.conf.nix:738-753`) carries only `TERM`, `TERM_PROGRAM`,
  `COLORTERM`, `TERMINFO`, `TERMINFO_DIRS`, `KITTY_LISTEN_ON`, `AEYE_HOST`, so a
  restored pane inherits the tmux server's environment and a user's shell
  `AEYE_BIN` can never reach it. It is worth keeping for bats, and it mirrors
  `tmux-claude-images.sh:548`, but a comment must not promise an override that
  cannot work.
- **Builder wiring — an explicit branch, or the placeholder ships raw.** The
  dispatch chain ends in `else mkScript name` (`config/tmux.conf.nix:584`), and
  `mkScript` performs **no substitution**: a script added only to `scriptNames`
  ships with a literal `@carousel_aeye@` in its body and the pane execs a
  nonexistent command. So give `tmux-carousel-restore` its own branch in the
  `:555-584` chain with a minimal builder substituting `@carousel_aeye@` only —
  `mkScriptWithLibs` / `mkScriptReconcile` / `mkScriptSplash` are the
  one-script-builder precedents.
- **Two ways to reintroduce the self-reference hazard, both to be avoided.**
  No cycle exists in the intended design — `tmux-update-icons` →
  `script.tmux-carousel-restore` → `carousel-aeye` terminates, because the last
  hop is an *input*, not a member of the recursive `script` attrset, exactly as
  `@reflow@` already resolves. But: (1) `@carousel_restore@` must go in
  `mkScriptIcons`'s **own** extension list (`:401-411`), **not** the shared
  `iconSubstFrom`/`iconSubstTo` (`:386-387`) which also feed `mkScriptFull`; and
  (2) `tmux-carousel-restore` must **not** be added to `scriptsWithIcons`
  (`:384`). Either would let the script substitute its own store path into
  itself, which is `infinite recursion encountered` at eval — the failure
  `:393-397` routes reflow around.
- `shellcheck` clean; `shfmt` with tabs (project default).

## Step 2: stamp from `scripts/tmux-update-icons.sh`

- Add `#{@claude_img_src}` to the batched `list-panes -s` format at line 161, as
  a **fixed middle field before the free-form `@window_task`** — the key is
  `|`-free by construction (digits, `-`). Add it to the `while IFS='|' read -r`
  variable list in the same commit, and extend the format's explanatory comment
  the way the existing fields are documented.
- Add `RESUME_CAROUSEL=${4:-}` beside `RESUME_CLAUDE=${2:-}` (~line 91), with the
  same comment shape. `${4:-}` so the bare `run-shell` invocation at
  `config/tmux.conf.nix:1240`, which passes only `$1`, means "off".
- **The stamped value comes from a new `@carousel_restore@` placeholder**, added
  to `mkScriptIcons`'s substitution list (`config/tmux.conf.nix:398-411`) beside
  `@reflow@`, resolving to
  `"${script.tmux-carousel-restore}/bin/tmux-carousel-restore"`.
  `tmux-update-icons` is dispatched to `mkScriptIcons` specifically (`:564-565`),
  and that builder exists for exactly this. **Do not stamp a bare name**: the
  comment at `:393-397` states the consequence — "a bare name resolves against
  the tmux server's frozen PATH and stays stale until a full server restart" —
  and the failure is silent, because the wrapper PATH at `:1296` makes a bare
  name resolve *today* and go stale only when the store path next moves. Not a
  `@carousel_bin`-style tmux global (`:1031`) and not a 5th argv; the placeholder
  is the established mechanism for a sibling script path.
- New stamp pass over the panes carrying a non-empty `@claude_img_src`, gated on
  `[[ $RESUME_CAROUSEL == on ]]`. **Not** inside the `claude_pane_ids()` loop — a
  viewer pane has no claude-status state file and never appears there.
- **Add a `pane_img_src` map** to the `declare -A` at line 105 and populate it in
  the batched loop beside `pane_cur_relaunch` (line 128). The loop-local read
  variable is not enough: the comment at `scripts/tmux-update-icons.sh:129-131`
  records that the EOF read blanks the read variables themselves, so without a
  map the stamp pass has nothing to iterate. Change-gate against the existing
  `pane_cur_relaunch` map.
- Write only on change, and use `tmux set -p` **without `-q`**, matching the
  Claude stamp's deliberate choice (a lost write must be loud — #373).
- Do **not** touch the Claude stamper. Its guard and the six existing
  `update-icons-resume-guard.bats` tests must pass unmodified.

## Step 3: `config/tmux.conf.nix`

- New input `resumeCarouselEnable ? false` (near `resumeClaudeEnable`, line 75)
  and a `resumeCarouselFlag` mirroring `resumeClaudeFlag` (line 136).
- `set -g @resume_carousel "${resumeCarouselFlag}"` beside line 993's
  `@resume_claude`.
- Pass it as the 4th argv to `tmux-update-icons` in `status-format[0]`
  (line 1053): `'#{@resume_carousel}'` after `'#{start_time}'`.
- Package `tmux-carousel-restore` in the script set (line ~346) so it gets a
  store path.
- **Viewer-binary resolution — use aeye's threaded store path, via a
  `@carousel_aeye@` placeholder in the restore script.** A bare `aeye` is wrong
  three times over:

  1. It is not on the tmux wrapper's PATH. `config/tmux.conf.nix:1296` adds
     `carousel-toggle`, never the viewer, and aeye's flake is explicit that the
     toggle does not re-export it: `toggle = writeShellApplication { runtimeInputs = [self'.packages.default]; }`
     with the comment "toggle only carries it internally and does not re-export
     it" (`aeye/flake.nix:110-114,121-124`). `runtimeInputs` sets PATH *inside*
     the generated script; its `$out/bin` holds only `tmux-claude-images`.
  2. It would therefore resolve only from the **user profile**, via
     `carouselDiagramTools` (`modules/home-manager.nix:829-846`, default
     `[carousel-aeye pkgs.resvg]`) — an option whose own docs say "Set to [] to
     opt out of diagram rendering". That would make carousel *restore* silently
     depend on a *diagram-rendering* option: a user setting `[]` gets a bare
     shell and no diagnostic.
  3. The GC argument runs the other way. A store-path reference to aeye *inside*
     the script makes aeye a registered runtime reference of the script's own
     derivation output, so it is retained exactly as long as the stamp's path is.
     The store path is strictly **more** GC-safe than a bare name, not less.

  **Threading sites — the real build goes through home-manager, not `flake.nix:82`:**
  - `modules/home-manager.nix:124-127` imports `tmux.conf.nix` with
    `inherit carousel-toggle;` and **no** `carousel-aeye`. This is the missing
    link and the site the user-facing build actually takes: `carousel-aeye` is
    *already* supplied to the module by `homeManagerModules.default`
    (`flake.nix:985-992`). Accept it as a module arg and add
    `inherit carousel-aeye;` here.
  - `config/tmux.conf.nix`: new `carousel-aeye ? null` input, and a
    `@carousel_aeye@` substitution for `tmux-carousel-restore` so the script
    holds the absolute path.
  - `flake.nix:82` (`perSystem`'s `tmuxConfig`, feeding `packages.default` and
    the checks) and `flake.nix:488` (`sixel-conf-assertions`) also need it, or
    the build and HM paths disagree.

  Threading *only* `flake.nix:82` would leave the home-manager path broken while
  `nix build .#default` and `nix flake check` both pass — the inverse of the
  hazard, and the reason to get the site right rather than the file.
- **Gate the emit on `carousel-aeye != null`**, not on `carousel-toggle != null`.
  The two are independent `? null` inputs, and `flake.nix:484-492`
  (`sixel-conf-assertions`) already imports with the toggle set and aeye unset —
  so a toggle-keyed gate emits the stamp while the viewer path is absent, which
  is step 1's self-perpetuating fake-viewer failure by construction. Gate on
  aeye (or on both, since a carousel needs the toggle to exist at all).

## Step 4: `modules/home-manager.nix`

- `programs.lazytmux.persist.resumeCarousel` option, `types.bool`,
  `default = false`, in the `persist` block beside `resumeCursor` (line 458).
  Docs in that block's established voice: what it stamps, that aeye's
  `session-backfill` supplies the images, that the pane becomes the viewer, the
  two-agent-panes ambiguity, and why it defaults false.
- `resumeCarouselEnable = cfg.persist.enable && cfg.persist.package != null && cfg.persist.resumeCarousel;`
  beside `resumeCursorEnable` (line 173), with the same "only when tmux-remux is
  installed to read it" comment.
- Thread it into the `tmux.conf.nix` import (line ~160) and add
  `tmuxConfig.script.tmux-carousel-restore` to the packages list under
  `lib.optionals resumeCarouselEnable` (mirroring line 1033-1034).

## Step 5: tests

New tests in `tests/update-icons-resume-guard.bats`, in that file's established
style — real script against the private config-less tmux server, `assert_relaunch`
for stamp assertions, and the `tmux-spy` PATH shim for "no write issued".

Mechanism:
- Viewer pane (`@claude_img_src` set) gets the carousel relaunch stamped.
- Second run over an unchanged pane issues no `set` (spy log).
- `RESUME_CAROUSEL` off / arg omitted → nothing stamped.
- A pane with no `@claude_img_src` is never stamped.
- **The six existing tests still pass unmodified** — the anti-clobber guarantee.
- The stamped value carries no `VAR=value` prefix and no shell metacharacters
  (fact 7a fish-safety), asserted on the value itself.

`tmux-carousel-restore` unit coverage (its own bats file, since it is a separate
script — follow `tests/enrich.bats`'s pure-logic pattern where the logic can be
sourced, else drive the script against the private server):
- **Condition 1 — a cross-repo pin, required, not a preference.** Asserting that
  *lazytmux's own* formula yields `<pid>-<pane>` is self-referential: it stays
  green when **aeye** changes its formula, which is exactly the silent breakage
  condition 1 exists to catch ("the carousel opens, finds nothing, and reads as
  an unrelated bug"). So the check must read aeye's side: grep
  `${inputs.aeye}/main.go` for the `main.go:37` contract string, and/or the
  runtime formula at `aeye/scripts/tmux-claude-images.sh:71-77`
  (`KEY="$srv-${PANE#%}"`), and fail when it no longer matches what this script
  computes.
  **Use `${inputs.aeye}`, never a local checkout path.** The guard derivation
  copies only `./scripts` and `./tests` (`flake.nix:683-693`), so a test reading
  `/home/noams/Data/git/noamsto/aeye` passes locally and fails `nix flake check`.
  `inputs.aeye` is already in scope where the check is defined.
- **Condition 2**: two agent panes in one window → lowest `pane_index` wins.
- Self-exclusion: the viewer's own pane is never chosen as host.
- Non-agent panes ignored; no host → exit 0, nothing stamped.
- **Unresolvable viewer → exit 0 with nothing stamped** (the B7 half-stamp
  hazard): `AEYE_BIN` pointed at a nonexistent path leaves `@claude_img_src`
  **unset** on that pane. Assert the option is unset, not merely that the script
  failed — the defect being pinned is a pane marked as a viewer while running a
  shell, which would otherwise be re-stamped every tick and reproduced on every
  later restore.

Outcome coverage (the spec's Outcome criteria, and the half the dispatcher
warned must not be quietly dropped): a snapshot/restore round-trip asserting the
restored pane runs the viewer, `@claude_img_src` holds the **new** key, the
window's pane count is unchanged, and the manifest at the new key carries the
pre-restore images.

**There is no "if unavailable" fork — the binary is available, so the downgrade
is not on the table.** An earlier draft of this plan made the round-trip
conditional on tmux-remux being present in the sandbox; that is a false
precondition (`inputs.tmux-remux` is declared at `flake.nix:29-32` and `inputs`
is in scope in `perSystem`, as `flake.nix:82` proves) and it handed the
implementer a free downgrade. Deleted deliberately. Note also that **no test in
this repo has ever executed the real `tmux-remux`** — every reference under
`tests/` is a stub (`tests/remote-cold-start.bats:82-83`), so this is new ground
and the derivation must be built, not assumed.

Split the coverage by what is actually reachable:

**(a) A real round-trip — outcome criteria 1, 3 and 6.** A new check derivation
(the existing `update-icons-resume-guard-tests` at `flake.nix:683-693` carries
only `[bats coreutils gnused git tmux]` and copies just `./scripts` and
`./tests`, so it cannot host this). Add
`inputs.tmux-remux.packages.${pkgs.system}.default` and `carousel-aeye` to
`nativeBuildInputs`, set `HOME` and `XDG_DATA_HOME` into the sandbox
(`state.db` lives at `$XDG_DATA_HOME/tmux-remux/state.db`), then assert on the
restored pane: `pane_current_command` is the viewer, `@claude_img_src` holds the
**new** `<srv>-<host>` key, and the window's pane count is unchanged.

**(b) The manifest half, decoupled — outcome criterion 2.** Invoke
`session-backfill.sh` directly against a transcript fixture and assert the
manifest lands at the new key. This *pins* fact 3 rather than trusting it, which
is worth more than the round-trip would have been. Reach it via
`${inputs.aeye}/adapters/claude-code/plugin/scripts/`, not a local checkout, for
the same sandbox reason as condition 1.

**Injecting a stub viewer.** Step 3 threads the absolute path via
`@carousel_aeye@`, so the script never consults PATH for the viewer and a PATH
stub would be inert. Inject through `AEYE_BIN` (the documented test seam) or by
substituting `@carousel_aeye@` with the stub path in the test's own `sed` pass —
the established pattern at `tests/update-icons-resume-guard.bats:45-52`, which
already seds `@lib_icons@` / `@lib_claude@` / `@reflow@` / `@MAX_ICONS@`.

**Genuinely unreachable, and to be disclosed rather than faked:** "shows the
images it had" **as an end-to-end round-trip**. It requires
`session-backfill.sh` to fire as a Claude Code **SessionStart** hook, and nothing
in a nix sandbox fires one. So criterion 2 is covered by (b) at the seam, and the
PR body must say plainly that the full agent-restore-to-images path is verified by
composition — (a) for the viewer, (b) for the manifest — plus hardware
verification, not by a single automated test. That is a stated gap, not a ticked
box.

## Step 6: `CLAUDE.md` + docs commit

- **Script Roles row** — the table carries *invocation* plus the non-obvious
  mechanism, never a description. For this row that is: invoked as the restored
  viewer pane's own command via `@remux_relaunch`; it `exec`s the viewer so the
  pane **becomes** it (no split, no kill, so remux's pane count and
  `select-layout` are untouched); host discovery is a command match with a
  sole-non-self-sibling fallback.
- **A `Key Conventions` bullet for `@claude_img_src`** — `tmux-update-icons`
  reads it as a boolean "is this a viewer pane". Worth stating because it makes
  lazytmux a second reader of an option aeye owns and writes.
- Extend the Persist section: the carousel row, that the pane becomes the viewer
  at the new key, that aeye's `session-backfill` supplies the images, and the
  `AEYE_DIR` and two-agent-panes residuals.
- **Commit BOTH the spec and this plan** alongside the code — CLAUDE.md
  §"Plans and Specs" requires the plan document too, and this plan lives at
  `docs/superpowers/plans/2026-09-08-carousel-remux-resume.md` for that reason.
  It carries four refuted designs' worth of history; losing it to a reclaimed
  worktree would be a real cost.

## Step 7: gate, then the PR body

`bats tests/` → `shellcheck` on both new/changed scripts → `nix build .#default`
→ `nix flake check` → `nix build .#lint` (the formatter/pre-commit set; the
CLAUDE.md warning that `nix flake check` does **not** cover it). Loop to green.
Baselines already recorded green before any change: `nix build .#default` exit 0,
`update-icons-resume-guard.bats` 6/6.

- **Never pipe the gate.** `nix flake check | tail` reports `tail`'s status, so a
  run that exited 1 with a failure and a `(cancelled)` reads green. Redirect to a
  file, read the bare exit status, and treat `cancelled` as a tell.
- **Commit from inside the devShell.** The `.pre-commit-config.yaml` symlink is
  generated by the devShell `shellHook` and is not checked in, so a commit from a
  worktree that never loaded it fails outright — and `PRE_COMMIT_ALLOW_NO_CONFIG=1`
  skips `shfmt`/`shellcheck` entirely, which are the two hooks a new shell script
  most needs. (direnv is loaded in this worktree; the earlier doc commits ran the
  full hook set, so this is already satisfied.)

**PR body must state, in prose** (dispatcher conditions, not optional):

1. **The ambiguity residual** — a window holding two agent panes resolves to the
   lowest pane index, and guessing wrong keys the carousel to the wrong sibling,
   recoverable with one `prefix + I`. CLAUDE.md is not a substitute: condition 2
   says "not only here".
2. **The verification gap** — the full agent-restore-to-images path is verified
   by composition (round-trip for viewer/key/pane-count, a direct
   `session-backfill.sh` test for the manifest) plus hardware, not by one
   end-to-end test, because nothing in a nix sandbox fires a Claude Code
   SessionStart hook.
3. **The accepted duplication** — lazytmux now carries aeye's key formula, pinned
   by a cross-repo test.
4. **Why the spec revision cap was passed**, and that the critic's stale-read
   claim was checked and did not change the outcome.
