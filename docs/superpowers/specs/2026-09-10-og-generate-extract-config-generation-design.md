# Spec — step 2: extract config generation into `og generate`

Issue #595 (step 2 of 4). Design of record:
`docs/superpowers/specs/2026-09-09-tmux-og-rename-and-packaging-design.md`.
Step 1 (`og` dispatcher) merged as `fb310f5`.

## Problem

`config/tmux.conf.nix` is the only thing that can produce a tmux.conf. It is
1605 lines of Nix: a 653-line `pkgs.writeText` template with 114 interpolation
sites, ~15 derived string bindings and 7 inline conditionals, plus the
per-script substitution pipeline that feeds it store paths. A Homebrew formula
or a curl installer cannot call any of it, so tmux-og cannot ship off Nix.

Step 2 makes tmux.conf generation a callable implementation with Nix as one
caller.

## Non-goals

- `og init`, `og doctor`, Homebrew, the install script (step 4).
- The `lazytmux` → `tmux-og` rename (step 3).
- Any behaviour change for an existing Nix user.
- Moving the Go binaries' own build or their Nix-side codegen out of Nix.
- **Script substitution.** See § Scope of the seam — a costed deferral, not an
  omission.

## The gate, stated precisely

The tmux.conf *content* Nix produces after this change must be identical to the
content it produces before it, across the option matrix in § The extraction
check.

Two claims are involved and they are proven differently, because one of them a
flake check structurally cannot see:

| Claim | Proven by |
|---|---|
| Given identical inputs, the Go template + TOML round-trip renders exactly what the Nix template renders | `checks.<system>.tmux-conf-extraction-assertions`, over the matrix, every `nix flake check` |
| The inputs themselves are unchanged — no derivation whose store path lands in tmux.conf is perturbed | by construction (§ Not perturbing existing derivations), confirmed once by a recorded base-vs-HEAD diff |

The in-tree check renders both ways in the *same* checkout, so both sides
interpolate the same store paths and it cannot detect store-path drift. Saying
so is the point: an untested difference class is worse than a stated one. The
second row closes it — `tests/verify-extraction.sh` builds `.#default` at an
explicit base SHA (`e79e925`, this branch's merge-base) and at `HEAD`, scrapes
the `-f <path>` argument out of each `result/bin/tmux` wrapper, and diffs the
two files. It scrapes rather than reading a flake output because no output
exposes `tmuxConf` on the base revision. It is committed, re-runnable and
pinned to a SHA rather than to `main`, so it does not silently change meaning
when `main` moves. (`e79e925` is this branch's current tip, which is also its
merge-base with `main`; the pin is to the SHA, not to that relationship.) It
aborts loudly if `flake.lock` differs between the two revisions — a lock bump
would otherwise produce a non-empty diff for a reason unrelated to the
extraction and discredit the one check covering store-path drift. Its output is recorded in the PR body. It is not a flake
check because it needs two revisions of the tree, which a check cannot have.

### Not perturbing existing derivations

`picker/default.nix:59-60` builds from `cp -r ${lib.cleanSource ./.}`, so any
file added under `picker/` changes the picker store path, which appears in
tmux.conf. A `cleanSourceWith` filter could exclude a new directory and keep the
hash — but that is a foot-gun with no failure signal: a later contributor adds a
file, the filter does not cover it, and the config silently repoints. The
generator therefore lives in its own top-level Go module, which achieves the
same result with nothing to forget. That is what makes the base-vs-HEAD diff
come out empty rather than merely small.

## Scope of the seam

`og generate` owns **tmux.conf**. Script substitution stays in Nix in this step.

Moving it now would change every substituted script's derivation, hence every
one of the ~30 script store paths in tmux.conf, hence the config Nix produces —
the exact thing the gate protects. (The obstacle is the derivation change, not
import-from-derivation: a `runCommand` running the generator has an
input-addressed output path and needs no IFD. It is simply a *different*
input-addressed path than `writeShellScriptBin`'s.) Nothing in step 2 consumes
substituted scripts off Nix, so the cost of deferring is bounded and the cost of
not deferring is the gate.

### What that deferral costs, named

Enumerated from the substitution call sites in `config/tmux.conf.nix`, not from
the option list. Ten module options and one file-local constant cross into the
script pipeline that stays in Nix:

| Option | Placeholder(s) | Substituted by | Consumer |
|---|---|---|---|
| `processIcons` | `@ICON_MAP@` | `mkLib` (:201), `mkScriptFull`/`mkScriptIcons` (:409) | `lib-icons` + the icon scripts |
| `claudeStatus.assumeDeadAfter` | `@assume_dead_after@` | `mkLib` (:201-202) | `lib-claude` |
| `enrich.providers` | `@providers@` | `lib-enrich` (:235-262) | `lib-enrich` |
| `enrich.icons` (nine) | `@enrich_icon_*@` | `lib-enrich` (:238-262) — **not** `mkScriptEnrich`, whose from-list carries no icon placeholder | `lib-enrich` |
| `enrich.prRefreshSeconds` | `@pr_refresh_seconds@` | `mkScriptEnrich` (:448) | `tmux-pr-enrich` |
| `enrich.prCheckRefreshSeconds` | `@pr_check_refresh_seconds@` | `mkScriptEnrich` (:449) | `tmux-pr-enrich` |
| `enrich.enable` | gates `@issue_stamp@`'s value | `mkScriptIcons` (:423-433), `mkScriptReconcile` (:591-597) | `tmux-update-icons`, `tmux-reconcile-window` |
| `notifications.enable` | gates `notifyBin` (:549-552), substituted as `@notify@` | `mkScriptWithLog` (:298), `mkScriptEnrich` (:455) — `mkScriptNotify` substitutes `@lib_notify@`/`@lib_log@` only | the notification producers |
| `agentUsage.refreshSeconds` | `@refresh_seconds@` | `mkScriptAgentUsage` (:486) | `tmux-agent-usage` |
| `splash.remote` | `@splash_remote@` | `mkScriptSplash` (:274) | `tmux-splash-maybe` |
| *(constant)* `fallbackIcon` | `@FALLBACK_ICON@` | `mkLib` (:201) | `lib-icons` — a file-local constant (:138), not a module option, so it is **not** a `config.toml` key |

So after step 2, **`config.toml` is not authoritative for those ten options.**
The generator reads them (they also appear in tmux.conf); the Nix script
builders read them too.

The invariant that keeps that from being drift, and which the implementation
must hold: **on the Nix path both consumers are fed from the same typed option,
in the same expression, never from two.** `config/tmux.conf.nix` receives each
key once as a function argument and passes it both to the `config.toml`
serializer and to the script builders. One authority — the option — with two
serializations of it. That is a property of the code, not a convention, and step
4 closes it when script substitution follows tmux.conf across the seam.

Off Nix the honest statement is stronger: `og generate --prefix` produces a
tmux.conf naming scripts nothing in this repo can yet produce off Nix. The
`--prefix` mode is a real resolver, not a real install.

## Decisions

### D1 — `config.toml` is a generated intermediate on the Nix path

A Nix user never hand-authors it and never merges into it. The module keeps its
typed options; they serialize to a `config.toml` in the store, which the
generator consumes.

Reasons:

- Two authorable front doors need merge semantics (which wins per key, how a
  partial file combines with defaults). A whole feature with no beneficiary: a
  Nix user already has typed options with defaults, `mkIf`, assertions and
  evaluation-time type checking. The TOML would be strictly less expressive.
- Drift between the front doors is the failure this extraction exists to
  prevent. One authority per install path removes it by construction — subject
  to the ten-option qualification above, which is stated rather than buried.
- It stays debuggable without being authorable: materialized in the store and
  exposed as `tmuxConfig.configToml`, so a Nix user can read exactly what their
  options serialized to. Editing it does nothing; the store is read-only and the
  next rebuild overwrites it. That is the intent.

### D1a — the TOML value contract

`config.toml` carries **user-facing values in the user's own dialect**. Every
dialect transformation the Nix side performs today moves into the generator, so
an `og init`-authored file and a Nix-authored file cannot carry different
dialects for the same key.

| Key | Today | In `config.toml` | Generator's job |
|---|---|---|---|
| `enrich.icons` | `modules/home-manager.nix:149` doubles `#`→`##` **for user overrides only**; the defaults at `config/tmux.conf.nix:161-172` are never doubled, and `enrichIconSetRaw` (:232) un-doubles just the overrides back for the shell path | single `#`, as the user typed it, overrides only | merge overrides over raw defaults; emit the doubled form at tmux-format sites and the raw form at shell sites. **Double only keys the user supplied**, preserving today's rule exactly — widening it to the whole merged set happens to be byte-identical only because no default glyph contains `#`, and the extraction check cannot see the difference |
| `processIcons` | `config/tmux.conf.nix:123-137` **rejects** a VS16 (U+FE0F) icon with `builtins.throw`; nothing is stripped | raw, as the user typed it | reject a VS16-bearing icon with the same message. Neither path strips. Only `claude`, `codex` and `cursor-agent` reach tmux.conf (:1290, under `agentUsage.enable`); the rest reach only script substitution and the picker |
| `extraConfig` | `extraConfText = tmuxStateConf + cfg.extraConfig` (`modules/home-manager.nix:144`), where `tmuxStateConf` (:113-122) is a `run-shell` naming a store path | `extra_config` = `cfg.extraConfig` **only** | emit the persist block itself, from a resolver key |

No `/nix/store` path appears in the `config.toml` the **extraction matrix**
generates, and the check asserts that. It is deliberately *not* stated as an
invariant of `config.toml` in general: `extra_config` is a verbatim copy of
`cfg.extraConfig`, and a Nix user's `extraConfig` may legitimately interpolate a
store path. Promoting the assertion into the module would break those configs.

#### The persist split, concretely

This requires an interface change that is part of this step's work:

- `config/tmux.conf.nix` gains an argument `persistWireScript ? null` (a path or
  null) beside the existing `extraConfText`, and `extraConfText` narrows to mean
  user text only.
- `modules/home-manager.nix:144` stops concatenating: it passes
  `extraConfText = cfg.extraConfig` and
  `persistWireScript = tmuxRemuxWireScript` (already null when persist is off,
  `modules/home-manager.nix:94-97`).
- `tmuxStateConf` (`modules/home-manager.nix:113-122`) is deleted; its text
  moves into the generator, which emits exactly these bytes when
  `persist_wire_script` resolves and nothing when it does not — a leading blank
  line, the header comment, the `run-shell` line, one trailing newline:

  ```
  <blank>
  # === tmux-remux (Phase 2a, opt-in via programs.lazytmux.persist) ===
  run-shell "<persist_wire_script> #{q:version}"
  ```

  immediately before `extra_config`, which is the byte position the
  concatenation occupies today (template tail: `${carouselHooks}`, blank line,
  `${extraConfText}`).

The path goes in `paths.toml` because it is a path — D3's own rule. The
generator gaining knowledge of the persist block is deliberate: off Nix, a user
with tmux-remux installed needs that block emitted, and nothing else would emit
it.

### D2 — three consumers, not two buckets

"An option whose value is a derivation is system wiring" is true but
insufficient: an option can feed more than one consumer, and a binary rule
silently picks one. There are three consumers. An option is classified by the
set it feeds.

**tmux.conf only** (via `og generate`): `defaultShell`, `focusFollowsMouse`,
`copyModeLineNumbers`, `sixelTerminals`, `extraConfig`, `picker.*`,
`remote.hosts`, `remote.authPersistSeconds`, `agentUsage.enable`,
`agentUsage.monthlyThreshold`, `splash.enable`, `aiNaming.enable`, and the
persist-derived `resumeClaude` / `resumeCarousel` booleans.

**tmux.conf *and* script substitution** — the § Scope ten:
`claudeStatus.assumeDeadAfter`, `enrich.enable`, `enrich.providers`,
`enrich.icons`, `enrich.prRefreshSeconds`, `enrich.prCheckRefreshSeconds`,
`notifications.enable`, `agentUsage.refreshSeconds`, `splash.remote`, and
`processIcons` (which feeds all three).

**tmux.conf *and* a Go derivation's codegen**: `prefix` (`:999-1000`, threaded
at `:312` to `picker/default.nix:54`) and `processIcons` (`:1290` and
`picker/default.nix:18`). These are `config.toml` keys because the generator
genuinely needs them, and they stay Nix arguments to the picker derivation.

**A derivation's build input only** — never reaches the generator, therefore
**not a `config.toml` key at all**: `tmuxPackage`, `popupTools`,
`carouselDiagramTools`, `agentIntegration.*`, `persist.*` (its only tmux.conf
contribution is the resolver path in D1a), `worktrunk.*`, `skills`,
`startupSession.*` (including the `ghostty`/`kitty` emulator presets, which are
**not** option groups — they are `emulatorDefaults` internals,
`modules/home-manager.nix:18-37`), `opencode`/`codexStatus`/`cursorStatus` hook
installation, `remote.exposePickOnPath`, `splash.tips` and `splash.timeout`
(both baked into `tips_generated.go`, `picker/default.nix:45-57`).

`enable` is none of these: it gates whether the module produces anything.

**One generator input is not a module option at all.**
`config/tmux.conf.nix:991-995` branches on `pkgs.stdenv.hostPlatform.isDarwin`
to emit `copy-command 'pbcopy'` versus `'wl-copy'`, so it escapes both this
classification and D3's path enumeration. Nix writes it as a `platform` key in
`config.toml` (`"darwin"` / `"linux"`), taken from `pkgs.stdenv.hostPlatform`;
off Nix it defaults from `runtime.GOOS`. The generator does **not** derive it
from `runtime.GOOS` on the Nix path: the generator runs on the build platform,
which under cross-compilation is not the host platform the config is for. The
matrix cannot vary this — each CI system renders only its own branch — so it is
named here rather than covered.

The design doc's list is wrong in three ways, verified against the module: it
names `ghostty`/`kitty`, which are not option groups; it names 18 groups where
the module has 27 top-level entries (missing `enable`, `tmuxPackage`,
`processIcons`, `sixelTerminals`, `extraConfig`, `prefix`, `defaultShell`,
`focusFollowsMouse`, `copyModeLineNumbers`, `popupTools`,
`carouselDiagramTools`); and it moves `persist` wholesale, when only its wire
script crosses.

### D3 — the install prefix is a resolver with two modes

`og generate` never hard-codes where an executable lives:

```
og generate --config config.toml --paths paths.toml --out DIR   # Nix
og generate --config config.toml --prefix DIR       --out DIR   # everyone else
```

- `--paths FILE` is an explicit `name -> absolute path` map. Nix writes it,
  because Nix's paths are content-addressed and cannot be derived from a layout.
- `--prefix DIR` synthesizes the same map from a fixed layout
  (`DIR/bin/<name>`, `DIR/libexec/<name>`, `DIR/share/tmux-og/<plugin>/...`).

Both modes produce a value of the same type; everything downstream is identical.
The prefix is an input, not a build-time constant, and steps 3 and 4 inherit a
resolver rather than a convention.

The key set is fixed by what the template references and is enumerated, since
the `--prefix` acceptance criterion turns on it being complete:

- **Scripts** (~26 distinct `${script.<name>}` sites) and the four Go binaries
  the template actually names: `tmux-splash` (:1106), `tmux-enrich-card`
  (:1124), `lztmux-remote-bridge-ctl` (:337, :1127, :1255) and `tmux-statusline`
  (:1290). `tmux-picker-generate`, `agent-detect` and `carousel-aeye` appear
  only in script substitution, which this step defers, so they are not resolver
  keys yet.
- **Plugins**, individually: `catppuccin`, `better-mouse-mode`,
  `vim-tmux-navigator`, `tmux-fzf` (all four in `pluginRunShells`,
  `config/tmux.conf.nix:921-924`) and `fingers` (:1537). There is no which-key
  derivation in the tree despite CLAUDE.md's mention.
- **`bash`**, used to run catppuccin's `.tmux` (:921).
- **`persist_wire_script`** (D1a).
- **Optional third-party**: `carousel-toggle`, `carousel-aeye`, `prdash`. These
  resolve to absent, and the template's existing "leading `@` means disabled"
  idiom is preserved verbatim.

Store paths and install layout stay out of `config.toml` (D1a).

### D4 — guarantees that degrade off Nix, and where they get reported

Nothing degrades **in this step**: the Nix path guarantees everything it
guarantees today, because none of the wiring moves. This step names the set so
step 4 implements against a list rather than a rediscovery. The table is
committed under `docs/superpowers/specs/` for that reason.

| Guarantee (Nix does it) | Off-Nix status | Reported by |
|---|---|---|
| `remote.exposePickOnPath` puts `lztmux-remote-picker` on PATH | already self-reporting — its absence *is* the capability probe | existing "remote lazytmux too old" message; `og doctor` locally |
| `agentIntegration.tools` (prdash, lazygit, yazi) on PATH | check | `og doctor` |
| `popupTools` (btop, k9s) on PATH | check; k9s already self-reports in its bind | `og doctor` |
| `carouselDiagramTools`, `resvg` behind the carousel | check | `og doctor` |
| `persist.package` (tmux-remux) present | check | `og doctor` |
| `worktrunk` present and configured | check | `og doctor` |
| tmux plugins fetched and pinned | install-time dependency | Homebrew formula deps |
| `skills` symlinked into `~/.claude/skills` | not a generator concern — excluded by the emit-nothing-else constraint | out of scope |
| `resume_carousel = true` with no carousel viewer | Nix gates it on `carousel-aeye != null` (`modules/home-manager.nix:183`) because there is no store path to stamp; off Nix nothing gates it | `og doctor` |
| Values baked into Go codegen — the `splash.tips` / `splash.timeout` options, and the `fallbackIcon` / `maxIconsPicker` file-local constants | **not expressible in `config.toml` at all**; an off-Nix build gets the compiled-in values | closed by moving the codegen in a later step |
| `prefix` and `processIcons` (D2, dual-consumer) | honoured by tmux.conf; **ignored** by the off-Nix picker/splash binaries, whose copies are compiled in | `og doctor`; same fix as the row above |
| The § Scope ten script-substitution options | honoured in tmux.conf; absent from the shell libs, which have no off-Nix producer yet | closed by step 4 |

## Design

### Components

1. **`config/tmux.conf.tmpl`** — the template, lifted out of `tmuxConfText`. Go
   `text/template` with default delimiters: the file contains zero `{{`, zero
   Nix `''${` escapes and zero `'''` escapes (measured over the whole file).
   Executed with `Option("missingkey=error")` so an absent key fails loudly
   rather than rendering `<no value>` and surfacing as a late diff.
2. **`generator/`** — a new top-level Go module (own `go.mod`, own
   `vendorHash`) producing one binary, `og-generate`. Packages: `config` (TOML
   schema + decoder), `paths` (the D3 resolver, both modes), `render` (derived
   values and template execution). Justified by § Not perturbing existing
   derivations, not by dependency weight — `BurntSushi/toml` is already in
   `picker/go.mod`.
3. **`config/tmux.conf.nix`** — keeps its argument interface except for the D1a
   persist split. Four flake checks import it directly with varied arguments
   (`flake.nix:79, 504, 900, 1480`) and must keep working. Its body becomes:
   serialize arguments to `config.toml`, write `paths.toml` from the derivations
   it already builds, run `og-generate`. `tmuxConfText` moves out; it is not
   rewritten.
4. **`modules/home-manager.nix`** — the D1a edit at `:113-122` and `:144`.
5. **`config/tmux.conf.reference.nix`** — `tmuxConfText` frozen at this commit,
   reachable only from the extraction check.
6. **`og generate` verb** — dispatches to `og-generate`. No new file under
   `scripts/`, so step 1's partition assertion keeps its meaning; but
   `ogPartitioned` (`config/tmux.conf.nix:775`) and `og`'s `mapAttrs`
   (`:801-805`) both dereference `v.script` unconditionally, so both need a
   branch for a non-script target or evaluation fails.
7. **`tests/verify-extraction.sh`** — § The gate.

### Template lift hazards

- **Nix indented-string de-indentation.** A nested `''…''` has its own common
  indentation stripped *independently* of the outer literal before insertion, so
  its body lands at column 0. Go `text/template` has no such rule. This applies
  to the six multi-line `lib.optionalString X ''…''` sites
  (`config/tmux.conf.nix:1103, 1109, 1141, 1263, 1543, 1557`) **and** to every
  multi-line `''` binding interpolated into the template —
  `carouselHooks` (:888), `pluginConfigs` (:895), `pluginRunShells` (:920) and
  the tick-hook `let` block (:1359-1368).
  `:1290` is *not* one of these: it is an inline single-line
  `lib.optionalString` inside a double-quoted string and carries no indentation
  at all.
- **Trailing-indent single-liners.** `defaultShellConfig` (:819-821) and
  `terminalConfig` (:811-816) are double-quoted single-line expressions ending
  in a literal newline plus four spaces, and they are interpolated *mid-line*
  (`${defaultShellConfig}set -g history-limit 1500000`). Their trailing indent
  is what leaves the following directive indented four spaces in the emitted
  conf. `carouselBind` (:831-833) and `prdashBind` (:877-880) are likewise
  single-line double-quoted strings. None of the four is de-indented; the trap
  is the opposite of the one above.
- **Map ordering.** Nix sorts attribute keys; Go maps do not. Every site that
  iterates a map sorts explicitly.

### The extraction check

`checks.<system>.tmux-conf-extraction-assertions` renders both ways and diffs,
and asserts no `/nix/store` path appears in the generated `config.toml`.

The matrix is defined here, not inherited from the flake's existing imports —
those cover only defaults, `sixelTerminals`, and enrich+agentUsage off
(`flake.nix:79, 504, 900, 1480`), and never exercise the off branch of
`splashEnable`, `notifyEnable` or `carousel-toggle`/`prdash`, which are five of
the de-indent hazard sites. One entry per template conditional, each flag both
on and off:

| # | Varies |
|---|---|
| 1 | defaults (carousel + prdash present, everything on) |
| 2 | `splashEnable = false` |
| 3 | `notifyEnable = false` |
| 4 | `enrichEnable = false` |
| 5 | `agentUsageEnable = false` |
| 6 | `carousel-toggle = null`, `carousel-aeye = null`, `prdash = null` |
| 7 | `sixelTerminals = ["foot" "wezterm"]`, `terminalTerm = "xterm-ghostty"` |
| 8 | `splashRemote = "static"`, then `"skip"` |
| 9 | `enrichIcons` containing a literal `#`; `defaultShell` set; `copyModeLineNumbers = "hybrid"`; `focusFollowsMouse = true` |
| 10 | non-empty `extraConfText` **and** a resolving `persistWireScript` |
| 11 | everything off at once (the all-`false` shape) |
| 12 | everything on at once, including `aiNamingEnable = true` and `resumeCarouselEnable = true` |

Entry 12 is not decoration. The coverage rule is **every derived flag string is
rendered in both of its values**, and `aiNamingFlag` / `resumeCarouselFlag`
(`config/tmux.conf.nix:142-155`) are hand-transcribed bool→string mappings
(`"1"`/`"0"`, `"on"`/`"off"`) whose arguments both default to `false` (:76, :87).
Entries 1 and 11 therefore render the same string for each, and an inverted
mapping in the generator would be byte-identical in the only state the matrix
covered — shipping `@ai_naming "0"` to a user who enabled it. `resumeClaudeFlag`
escapes this only by luck, its default being `true` (:81).

A frozen reference generator is duplication, and it is the price of the gate
being mechanical. Two things bound it: the reference file's header states that a
tmux.conf change must land in both files until it is deleted, and its removal
condition is written down — it goes when step 3 lands.

### What is materialized where

`og generate --out DIR` writes `DIR/tmux.conf` and nothing else in this step. No
symlink, no installed binary, no unit or plist.

## Risks

- **Template lift fidelity.** 653 lines by hand is where a byte-level error
  hides. Mitigated by the extraction check being the acceptance criterion for
  every step of the lift, not a final gate.
- **Two serializations of ten (plus two dual-consumer) options.** Bounded by the
  one-authority invariant in § Scope, which the implementation must hold and the
  reviewer must check.
- **The persist split** touches `modules/home-manager.nix`, which no existing
  check exercises. Matrix entry 10 is the cover.
- **Duplication window** for the frozen reference: steps 2→3.

## Acceptance criteria

- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` all pass.
- [ ] `tmux-conf-extraction-assertions` passes over all 12 matrix entries and
      asserts no store path in `config.toml`.
- [ ] `tests/verify-extraction.sh` reports an empty diff between `e79e925`'s and
      `HEAD`'s built tmux.conf; that output is in the PR body.
- [ ] `config/tmux.conf.nix` no longer contains the tmux.conf text; the live
      path goes through `og-generate`.
- [ ] `tmuxConfig.configToml` is exposed and is a readable TOML file — D1's
      debuggability argument rests on it.
- [ ] `og generate --prefix DIR` renders a tmux.conf containing no `/nix/store`
      path, proving the resolver's second mode is real. It does **not** prove a
      working off-Nix install; scripts are still Nix-only (§ Scope).
- [ ] `og generate` rejects a VS16-bearing process icon with the same message
      Nix throws (D1a).
- [ ] Step 1's `og` partition assertion still passes, with `og generate` present
      as a non-script verb.
- [ ] Spec (including the D4 table) and plan committed under
      `docs/superpowers/`.
