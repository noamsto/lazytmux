# `og` dispatcher — step 1 of the tmux-og migration

Spec for step 1 of `2026-09-09-tmux-og-rename-and-packaging-design.md`:

> Add the `og` dispatcher over today's scripts. No rename, no behavior change,
> both names on PATH. Pure addition.

Steps 2-4 (extract `og generate`, flip the name, Homebrew) are out of scope.

## The invariant this step must not break

A user who never types `og` must not be able to tell this landed. Every
existing script keeps its current name, its current store path, and its
current behaviour. `og` is a second front door onto the same derivations, not
a new layer between tmux and its scripts. Nothing in `config/tmux.conf.nix`'s
binds, hooks or `#()` jobs changes to route through `og`.

The sharp end of that invariant is `lztmux-remote-picker`. Its presence on a
remote host *is* the bridge capability probe
(`scripts/lztmux-remote-picker.sh:227` runs `command -v lztmux-remote-picker`
over ssh). It must not move, be wrapped, or become a shim — a rename there
makes every un-rebuilt remote report "remote lazytmux too old".

## Decision 1 — the subcommand tree

### The rule

The dispatcher's value is that it is **enumerable**. Wrapping all 41
executable scripts would be 41 scripts on PATH with three extra characters of
typing, and `og help` would not fit on a screen. So the tree is a *curated*
public API. It is not derived from a single mechanical predicate, and claiming
otherwise would be the easiest way to get it wrong. It is built in three
layers, two mechanical and one editorial, and the editorial layer is listed
exhaustively so a reviewer can check it rather than re-derive it.

**Layer 1 — necessary condition (mechanical, excludes).** A script gets no
verb if it is pure tmux plumbing: a `#()` status job, a `set-hook` body, a
keybind-only helper, a background poller reached only by `--tick`, or a
provider implementation dispatched by a parent script. These are reached only
by store-path interpolation from `config/tmux.conf.nix` and a human running
one standalone gets nothing useful.

**Layer 2 — sufficient condition (mechanical, includes).** Every script in
`modules/home-manager.nix`'s `home.packages` block gets a verb. That block is
exactly the set this project already puts on a user's PATH under its own name
for an external caller — agent hooks, an external tool's config, a sibling's
`command -v`. There are **12**: `claude-status`, `claude-status-update`,
`cursor-status-hook`, `cursor-hooks-install`, `cursor-relaunch-hooks-install`,
`cursor-relaunch-stamp`, `codex-relaunch-stamp`, `tmux-issue-stamp`,
`tmux-issue-stamp-linear`, `tmux-issue-stamp-github`, `tmux-pr-enrich`,
`lztmux-remote-picker`.

Layer 2 is what resolves the provider-implementation question, which layer 1
alone gets wrong: `tmux-issue-stamp-linear` and `tmux-issue-stamp-github` are
provider impls dispatched by `tmux-issue-stamp`, exactly as
`tmux-agent-usage-{claude,codex,cursor}` are dispatched by `tmux-agent-usage`.
The enrich providers get verbs and the usage providers do not, and the *only*
reason is that the enrich providers are in `home.packages`
(`modules/home-manager.nix:1037-1038`) and the usage providers are not. That
is a fact about who already calls them by name, not a taste judgement.

**Layer 3 — editorial (judged, exhaustive).** Ten more scripts get verbs
because a human initiates them directly. Each is listed with its reason; there
is no rule to re-derive, and anything not on this list and not in layer 2 is
internal:

| Script | Why it earns a verb |
|---|---|
| `lztmux-remote-open` | a human opens a remote session; also exec'd by name from the picker |
| `lztmux-remote-detach` | a human detaches a bridge; the CLI twin of the picker's stop |
| `lztmux-remote-auth` | a human runs one interactive ssh handshake |
| `lztmux-remote-theme` | a human fans a theme change out to mirrors |
| `tmux-session-picker` | flagship UI; the thing a stranger types first |
| `tmux-window-picker` | flagship UI |
| `tmux-window-wall` | flagship UI |
| `lztmux-notify` | a human sends a test notification while wiring routing |
| `lztmux-notify-center` | a human opens the notification history |
| `lazytmux-debug` | a human runs it to diagnose; that is its whole purpose |

That gives **22 verbs over 22 scripts**, leaving **19 of the 41 executables**
internal.

### The tree

Noun first, then verb, capped at two tokens. Two tokens is the whole depth of
the tree, which keeps resolution trivial (try two tokens, then one) and keeps
every verb typeable without a reference card.

| Verb | Target script | Layer |
|---|---|---|
| `og status` | `claude-status` | 2 |
| `og status update` | `claude-status-update` | 2 |
| `og remote open` | `lztmux-remote-open` | 3 |
| `og remote picker` | `lztmux-remote-picker` | 2 |
| `og remote detach` | `lztmux-remote-detach` | 3 |
| `og remote auth` | `lztmux-remote-auth` | 3 |
| `og remote theme` | `lztmux-remote-theme` | 3 |
| `og pick session` | `tmux-session-picker` | 3 |
| `og pick window` | `tmux-window-picker` | 3 |
| `og pick wall` | `tmux-window-wall` | 3 |
| `og issue stamp` | `tmux-issue-stamp` | 2 |
| `og issue linear` | `tmux-issue-stamp-linear` | 2 |
| `og issue github` | `tmux-issue-stamp-github` | 2 |
| `og pr` | `tmux-pr-enrich` | 2 |
| `og cursor hooks` | `cursor-hooks-install` | 2 |
| `og cursor relaunch` | `cursor-relaunch-hooks-install` | 2 |
| `og cursor stamp` | `cursor-relaunch-stamp` | 2 |
| `og cursor status-hook` | `cursor-status-hook` | 2 |
| `og codex stamp` | `codex-relaunch-stamp` | 2 |
| `og notify` | `lztmux-notify` | 3 |
| `og notify center` | `lztmux-notify-center` | 3 |
| `og debug` | `lazytmux-debug` | 3 |

`og status` / `og status update` and `og notify` / `og notify center` are the
two places where a one-token verb and a two-token verb share a first token.
Longest-match resolution is what makes them coexist, and both pairs are
acceptance criteria precisely because they are the case a naive dispatcher
gets wrong.

`og pr` is a background poller driven by `--tick`, not an interactive command;
it earns its verb through layer 2 only. Its help summary must say so, or the
verb reads like something a user should run. `og cursor relaunch` (an
installer) and `og cursor stamp` (a stamper) are confusable neighbours; the
two-token depth cap is what prevents the clearer
`og cursor relaunch install` / `og cursor relaunch stamp`, and that is a real
cost of the cap, accepted here in exchange for trivial resolution.

### Deliberately absent

- **`og generate`, `og init`, `og doctor`.** The design doc proposes all
  three. None exists yet, and step 1 is a dispatcher over *today's* scripts.
  The names are reserved by not being used, not by being stubbed.
- **`og usage`.** `tmux-agent-usage` was a candidate and is deliberately not a
  verb. Its sole call site is `config/tmux.conf.nix:1053`, a `#()` status job
  passing `--tick`, and it is not in `home.packages` — layer 1 excludes it and
  layer 2 does not rescue it. Run bare it is a background poller a human has
  no reason to invoke.
- **`og worktree *`.** `tmux-worktree-match` and `tmux-reconcile-window` are
  worktrunk-hook helpers reached by store path from the module's generated
  worktrunk config. No external caller names them, and they are not in
  `home.packages`.
- **The Go mains.** `tmux-picker-generate`, `tmux-splash`, `tmux-statusline`,
  `agent-detect`, `lztmux-remote-bridge`, `lztmux-remote-bridge-{ctl,daemon,renderer}`
  and the enrich card are all reached by store path from tmux config, a parent
  script, or a `respawn-pane` argv. None is typed by a human, so none gets a
  verb. Where a shell script already fronts one (`og pick session` →
  `tmux-session-picker` → `tmux-picker-generate --tui`), the verb points at
  the shell script, unchanged.
- **The 19 internal scripts**, listed for the record so a reviewer can check
  the partition rather than guess it: `tmux-agent-usage`,
  `tmux-agent-usage-claude`, `tmux-agent-usage-codex`,
  `tmux-agent-usage-cursor`, `tmux-apply-theme-colors`,
  `tmux-branch-display`, `tmux-default-size`, `tmux-dir-display`,
  `tmux-float-refit`, `tmux-kill-pane-guard`, `tmux-reconcile-window`,
  `tmux-reflow-windows`, `tmux-scratchpad`, `tmux-smart-nav`,
  `tmux-splash-maybe`, `tmux-update-icons`, `tmux-window-nav`,
  `tmux-worktree-match`, `lazytmux-log-event`.

22 verbs + 19 internal = 41 executables. The 7 `lib-*.sh` files are not
executables at all — they are built with `pkgs.writeShellScript`, which
produces a bare store file with no `bin/`, and are sourced by absolute path.
48 files, 7 libraries, 41 executables, 22 with verbs.

## Decision 2 — the dispatcher is a bash script, not a Go main

Step 1's dispatcher is a routing table. Shell routes; that is what it is for.

Three reasons beyond that:

1. **The substitution seam is a Nix string-replacement mechanism.** Every
   script in this repo reaches its store paths through
   `builtins.replaceStrings` over `@placeholder@` tokens. A shell dispatcher
   joins that mechanism with one more placeholder. A Go dispatcher would need
   store paths injected by `ldflags` or a generated `.go` file — new
   machinery, for the same result.
2. **`og` will never be the single-artifact story anyway.** The design doc's
   open question — whether `og` absorbs the Go mains — is constrained by the
   bridge renderer, which is respawned per pane and must stay small. Step 1
   dispatches to the Go mains as separate binaries and leaves that question
   where the doc left it.
3. **The public contract is the verb tree, not the language.** Step 2 needs
   real logic (TOML parsing, templating) for `og generate`; it can add a Go
   binary that the shell dispatcher execs, or replace the dispatcher
   wholesale. Either is contained, because what step 2 must not break is
   `og <noun> <verb>`, which is language-agnostic.

## Decision 3 — discoverability

- **`og` with no arguments** and **`og help`** print the same thing: a usage
  line, then every verb grouped by noun with a one-line summary each. Exit 0.
- **`og <noun>`** where the noun has sub-verbs but no one-token verb of its
  own (`og remote`, `og pick`, `og issue`, `og cursor`, `og codex`) prints just
  that noun's verbs. Exit 0. `status` and `notify` are **not** in this list:
  each owns a one-token verb, so bare `og status` and bare `og notify` exec
  their targets rather than listing. That is longest-match being consistent,
  and it costs one thing worth stating: `og notify` alone execs
  `lztmux-notify` with no arguments, which `scripts/lztmux-notify.sh:31`
  makes a silent exit 0. A user hunting for `og notify center` that way finds
  nothing; `og help` is the discovery path, and it lists both.
- **An unrecognised token** prints `og: unknown command: <tokens>` on stderr
  plus a pointer to `og help`. Exit 2.
- **`og <verb> --help`** (or `-h`), when the flag is the *only* argument after
  the verb, prints that verb's summary and the absolute store path it would
  exec, and exits 0 **without running the target**.

That last rule is the one that needs a reason, because passing flags through
is the obvious default. It is wrong here: **not one of the 22 target scripts
implements `--help` today** (verified by grep across all 22). Passing it
through would at best print nothing and at worst be parsed as a positional
argument — `lztmux-remote-open --help` would try to open a bridge to a host
named `--help`. Intercepting is the only behaviour that is honest about what
exists today. When a target grows a real `--help`, step 2 can pass through;
until then the dispatcher is the only thing in the system that can answer the
question.

Everything else after the verb is passed through verbatim, including flags,
`--`, and empty arguments. `og status update issue add ENG-1` reaches
`claude-status-update issue add ENG-1` unchanged.

## Decision 4 — Nix packaging and the substitution seam

`config/tmux.conf.nix` already builds `script`, an attrset of
`name -> derivation` where every derivation has its placeholders already
substituted. That attrset **is** the seam. The dispatcher consumes those
outputs, so it never restates the substitution list (`@lib_icons@`,
`@lib_claude@`, `@claude_status_bin@`, ...) and cannot drift from it.

Shape:

- A new `scripts/og.sh`, real reviewable bash, holding all dispatcher logic
  and exactly **one** placeholder — `source @og_table@`, preceded by
  `# shellcheck source=/dev/null`. That is verbatim the seam this repo
  already uses for `source @lib_icons@` / `source @lib_claude@`
  (`scripts/tmux-update-icons.sh:8-11`), so it is known to survive the
  `shellcheck` and `shfmt` pre-commit hooks with the placeholder in place.
- **Not** an inline `declare -A OG_TARGET=(@og_targets@)`. That shape was
  tried and rejected empirically: bash parses it and `shfmt` accepts it, but
  `shellcheck` raises **SC2190** ("elements in associative arrays need
  index") on the bare placeholder, and the repo lints `scripts/*.sh` in
  `packages.lint` and in the pre-commit hooks. The `source` form has no such
  problem and additionally makes the generated table an independently
  inspectable store file.
- The sourced table is a Nix `writeText` file assigning into associative
  arrays the dispatcher pre-declares: `OG_TARGET[<verb>]=<store path>`,
  `OG_SUMMARY[<verb>]=<one-line summary>`, plus an `OG_ORDER` array fixing
  help output order (help must not depend on bash's hash iteration order, or
  `og help` output churns between builds).
- A single `ogVerbs` attrset in `config/tmux.conf.nix` mapping
  `"<verb tokens>" -> { target = script.<name>; summary = "..."; }`. The
  table file is generated from that one attrset, so a verb cannot exist in
  the routing table but not in help, or vice versa.
- An `ogInternal` list sits beside `ogVerbs` in `config/tmux.conf.nix`,
  naming the 19 scripts that deliberately have no verb, and an eval-time
  `assert` requires `scriptNames == (attrNames of ogVerbs' targets) ∪
  ogInternal` with no overlap. Recording the internal set *in Nix* rather
  than only in this document is what makes the partition checkable at all
  (see criterion 10), and an `assert` gates `nix build .#default` rather than
  only `nix flake check`.
- `og` is **not** a member of `scriptNames`. It is built by its own `mkOg`,
  not by the generic `mkScript` chain that reads `scripts/${name}.sh`, so the
  partition equation above is over the 41 pre-existing executables only and
  `og` does not have to be its own verb.
- **`mkOg` is a function, not a fixed derivation:** `mkOg = verbs: ...`,
  with `og = mkOg ogVerbs`. Both are exported. A check can then instantiate a
  dispatcher over a table of its own pointing at stub targets, which is the
  only way to test argument passthrough (criterion 7) without depending on a
  real script's behaviour. This mirrors `sixel-conf-assertions`
  (`flake.nix:483-492`), which re-imports `config/tmux.conf.nix` with
  different arguments for exactly this reason.
- `config/tmux.conf.nix:1301` currently reads
  `{ inherit tmux-wrapped tmuxConf script; }` and must also inherit `og` and
  `mkOg`, or neither `modules/home-manager.nix` nor the check can reach them.
- `og` is added to `home.packages` in `modules/home-manager.nix`
  unconditionally, so it is typeable in any shell, and exposed as
  `packages.og` in `flake.nix` — the precedent is `packages.codex-relaunch-stamp`
  (`flake.nix:991`), a single script derivation exposed for an outside
  consumer.
- **The wrapped tmux's `--prefix PATH` list is left alone.** An earlier draft
  added `og` to it. That is unnecessary and is now deliberately not done: a
  pane's PATH already carries `og` through the user profile that
  `home.packages` populates, and nothing inside tmux — no bind, no hook, no
  `#()` job — may route through `og` under this step's invariant. Leaving
  `config/tmux.conf.nix:1296` untouched means this PR edits **no existing
  line** of the wrapper, which is the strongest form of the additive-only
  guarantee available.

`og` being in `home.packages` unconditionally while its *targets* are gated
(`agentIntegration`, `enrich`, `cursorStatus`, `exposePickOnPath`) is
deliberate: the dispatcher execs an absolute store path rather than resolving
a name, so a verb works even when its target is not itself on PATH. That is
additive — it takes nothing away.

One target is an exception and must be stated rather than glossed.
`scripts/cursor-status-hook.sh:6-10` locates `claude-status-update` as a
sibling of `$0`, falls back to `command -v`, and **exits 0 silently** if
neither resolves. `writeShellScriptBin` gives every script its own
single-binary store directory, so the sibling lookup never succeeds. Under
`og cursor status-hook` on a host with `agentIntegration.enable = false`, the
verb is therefore a silent no-op.

It is also barely reachable: `modules/home-manager.nix:1011-1017` already
asserts `cursorStatus.enable → agentIntegration.enable`, so a host configured
through the module cannot land in that state at all. It takes bypassing the
module to get there.

This is **not** a regression and not something `og` introduces: `$0` is the
same `cursor-status-hook` store path whether it is invoked through the
dispatcher or directly from PATH, so the fallback resolves identically both
ways. `og` neither creates the hazard nor fixes it, which is what additive-only
requires. Fixing it would mean editing `cursor-status-hook`, which belongs in
step 2 or 3. Recorded here so step 2 inherits it as known work rather than
discovering it.

## Amend the design doc

`2026-09-09-tmux-og-rename-and-packaging-design.md`'s "Genuinely crosses a
machine boundary" list names three things. It is the list step 3's hard flip
will be planned against, so it must be complete for the class it describes:
**any name this repo or one of its flake inputs owns that is sent to a
remote host by bare name**.

The ownership qualifier is load-bearing, and an earlier draft omitted it. The
literal class "any binary name sent to a remote" also captures `sh`, `id`,
`uname`, `hostname`, `cat`, `stat`, `getconf`, `ps`, `mktemp`, `find`, `rm`,
`printf`, `systemctl`, `launchctl` and more — all genuinely sent, none of
them renameable by anyone here. POSIX, coreutils and init-system names are
**explicitly out of class**. Recording that is what lets a future auditor tell
an omission from a name that was never in scope; without it, "complete" is
unfalsifiable.

Audit result — the class has seven more members, of which the ownership split
is the thing that matters:

**Sent by the Go daemon:**

- `tmux-claude-images` — `picker/remotebridge/daemon/ctl.go:405` runs
  `command -v tmux-claude-images` in a shell on the remote, and `:416` names
  it again in the not-found message. Owned by the `aeye` input, not this repo.
- `theme-toggle` — `ctl.go:323` and `:339`, same shape. Ships from the desktop
  profile; not this repo's to rename.
- `prdash`, `lazygit`, `yazi` — `ctl.go:153-164` send bare tool names for the
  mirror's tool binds. Third-party.

**Sent by the shell launchers and by `picker/`:**

- `tmux` — the single most-sent name, and **this repo owns it**.
  `config/tmux.conf.nix:1289-1298` builds `tmux-wrapped`, whose only binary is
  `$out/bin/tmux` with `meta.mainProgram = "tmux"`, installed by
  `modules/home-manager.nix:1022`. It is sent to the remote by bare name with
  a hard-coded per-user-profile fallback from `picker/remote.go:39` (used by
  the session probe and by `picker/remote_resources.go`'s resource fetch),
  from `scripts/lztmux-remote-open.sh:129`, from
  `picker/remotebridge/cmd/daemon/main.go:169` (`LZTMUX_BRIDGE_TMUX` defaults
  to the bare name), and inside `ctl.go`'s own resolve scripts at `:371`,
  `:397`, `:403`, `:413`. Like the startup unit, it survives the rename only
  because the name carries no project prefix — step 3 must keep it that way
  deliberately.


- `tmux-startup.service` and `org.nix-community.home.tmux-startup` —
  `scripts/lztmux-remote-open.sh:224-231` restarts the remote's startup unit
  by name over ssh on the cold-start path. **Owned by this repo's
  home-manager module.** It survives the rename only because the name carries
  no project prefix; step 3 must keep it that way deliberately rather than by
  luck.
- `tmux-remux` — `lztmux-remote-open.sh:264` and `picker/remote.go:70` both
  run `command -v tmux-remux` on the remote. Separate repo.

**The audit surface is seven files, and better expressed as a recipe.** The
list is "files that execute code on a remote host, to be audited and then
filtered by ownership" — not "files that send owned names". That distinction
is why `graphics/fetch.go` belongs on it despite sending only `stat` and
`cat`, both out of class.
`ctl.go` and `lztmux-remote-open.sh` are where the interesting names live,
but four more files send names: `picker/remote.go`,
`picker/remote_resources.go`, `picker/remotebridge/graphics/fetch.go` (the
graphics fetcher's `ssh … sh -c` with `stat`/`cat`), `picker/remotebridge/cmd/daemon/main.go` — which holds `LZTMUX_BRIDGE_TMUX`'s
bare-name default at `:169` and the paste-upload script at `:140-158` — and
`scripts/lztmux-remote-picker.sh`, whose `:227` probe
(`command -v lztmux-remote-picker`) is the single most load-bearing
cross-boundary send in the repo and the reason this step must not move that
script. That
last one is the sharpest omission an earlier draft made: it cites that line
as evidence that `tmux` crosses, then leaves its file off the surface.

Rather than a file list that rots, the amendment records a two-leg recipe:

1. **Executed binaries** — grep `picker/**` and `scripts/lztmux-remote-*.sh`
   for `command -v`, `sh -c` and `ssh` invocations, then filter to names owned
   by this repo or its flake inputs. Seven files is what that leg returns
   today.
2. **Published environment** — grep for control-mode `set-environment`. This
   leg exists because `LZTMUX_RELAY_GRAPHICS`
   (`picker/remotebridge/daemon/relayenv.go:15`) is one of the design doc's
   original three cross-boundary names, is repo-owned, and crosses by being
   *written into the remote session's environment* rather than executed. Leg 1
   can never return it, so a single-leg recipe would silently drop a member
   the doc already knows about.

So the doc's three-item list is right about *what this repo's rename breaks*
but wrong about *what crosses the boundary*, and step 3 needs the second list
to reason about the first. The amendment splits the section into
"crosses and this repo owns the name" (the original three, plus the startup
unit and `tmux` itself) and "crosses, owned elsewhere" (`tmux-claude-images`,
`theme-toggle`, `prdash`, `lazygit`, `yazi`, `tmux-remux`), records the grep recipe that
regenerates the audit surface, and states the class explicitly so a
future audit can be re-run mechanically.

## Acceptance criteria

Every criterion names the mechanism that keeps it true. Criteria 1-8 are
covered by one new `nix flake check` derivation, **`og-dispatch-assertions`**,
following the `*-conf-assertions` shape (`flake.nix:452` `float-conf-assertions`
is the closest model) but *running the built `og` binary* rather than grepping
generated text. Criterion 7 additionally instantiates `mkOg` over a stub table,
the way `sixel-conf-assertions` (`flake.nix:483`) re-imports the config module
with different arguments.

| # | Criterion | Verified by |
|---|---|---|
| 1 | `og help` and bare `og` list all 22 verbs grouped by noun; exit 0 | check runs `og help`, greps every verb string |
| 2 | Every target path in the generated table exists and is executable | check iterates the table, `test -x` each |
| 3 | `og status update` resolves to `claude-status-update`, not `claude-status` with `update` as an argument; same for `og notify center` | check asserts `og status update --help` and `og notify center --help` name the two-token targets |
| 4 | `og remote picker` execs the *same derivation* `exposePickOnPath` puts on PATH — the bridge capability probe is unaffected | check compares the table's store path against `tmuxConfig.script.lztmux-remote-picker` |
| 5 | `og remote open --help` exits 0 and prints the target store path, without exec'ing the target | check asserts exit 0 and the path on stdout. *Not* "no ssh attempted" — there is no `ssh` in the sandbox and the code path returns before exec, so that phrasing was unobservable |
| 6 | An unknown token exits 2 with a message on stderr | check runs `og bogus`, asserts exit 2 and empty stdout |
| 7 | Arguments after the verb reach the target verbatim, including flags, `--`, and empty arguments | check builds `mkOg` over a stub table whose target is a `writeShellScript` echoing `"$@"`, then compares |
| 8 | The set of 41 executable script names is unchanged, and `og` is additional to it | check compares `attrNames script` against a pinned list of 41. This proves the *name set* is intact, not that each derivation is byte-identical — it is an additive-only tripwire, not a proof |
| 9 | **`og` is referenced nowhere in the generated tmux config** | check binds `CONF = tmuxConfig.tmuxConf` and asserts the `og` store path does not appear in it |
| 10 | The verb/internal partition covers `scriptNames` exactly, and stays so | eval-time `assert` in `config/tmux.conf.nix` over `ogVerbs` + `ogInternal`; gates `nix build .#default`, not just the check |
| 11 | `nix build .#default`, `nix flake check`, `nix build .#lint` all pass | the three gates |
| 12 | The design doc's cross-boundary section is amended: ownership-qualified class, out-of-class names recorded, grep recipe for the surface | review |

Criterion 9 replaces an earlier "the generated conf is byte-identical to
`main`'s, diffed against a pinned hash". That was unimplementable and a
correct implementation would have failed it: `tmuxConf` is almost entirely
interpolated `/nix/store` paths, so its hash moves on any `flake.lock` bump —
the commit immediately before this branch is `353d030 chore(deps): update
flake.lock` — and differs per system, while CI runs `nix flake check` on both
`x86_64-linux` and `aarch64-darwin`. One pinned hash cannot be valid on both.

Its stated justification was also wrong. It claimed the risk came from editing
the wrapper's `--prefix PATH` list, but `tmuxConf` is bound at
`config/tmux.conf.nix:1286`, three lines *before* `tmux-wrapped` at `:1289`,
and nothing in the wrapper feeds back into it. That risk could not occur.
Decision 4 now leaves that line untouched anyway.

What criterion 9 tests instead is the invariant that is genuinely at risk and
is cheap to check: tmux's own config must not learn about `og`. If a binder,
hook or `#()` job ever routes through the dispatcher, this step's promise —
that a user who never types `og` cannot tell it landed — is broken, and the
`og` store path appearing in `$CONF` is exactly that event.

Criterion 10 is what stops the 22/19 partition from rotting. A verb pointing
at a nonexistent script already fails at Nix eval, since `ogVerbs` references
`script.<name>`. The uncovered direction is a *new* script landing in
`scriptNames` with no verb-or-internal decision recorded, which is silent
today and is how a table like this decays.

## Out of scope

- Any change to an existing script's behaviour, name, or PATH position.
- Routing tmux's own binds, hooks or `#()` jobs through `og`.
- `og generate`, `og init`, `og doctor`.
- Absorbing the Go mains.
- Shell completion for `og`.
