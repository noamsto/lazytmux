# `og init` and `og doctor`

Plan for issue #595 (second half): the CLI surface that makes a non-Nix
install of tmux-og usable (`og init`) and diagnosable (`og doctor`). See
`docs/superpowers/specs/2026-09-09-tmux-og-rename-and-packaging-design.md`
("Proposed CLI surface", "Guarantees downgrade to checks") for the design
context. `og generate`/`paths.FromPrefix` (step 2) and the `tmux-og` rename
(step 3) are already merged and are not touched here. The Homebrew tap and
`install.sh` are a separate, later PR.

Revised after two adversarial plan-critic passes (pass 1: revise, 6 blocking
findings; pass 2, on the revision: revise-narrow, 1 blocking finding — the
critic's own assessment was that it was safe to apply directly rather than
spend the plan's second and final revision round on a re-review, so that fix
(B7) is folded in here without a third pass). Every blocking finding is
folded into the Decisions/Steps below; each is marked `[critic]` at the point
it's addressed so the reasoning stays attached to the risk.

## Decisions

**Where `og init` writes.** `${XDG_CONFIG_HOME:-$HOME/.config}/tmux-og/config.toml`,
spelled explicitly rather than via `os.UserConfigDir()` `[critic]` — the
stdlib helper returns `~/Library/Application Support/tmux-og` on darwin,
which is in the CI matrix, and every other XDG consumer in this repo
(`scripts/lib-log.sh`, `scripts/lib-claude.sh`) spells the Linux-convention
fallback explicitly rather than deferring to platform convention. This repo
already uses exactly the `$XDG_CONFIG_HOME/tmux-og/...` shape for `state.toml`
(`docs/superpowers/specs/2026-04-26-tmux-state-store-design.md`, an
unimplemented spec under the pre-rename name — not a live precedent, but the
same shape). Overridable with `--out FILE`. `og doctor` defaults its own
`--config` to this same path, so a bare `og init` then `og doctor` composes
with no flags — see the Nix-wiring note below for why that default alone
isn't enough on the Nix path.

**Overwrite policy: refuse, not `.new`.** `og init` exits 1 with a message
telling the user to pass `--force` if the target file already exists.
Reasoning: `install.sh` (the next PR) calls `og init` on every install and
upgrade per the task doc — the idempotent, safe behavior for a repeatable
installer is "leave my hand-edited config alone," not "drop a second file the
installer has to reconcile." A `.new` file just moves the data-loss risk to
whichever script forgets to diff it.

**What "detected defaults" means, precisely.** `generator/config/config.go` is
authoritative on *which fields exist*, but it is not authoritative on every
field's *default value* — some fields have no code-level fallback at all
because on the Nix path `modules/home-manager.nix` supplies one and the
generator was never asked to. Three tiers, re-derived from grepping
`config/tmux.conf.tmpl` for every unconditional `{{.Config....}}` site and
verified against downstream consumers, not just the template
(`generator/paths` and `generator/render` are not touched):

1. **Host-detected, written live:** `platform`. `config.Defaults()` (new,
   §1) reuses `Config.applyDefaults`'s existing `runtime.GOOS` fallback, so
   `og init` and `og generate` agree by construction. Commented to say it's
   re-detected at generate time if the line is removed — a config.toml
   authored on one host and copied to another shouldn't pin the wrong OS.
2. **No code-level default, rendered unconditionally, and an unsafe zero
   value:** exactly `tmux.prefix` and `remote.auth_persist_seconds`
   `[critic: B1, B2]`. The unconditional `{{.Config.*}}` sites, enumerated in
   full from the template (`config/tmux.conf.tmpl:63-64,86,288-291,304,582`):
   `prefix` (no fallback; `0`-length renders `set-option -g prefix ` and
   `bind  send-prefix` — both malformed), `copy_mode_line_numbers` (has a
   code-level default, safe to omit), `zoxide_exclude` (empty is a valid "no
   exclusions" state), `list_ratio` (no fallback, but `picker/tui.go`'s own
   `min(max(r,20),80)` clamp turns `0` into `20` — self-healing, not unsafe;
   dropped from this tier), `layout` (code-level default), `hosts` (empty is
   a valid "no remotes" state), `auth_persist_seconds` (no fallback; `0`
   feeds `ssh -o ControlPersist=0` in `scripts/og-remote-auth.sh` and
   `scripts/og-remote-picker.sh`, reaping the ControlMaster the moment its
   probe command exits — the handshake those scripts exist for is defeated
   silently), `extra_config` (empty is correct — no extra config). Written
   live with the same values `modules/home-manager.nix` defaults to
   (backtick, `14400`), each commented as `og init`'s own default because
   `config.go` has none — the one hand-maintained pair in the file, and both
   the write-site comment *and* this doc name them explicitly so a future
   template change that adds a fallback (or changes which fields are
   unconditional) is a prompt to revisit the set, not a silent staleness.
3. **Everything else:** written commented-out, showing the value
   `config.Defaults()` (schema defaults + Go zero values) actually produces.
   This is deliberately not a guess — a bool defaults `false`, an int `0`, a
   string `""`, matching what `config.Load` would do if the line were simply
   absent. Two sub-cases get an extra line of comment beyond the generic
   "default: X", because a value existing in the schema doesn't mean editing
   it does anything off-Nix yet:
   - `process_icons`: real defaults live only in `config/process-icons.nix`
     ("the defaults have exactly one home, on the Nix side" —
     `generator/config/config.go`'s own doc comment), so duplicating them
     into Go would be exactly the drift the task warns against. The comment
     names this as a known non-Nix limitation and points at
     `config/process-icons.nix` rather than presenting an empty table as
     simply "the default" `[critic: note 5]` — this repo already has
     precedent for codegen'ing Nix-only data into Go
     (`picker/splash/tips_generated.go`), so a future PR has a documented
     path to close this rather than a silent gap.
   - `enrich.providers`, `enrich.pr_refresh_seconds`,
     `enrich.pr_check_refresh_seconds`, `agent_usage.refresh_seconds`,
     `claude_status.assume_dead_after`: decoded and validated by
     `generator/config`, but `generator/render` never reads them — on the
     Nix path these are baked into scripts via `replaceStrings`
     (`config/tmux.conf.nix:155,188,222,371`), which `og generate` does not
     do `[critic: note 4]`. Each of these keys' comment says "not yet wired
     into `og generate`'s output off-Nix" so a non-Nix user editing one
     doesn't spend a debugging session on a setting that can't take effect.
     This is a pre-existing generator gap, not something this PR fixes.

**One source for comments and defaults.** `generator/config` grows one new
exported function, `Defaults() (*Config, error)`, that decodes an empty TOML
document through the *existing* `applyDefaults` path (no duplicated default
table). `generator/initcfg` holds the template and one hand-maintained
`fieldDocs` table (key path → one-line description) — comments have no other
canonical source in Go (the Nix option `description`s live in a different
language runtime this generator cannot evaluate), so this is the "one place"
rather than a parallel string: a single file, and a test (§2c) that fails the
build if a `Config` field's key path is missing from either the template or
`fieldDocs`, so schema drift is a test failure, not a silent gap.

**`og doctor`'s check list, corrected.** `[critic: B5]` The original draft
misread CLAUDE.md's "What the Remote Host Needs on PATH" table as also
answering "what does the *local* host need" — it doesn't; it's scoped to the
remote leg of the bridge. The authoritative local list is what the tmux
wrapper's `--prefix PATH` actually puts there
(`config/tmux.conf.nix:903`) plus `agentIntegration`'s default tool set
(`modules/home-manager.nix:832`), which off-Nix has to be supplied by hand:

- **Checked, local:** `tmux`, `ssh`, `gh`, `resvg`, `zoxide`, `jq`, `curl`,
  `chafa`, `socat`, `btop`, `sesh`, `lazygit`, `yazi`, `prdash`, an
  `xdg-open`-providing package (`xdg-utils` on Linux; skipped on darwin,
  where `open` is always present — this check is platform-aware, not a
  false positive on macOS). Clipboard tool for image paste (`ctrl+v`):
  `xclip` OR `wl-paste`, either satisfies it, reported as informational
  ("paste forwards the raw byte instead" — CLAUDE.md's own documented
  degrade, not a hard failure) rather than a red X. `git` and `linear` are
  deliberately excluded even though `tmux-branch-display`,
  `tmux-dir-display`, `tmux-worktree-match` and the Linear issue-stamp
  provider all shell out to them: the rule this checklist follows is "what
  the Nix wrapper's PATH guarantees" (`config/tmux.conf.nix:903`), and
  neither is in that list — `git` is assumed present on any dev machine this
  tool is relevant to, and CLAUDE.md documents `linear` as optional and
  already degrading gracefully. Stated here so the boundary is a choice, not
  an oversight.
- **Checked, per remote host** (`remote.hosts`, space-separated): one ssh
  round-trip per host running a single `command -v` sweep for exactly
  CLAUDE.md's remote-needs column: `tmux-claude-images`, `resvg`,
  `claude-status-update`, `tmux-reflow-windows`, `og-remote-picker`,
  `tmux-pr-enrich`, `gh`, `lazygit`, `yazi`, `prdash`, `theme-toggle`.
  `og-remote-picker`'s absence gets its own documented message ("remote too
  old to probe itself") since CLAUDE.md calls it out as the one
  self-reporting capability probe — everything else in this list is exactly
  the CLAUDE.md rows that have **no** probe today, which is where the table
  says a check earns its keep.
- **Explicitly out of scope, and why (printed as a footer note, not
  fabricated as a check):** `tmux-startup.service`/launchd-agent *enabled*
  state (systemd/launchd introspection is a different, OS-branching problem
  from a PATH check, and CLAUDE.md's own cold-start row is about unit state,
  not a binary) and the `@crew_name`/`@crew_color` fan-out harness (CLAUDE.md
  names it as having no fixed binary — "whatever fan-out harness," not a
  checkable name).

**The ssh-transport claim, scoped honestly.** `[critic: B4]` The original
draft claimed reuse of "the bridge's existing ssh transport," but the bridge
daemon's transport is a **per-dial** `ControlPath` passed on its own command
line (`picker/remotebridge/cmd/daemon/main.go`), which overrides the user's
ssh config and is *not* discoverable from outside a running daemon — and
`scripts/og-remote-auth.sh`'s own header comment says outright that the
daemon and the graphics fetcher authenticate independently of the config
master for exactly this reason. `og doctor` is a standalone diagnostic that
may run with no bridge attached at all (the design doc's framing — "the most
useful thing to hand a stranger" — assumes exactly that), so there is no live
per-dial socket to discover in the common case, and hunting for one when a
bridge *is* up is a different, session-scoped feature this tool doesn't need.
What `og doctor` actually does, restated precisely: it opens its own
`ssh -o BatchMode=yes -o ConnectTimeout=5 -- host '<sweep>'` connection per
host, the same **plain-ssh, no-`ControlPath`-override** style
`scripts/og-remote-open.sh` uses — so it does not *invent* a second
transport mechanism (no ssh library, no new auth path), and it naturally
benefits from the user's own `ControlMaster` config if one is set, but it
does not specifically target a live bridge daemon's socket. `BatchMode=yes`
makes a key-less host fail fast instead of parking a password prompt.

Bounded per host: a hard `context.WithTimeout` (10s: 5s `ConnectTimeout` +
headroom for the remote `command -v` sweep) wraps each ssh call, and hosts
run concurrently (one goroutine each) so one unreachable host costs its own
timeout, not the sum of every host's.

**Go, not bash, under `generator/`.** The gate explicitly asks for pure logic
— "default detection, comment rendering, a check's verdict from a given PATH"
— to be unit-testable without a live tmux/ssh, which is what the rest of
`generator/` already does (`generator/config`, `generator/paths`,
`generator/render` are all pure-function-tested). Two new `main` packages,
`generator/init` and `generator/doctor`, plus `generator/initcfg` (init's pure
template/defaults logic) and `generator/doctor` doubling as both the checks
package and the CLI (checks are pure enough — see step 3 — that a
sub-package isn't worth the indirection).

**Wiring into `og`, and making `og doctor` actually work on the Nix path.**
`[critic: B6]` `generator/default.nix` already builds every `main` package
under `generator/` via `buildGoModule`'s default `go install ./...`
(confirmed against `pkgs/build-support/go/module.nix`'s `getGoDirs`: it's a
plain `find . -name '*.go' -exec dirname`, filtered only on `/vendor/` and
`examples|Godeps|testdata` — `./init` and `./doctor` pass and get installed
named by directory basename, exactly like the existing `generator` →
`og-generate` rename) — no new Nix derivation, no new `vendorHash` (no new
dependencies), just two more `mv` lines in the existing `postInstall`
(neither needs `wrapProgram` at the `generator/default.nix` level — see
below for why `doctor` still needs one, added elsewhere).

But `og doctor`'s default `--config` path
(`${XDG_CONFIG_HOME:-$HOME/.config}/tmux-og/config.toml`) is where a
Homebrew/curl install's `og init` writes — it is **not** where the Nix path's
config.toml lives, which is a store path built by `tomlFormat.generate`
(`config/tmux.conf.nix:756`, bound to `configToml`). Left alone, `og doctor`
on a Nix install would report on a file that doesn't exist. Fix: in
`config/tmux.conf.nix`, wrap `og-doctor` specifically (not `og-generate`,
which already takes `--config` per-call, and not `og-init`, which needs no
config) with a small `pkgs.writeShellScriptBin` that execs the real binary
with `--config ${configToml}` prepended — the same "baked default, `flag`
parsing takes the last occurrence so an explicit override still wins"
contract `og-generate`'s own `wrapProgram --add-flags "--template ..."`
already relies on (documented at `generator/default.nix`'s existing
comment). The `"doctor"` `ogVerbSpec` entry then targets this wrapper's
`bin/og-doctor` instead of `${og-generate}/bin/og-doctor` directly.

Two new `ogVerbSpec` entries (`"init"`, `"doctor"`), `target`-shaped like the
existing `"generate"` entry — neither is a `scripts/*.sh` file, so
`ogPartitionOk`'s partition (which filters on `v ? script`,
`config/tmux.conf.nix:707`) is untouched by construction. Verified no
existing verb is named `init` or `doctor`. Separately, `og debug`'s current
summary — "Diagnose a tmux-og installation" — collides in `og help`'s output
with what `og doctor` actually does (`og-debug.sh` is an event-log toggle,
`{on|off|toggle|status|tail}`); its summary is fixed to describe the toggle,
not "diagnose" `[critic: note 1]`.

`generator/default.nix`'s `pname` stays `og-generate` even though the
derivation now produces three binaries — renaming it is out of scope per the
task's "do not rename anything," so a one-line comment there just says why a
reader of `nix run .#og-generate` shouldn't be surprised at what else is on
`$out/bin` `[critic: note 10]`.

## Steps

- [ ] **Step 1: `config.Defaults()` and `config.DefaultPath()`.** Add to
  `generator/config/config.go`: `Defaults()` decodes `""` through
  `toml.Decode` into a zero `Config`, calls the existing
  `(*Config).applyDefaults(&md)`, returns it — no new defaulting logic, just
  an exported entry point to the logic `Load` already has. `DefaultPath()
  string` returns
  `filepath.Join(cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home,
  ".config")), "tmux-og", "config.toml")` (`home` via `os.UserHomeDir()`) —
  the one place both `og init` and `og doctor` get this path from, so they
  can't drift from each other. Add `TestDefaults` (asserts no error,
  `Platform == runtime.GOOS`, and the three enum fields equal their
  documented fallbacks `off`/`preview`/`full` — mirrors the existing
  `TestEnumDefaultsWhenAbsent` shape) and `TestDefaultPath` (asserts the
  `XDG_CONFIG_HOME`-set and unset cases via `t.Setenv`) to
  `generator/config/config_test.go`.

- [ ] **Step 2: `generator/initcfg` (pure logic, no CLI).**
  - `Render() (string, error)`: calls `config.Defaults()`, builds the file
    from a single Go raw-string template with substitutions for the tier-1
    and tier-2 live values (`platform`, `prefix`, `auth_persist_seconds`) and
    static text for every tier-3 commented field/section. Every commented
    line uses a fixed `# ` prefix consistently (needed by the drift test
    below).
  - `fieldDocs`: a `map[string]string` (or ordered slice of `{path, doc}`)
    keyed by the same dotted path the drift test walks, one line each. The
    `process_icons` and generator-gap fields (listed in the Decisions
    section) get their extra caveat sentence here, in the doc string itself
    — not just in this plan.
  - `TestRenderIsValidConfig` (round-trip): write `Render()`'s output to a
    temp file, `config.Load` it, assert no error, assert the result
    deep-equals `config.Defaults()` **with the tier-2 fields overridden to
    their written values first** (`Prefix = "`"`, `AuthPersistSeconds =
    14400`) `[critic: B3]` — the expected-value override lives right next to
    the tier-2 constant definitions (one small `expectedLive(*config.Config)`
    helper both `Render` and this test call), so a third field promoted to
    tier-2 later has to touch both call sites, not silently pass a stale
    test. Note the honest limit of this: `expectedLive()` keeps the test
    consistent with `Render()`, not with `modules/home-manager.nix` — Go
    cannot evaluate Nix, so nothing catches the module's defaults drifting
    away from the pinned backtick/`14400`. One sentence at the tier-2
    constants' definition site says this pair is manually synced to the
    module, so the next reader treats it as a sync point, not an
    independent choice.
  - `TestRenderCoversEveryField` (drift guard, strengthened) `[critic: note
    2]`: reflect over `config.Config{}` (recursing into nested struct
    fields, treating `map`/`slice`/`*string`/scalar fields as leaves) to
    collect every `toml:"..."` tag as a dotted path. Rather than a substring
    search (which a shared leaf name like `enable` or `icons` would pass
    vacuously across five sections), strip the `# ` comment prefix from
    `Render()`'s output, `toml.Decode` the *entire* de-commented text, and
    assert `md.IsDefined(path...)` for every reflected path plus
    `md.Undecoded()` empty. This also validates the commented half's TOML
    *syntax* (nesting, quoting), which the round-trip test structurally
    cannot reach, since a commented line is never parsed by `config.Load` as
    written.
  - `TestGenerateAcceptsInitOutput` (a `render.Build` smoke, not a full
    render) `[critic: note 3, corrected per B7]`: feed `Render()`'s output
    through `config.Load` then `render.Build` (using
    `paths.FromPrefix(t.TempDir())` — `generator/paths/paths_test.go`
    confirms an empty prefix dir suffices, no `paths.toml` fixture needed)
    and assert no error/panic. Described honestly as what it is: proof that
    `render.Build` accepts the decoded config, not proof the real
    `config/tmux.conf.tmpl` renders — `generator/default.nix`'s `src =
    lib.cleanSource ./.` scopes the Go module's test sandbox to `generator/`
    alone, so a Go test reaching for `../../config/tmux.conf.tmpl` passes
    locally and fails under `nix build .#default` `[critic: B7]`. The
    real end-to-end proof — `og init`'s output surviving the actual
    template — belongs in Nix and already has a home; see step 6a.

- [ ] **Step 3: `generator/doctor` (checks, pure where possible).**
  - `type Result struct { Name, Feature string; OK bool; Detail string }`
  - `localChecklist`, `remoteChecklist` (unexported `[]string` + feature
    labels), built from the corrected lists in the Decisions section above —
    not from a re-reading of CLAUDE.md's remote-only table for the local
    side.
  - `CheckLocal(lookup func(string) (string, error)) []Result`: pure —
    tests inject a fake `lookup` (no real `PATH`/filesystem needed), matching
    the gate's "a check's verdict from a given PATH" wording directly.
    Production call site passes `exec.LookPath`. The clipboard check
    (`xclip` OR `wl-paste`) is a small either/or variant of the same
    function, not a special case in the caller.
  - `ParseRemote(output string) []Result`: pure — parses `name=0`/`name=1`
    lines (one per checklist entry) out of the remote sweep's stdout; a
    missing line for a known name reports `OK: false, Detail: "no answer"`
    rather than panicking, since ssh output is untrusted input. Fully unit
    testable with canned strings, no ssh involved.
  - `RunRemote(ctx context.Context, host string) (string, error)` (thin, not
    unit-tested — mirrors how `paths.FromPrefix`'s `os.Stat` calls are also
    left as an untested I/O boundary): `exec.CommandContext` running
    `ssh -o BatchMode=yes -o ConnectTimeout=5 -- <host> '<command -v sweep>'`
    with a 10s `context.WithTimeout` at the call site — no `ControlPath`
    override, per the Decisions section's ssh-transport scoping.
  - `generator/doctor/main.go`: reads `--config FILE` (defaults to
    `${XDG_CONFIG_HOME:-$HOME/.config}/tmux-og/config.toml`, computed with
    the same explicit fallback `og init` uses). Decided, not left open: the
    helper lives in `generator/config` (a `DefaultPath() string` beside
    `Load`/`Defaults`), not `generator/initcfg` — `doctor` has no other
    reason to import `initcfg` (it never renders a config.toml), and pulling
    in the whole comment-template package for a three-line path function
    would be the wrong dependency direction. `init` calls the same
    `config.DefaultPath()`. Runs `CheckLocal` against the real PATH, splits
    `Config.Remote.Hosts` on whitespace and runs `RunRemote`+`ParseRemote`
    per host concurrently (`sync.WaitGroup`, each goroutine's own
    `context.WithTimeout`), prints a grouped human-readable report (`Local:`
    block, then one block per host — an unreachable host prints one line,
    not one line per missing binary), appends the two "explicitly out of
    scope" footer lines, and exits 1 if any check failed or any host was
    unreachable, 0 otherwise (so a future `install.sh` can gate on it). A
    missing config file at the default path is reported as "no config found
    — run `og init`," not a crash.
  - Tests: `TestCheckLocal` (fake lookup, mixed hits/misses, including the
    either/or clipboard case), `TestParseRemote` (canned sweep output:
    all-present, all-missing, partial, and a name the checklist doesn't know
    about — ignored, not an error), `TestParseRemoteMissingLine` (a
    checklist name absent from the output reports not-OK with a distinct
    detail, not a false "present"), `TestNoRemoteHostsConfigured` (empty
    `Remote.Hosts` produces zero remote blocks and a clean local-only report,
    not an error).

- [ ] **Step 4: `generator/init/main.go`.** Flags: `--out FILE` (defaults to
  `config.DefaultPath()`, step 1), `--force`
  (bool). Refuses via `os.Stat` when the target exists and `--force` is
  unset, printing the exact remediation (`--force` or remove the file) to
  stderr and exiting 1. Otherwise `os.MkdirAll` the parent, call
  `initcfg.Render()`, write `0o644`, print the written path and one-line
  "run `og generate --config <path> --prefix <dir>` to build tmux.conf"
  hint (`og generate` already exists; this is documentation, not new
  behavior). `main.go` itself stays flag parsing and file I/O glue, mirroring
  `generator/main.go`/`main_test.go`'s division of labor: a `run(args
  []string) error` function the `TestRefusesExistingFile`/
  `TestForceOverwrites`/`TestWritesDefaultPath` table exercises via temp
  dirs and an injected `--out`, `main()` a two-line wrapper.

- [ ] **Step 5: Nix wiring.**
  - `generator/default.nix`: two more `mv $out/bin/<dir> $out/bin/og-<dir>`
    lines in `postInstall` (no `wrapProgram` here — `og-doctor`'s
    `--config` default is wrapped at the `config/tmux.conf.nix` level, see
    below, since only that file knows the store path). Add a one-line
    comment noting `pname` stays `og-generate` for three binaries.
  - `config/tmux.conf.nix`: a small `og-doctor-wrapped = pkgs.writeShellScriptBin
    "og-doctor" ''exec ${og-generate}/bin/og-doctor --config ${configToml}
    "$@"'';` beside where `configToml`/`og-generate` are already defined
    (a one-line comment there notes that `nix run .#og -- doctor` — the
    flake's default-args instantiation, distinct from the real
    `home.packages` install which always carries the user's own options —
    bakes the flake's default `configToml` and so reports zero remote hosts
    regardless of a user's actual settings; the installed path is correct),
    and two new `ogVerbSpec` entries:
    - `"init" = { target = "${og-generate}/bin/og-init"; summary = "Write a
      commented config.toml with detected defaults"; };`
    - `"doctor" = { target = "${og-doctor-wrapped}/bin/og-doctor"; summary =
      "Diagnose what is missing on PATH, here and on each configured
      remote"; };`
    Fix `"debug"`'s summary (currently "Diagnose a tmux-og installation") to
    describe the event-log toggle it actually is, so `og help` doesn't list
    two verbs both claiming to diagnose. No changes to `ogInternal`,
    `ogPartitioned`, or the option tree.

- [ ] **Step 6a: the real end-to-end proof, in `flake.nix`.** `[critic: B7]`
  `tmux-conf-extraction-assertions` (`flake.nix:830-874`) already builds
  `OG_GENERATE` from the same `generator/` derivation and ends with a
  `--prefix` smoke that renders the *real* `config/tmux.conf.tmpl` and greps
  it positively — the thing a Go-level test cannot do, since
  `generator/default.nix`'s `src = lib.cleanSource ./.` doesn't see
  `config/` at all. Add an `OG_INIT` env binding beside `OG_GENERATE` (same
  derivation, so this costs nothing extra, matching the existing comment
  there) and a second leg after the `--prefix` smoke: run
  `$OG_INIT --out init.toml`, then `$OG_GENERATE --config init.toml --prefix
  /opt/tmux-og --template "$TEMPLATE" --out initout`, then the same two
  positive greps the `--prefix` smoke already uses (`^set -g ` present,
  `/opt/tmux-og/bin/` present) plus `no_store_path` on the result. This is
  the highest-value assertion in the plan — it's the only place `og init`'s
  actual output is proven to survive `og generate` end to end.

- [ ] **Step 6: Gate.** `nix build .#default` (also runs `go test ./...`
  across `generator/` via `buildGoModule`'s default `doCheck = true` —
  confirmed already on for this derivation with `subPackages` unset; note
  `-vet=off` is part of that default too, per
  `pkgs/build-support/go/module.nix`, which is why the manual `go vet` below
  isn't redundant with it), `nix flake check` (this is also what runs
  `og-dispatch-assertions`, which derives its verb list from `ogVerbSpec`
  itself and checks `[ -x "$target" ]` per verb — the thing that would
  actually catch a typo in step 5's `mv` lines, since Nix eval alone won't —
  and step 6a's extraction assertions), `nix build .#lint`. Fix forward on
  any failure; none of these subsumes another so all three run. `nix build
  .#lint`'s hook set has no Go formatter or vet step, so run `gofmt -l` and
  `go vet ./...` over the new packages by hand before considering the step
  done.

- [ ] **Step 7: README.** `README.md`'s `## Installation` section is
  entirely Nix-flake-flavored today and doesn't mention `og generate` either
  — pre-existing, out of scope to fully fix here. Add a short paragraph
  under `## Installation` naming the three `og` CLI verbs (`init`,
  `generate`, `doctor`) and noting that Homebrew/curl packaging around them
  lands in a follow-up PR, so the commands being added here aren't
  invisible to anyone reading the README before the next PR ships
  `[critic: note 7]`.

## Test plan

- `generator/config`: `TestDefaults`, `TestDefaultPath` (step 1).
- `generator/initcfg`: `TestRenderIsValidConfig`, `TestRenderCoversEveryField`,
  `TestGenerateAcceptsInitOutput` (`render.Build` smoke, step 2).
- `flake.nix`'s `tmux-conf-extraction-assertions`: the `og init` leg (step
  6a) — the actual end-to-end proof that lives outside `go test`.
- `generator/doctor`: `TestCheckLocal`, `TestParseRemote`,
  `TestParseRemoteMissingLine`, `TestNoRemoteHostsConfigured` (step 3).
- `generator/init`: `TestRefusesExistingFile`, `TestForceOverwrites`,
  `TestWritesDefaultPath` (step 4).
- All new tests run under plain `go test ./...` — no tmux server, no ssh
  connection, matching the gate's requirement.

## Scope discipline

Purely additive: no change to `generator/paths`, `generator/render`, or the
Nix module's option tree; no renames. Two small touches outside the new
packages, both justified above rather than snuck in: `og debug`'s
`ogVerbSpec` summary (avoids a real `og help` collision) and a short README
paragraph (the new commands would otherwise be undocumented anywhere a user
looks). The tier-2 hand-defaulted pair (`tmux.prefix`,
`remote.auth_persist_seconds`) is the one piece of genuinely new reasoning
outside straightforward plumbing — called out inline in `generator/initcfg`
at the point it's special-cased, not just here, so a future reader hits the
explanation where the risk actually lives.
