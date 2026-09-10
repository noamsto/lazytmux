# Decomposition — step 2: extract config generation into `og generate`

Issue #595 (step 2 of 4). Companion to
`docs/superpowers/specs/2026-09-10-og-generate-extract-config-generation-design.md`.
Decisions D1/D1a/D2/D3/D4, § Scope of the seam, § The
gate and the 11-entry matrix are taken as given. This document says what the
work splits into, in what order the pieces can land, and which contracts hold
the pieces together where a mismatch would be silent.

Line numbers refer to `config/tmux.conf.nix` at `e79e925`, this branch's base.

Two facts shape the whole split:

- **The gate is one whole-file byte diff**, so a partial template lift is only
  verifiable if something can splice the not-yet-lifted remainder in. The
  frozen reference is therefore sectioned (interface I4) and the extraction
  check has a *hybrid* mode that renders `go(prefix) ++ nix(remainder)`. Every
  cut of the lift is then green-or-red on its own, against the same oracle
  the final gate uses.
- **The live path can go through the frozen reference before any Go exists.**
  Freezing `tmuxConfText` into `config/tmux.conf.reference.nix` and importing
  it back is a no-op by construction (and provable with a diff), so the
  switchover at the end is one binding — `tmuxConf` repointed from the
  reference's output to the generator's — not a big bang.

## components

### `reference-freeze`
`tmuxConfText` and every binding only it uses (`icons`, `enrichIconDefaults`,
`bridgeGate`/`bridgeCtl`/`bridgeOpt`, `mkFloat`/`floatBind`/`bridgedFloatTool`,
`carouselBind`, `prdashBind`, `carouselHooks`, `pluginConfigs`,
`pluginRunShells`, `terminalConfig`, `defaultShellConfig`, the flag strings,
the tick-hook `let` block, `tmuxPlugins`, `catppuccin`) move verbatim into
`config/tmux.conf.reference.nix`, split into four named sections (I4), plus
the persist-block text from `modules/home-manager.nix:113-122` as a fifth
input (I7). `config/tmux.conf.nix` imports it and — until `switchover` — sets
`tmuxConf` to its concatenation, so the live path is unchanged in bytes and
`tmux.conf.nix` already "no longer contains the tmux.conf text". Gains the
`persistWireScript ? null` argument and forwards it. Header states the
two-file rule and the step-3 removal condition.
- may-touch: `config/tmux.conf.reference.nix` (new), `config/tmux.conf.nix`
  (bindings 84-190 and 830-1582 move out; return attrset gains
  `referenceConf`)
- must-not-touch: `scripts/**`, `picker/**`, `modules/**`, `flake.nix`
- risk: medium — a sectioned `''` literal de-indents per piece; the identity
  diff (I4) is what makes this safe, and it is the first thing that runs.

### `generator-build`
New top-level Go module `generator/` (own `go.mod`, `BurntSushi/toml`, own
`vendorHash`) producing one binary `og-generate`; a `generator/default.nix`
that builds it and injects the template's default location (I6); flake wiring
as `packages.og-generate`. Nothing under `picker/` changes, so no store path
that lands in tmux.conf moves (§ Not perturbing existing derivations).
- may-touch: `generator/go.mod`, `generator/go.sum`, `generator/main.go`,
  `generator/default.nix`, `flake.nix` (packages), `.gitignore`
- must-not-touch: `picker/**`, `config/**`, `scripts/**`, `modules/**`
- risk: low — vendorHash churn is loud, not silent.

### `generator-config`
`generator/config`: the `config.toml` schema (I1) as Go structs, a **strict**
decoder (undecoded keys are an error), validation (enum values for
`copy_mode_line_numbers`, `picker.layout`, `splash.remote`), and the VS16
rejection with the exact Nix message (I9). Unit-tested against fixture TOML,
including one carrying a literal `#` icon and one carrying a VS16 glyph.
- may-touch: `generator/config/**`
- must-not-touch: everything outside `generator/`
- risk: medium — the key names are the seam with `nix-serializer`; strict
  decode + `missingkey=error` make a name mismatch loud, so what remains
  silent is value encoding, which the gate covers.

### `generator-paths`
`generator/paths`: the D3 resolver. Decodes `paths.toml` (I2) or synthesizes
the same map from `--prefix DIR`; both yield one Go type. Required keys are
enumerated (I2) and an absent required key is an error at resolve time, not at
render time; the optional keys resolve to absent, which the render layer maps
to "conditional off". Unit-tested for both modes and for the fixed layout.
- may-touch: `generator/paths/**`
- must-not-touch: everything outside `generator/`
- risk: low — a wrong path is a loud failure at tmux load; a *missing* key is
  loud by design here.

### `generator-render`
`generator/render` skeleton: the template data struct (I3), `text/template`
execution with `Option("missingkey=error")`, the `--template` file read, and
the `platform` → `copy-command` mapping (`pbcopy`/`wl-copy`, I1). Carries **no** derived
values yet — each `lift-*` component adds the ones its cut needs, so the
render package grows in lockstep with the template and each addition is
covered by the hybrid check that admits that cut.
- may-touch: `generator/render/**`, `generator/main.go`
- must-not-touch: `config/tmux.conf.tmpl` (the lifts own it)
- risk: low on its own; the risk lives in the lifts.

### `lift-a` — base settings through prefix (lines 929-1001)
`config/tmux.conf.tmpl` created with section A. Derived values added to
`render`: `DefaultShellConfig`, `TerminalConfig` (both with their trailing
`\n    `, I3), `PluginConfigs` (ends `\n\n`), `PluginRunShells` (five plugin
paths + `bash`), the `focus-follows-mouse` on/off, `copy-command` by OS,
`prefix` (two sites). Gate: hybrid check at cut A over the full matrix.
`copy-command` reads `platform` (I1); the matrix cannot vary it, so its two
values are covered only by the two CI systems each rendering their own.
- may-touch: `config/tmux.conf.tmpl`, `generator/render/**`, the check's cut
  parameter
- must-not-touch: `config/tmux.conf.reference.nix`, `config/tmux.conf.nix`
- risk: high — three of the multi-line de-indent hazards and the two
  trailing-indent quirks are here; matrix entries 7 and 9 are the cover.

### `lift-b` — keybinds through vim navigation (lines 1002-1201)
Section B. Adds: `bridgeGate`/`bridgeCtl` (15 + 14 sites), `mkFloat` /
`floatBind` / `bridgedFloatTool` / `floatNewPaneGuard` with its `"`→`\"`
escaping, `carouselBind`, `prdashBind`, the `splashEnable` bind, the
`enrichEnable` float-card block (raw enrich icons, I8), the `notifyEnable`
bind, `is_vim`. Bare `tmux-smart-nav` stays bare (I2). Gate: hybrid at cut B.
- may-touch / must-not-touch: as `lift-a`
- risk: high — five of the seven conditionals and the deepest quoting live
  here; matrix entries 2, 3, 4, 6 are the cover.

### `lift-c` — window titles through the tick-hook block (lines 1202-1370)
Section C. Adds: the `@ai_naming`/`@resume_*` flag strings, the `icons`
constants, picker/remote option lines (four script paths, `bridge_ctl_bin`),
the `carousel-toggle` `@carousel_bin` conditional, `status-format[0]` with
the doubled enrich icons (I8) and the inline `agentUsageEnable` argument
group (merged `process_icons` lookup with the `🧠`/`🤖`/`🧊` fallbacks),
`status-format[1]` with `bridgeOpt` (13 sites), and the tick-hook block:
sorted hook names, `clears ++ setters` joined by ` \; `, `"`→`\"` escaped,
inside the `if-shell` probe. Gate: hybrid at cut C.
- may-touch / must-not-touch: as `lift-a`
- risk: high — the two longest single lines in the file and the only
  `let`-block interpolation; matrix entries 4, 5, 6, 9 are the cover, and
  entry 12 is what renders `aiNamingFlag`/`resumeCarouselFlag` in their
  non-default value — without it an inverted `"1"`/`"0"` or `"on"`/`"off"`
  mapping is byte-identical everywhere else.

### `lift-d` — hooks through the tail (lines 1371-1581)
Section D. Adds: the hook lines (nine script paths), the `splashEnable` and
`notifyEnable` hook blocks, `carouselHooks`, the persist block from
`persist_wire_script` (I7), and `extra_config` last. When this lands the
template is complete and the hybrid remainder is empty; the check's cut
parameter and the reference's per-section exposure lose their consumer.
- may-touch / must-not-touch: as `lift-a`
- risk: medium — mostly path substitution, but the tail is where the D1a
  persist split lands, and matrix entry 10 is its only cover.

### `nix-serializer`
`config/tmux.conf.nix` gains the two serializers and the generator call:
`configToml` (arguments → I1, via a real TOML encoder, nulls omitted, no
store path), `pathsToml` (the derivations it already builds → I2), and a
`runCommand` running `og-generate --config --paths --out --template`,
whose `tmux.conf` becomes the generator-side `tmuxConf` candidate. Until
`switchover` the live `tmuxConf` stays on the reference and these are exposed
attributes the check consumes. Also holds the one-authority invariant for the
§ Scope ten: each option is a single function argument passed to both the
serializer and the script builders — and for `enrichIcons` the script path
becomes `enrichIconDefaults // enrichIcons` (raw, I8).
- may-touch: `config/tmux.conf.nix` (argument block, new bindings, return
  attrset), `flake.nix` (threading `og-generate` into the import)
- must-not-touch: the script builders' substitution lists (196-620) except
  the `enrichIconSetRaw` line, `picker/**`, `scripts/**`
- risk: high — this is where a value-encoding mistake (a `\` in a glyph, a
  `"` in `extraConfig`, a `#` in an icon) becomes a silent diff; the gate and
  the "no `/nix/store` in `config.toml`" assertion are the cover.

### `extraction-check`
`checks.<system>.tmux-conf-extraction-assertions`: twelve imports of
`config/tmux.conf.nix` with the matrix arguments, each diffed
`generatedConf` vs `referenceConf`, plus the `config.toml` store-path
assertion over those twelve files (the matrix's own — never a general
invariant, since a user's `extraConfig` may carry a store path), the
`--prefix DIR` smoke (renders; output contains no
`/nix/store`), and a `grep -Fx` for the persist block's three literal lines on
entry 10 (a third copy of those bytes, deliberately — see I7). Carries the
hybrid cut parameter during the lift and drops it after `lift-d`.
- may-touch: `flake.nix` (checks block only)
- must-not-touch: the four existing direct imports at `flake.nix:79, 504,
  900, 1480` and their argument sets
- risk: medium — a check that compares a thing to itself is the failure mode;
  the matrix must vary every flag both ways, and the persist grep and the
  prefix smoke are the two assertions that are not self-referential.

### `hm-caller-edit`
`modules/home-manager.nix`: delete `tmuxStateConf` (113-122); pass
`extraConfText = cfg.extraConfig` and `persistWireScript =
tmuxRemuxWireScript` (144); stop doubling `#` in `enrichIcons` (149) — the
generator does that now, for user-supplied keys only (I8). No other option
threading changes. Verified through the reference before the generator is
live (the reference builds the persist block from `persistWireScript`, so
the bytes a module user gets are unchanged from the moment this lands).
- may-touch: `modules/home-manager.nix` lines 60-200 only
- must-not-touch: the option block (252+), `startupSession`/`skills` wiring,
  activation scripts
- risk: medium — no existing check imports the module; matrix entry 10 plus
  the literal grep are the cover for the persist half, entry 9 for the
  icon-doubling half.

### `og-generate-verb`
`ogVerbSpec."generate" = { target = "${og-generate}/bin/og-generate";
summary = ...; }` — a `target`-shaped entry beside the `script`-shaped ones.
`ogPartitioned` and `og`'s `mapAttrs` each gain a `v ? script` branch so a
non-script verb neither breaks the partition nor gets resolved through
`script.<name>`. The existing `og-dispatch-assertions` check enumerates
`ogVerbSpec` attrNames and asserts `og <verb> --help`'s last line is an
executable path; a `target` entry satisfies it unchanged.
- may-touch: `config/tmux.conf.nix` lines 645-810 (`ogVerbSpec`,
  `ogPartitioned`, `og`)
- must-not-touch: `scripts/og.sh`, `ogInternal`, `scriptNames`
- risk: low.

### `verify-extraction-script`
`tests/verify-extraction.sh`: builds `.#default` at `e79e925` and at `HEAD`
(two worktrees or `git archive` into temp dirs), scrapes the `-f <path>`
argument out of each `result/bin/tmux` wrapper, diffs the two files, prints
the diff and exits non-zero on any. Aborts loudly, before building, if
`flake.lock` differs between the two revisions — a lock bump would produce a
non-empty diff for a reason unrelated to the extraction and discredit the one
instrument that sees store-path drift. Pinned to the SHA, not `main`.
shellcheck clean; shfmt tabs.
- may-touch: `tests/verify-extraction.sh` (new)
- must-not-touch: everything else
- risk: low — it is the one instrument that sees store-path drift, and it is
  independent of every other component.

### `switchover`
`tmuxConf` in `config/tmux.conf.nix` repointed from the reference's
concatenation to `"${generated}/tmux.conf"`; the reference stays reachable
only via `referenceConf`; `configToml` stays exposed (D1's debuggability
claim). `tmuxConf` remains a store path to a regular file, which every
consumer (`CONF=` in nine checks, `source-file` in the activation script,
`xdg.configFile."tmux/tmux.conf".source`) already requires.
- may-touch: `config/tmux.conf.nix` (one binding)
- must-not-touch: everything else
- risk: low — by this point the gate has been green on the generator side for
  every cut; the switch changes which of two identical files is named.

### `spec-and-plan-docs`
`docs/superpowers/specs/2026-09-10-og-generate-extract-config-generation-design.md`
(the accepted spec, D4 table included) and the matching plan under
`docs/superpowers/plans/`. Committed alongside the code per CLAUDE.md.
- may-touch: `docs/superpowers/**`
- must-not-touch: code
- risk: low.

## ordering

```
reference-freeze
  │
  ├──▶ generator-build ∥ generator-config ∥ generator-paths ∥ verify-extraction-script ∥ spec-and-plan-docs
  │            │              │                 │
  │            └──────────────┴─────────────────┘
  │                           │
  │                    generator-render
  │                           │
  ├──▶ nix-serializer ◀───────┤        (needs I1/I2 fixed; can land before any lift)
  │        │                  │
  │        └────▶ extraction-check (hybrid mode, cut = none: go renders nothing, remainder = whole reference)
  │                           │
  ├──▶ hm-caller-edit ∥       lift-a ──▶ lift-b ──▶ lift-c ──▶ lift-d
  │                                                              │
  ├──▶ og-generate-verb ∥ ────────────────────────────────────── ┤   (needs generator-build only)
  │                                                              │
  └──────────────────────────────────────────────────────▶ switchover
                                                                 │
                                          verify-extraction-script *run*, output into the PR body
```

Reading the graph:

- **Before the live path moves** (everything above `switchover`): the
  reference freeze, all three generator packages, all four lifts, the
  serializers, the extraction check, the module edit and the verb. Each is
  verified in place: the freeze by the identity diff, the lifts by the hybrid
  check, the serializers by the check consuming their outputs, the module edit
  through the reference.
- `hm-caller-edit` is parallel to the lifts because the reference already
  builds the persist block from `persistWireScript`; it does not wait for
  `lift-d`.
- `og-generate-verb` needs only the binary. It is parallel to everything after
  `generator-build`.
- The lifts are strictly sequential: each cut's hybrid render assumes the
  previous cut's prefix is complete.
- `switchover` is last and is one binding. `verify-extraction-script` is
  written early and *run* last; its output is an acceptance artefact.

## interfaces

### I1 — `config.toml` (nix-serializer ↔ generator-config)

Values are carried as `config/tmux.conf.nix` **receives** them, because that
argument interface is frozen by the spec (component 3): lists where the
argument is a list, joined strings where the module has already joined. A
null-valued argument is an **absent key**, never `""`; the two are distinct
where the template distinguishes them (`default_shell`, `terminal_term`).

```toml
platform = "linux"               # "linux" | "darwin", from pkgs.stdenv.hostPlatform (D2)

[tmux]
prefix = "`"                     # prefix
default_shell = "/…/fish"        # defaultShell — absent when null
focus_follows_mouse = false      # focusFollowsMouse
copy_mode_line_numbers = "off"   # copyModeLineNumbers: off|default|absolute|relative|hybrid
terminal_term = "xterm-ghostty"  # terminalTerm — absent when null
sixel_terminals = ["foot"]       # sixelTerminals
extra_config = ""                # extraConfText, user text ONLY (D1a)

[picker]
zoxide_exclude = "*/.ssh,/tmp/*" # zoxideExclude, comma-joined as received
list_ratio = 50                  # pickerListRatio
layout = "preview"               # pickerLayout: preview|list

[remote]
hosts = "halo mbp"               # remoteBridgeHosts, space-joined as received
auth_persist_seconds = 14400     # remoteAuthPersistSeconds

[enrich]
enable = true
providers = ["linear", "github"]
pr_refresh_seconds = 120
pr_check_refresh_seconds = 300
[enrich.icons]                   # user OVERRIDES only, single '#' as typed (D1a)
conflict = "#"

[notifications]
enable = true

[agent_usage]
enable = true
refresh_seconds = 120
monthly_threshold = 50

[claude_status]
assume_dead_after = 0

[splash]
enable = true
remote = "full"                  # full|static|skip

[ai_naming]
enable = false

[resume]                         # the module's already-gated booleans
claude = true                    # resumeClaudeEnable
carousel = false                 # resumeCarouselEnable

[process_icons]                  # the MERGED map (process-icons.nix // extraProcessIcons)
claude = "🧠"
```

- `platform` is the one key that is not a module option: it picks the
  `copy-command` line. Nix writes it from `pkgs.stdenv.hostPlatform`, never
  from the generator's own `runtime.GOOS` — under cross-compilation the
  generator runs on the build platform, not the host the config is for. Off
  Nix (`og init`, step 4) an absent key defaults to `runtime.GOOS`.
- `process_icons` is the merged map, not the overrides: the defaults live in
  `config/process-icons.nix` and the generator must not carry a second copy
  (D2 already hands the merged map to the picker derivation the same way).
  The VS16 check runs over this whole map, so its message can name the same
  keys Nix's does.
- Not keys, by D2: `splash.tips`, `splash.timeout`, `fallbackIcon`,
  `maxIcons`, `maxIconsPicker`, `tmuxPkg`, `carousel-*`, `prdash`, anything
  under `persist.*` besides the two `resume` booleans.
- The decoder is strict: a key the schema does not know is an error. The
  serializer therefore cannot add a key without the Go side agreeing, and a
  renamed key fails the build rather than rendering a default.
- Encoding: a real TOML encoder on the Nix side (nixpkgs' formatter or an
  equivalent), never string concatenation. The values contain PUA glyphs,
  `#`, `\`, `"` (in `extra_config`) and multi-line text; every one of those is
  a class a hand-rolled escaper gets wrong once.
- Asserted by the check over the twelve matrix files: no `/nix/store`. Not
  an invariant of `config.toml` in general — `extra_config` is a verbatim
  copy of `cfg.extraConfig`, which a Nix user may legitimately interpolate a
  store path into.

### I2 — `paths.toml` (nix-serializer ↔ generator-paths), and the `--prefix` layout

Every value is the **final path the template emits**, verbatim — an executable
for scripts and binaries, the `.tmux` entry file for plugins. The resolver is
the only thing that knows a layout; the template never joins path segments.

```toml
bash = "/nix/store/…-bash/bin/bash"
persist_wire_script = "/nix/store/…-tmux-remux-wire"   # optional
carousel_toggle = "/nix/store/…/bin/tmux-claude-images" # optional
carousel_aeye = "/nix/store/…/bin/aeye"                  # optional, accepted, unread in this step
prdash = "/nix/store/…/bin/prdash"                      # optional

[scripts]      # 25 required keys — exactly the ${script.<name>} sites the template has
tmux-reflow-windows = "/nix/store/…-tmux-reflow-windows/bin/tmux-reflow-windows"
# claude-status-update lazytmux-debug lztmux-notify lztmux-notify-center
# lztmux-remote-auth lztmux-remote-detach lztmux-remote-open lztmux-remote-picker
# lztmux-remote-theme tmux-agent-usage tmux-apply-theme-colors tmux-default-size
# tmux-float-refit tmux-issue-stamp tmux-kill-pane-guard tmux-pr-enrich
# tmux-reconcile-window tmux-scratchpad tmux-session-picker tmux-splash-maybe
# tmux-update-icons tmux-window-nav tmux-window-picker tmux-window-wall

[bin]          # 4 required keys — the Go binaries the template names
tmux-splash = "…/bin/tmux-splash"
tmux-statusline = "…/bin/tmux-statusline"
tmux-enrich-card = "…/bin/tmux-enrich-card"
lztmux-remote-bridge-ctl = "…/bin/lztmux-remote-bridge-ctl"

[plugins]      # 5 required keys — the entry file, not the package root
catppuccin = "…/share/tmux-plugins/catppuccin/catppuccin.tmux"
better-mouse-mode = "…/share/tmux-plugins/better-mouse-mode/scroll_copy_mode.tmux"
vim-tmux-navigator = "…/share/tmux-plugins/vim-tmux-navigator/vim-tmux-navigator.tmux"
tmux-fzf = "…/share/tmux-plugins/tmux-fzf/main.tmux"
fingers = "…/share/tmux-plugins/tmux-fingers/tmux-fingers.tmux"
```

- `--prefix DIR` synthesizes: `scripts.*` and `bin.*` → `DIR/bin/<name>`;
  `plugins.<p>` → `DIR/share/tmux-og/plugins/<p>/<entry>` with the entry
  filenames above compiled in; `bash` → `DIR/bin/bash`; the optionals →
  present iff the file exists under `DIR/bin/`.
- `carousel_aeye` is in D3's optional set, so the resolver accepts it in both
  modes, but no template site reads it in this step (it reaches tmux.conf only
  through `tmux-carousel-restore`'s script substitution, which stays in Nix).
  A template reference to it would be a `missingkey=error` today; that is
  the intended state until step 4.
- A missing required key is a resolve-time error in both modes. An optional
  key that is absent renders its conditional off (the template's own
  `carousel-toggle != null` idiom); it is never emitted as an empty string.
- `tmux-smart-nav`, `wl-copy`, `k9s`, `btop`, `lazygit`, `yazi` are **bare
  names** in the template today and stay bare. They are not keys.
- `agent-detect`, `tmux-picker-generate`, the bridge daemon and renderer are
  reached only through scripts, never by the template. Not keys.

### I3 — template data (generator-render ↔ `config/tmux.conf.tmpl`)

The template is executed with `missingkey=error`; every name it references
exists on the data struct or the render fails. Byte contracts the derived
values must honour, because Nix's `''` de-indentation applied to the literal
*before* interpolation and the interpolated value was inserted raw:

| value | bytes |
|---|---|
| `DefaultShellConfig` | `""` or `"set -g default-shell <p>\n    "` — the four trailing spaces are real: the next line in the output is `    set -g history-limit 1500000` whenever a shell is set |
| `TerminalConfig` | zero or more lines each ending `\n    `; same effect on the `*:hyperlinks` line that follows |
| `PluginConfigs` | ends `'off'\n\n` (the literal's blank last line survived) |
| `PluginRunShells`, `CarouselHooks`, every `optionalString` block | body at column 0, one trailing `\n`; the enclosing template line contributes nothing but the surrounding blank lines that are already in the literal |
| the tick-hook block | `clears ++ setters` joined by `" \; "`, then `"`→`\"`, spliced into the string-form `if-shell`; hook names in the fixed order `pr backfill usage sweep`, clears before setters |
| `bridgeOpt` | `#{?#{@bridge_win},#{@bridge_<n>},#{@<n>}}` — 13 sites, `n` ∈ `crew_name crew_color pr_number pr_state pr_check_state pr_mergeable` |
| `mkFloat` stamp | `set -p @float_geom '<w> <h> <x> <y>' \; set -p remain-on-exit off`, four fixed shapes (`floatFull`, `floatShort`, `floatCard`) |
| `floatNewPaneGuard` | `"`→`\"` on the inner command only, both branches emitted |
| `CarouselBind`, `PrdashBind` | single-line double-quoted strings, **no** trailing newline of their own — the template line supplies it, so an absent bind leaves one empty line in the output (matrix entry 6) |
| map iteration | every `for` over a map sorts keys; Nix sorted them |

The lifted text contains no `{{`, no `''${` and no `'''` (spec measured it),
so the template needs no delimiter change; a `{{` appearing in a *value*
(`extra_config`) is data, not template, and must render verbatim.

### I4 — reference sections (reference-freeze ↔ extraction-check hybrid ↔ lifts)

The reference exposes `sections` as an ordered list of `{ name; text; }` and
`tmuxConf` as the `writeText` of their concatenation. Boundaries, by the first
source line of each (at `e79e925`):

| name | first line | first bytes of the section |
|---|---|---|
| `base` | 929 | `# === Base Settings ===` |
| `keys` | 1002 | `# Config reload` |
| `status` | 1202 | `# Window titles` |
| `hooks` | 1371 | `# Reflow hooks: clear stale hooks first …` |

- Every section's text ends with `\n` and the next begins at column 0.
  Splitting a `''` literal at these lines changes what Nix de-indents per
  piece, so the freeze is verified by `concat(sections) == tmuxConfText` at
  the base — the identity diff, run before anything else in this list.
- The hybrid render for cut *k* is `go(template up to and including
  section k) ++ concat(sections after k)`; a cut boundary in the `.tmpl` file
  is the same byte position as the section boundary.
- The section list is scaffolding for the lift and the check; after `lift-d`
  the check compares whole files only, and `sections` may be folded back
  into one string — or kept, since the reference dies at step 3 either way.

### I5 — `config/tmux.conf.nix` argument and return interface

- Arguments: unchanged set **plus** `persistWireScript ? null`; `extraConfText`
  narrows to user text; `enrichIcons` is **raw** (single `#`, overrides only)
  — the module stops doubling and `tmux.conf.nix` stops un-doubling, in the
  same move (I8). The four direct imports in `flake.nix` pass none of these
  three and keep working unchanged.
- Return: `{ tmux-wrapped tmuxConf script og mkOg ogVerbSpec }` plus
  `configToml` (D1), `pathsToml`, `referenceConf` (check-only), and — during
  the lift — the generator-side candidate the check diffs. `tmuxConf` is
  always a store path to a regular file.
- The one-authority invariant: each of the § Scope ten (`enrichEnable`,
  `enrichProviders`, `enrichIcons`, `enrichPrRefreshSeconds`,
  `enrichPrCheckRefreshSeconds`, `notifyEnable`, `agentUsageRefreshSeconds`,
  `splashRemote`, `claudeStatusAssumeDeadAfter`, `processIcons`) is one
  function argument read by both the serializer and the script builders.
  Nothing re-derives one from the other.

### I6 — `og-generate` CLI (generator ↔ nix-serializer ↔ og-generate-verb ↔ extraction-check)

```
og-generate --config FILE (--paths FILE | --prefix DIR) --out DIR
            [--template FILE]
```

- `--template` defaults to the location Nix injects at build; the check and
  the hybrid mode always pass it explicitly. Go cannot `embed` a file outside
  its module, and the template's home is `config/` by the spec, so the read
  is at run time.
- There is no OS flag: the `copy-command` line comes from the `platform` key
  in `config.toml` (I1, D2), so the CLI surface is the config, the paths and
  the output — nothing a caller could set inconsistently with the file.
- Writes `DIR/tmux.conf` and nothing else. Non-zero exit and the I9 message on
  a VS16 icon; non-zero on any missing required path key or unknown config
  key.
- The verb entry is `{ target = "${og-generate}/bin/og-generate"; summary; }`
  under the key `"generate"` — a bare single-token verb with no subverbs, so
  `og generate --help`'s last line is the target path, which the existing
  `og-dispatch-assertions` check asserts is executable. `ogPartitioned` and
  `og`'s `mapAttrs` filter on `v ? script`.

### I7 — the persist block (hm-caller-edit ↔ reference-freeze ↔ lift-d ↔ extraction-check)

Emitted when `persist_wire_script` resolves, else nothing, at the byte
position `${extraConfText}` occupies today (after `carouselHooks` and one
blank line, before `extra_config`):

```
\n# === tmux-remux (Phase 2a, opt-in via programs.lazytmux.persist) ===\nrun-shell "<persist_wire_script> #{q:version}"\n
```

Three copies of these bytes exist by design: the reference (the old way, so a
module user's output is unchanged from `hm-caller-edit` onward), the generator
(the new way), and a `grep -Fx` literal in the check on matrix entry 10. The
third exists because the first two are compared only to each other — a
transcription error shared by both would pass the diff. The `#{q:version}` is
literal text; nothing expands it at generate time.

### I8 — enrich icon dialects (hm-caller-edit ↔ nix-serializer ↔ lift-b ↔ lift-c)

- Input everywhere: raw glyphs, single `#`, overrides only.
- Doubled (`#`→`##`) form, **user-supplied keys only**, merged over raw
  defaults: the two `status-format[0]` arguments `--icon-linear` /
  `--icon-github` (line 1290). Doubling the whole merged set is byte-identical
  today only because no default contains `#`; the check cannot see the
  difference, so the rule is honoured, not approximated.
- Raw form: the nine `--icon-*` arguments of the enrich card (1141-1150) and
  the `lib-enrich` substitution, which after this step reads
  `enrichIconDefaults // enrichIcons` with no un-doubling.
- A side missed yields `####` or a bare `#` in tmux-format context; matrix
  entry 9's literal `#` override is the only cover.

### I9 — VS16 rejection message (generator-config ↔ Nix `processIcons`, lines 123-137)

Same bytes as the `builtins.throw`, names joined by `, ` in sorted key order:

```
process-icons: VS16 emoji (U+FE0F) cause alignment bugs in the picker.
Strip the trailing ️ from: <k1>, <k2>
See: charmbracelet/lipgloss#55
```

Nix's message carries the de-indented body of the `''` literal with one
trailing newline; the Go message ends the same way. Neither path strips.
