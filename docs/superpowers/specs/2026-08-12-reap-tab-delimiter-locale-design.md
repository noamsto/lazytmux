# Design: tab-delimited tmux `-F` formats collapse under a non-UTF-8 locale

Fixes #373. Also fixes #366 (the same flake, filed from the aarch64-darwin side).

## Problem

`checks.<system>.update-icons-resume-guard-tests` fails intermittently — measured
1-in-8 in the issue, 1-in-7 here. It has gone red on `main` and blocked two
unrelated PRs, so `nix flake check` is currently an unreliable gate for the repo.

The signature is that no `@remux_relaunch` write happens at all: the two tests
that expect a write fail together with `invalid option: @remux_relaunch`, and the
two that pass trivially when no write happens pass.

## Root cause (established, not hypothesised)

**tmux rewrites a literal TAB to `_` in `-F` output whenever the tmux *client*
running the command has no UTF-8 locale.** Verified directly, including that the
*client*, not the server, decides:

```
$ env -u LANG -u LC_ALL -u LC_CTYPE tmux list-panes -a -F $'#{pane_id}\t#{pane_current_command}'
%0_bash                          # tabs gone
$ LC_ALL=C.UTF-8 tmux list-panes -a -F $'#{pane_id}\t#{pane_current_command}'
%0<TAB>bash
$ env -u LANG -u LC_ALL -u LC_CTYPE tmux list-panes -a -F '#{pane_id}|#{pane_current_command}'
%0|bash                          # '|' survives
# server started under LC_ALL=C.UTF-8, queried by a stripped-locale client:
%0_tmux                          # still mangled -> the CLIENT decides
```

`scripts/tmux-update-icons.sh:58` fetches the presence-sweep rows with a
literal-TAB `-F` format. **Two** parsers consume that one string:

- `scripts/lib-claude.sh:118` — `claude_reap_dead_panes`, `IFS=$'\t' read -r pid rest`
- `scripts/tmux-update-icons.sh:68` — the arm/stamp loop, `IFS=$'\t' read -r pid cmd piped`

With the tabs rewritten, the whole row lands in `$pid`. The reap's live-set is
keyed `0_bash_0`, so **no real pane id is ever marked live and every file under
`panes/`, `screen/`, `interrupt/`, and `watchers/` is deleted**; the arm/stamp
loop sees an empty `$cmd`, so nothing ever matches `AGENT_COMMANDS` and neither
agent-detect arming nor the live-presence stamp runs.

The `update-icons-resume-guard-tests` derivation deliberately pins no
`LANG`/`LC_ALL`, so the nix sandbox is exactly that stripped locale.

### Why it presents as a race

It isn't one. `arm_agent_detect` throttles the sweep with
`((CLAUDE_NOW % 5)) && return 0` (`tmux-update-icons.sh:51`), so the destruction
only happens on one wall-clock second in five. That throttle is the whole of the
intermittency, and it is *run*-global rather than *test*-global because all four
bats tests execute inside the same second bucket.

The issue read the correlated 2+3-fail / 1+4-pass split as evidence of a
run-global cause. It is weaker evidence than it looks: tests 1 and 4 assert "the
stamp is unchanged" and "no write was issued", so a run that deleted the state
dir satisfies them **vacuously** — they cannot go red on a skipped stamper at
all. The load-bearing evidence is the trace below, not the split.

A `bash -x` trace of a failing `nix build` confirms the chain end to end:

```
+ claude_reap_dead_panes %0_bash_0
+ id=0
+ [[ -n '' ]]
+ rm -f /build/.../claude-status/panes/0
++ [[ -f /build/.../claude-status/panes/* ]]   # glob no longer matches
++ continue
```

With `panes/0` gone, `claude_pane_ids` yields nothing, the stamper's
`read_pane_state … || continue` is never reached, and no write is issued.

### Candidates ruled out

- **`claude_prune_stale_state`'s whole-second comparison** (the issue's prime
  suspect). The same trace shows it computing `mt=1786524360` against
  `server_start=1786524360` and correctly keeping the file: the fixture is
  written *after* the server starts, and `((mt < server_start))`
  (`lib-claude.sh:98`) keeps equality. Not implicated.
- **`list-panes -s` returning nothing while the server settles.**
  `tests/update-icons-resume-guard.bats:62` already round-trips `list-panes` in
  `setup()` before any test body runs, so the server and pane are proven present.
- **`arm_agent_detect`'s watchers racing the state dir.** `arm` and `stamp` are
  both 0 in this harness (unsubstituted `@agent_detect_bin@` /
  `@assume_dead_after@`), so the reap is the only effect that fires.

### Why every existing test missed it

Nothing crosses the seam between *these* `-F` format strings and their parsers.
`tests/prune-stale-state.bats:74,91,105` hand-builds tab-separated rows;
`tests/agent-liveness.bats:207` installs a fake `tmux` that `printf`s real tabs.
Both feed the parser exactly the bytes it wants, so the producer is never
exercised.

The repo has already learned this lesson once, for a different script:
`tests/worktree-match-integration.bats:63-78` ("the field delimiter survives a
non-UTF-8 locale") tests it with real tmux under `LC_ALL=C`, and
`flake.nix:569-574` explains why that check pins no locale. Four call sites never
got the memo.

## Scope of the defect

Four sites in `scripts/` — two destructive, one latent, one dead code:

| site | consumer | consequence under a stripped locale |
|---|---|---|
| `tmux-update-icons.sh:58` | `claude_reap_dead_panes` (`lib-claude.sh:118`) **and** the arm/stamp loop (`tmux-update-icons.sh:68`) | **destructive** — wipes all `panes/`/`screen/`/`interrupt/`/`watchers/` state every 5s; agent-detect arming and the live stamp never fire |
| `claude-status-update.sh:74` | `cleanup_stale_panes` | **destructive** — the "map is non-empty ⇒ don't bail" guard does *not* save it: the single mangled key makes the map non-empty, so every `panes`/`issues`/`tasks`/`names`/`images`/`screen` file is deleted |
| `tmux-float-refit.sh:26` (`$'…\t…'`) | the refit loop at `:19` | **latent** — `geom` is empty, so `yoff` is empty and every float `continue`s: the #372 float-refit feature silently becomes a no-op. Reached from the `window-resized` hook |
| `tmux-reflow-windows.sh:223` | `win_procs` | **none** — `win_procs` is declared at `:112`, written at `:221`, and **read nowhere** in the repo. The loop is dead code (reflow deliberately does not own icon content — see the comment at `:227`) |

### Seven more sites, deliberately deferred

The same defect exists outside `scripts/`. This PR does **not** fix them, and the
new guard's coverage is scoped to match — the guard scans `scripts/` and claims
nothing more, so it is not a false green:

- `modules/home-manager.nix:1364` and `:1419` — the worktrunk `post-switch` /
  `post-remove` hooks, `list-windows -F '…\t…'` piped to `awk -F'\t'`. Under a
  stripped locale `wt switch` stacks a duplicate window every time and
  `wt remove` never kills the worktree's window — verbatim the symptom
  `tmux-worktree-match.sh:35` was pipe-ified to prevent. Deferred because it is a
  user-config surface outside the task doc's scope **and** because `|` there
  needs the fail-closed field-count handling `tmux-worktree-match.sh:58-61`
  already has (`pane_current_path` can legally contain `|`) — a design question
  that deserves its own PR, not a drive-by.
- `picker/main.go:125,185,224,736` (`\t`-joined formats split by
  `strings.SplitN(line, "\t", N)`) and `picker/statusline/main.go:55` (fields
  joined with `US`/`0x1f`). `picker/**` is held by another worker.

Both groups get a filed follow-up issue, referenced from the PR.

## Where this actually bites

Stated honestly, because the fix's justification should not rest on a guess:

- **Proven**: the nix build sandbox. That is not hypothetical — it is the
  observed CI failure, on x86_64-linux *and* aarch64-darwin.
- **Not observed on this host**: the running tmux server here carries
  `LANG=en_US.UTF-8`/`LC_ALL=en_US.UTF-8`, so its `#()` forks are fine. An
  earlier draft of this spec claimed the systemd startup unit leaves the server
  locale-free because it imports only `DISPLAY`/`XDG_*`/`COLORTERM`/`TERM`/
  `TERMINFO` (`modules/home-manager.nix:63-71`); that import list is real, but
  systemd propagates `LANG` from `/etc/locale.conf` independently, and the claim
  was not verified. It is withdrawn.
- **Plausible but unverified**: any tmux server whose environment lacks a UTF-8
  locale — the darwin launchd agent, a minimal `ssh`/`cron` invocation, a user
  who sets `LC_ALL=C`.

So the product fix is justified on the ground that **correctness must not depend
on the ambient locale**, and that when the dependency is violated the failure is
silent and total (all agent state deleted), not on a claim that every host is
currently broken.

## Goals

1. Remove the defect at all four `scripts/` sites so the parsers are
   locale-independent; name and file the seven outside it. No
   `sleep` and no ordering dependency — the fix removes the dependency itself.
2. Pin the format↔parser seam with a test that uses **real tmux output under the
   sandbox's stripped locale**, and a repo-wide source assertion so a future
   regression to a whitespace delimiter fails the build.
3. Make the flaky suite *deterministic*, not merely lucky: it must exercise the
   sweep on every run rather than one run in five.
4. Make the `@remux_relaunch` write site honest — `tmux set -pq` returns 0 and
   prints nothing even on a bad target — and make the failure observable where it
   is currently discarded.
5. Make a failed assertion report the option's actual value instead of
   `invalid option: @remux_relaunch`.
6. Demonstrate determinism by repeated `--rebuild` runs, reporting the count.

## Non-goals

- Pinning `LANG`/`LC_ALL` in the tmux-integration check derivations. The stripped
  locale is the hostile case and the repo deliberately keeps it; pinning would
  hide exactly this class of bug.
- Setting a locale in the systemd unit or launchd agent. That papers over a
  defect that must not depend on the environment at all.
- Auditing `picker/**` (held by another worker) or changing
  `config/tmux.conf.nix` bind-keys or the remote-bridge daemon.
- Changing the `((CLAUDE_NOW % 5))` throttle itself. It is correct; it only
  *revealed* the defect.

## Approach

**Delimiter.** Switch all four tab-delimited `-F` formats in `scripts/` and *all*
their parsers to `|`, the delimiter `tmux-update-icons.sh:161` and
`tmux-worktree-match.sh:37` already use. `|` is printable ASCII and survives both
locales (verified). The fields carried are pane ids (`%N`), process names, window
indices, `0`/`1` flags, and `@float_geom` (a space-separated
`<width> <height> <xoff> <yoff>`, values written as percentages or cells — see
`config/tmux.conf.nix:598-604`) — none can contain `|`.

`tmux-reflow-windows.sh:223` gets the same one-character change, **not** a
deletion. The loop is provably dead, but removing 12 lines from the repo's most
intricate script inside a PR whose job is unblocking CI trades review surface for
a benefit that can land on its own; the dead code goes in the same follow-up
issue as the deferred sites.

Update `claude_reap_dead_panes`'s doc comment, which states the contract as
`"%N<TAB>..."`.

**Determinism seam.** `CLAUDE_NOW` is assigned unconditionally at
`lib-claude.sh:25`, so a suite cannot pin the second and the `% 5` throttle stays
a coin flip. Make it an env seam — fork-free, same shape as
`CLAUDE_ASSUME_DEAD_AFTER` — and have `update-icons-resume-guard.bats` **export**
`CLAUDE_NOW=$(( $(date +%s) / 5 * 5 ))`, driving the sweep on **every** run
instead of one in five. Exporting is required (the script runs as a child), and
the value must be *floored from the real clock*, not an arbitrary constant: the
fixtures stamp `timestamp=$(date +%s)`, so a constant would desynchronize the
reader's clock from them. Flooring keeps `age ∈ [-4, 0]`, which is inert
everywhere in `read_pane_state`.

**Falsifiable tests.** With the sweep forced, tests 2 and 3 go red on the unfixed
script every time. Tests 1 and 4 do **not** — they assert "the stamp is
unchanged" and "no write was issued", which a run that wiped the state dir
satisfies vacuously. That is the same blind spot that let the bug ship, so both
gain an assertion that the pane's state file still exists after the run. Then all
four are capable of failing.

**Seam test.** A test that crosses the real producer→parser boundary: run the
script's own `list-panes -a` under a **forced** hostile locale
(`env -u LANG -u LC_ALL -u LC_CTYPE`) and feed the result to
`claude_reap_dead_panes`, asserting the live pane's file survives. Forcing the
locale rather than relying on the sandbox being stripped is what makes it red on
unfixed code in `nix develop` too — the trap the task doc names ("a green bats
loop is NOT evidence of a fix").

It does **not** duplicate the underlying tmux-behaviour pin from
`tests/worktree-match-integration.bats:74-78` ("a tab does NOT come back as a
tab"). That assertion already exists, and it is made there against the *shipped*
tmux (`mkTmux pkgs`, `flake.nix:575`), whereas this check builds against
`pkgs.tmux` (`flake.nix:546`) — so re-asserting it here would be a weaker copy of
a stronger existing test. This test pins **our** format↔parser contract instead.

**Honest write.** Drop `-q` from the `@remux_relaunch` set so a failed write
returns non-zero and says why, and stop discarding that stderr in the test
harness (`run_update_icons` currently sends it to `/dev/null`) — surface it in
the failure output. Both halves are needed: **a `#()` job's stderr is
`/dev/null`** (verified by probing `readlink /proc/self/fd/2` from inside a live
`#()` job), so in production the message is discarded by design and only the
non-zero return exists; the test is where it becomes observable.

**Guard.** A `checks.<system>.tmux-format-delimiter-assertions` derivation
(precedent: `float-conf-assertions:400`, `notify-conf-assertions:357`). The
invariant is **"a tmux format must not carry a tab or other C0 byte as a field
delimiter"** — stated that way rather than "must not be a TAB", because the
narrower rule would pass a regression to `\x1f`, which someone in this repo has
already reached for (`picker/statusline/main.go:55`). Two rules, both needed,
applied to every line in `scripts/` containing `#{`:

- **(a)** no C0 byte or DEL — but only counting a TAB that is **preceded by a
  non-TAB** or that appears **after the `#{`**. The bare form is unusable: this
  repo indents with tabs, and 11 files carry `#{`-bearing lines that begin with
  one. Verified: the qualified form matches exactly the three literal-TAB sites
  (`tmux-update-icons.sh:58`, `claude-status-update.sh:74`,
  `tmux-reflow-windows.sh:223`) and nothing else in `scripts/`.
- **(b)** no escape text `\t`, `\x09`, or `\x1f`. Verified: the only `#{`-bearing
  line in `scripts/` containing `\t` today is `tmux-float-refit.sh:27`, a site
  being fixed.

Rule (a) alone misses `$'…\t…'` (whose source bytes are printable); rule (b)
alone misses a literal TAB. Scanning whole files rather than `-F`-bearing lines
is what catches `-F "$FMT"` indirection (`tmux-reflow-windows.sh:211`, whose
`FMT` is defined 84 lines earlier) — the *definition* line carries the `#{`.

Deliberately **not** rejected: multi-byte UTF-8 (reflow's `FMT` legitimately
carries `├─`/`╰─`), and `\n` — `tmux-reflow-windows.sh:80` uses
`-p $'#{session_windows}\n#{@reflow_key}\n#{client_height}'` and must keep
working, because tmux splits the output buffer on LF before sanitizing, so a
newline separator is not mangled the way an embedded TAB is.

The scanner takes the directory to scan as an argument so the derivation can run
it twice: once over `${./scripts}` (must pass) and once over a fixtures directory
holding three deliberately-broken files — a literal TAB, a `$'…\t…'`, and a tab
hidden in a `FMT=` variable used as `-F "$FMT"` — each of which must be rejected.
The fixtures live outside `scripts/` (the real scan would reject them), and the
`$'…\t…'` fixture must **not** be tab-indented, or rule (a) catches it and it
stops proving rule (b).

**Legible assertion.** A helper that reads the option with a defaulted value and,
on mismatch, prints expected vs actual (`<unset>` when tmux has no such option).

## Changes

| file | change |
|---|---|
| `scripts/tmux-update-icons.sh` | `-F` tab → `\|` (`:58`); both parsers' `IFS` (`:68`, and the doc'd contract); drop `-q` from the `@remux_relaunch` set (`:198`) |
| `scripts/lib-claude.sh` | `claude_reap_dead_panes` `IFS` + doc comment; `CLAUDE_NOW` env seam |
| `scripts/claude-status-update.sh` | `-F` tab → `\|` and its `IFS` |
| `scripts/tmux-float-refit.sh` | `-F` `$'…\t…'` → `\|` and its `IFS` |
| `scripts/tmux-reflow-windows.sh` | `-F` tab → `\|` and its `IFS` (`:223`) |
| `tests/update-icons-resume-guard.bats` | export a pinned `CLAUDE_NOW`; legible assertion helper; capture + surface update-icons stderr; state-file-survives assertions in tests 1 and 4; new seam test under a forced hostile locale |
| `tests/prune-stale-state.bats` | reap fixtures tab → `\|` (`:74,91,105`) |
| `tests/agent-liveness.bats` | **both** fake-tmux blocks (`:207` and `:290`) |
| `tests/agent-detect-arm.bats` | fake-tmux row (`:16`) — feeds the arm/stamp parser |
| `tests/claude-issues.bats` | `PANE_LIST` fixtures (`:361,369,378,386,408,416`) + the TSV comment at `:344` — feeds `cleanup_stale_panes` |
| `flake.nix` | new `tmux-format-delimiter-assertions` check (precedent: `float-conf-assertions:400`, `notify-conf-assertions:357`) |
| `CLAUDE.md` | document the `\|`-delimiter convention and the locale hazard |
| `docs/superpowers/plans/` | plan doc committed with the code |

## Acceptance criteria

- [ ] `claude_reap_dead_panes` keeps a live pane's state file when fed rows from a
      real `tmux list-panes -a` run under a **forced** hostile locale — red on
      unfixed code in `nix develop` as well as in the sandbox.
- [ ] With `CLAUDE_NOW` exported as a floored multiple of 5, resume-guard **tests
      2 and 3 fail on the unfixed script every run** and pass on the fixed one —
      the red-before-green is exhibited, not assumed. (Tests 1 and 4 cannot go
      red on a skipped stamper by construction, which is why they gain the
      state-file-survives assertion; with it, they too go red unfixed.) This
      red-before-green is **environment-bound**: it reproduces where the locale
      is hostile — the nix sandbox — not in `nix develop` on a UTF-8 host. Only
      the seam test, which forces the locale itself, is red in both.
- [ ] A new `checks.<system>.tmux-format-delimiter-assertions` rejects all three
      evasions, each proved by a deliberately-broken fixture: a literal TAB byte,
      `$'…\t…'`, and a tab hidden in a `FMT=` variable used as `-F "$FMT"`. Its
      coverage is `scripts/`, matching this PR's fix set.
- [ ] A deliberately broken expectation reports the option's actual value (or
      `<unset>`), not `invalid option: @remux_relaunch`.
- [ ] A failed `@remux_relaunch` write is visible: non-zero from `tmux set -p`
      and its stderr reaches the test's failure output.
- [ ] `nix build .#checks.x86_64-linux.update-icons-resume-guard-tests --rebuild`
      passes **≥ 30 consecutive runs**. Arithmetic uses the *measured* rate, not
      the theoretical one: at the observed 1-in-7, an unfixed tree survives 30
      runs with probability `(6/7)^30 ≈ 1.0%`. Report the observed count.
- [ ] `nix build .#default`, `nix flake check`, and `nix build .#lint` all pass.
- [ ] PR body carries `Closes #373` **and** `Closes #366`, states that the
      aarch64-darwin leg cannot be exercised locally, calls out the scope
      widening to the two extra scripts, and links the follow-up issue covering
      the seven deferred sites plus the reflow dead code.

## Risks

- **Scope widening beyond the task doc's four files.** `claude-status-update.sh`,
  `tmux-float-refit.sh` and `tmux-reflow-windows.sh` are the same defect class;
  leaving them ships a knowingly half-fixed product bug, and the new assertion
  check would fail on them anyway. None is on the held list. Called out loudly in
  the PR body rather than done quietly. The line is drawn at `scripts/` —
  `modules/` and `picker/**` are named, deferred, and filed.
- **A guard that under-claims is safe; one that over-claims is not.** Scoping the
  check to `scripts/` is deliberate: it must not advertise repo-wide coverage
  while seven known sites survive outside it.
- **`CLAUDE_NOW` becomes env-overridable.** A stray exported `CLAUDE_NOW` would
  freeze the clock for every script that sources lib-claude. Accepted: the name
  is namespaced and internal, the repo already exposes `CLAUDE_STATUS_DIR` and
  `CLAUDE_ASSUME_DEAD_AFTER` the same way, and `agent-liveness.bats:219` already
  pins `CLAUDE_NOW` — today only by the accident that the raw script never
  sources lib-claude. This makes an existing assumption explicit.
- **`|` is not IFS-whitespace; TAB is.** Empty middle fields therefore stop
  collapsing after the switch — an improvement, and the same reasoning already
  written up at `tmux-update-icons.sh:111-122` for the 20-field format. A row
  with an empty `pane_current_command` now parses `pane_pipe` correctly instead
  of shifting it left. Intentional, not incidental.
- **`pane_current_command` is a middle field.** "None of these fields can contain
  `|`" is a claim about the kernel's `comm`, not a guarantee. The exposure is
  identical to today's TAB and matches the existing `|` format at `:161`, so it
  is accepted — but it is not impossible.
- **`tmux-float-refit.sh`'s inner `read` must stay on the default IFS.** The
  outer `IFS='|' read` is an assignment *prefix*, which does not persist, so
  `read -r width height xoff yoff <<<"$geom"` at `:20` still splits `@float_geom`
  on spaces. Hoisting `IFS='|'` to a plain assignment would break the geometry
  split silently — and there is no float-refit test anywhere in `tests/` to catch
  it.
- **Dropping `-q`.** On a vanished pane tmux writes `no such pane: %N` to stderr
  and returns 1 (verified). The script is not `set -e`, and a `#()` job's stderr
  is `/dev/null` (verified by probing `/proc/self/fd/2` from inside a live job),
  so rendering cannot be affected. The exposure is also narrower than it looks:
  `tmux-update-icons.sh:175` already `continue`s for any pane absent from
  `pane_to_win`, so `:198` only ever targets a pane that was live at `:161` of
  the same invocation — the window is a sub-second race, not every dead pane.
