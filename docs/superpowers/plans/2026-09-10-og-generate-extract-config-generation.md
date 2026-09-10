# Plan — step 2: extract config generation into `og generate`

Issue #595 (step 2 of 4). Design:
`docs/superpowers/specs/2026-09-10-og-generate-extract-config-generation-design.md`.
Decomposition:
`docs/superpowers/plans/2026-09-10-og-generate-extract-config-generation-decomposition.md`.

Constrained by that decomposition: every step maps to exactly one component,
step order respects its `ordering` graph, no step touches outside its
component's `boundaries` unless the deviation is justified inline, and
interfaces I1-I9 are preserved. The design carries the decisions; neither is
relitigated here.

Line numbers are `config/tmux.conf.nix` at `e79e925`.

## Verification vocabulary

- **build** — `nix build .#default`. Proves the wrapper still builds. It does
  **not** compare bytes.
- **check** — `nix flake check`.
- **lint** — `nix build .#lint`.

Only three instruments compare bytes: step 1's identity diff, the extraction
check from step 7 onward, and `tests/verify-extraction.sh` at step 15. Two paths
are outside all of them and need their own cover, called out where they arise:
`modules/home-manager.nix`, which no check imports, and store-path shape, which
only `verify-extraction.sh` sees.

---

- [ ] **Step 1: freeze `tmuxConfText` into a sectioned reference** (component `reference-freeze`) (implement: escalated)

  **Capture the oracle first, before touching anything.** `tmuxConf` is not a
  flake output at `e79e925`, so capture the base side the way
  `tests/test-display.sh:130` does:

  ```
  nix build .#default
  grep -o -- '-f /nix/store/[a-z0-9]*-tmux[.]conf' result/bin/tmux | head -1 | cut -d' ' -f2
  ```

  Copy that file into the session scratchpad as the oracle (not `/tmp`). After the move, rebuild and scrape
  again; the two must be byte-identical. That is the step's real gate — a
  section boundary changes what Nix de-indents per piece, and nothing else in
  the plan would catch it.

  Then move `tmuxConfText` (929-1581) and the bindings only it uses — `icons`,
  `enrichIconSet`, `bridgeGate`,
  `bridgeCtl`, `bridgeOpt`, `mkFloat`, `floatNewPaneGuard`, `floatBind`,
  `floatFull`/`floatShort`/`floatCard`, `bridgedFloatTool`, `carouselBind`,
  `prdashBind`, `carouselHooks`, `pluginConfigs`, `pluginRunShells`,
  `terminalConfig`, `defaultShellConfig`, `aiNamingFlag`, `resumeClaudeFlag`,
  `resumeCarouselFlag`, the tick-hook `let` block, `tmuxPlugins`, `catppuccin`
  — verbatim into `config/tmux.conf.reference.nix`, split into the four sections
  of I4 (`base` 929, `keys` 1002, `status` 1202, `hooks` 1371). Export
  `sections` (ordered `{name; text;}`) and `tmuxConf` = `writeText` of their
  concatenation.

  **`enrichIconDefaults` (:161) and `enrichIconSetRaw` (:232) stay in
  `config/tmux.conf.nix`.** `enrichIconSetRaw` has a live consumer that does not
  move — the `lib-enrich` substitution at :253-261 — so moving it leaves
  `undefined variable 'enrichIconSetRaw'` and step 1 fails its own build.
  DECOMPOSITION agrees: `:232` is outside `reference-freeze`'s may-touch ranges
  and `nix-serializer` claims that line explicitly. The reference therefore
  takes the two icon maps as **arguments** (`enrichIconsDoubled`,
  `enrichIconsRaw`) rather than re-deriving them; at this step
  `config/tmux.conf.nix` passes `enrichIconSet` and `enrichIconSetRaw`
  unchanged, so no byte moves.

  `config/tmux.conf.nix` imports it, gains `persistWireScript ? null` and
  forwards it, binds `tmuxConf` to the reference's output, and returns
  `referenceConf` **and the reference's ordered `sections`**. `sections` is an
  addition to I5's declared return set, and it is what step 7's hybrid render
  reads: the check's boundary is `flake.nix`'s checks block and it cannot import
  `config/tmux.conf.reference.nix` directly, since that file needs the script
  derivations, plugin paths and icon maps built inside `config/tmux.conf.nix`.
  Step 14 removes it again. The reference emits the persist block (I7) from
  `persistWireScript`, so step 8 changes no bytes.

  File header states the two-file rule and the step-3 removal condition.

  Capturing the oracle under default arguments only is sufficient: Nix's `''`
  de-indentation is a property of the literal source text, not of the option
  values interpolated into it, so a section boundary that de-indents correctly
  at defaults does so for every option set. Worth stating because the reference
  becomes the oracle for everything afterwards and nothing re-measures it
  against pre-move bytes again.

  *Acceptance:* the identity diff above is empty; **build**, **check**, **lint**.

  `git add` the new file before building — a flake's source is the git tree, so
  an untracked file is simply absent inside the store copy. The same applies to
  every new file in later steps.

- [ ] **Step 2: new `generator/` Go module and its Nix build** (component `generator-build`)

  `generator/go.mod` (`BurntSushi/toml` only), `generator/main.go` with the I6
  flag surface parsed but not implemented, `generator/default.nix`
  (`buildGoModule`, own `vendorHash`), and `packages.og-generate` in
  `flake.nix`. Nothing under `picker/` changes.

  **`--template` is a required flag with no default.** Do not inject a
  build-time default path here: `config/tmux.conf.tmpl` does not exist until
  step 6 and a Nix path literal to a missing file fails at instantiation. The
  default is **deferred, not dropped** — step 6 restores it once the file
  exists, because I6 declares `--template` optional and `og generate` on the
  user-facing surface (step 9) must run without it.

  *Acceptance:* `nix build .#og-generate` produces the binary; **build**,
  **check**, **lint**. Assert the picker derivation's store path is unchanged by
  comparing `nix eval --raw .#...picker outPath` before and after — this is the
  § Not perturbing existing derivations claim, and it is cheap to check here
  rather than discover at step 15.

- [ ] **Step 3: `generator/config` — schema, strict decode, VS16 rejection** (component `generator-config`)

  Go structs for I1 exactly, `platform` at top level, `process_icons` as the
  merged map. Strict decoding: a non-empty `toml.MetaData.Undecoded()` is an
  error. Enum validation for `copy_mode_line_numbers`, `picker.layout`,
  `splash.remote`. `*string` (not `string`) for `default_shell` and
  `terminal_term`, so absent stays distinguishable from empty. VS16 rejection
  reproducing I9's bytes exactly, names sorted and `, `-joined.

  *Acceptance:* `go test ./config/...` green, with fixtures covering a literal
  `#` in an override icon, a VS16 glyph, an unknown key, a bad enum value, and
  absent-versus-empty `default_shell`.

- [ ] **Step 4: `generator/paths` — the D3 resolver** (component `generator-paths`)

  Decode `paths.toml` per I2, or synthesize from `--prefix DIR`; one Go type
  either way. Required keys enumerated (25 scripts, 4 binaries, 5 plugin entry
  files, `bash`); a missing required key errors at resolve time, not render
  time. Optional keys (`persist_wire_script`, `carousel_toggle`,
  `carousel_aeye`, `prdash`) resolve to absent, never `""`. Plugin entry
  filenames compiled in for the `--prefix` layout.

  *Acceptance:* `go test ./paths/...` green for both modes, a missing required
  key, and each optional absent.

- [ ] **Step 5: `generator/render` skeleton** (component `generator-render`)

  The I3 data struct, `text/template` with `Option("missingkey=error")`,
  `--template` read at run time, and the `platform` → `copy-command`
  (`pbcopy`/`wl-copy`) mapping. No derived values yet — each lift adds its own.
  Wire `main.go` end to end: config + paths + template → `DIR/tmux.conf`, and
  nothing else written.

  *Acceptance:* `go test ./render/...` green; `og-generate` renders a one-line
  fixture template and writes exactly one file; a template naming an unknown key
  exits non-zero.

- [ ] **Step 6: the Nix-side serializers and the generator call** (component `nix-serializer`) (implement: escalated)

  `config/tmux.conf.nix` gains `configToml` (arguments → I1 through a real TOML
  encoder, never string concatenation; nulls omitted), `pathsToml` (I2, from the
  derivations it already builds), and the generator call. All three go on the
  returned attrset; the live `tmuxConf` still points at the reference.

  **Create `config/tmux.conf.tmpl` as an empty file in this step.** That crosses
  `lift-a`'s boundary and is justified: the `runCommand` needs a file to pass to
  `--template`, and the alternative — a generator call with no template until
  step 11 — leaves steps 6 and 7 unbuildable. The lifts fill it; they do not
  create it.

  **Preserve the store-path shape.** The generated conf must remain a store path
  to a regular file whose name ends `-tmux.conf`:

  ```nix
  generated = pkgs.runCommand "tmux.conf" {} ''
    mkdir -p out
    ${og-generate}/bin/og-generate --config ${configToml} --paths ${pathsToml} \
      --template ${../config/tmux.conf.tmpl} --out out
    cp out/tmux.conf $out
  '';
  ```

  `tests/test-display.sh:130` scrapes `-f /nix/store/[a-z0-9]*-tmux[.]conf` out
  of the wrapper, and a directory-shaped output would silently empty its `CONF`
  and stop testing the conf. `writeText "tmux.conf"` and
  `runCommand "tmux.conf"` with a file `$out` produce the same shape, so no
  consumer changes.

  **Restore the `--template` default now that the file exists** (I6):
  `generator/default.nix` bakes `${../config/tmux.conf.tmpl}` in, by substituted
  path or `-ldflags -X`. The generator call above and the check keep passing
  `--template` explicitly; the default exists for `og generate` run by hand.

  **Reach the generator without breaking the direct importers.** Use
  `pkgs.callPackage ../generator {}` inside `config/tmux.conf.nix`, or an
  argument with a default. A *required* new argument would break the four direct
  imports at `flake.nix:79, 504, 900, 1480` with `called without required
  argument`, and `extraction-check`'s must-not-touch forbids repairing them
  there.

  Hold the I5 one-authority invariant: each of the § Scope ten is one function
  argument read by both the serializer and the script builders, nothing
  re-derived. **Do not touch `enrichIconSetRaw` here** — the whole I8 dialect
  flip is atomic in step 8.

  Expected and self-correcting: between this step and step 8 the module still
  doubles `#`, so `configToml` on the module path carries `##` under
  `[enrich.icons]`. Nothing live or measured reads it — matrix entry 9 imports
  `config/tmux.conf.nix` directly with raw icons — so do not "fix" the
  serializer for it.

  *Acceptance:* **build**, **check**, **lint**; `nix build` of the `configToml`
  attribute yields TOML that `og-generate` decodes without error; the generated
  attribute's store path ends `-tmux.conf`, asserted as `[ -f "$conf" ]` inside
  the step-7 check rather than by eye — it is the one property step 10's
  shape-agnostic scrape deliberately cannot see.

- [ ] **Step 7: the extraction check, in hybrid mode** (component `extraction-check`)

  `checks.<system>.tmux-conf-extraction-assertions`: twelve matrix entries from
  the design's § The extraction check (thirteen renders — entry 8 varies
  `splashRemote` twice), each importing `config/tmux.conf.nix` and diffing the
  generated conf against `referenceConf`; the no-`/nix/store` assertion over
  those `config.toml` files; the `--prefix DIR` smoke; and the `grep -Fx` of the
  persist block's three literal lines on entry 10. Entry 10 passes a real store
  path as `persistWireScript` (any `writeShellScript` stub) so that grep means
  something.

  The check carries a hybrid cut parameter: cut *k* renders
  `go(template through section k) ++ concat(sections after k)`. At this step the
  cut is *none*.

  **Say plainly in the check's comment that cut none is nearly vacuous**: the
  candidate is `"" ++ concat(sections)` against an oracle of `concat(sections)`,
  so the diff compares the reference to itself, and the `--prefix` smoke renders
  an empty file. Only the `config.toml` store-path assertions bite.

  Add a positive control, **on the generated side**: put one stray byte in the
  otherwise-empty `config/tmux.conf.tmpl`, confirm the check goes red on every
  matrix entry, then remove it. Perturbing the *reference* cannot prove anything
  at cut none — both sides derive from the same `sections`, so an edit there
  moves candidate and oracle together and the diff stays empty. This control is
  what shows the generated prefix is actually diffed, which is this component's
  named failure mode.

  Do not touch the four existing direct imports at `flake.nix:79, 504, 900,
  1480`.

  *Acceptance:* **check** passes at cut none across all entries; the positive
  control goes red.

- [ ] **Step 8: the I8 icon-dialect flip and the persist edit, atomically** (component `hm-caller-edit`) (implement: escalated)

  Three edits that must land together, because each alone changes bytes:

  1. `modules/home-manager.nix:149` stops doubling `#`→`##` in `enrichIcons`.
  2. `config/tmux.conf.nix` takes over the doubling and computes both dialects
     from the now-raw `enrichIcons`, in the one place they are defined:
     `enrichIconsDoubled = enrichIconDefaults // (builtins.mapAttrs (_: v: builtins.replaceStrings ["#"] ["##"] v) enrichIcons)`
     (user-supplied keys only, per I8 — defaults are never doubled) and
     `enrichIconsRaw = enrichIconDefaults // enrichIcons`. `enrichIconSetRaw`
     (:232) becomes `enrichIconsRaw` and its `##`→`#` un-doubling is deleted in
     the same edit; the `lib-enrich` substitution (:253-261) reads it. The
     reference receives `enrichIconsDoubled` for `:1290` and `enrichIconsRaw`
     for the enrich card (`:1134-1138`), through the two arguments step 1 gave
     it.
  3. `modules/home-manager.nix`: delete `tmuxStateConf` (113-122), pass
     `extraConfText = cfg.extraConfig` and
     `persistWireScript = tmuxRemuxWireScript` (144), and fix the now-dangling
     "see tmuxStateConf above" comment at `modules/home-manager.nix:1518` — a
     comment-only excursion outside `hm-caller-edit`'s declared 60-200 range,
     noted because the decomposition is a hard constraint.

  This crosses `hm-caller-edit`'s boundary into `config/tmux.conf.nix` and
  `config/tmux.conf.reference.nix`. Justified: the reference is the extraction
  check's oracle, so it must keep emitting today's bytes; splitting the flip
  across steps emits a single `#` where today emits `##` for any user with a
  `#`-bearing override, in a window nothing measures.

  **Add a cover the gate can see.** No check imports
  `modules/home-manager.nix`, and matrix entry 9 imports
  `config/tmux.conf.nix` directly — so entry 9's raw `#`-bearing
  `enrichIcons` must additionally be asserted literally, I7-style: `##` at the
  `status-format[0]` site and a single `#` at the enrich-card site, both by
  `grep -F` on the generated conf.

  *Acceptance:* **build**, **check**, **lint**; the two literal icon greps pass
  on entry 9; the persist grep still passes on entry 10.

- [ ] **Step 9: the `og generate` verb** (component `og-generate-verb`)

  `ogVerbSpec."generate"` as a `target`-shaped entry. `ogPartitioned` (775) and
  `og`'s `mapAttrs` (801-805) each gain a `v ? script` branch.

  *Acceptance:* **check** — `og-dispatch-assertions` passes unchanged and the
  partition assertion still holds; `og generate --help` prints the target path.

- [ ] **Step 10: `tests/verify-extraction.sh`** (component `verify-extraction-script`)

  Build `.#default` at `e79e925` and at `HEAD` (git worktrees or `git archive`
  into temp dirs) and diff the two configs. **Scrape shape-agnostically** — `-f`
  followed by any non-space token, not the `-tmux[.]conf` pattern
  `tests/test-display.sh` uses — so the script cannot be broken by a future
  output-shape change it exists to detect. Abort before building if
  `flake.lock` differs between the revisions. Pinned to the SHA, not `main`.
  Bash, shfmt tabs.

  *Acceptance:* `shellcheck` clean; **lint**. Running it is step 15's job.

- [ ] **Step 11: lift A — base settings through prefix (929-1001)** (component `lift-a`)

  Fill section A of `config/tmux.conf.tmpl` and advance the check's cut
  parameter to A in the same step. Add to `render`: `DefaultShellConfig`,
  `TerminalConfig`, `PluginConfigs`, `PluginRunShells` (five plugin entry paths
  plus `bash`), `focus-follows-mouse`, `copy-command` from `platform`, and
  `prefix` at both sites.

  Byte contracts to verify by inspection of the rendered output, not only by the
  diff: `DefaultShellConfig` is `""` or ends with a newline plus four spaces, so
  the following `set -g history-limit` line is four-space indented whenever a
  shell is set; `TerminalConfig` behaves the same before the `*:hyperlinks`
  line; `PluginConfigs` ends `'off'` followed by a blank line.

  *Acceptance:* **check** at cut A, every matrix entry.

- [ ] **Step 12: lift B — keybinds through vim navigation (1002-1201)** (component `lift-b`)

  Section B; advance the cut to B. Add `bridgeGate`/`bridgeCtl`, `mkFloat` /
  `floatBind` / `floatNewPaneGuard` / `bridgedFloatTool` with `"`→`\"` escaping
  on the inner command only, `carouselBind`, `prdashBind`, the `splashEnable`
  bind, the `enrichEnable` float-card block (raw enrich icons, I8), the
  `notifyEnable` bind, and `is_vim`. `tmux-smart-nav` stays a bare name.

  Byte contract: `CarouselBind` and `PrdashBind` carry no trailing newline of
  their own — the template line supplies it, so an absent bind leaves exactly
  one empty line (matrix entry 6 is the cover).

  *Acceptance:* **check** at cut B, every matrix entry.

- [ ] **Step 13: lift C — window titles through the tick-hook block (1202-1370)** (component `lift-c`)

  Section C; advance the cut to C. Add the `@ai_naming` / `@resume_claude` /
  `@resume_carousel` flag strings, the `icons` constants, the picker and remote
  option lines, the `carousel-toggle` `@carousel_bin` conditional,
  `status-format[0]` (doubled enrich icons per I8, plus the inline
  `agentUsageEnable` argument group with the merged `process_icons` lookup and
  its emoji fallbacks), `status-format[1]` (13 `bridgeOpt` sites), and the
  tick-hook block — hook names in the fixed order pr/backfill/usage/sweep,
  clears before setters, joined by ` \; `, `"`→`\"` escaped, inside the
  string-form `if-shell`.

  Byte contract: every iteration over a map sorts its keys, because Nix sorted
  them.

  *Acceptance:* **check** at cut C, every matrix entry. Entry 12 is what proves
  the flag mappings are not inverted; a green run without it would not.

- [ ] **Step 14: lift D — hooks through the tail (1371-1581)** (component `lift-d`)

  Section D: the hook lines, the `splashEnable` and `notifyEnable` hook blocks,
  `carouselHooks`, the persist block from `persist_wire_script` (I7), and
  `extra_config` last. The template is now complete — remove the check's cut
  parameter and the reference's per-section exposure.

  *Acceptance:* **check** — whole-file diff, every matrix entry, no hybrid.

- [ ] **Step 15: switch the live path** (component `switchover`)

  Repoint `tmuxConf` from the reference's concatenation to the generated
  derivation from step 6 — the derivation itself, **not**
  `"${generated}/tmux.conf"`. That is a deliberate deviation from
  DECOMPOSITION § switchover's literal wording and an agreement with the same
  paragraph's requirement that `tmuxConf` be "a store path to a regular file":
  the directory form is what breaks `tests/test-display.sh:130`. Stated here as
  well as in step 6 so a reviewer does not restore it. `referenceConf` stays for the check; `configToml`
  stays exposed. The store-path shape is already correct (step 6), so
  `tests/test-display.sh`, the activation script and
  `xdg.configFile."tmux/tmux.conf".source` need no change — confirm rather than
  assume by running `tests/test-display.sh` after **build**.

  *Acceptance:* **build**, **check**, **lint**; `tests/test-display.sh` passes;
  `tests/verify-extraction.sh` reports an empty diff. That output goes in the PR
  body.

- [ ] **Step 16: commit the spec and the plan** (component `spec-and-plan-docs`)

  `SPEC.md` → `docs/superpowers/specs/2026-09-10-og-generate-extract-config-generation-design.md`
  (D4 table included), `PLAN.md` →
  `docs/superpowers/plans/2026-09-10-og-generate-extract-config-generation.md`,
  `DECOMPOSITION.md` committed beside the spec. Remove the scratch copies and
  `WORKER_TASK.md` from the worktree root.

  > **Amendment (applied during execution).** The decomposition landed in
  > `docs/superpowers/plans/` as
  > `2026-09-10-og-generate-extract-config-generation-decomposition.md`, not
  > beside the spec — every existing decomposition in this repo is under
  > `plans/` and none is under `specs/`, so "beside the spec" is read as
  > "in the same PR", not as a directory.

  *Acceptance:* **lint**; the files exist under `docs/superpowers/`.

  *Amendment (applied during execution):* the decomposition went to
  `docs/superpowers/plans/2026-09-10-og-generate-extract-config-generation-decomposition.md`,
  not beside the spec. Every existing decomposition in this repo lives in
  `plans/` and `specs/` holds only `-design.md`; "beside the spec" above was
  loose phrasing about committing them in one PR, not a directory choice.

---

## Order and parallelism

Steps 2-5 and 10 are parallel-safe after step 1 (disjoint files). Steps 9 and 10
are parallel to the lift chain; step 8 is not — it edits
`config/tmux.conf.reference.nix`, which the lifts diff against, so it lands
before step 11. Steps 11-14 are strictly sequential: each cut's hybrid render
assumes the previous cut's prefix is complete. Step 15 is last and is one
binding.

## What would falsify this plan mid-flight

- **Step 1's identity diff failing.** Sectioning at those four lines changes the
  de-indentation. Recut at a boundary whose surrounding lines are at column 0,
  or keep the reference unsectioned and lift in one step — which costs the
  hybrid check and makes steps 11-14 one unverifiable move, so recutting is
  strongly preferred.
- **`og-generate` not reachable from `config/tmux.conf.nix`** without recursion
  in step 6. It is a plain derivation dependency, so this should not arise; if
  it does, pass the generator in as a function argument from `flake.nix` the way
  the carousel and prdash packages already are.
- **The `runCommand`'s `$out`-as-file shape being rejected** by any consumer
  found at step 15. Then `tests/test-display.sh:130` and
  `tests/verify-extraction.sh` both need the shape-agnostic scrape, and step 15
  grows that edit.
