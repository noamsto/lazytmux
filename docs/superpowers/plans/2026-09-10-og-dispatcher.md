# Plan — `og` dispatcher (step 1 of the tmux-og migration)

Implements `docs/superpowers/specs/2026-09-10-og-dispatcher-design.md`.
Additive only: no existing script changes name, store path, or behaviour, and
this PR edits no existing line of the tmux wrapper.

**Naming note against the spec.** Decision 4 of the spec sketches `ogVerbs`
with `target = script.<name>`. This plan splits that into `ogVerbSpec` (the
human-authored table, keyed by verb tokens, naming its target by *script
name*) and `mkOg`, which takes **already-resolved** target paths so a check
can instantiate it over stubs. Same design, two names; the split is what makes
spec criterion 7 testable at all.

Two data-shape decisions are settled here so no step has to improvise them:

- **`ogVerbSpec` is keyed by verb tokens and names its target by *script
  name*,** not by derivation: `"remote open" = { script = "lztmux-remote-open";
  summary = "..."; }`. The exec path is then
  `"${script.<name>}/bin/<name>"`, which is correct because every target is a
  `writeShellScriptBin` (`$out/bin/<name>`), and the partition assert can read
  `v.script` directly instead of digging a name out of a derivation.
- **`mkOg` takes already-resolved targets** — `{ "<tokens>" = { target =
  "<absolute exec path>"; summary = "..."; }; }` — so a check can instantiate
  it over stubs. `og = mkOg (resolved ogVerbSpec)`.

Help order is `builtins.attrNames` order (Nix sorts keys), which already
groups nouns: `codex stamp`, `cursor *`, `debug`, `issue *`, `notify`,
`notify center`, `pick *`, `pr`, `remote *`, `status`, `status update`. The
renderer emits a blank line whenever the first token changes; no separate
ordering table is needed.

---

- [ ] **Step 1 — `scripts/og.sh`, the dispatcher.**
  Pure bash, tabs (shfmt), `set -euo pipefail`. Exactly one placeholder:
  `# shellcheck source=/dev/null` then `source @og_table@`, matching
  `scripts/tmux-update-icons.sh:8-11`. Pre-declare `OG_TARGET`, `OG_SUMMARY`
  (associative) and `OG_ORDER` (indexed) before the source so the file parses
  and lints standalone.
  Behaviour: longest-match over two tokens then one; unknown token → message
  on stderr, exit 2, empty stdout; `--help`/`-h` as the *sole* remaining
  argument → exit 0 without exec'ing, printing a **pinned two-line shape**:
  line 1 is `og <verb> — <summary>`, and the **last line is the absolute
  target path alone, nothing else on it**. Step 4 makes five assertions
  against this, extracting the path with `tail -1` and matching with
  `grep -qxF`, so the shape is a contract, not a formatting choice; bare `og`, `og help`, `og --help` and `og -h` → grouped listing, exit 0.
  The flag form with no verb is included deliberately: it is what most people
  type first, and without it the flags fall into the unknown-token branch and
  exit 2; a noun with sub-verbs and no one-token
  verb → that noun's verbs, exit 0; otherwise `exec` the target with `"$@"`
  verbatim.
  *Accept:* `bash -n`, `shellcheck` and `shfmt -d` all clean with the
  placeholder in place.

- [ ] **Step 2 — `ogVerbSpec`, `ogInternal`, `mkOg`, `og`, and the partition assert.**
  In `config/tmux.conf.nix`: add the 22-entry `ogVerbSpec` (verb tokens →
  `{ script; summary; }`) and the 19-entry `ogInternal` from the spec. Write
  the 22 summaries here — the spec's table carries verb/target/layer only. One
  summary is constrained by the spec: `og pr` must say it is a `--tick`
  background poller, not an interactive command, or the verb reads like
  something a user should run.
  `mkOg = verbs: ...` writes the table via `pkgs.writeText` (one
  `OG_TARGET[...]=` / `OG_SUMMARY[...]=` line per verb, plus `OG_ORDER=(...)`),
  then `writeShellScriptBin "og"` over `scripts/og.sh` with `@og_table@`
  replaced by that file's store path. `og = mkOg (resolved ogVerbSpec)`, where
  resolution maps each entry to `"${script.<name>}/bin/<name>"`.
  `og` is **not** added to `scriptNames`, and the `--prefix PATH` line
  (`:1296`) is **not** touched.

  **The assert goes on the returned attrset, not on `og`:**
  ```nix
  in
    assert ogPartitionOk;
    {inherit tmux-wrapped tmuxConf script og mkOg;}
  ```
  at `config/tmux.conf.nix:1300-1302`. Putting it on `og`'s own expression
  would be a silent no-op: `tmux-wrapped` never references `og` (that is the
  point of leaving `:1296` alone and keeping `og ∉ scriptNames`), so
  `nix build .#default` would never force it and the gate would look green
  while testing nothing. On the returned attrset, any attribute selection —
  including `tmuxConfig.tmux-wrapped` — forces it. Verified:
  `(assert false; { a = 1; }).a` does fail.

  Write the comparison as
  `builtins.sort builtins.lessThan (map (v: v.script) (lib.attrValues ogVerbSpec) ++ ogInternal)
   == builtins.sort builtins.lessThan scriptNames`.
  `builtins.sort` takes the **comparator first**; `sort xs == sort ys` would
  compare two partially-applied functions and could never pass. Use
  `lib.attrValues`, not a bare `attrValues` — this file has no top-level
  `with lib;` (only `meta = with lib;` at `:171`), so the unqualified form is
  an undefined variable. Bind the comparison as `ogPartitionOk` in the `let`
  so the name shows up in the assertion trace.

  Also add `og = tmuxConfig.og;` to `packages` in `flake.nix` in this step —
  it depends only on the return-set change made here, and without it there is
  no way to run the binary this step produces. `.#default` is
  `tmux-wrapped`, which by design contains no `og`.

  *Accept:* `nix build .#default` succeeds; `nix build .#og` then
  `./result/bin/og help` lists 22 verbs; and, as a negative test, deleting one
  entry from `ogInternal` makes **`nix build .#default`** (not `.#og`) fail
  with the assert message. The negative test is the only thing that proves the
  gate is wired to the right expression.

- [ ] **Step 3 — expose `og` on users' PATH.**
  `modules/home-manager.nix`: add `tmuxConfig.og` to `home.packages`
  unconditionally, beside `tmuxConfig.tmux-wrapped` and outside every
  `lib.optionals`.
  *Accept:* review-only, and say so — this repo has no home-manager module
  eval among its checks (`flake.nix:274`, `:285`, `:307`, `:329` only copy the
  module into sandboxes for text scans). Confirm by reading the diff that the
  addition sits outside every `lib.optionals`.

- [ ] **Step 4 — the `og-dispatch-assertions` check.**
  New check in `flake.nix` on the `*-conf-assertions` shape, with
  `nativeBuildInputs = [pkgs.gnugrep pkgs.coreutils]` (as
  `float-conf-assertions`, `flake.nix:454`), binding `OG = tmuxConfig.og` and
  `CONF = tmuxConfig.tmuxConf`.

  **The check never parses the generated table.** Every target assertion goes
  through `og <verb> --help`, which Decision 3 already makes print the target
  store path. That keeps the check reading the dispatcher's public behaviour
  rather than its private data file, and needs no `passthru`.

  `runCommand` runs under `set -e`, so every non-zero expectation needs an
  `if ! ...; then` or `rc=$?` guard rather than a bare invocation.

  Covers: bare `og` **and** `og help` both list all 22 verbs and exit 0;
  `og remote` prints just that noun's verbs and exits 0; every target printed
  by `--help` is `-x`; `og status update --help` and `og notify center --help`
  name the two-token targets; `og remote picker --help` prints exactly
  `${tmuxConfig.script.lztmux-remote-picker}/bin/lztmux-remote-picker`;
  `og remote open --help` exits 0 printing its target; `og bogus` exits 2 with
  empty stdout; `attrNames script` equals a pinned list of 41; and the `og`
  store path does **not** appear in `$CONF`.

  Passthrough uses a second instantiation:
  `tmuxConfig.mkOg { "t echo" = { target = "${stub}/bin/stub"; summary = "stub"; }; }`
  with `stub` a `writeShellScriptBin` running `echo "$#"` then
  `printf '[%s]\n' "$@"` — not a bare `echo "$@"`, which distinguishes a
  dropped empty argument only by a doubled space. Assert the count and the
  bracketed forms for a flag, a `--`, and an empty argument.
  *Accept:* `nix build .#checks.<system>.og-dispatch-assertions` passes.

- [ ] **Step 5 — amend the design doc.**
  In `docs/superpowers/specs/2026-09-09-tmux-og-rename-and-packaging-design.md`,
  replace the "Genuinely crosses a machine boundary" section per the spec's
  "Amend the design doc": state the ownership-qualified class, record the
  out-of-class POSIX/coreutils/init names, split into "crosses and this repo
  owns the name" (`lztmux-remote-picker`, `LZTMUX_RELAY_GRAPHICS`, the
  marketplace name, `tmux-startup.service` + its launchd label, and `tmux`
  itself) and "crosses, owned elsewhere" (`tmux-claude-images`,
  `theme-toggle`, `prdash`, `lazygit`, `yazi`, `tmux-remux`), and record the
  two-leg grep recipe over the seven-file surface.
  *Accept:* every cited path:line in the amendment resolves in the repo.

- [ ] **Step 6 — gates and PR.**
  Update `CLAUDE.md`: its "Script packaging" note says every `scripts/*.sh`
  becomes a store binary via `writeShellScriptBin`, which `mkOg` sidesteps,
  and the Script Roles table gains no `og` row. `og` is the new public front
  door, so both belong in this PR.
  Then `nix build .#default`, `nix flake check`, `nix build .#lint`, in that
  order, looped to green. Commit spec + plan + code together (repo rule). PR body
  carries `Refs #595` (**not** `Closes` — #595 is the four-step epic), a
  `## Plan` note is not needed since the plan phase ran, and any unresolved
  review findings go under `## Review notes`. Tick the step-1 checkbox in
  issue #595's body.
  *Accept:* all three gates green; PR open against `main`.

---

## Risks

- **`og.sh` must lint with the placeholder in place.** The inline
  `declare -A X=(@placeholder@)` shape was tried and raises `shellcheck`
  SC2190; the `source` form is the validated one. Step 1's accept criterion is
  exactly this.
- **`writeShellScriptBin` produces `$out/bin/<name>`.** Every path in the
  table must be `${drv}/bin/<name>`, not `${drv}`. A bare `${drv}` yields a
  directory and every verb fails at exec.
- **The partition assert must sit on the returned attrset.** An earlier draft
  of this plan offered "attach it to `og`'s own expression" as an alternative.
  That is wrong here and fails silently: nothing in `tmux-wrapped` references
  `og`, so `nix build .#default` would never force it. Step 2 mandates the
  placement and requires a negative test that proves it.
- **`builtins.sort` takes the comparator first.** `sort xs == sort ys`
  compares two functions and can never hold.
