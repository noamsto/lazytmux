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
- **Bounded retry** around discovery: the agent pane's relaunch runs
  asynchronously (fact 9). A small fixed number of attempts with a short sleep,
  no unbounded wait. On exhaustion: exit 0 without stamping, leaving today's
  bare shell (spec Design step 5) — never a partial stamp.
- **Key computation.** `key="<server pid>-<host pane id sans %>"`. Read the
  server pid from `$TMUX` (`<socket>,<pid>,<session>`) exactly as aeye's
  `resolve_target` does — no `tmux` fork needed. **Carry the condition-1 comment
  here** naming `aeye main.go:37` as the contract source, and say what breaks if
  it drifts (silent empty carousel).
- **Stamp its own pane**: `@claude_img_src="$key"` and `@claude_img_axis`
  (derive the axis the way aeye's `resolve_axis` does from the window dims, or
  fall back to `side` — matching aeye's own empty/non-positive fallback).
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
- **Builder wiring.** This script needs `@carousel_aeye@` substituted but not the
  icon set, so it does not belong in `scriptsWithIcons`/`mkScriptIcons`. Give it
  its own branch in the `script` dispatch chain (`config/tmux.conf.nix:560-570`).
  No cycle is introduced: `tmux-update-icons` → `tmux-carousel-restore` →
  `carousel-aeye` is acyclic, and unlike the `@reflow@` case flagged in
  `mkScriptIcons`'s comment, `tmux-carousel-restore` never references its own
  store path.
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
  viewer pane has no claude-status state file and never appears there. Reuse the
  `pane_cur_relaunch` map already populated at line 128 for change-gating.
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
- **Condition 1**: the computed key matches `<server pid>-<pane>`. Assert the
  shape, and prefer deriving the expectation from aeye's own
  `main.go:37` help string or a recorded fixture over a hand-copied literal.
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

**This is reachable — do not settle for a proxy.** Checked: `tmux-remux` is on
the user profile PATH, and while the bats check derivations pass explicit
`nativeBuildInputs` (flake.nix:180, 196, 206, …) so a sandboxed run would *not*
inherit it, the flake already declares `inputs.tmux-remux` (flake.nix:29-32) and
resolves it as `tmux-remux-pkg` (flake.nix:987). So the round-trip test gets its
own check derivation with `inputs.tmux-remux.packages.${pkgs.system}.default`
added to `nativeBuildInputs`, plus `XDG_DATA_HOME` pointed into the sandbox
(tmux-remux stores state at `$XDG_DATA_HOME/tmux-remux/state.db`).

The viewer binary is needed too — assert on the restored pane's
`pane_current_command` and `@claude_img_src`, which does not require the real
`aeye` to render; if the sandbox cannot supply `aeye`, stub it on PATH rather
than dropping the assertion. Only if the round-trip proves genuinely
unreachable does the fallback apply: assert what is reachable and **state the gap
explicitly in the PR body** — never leave an outcome criterion ticked by nothing.

## Step 6: `CLAUDE.md` + spec commit

- Add `tmux-carousel-restore` to the Script Roles table.
- Extend the Persist section: the carousel row, that the pane becomes the viewer
  at the new key, that aeye's `session-backfill` supplies the images, and the
  `AEYE_DIR` and two-agent-panes residuals.
- Commit the spec alongside the code (repo convention for a substantial change).

## Step 7: gate

`bats tests/` → `shellcheck` on both new/changed scripts → `nix build .#default`
→ `nix flake check` → `nix build .#lint` (the formatter/pre-commit set; the
CLAUDE.md warning that `nix flake check` does **not** cover it). Loop to green.
Baselines already recorded green before any change: `nix build .#default` exit 0,
`update-icons-resume-guard.bats` 6/6.
