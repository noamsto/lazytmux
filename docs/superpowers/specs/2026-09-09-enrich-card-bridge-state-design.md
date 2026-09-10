# Enrich card reads bridge state in mirror windows — design

Closes #598.

## Problem

`prefix + i` inside a **mirror** window always renders "no issue / no PR", and
its branch/dir lines describe the *launcher's* cwd — a different repo on a
different host. Three separate causes, all confirmed against a live float
(`%296`, session `halo-lazytmux`, window `@88`, 2026-09-09):

1. `picker/enrichcard/` contains zero occurrences of `bridge`. It reads
   `@issue_*`/`@pr_*`/`@branch`/`@worktree`/`@git_root` — the *real* option
   names, which a mirror window does not carry by design.
2. The daemon deliberately stamps `@bridge_*` and never the real names
   (CLAUDE.md → "Remote Window Labels"): `tmux-reflow-windows` stamps the real
   names on every window of the mirror session, so a same-name write is a
   two-writer race the daemon loses on every reflow pass.
3. What a mirror window *does* carry under the real names is stale residue from
   the local `after-new-window` hook, which fired against the launcher's cwd.
   `@88`'s `@git_root` was `/home/noams/Data/git/factify/mono`.

And the shipped `@bridge_*` set is too small to fix (1) by a read alone. Nine
fields today: `@bridge_crew_name`, `@bridge_crew_color`, `@bridge_pr_number`,
`@bridge_pr_state`, `@bridge_pr_check_state`, `@bridge_pr_mergeable`,
`@bridge_pr_plain`, `@bridge_label_id`, `@bridge_label_rest_long`. No issue
identity, no URLs, no branch, no path. Teaching the card to read `@bridge_*`
gets it the PR badge and the composed label id only — `[o]`/`[p]` would still
have no URL to open.

## Measured facts this design rests on

**M1 — a tmux format can strip `|` remotely, inside a live subscription.**
Measured on tmux 3.7c, scratch server, control client over a pty:

```
set -w @t 'has|pipe and #[fg=red]markup'
refresh-client -B "t598sub:@*:#{window_id}|#{s/[|]/ /:@t}|tail"
-> %subscription-changed t598sub $0 @0 0 - : @0|new val #[fg=red]x|tail
```

`#{s/[|]/ /:@opt}` renders in both a `-F` read and a `%subscription-changed`
value, and the pipe is gone. The bracket expression is what makes it safe: a
bare `s/|/ /` is an ERE empty alternation. This retires the "only one free-form
field, and it must be last" constraint that `windowLabelFormat`'s comment
records — any number of free-form fields can be carried, each wrapped.

Three further properties, measured on the same server because D1 depends on all
three and none follows from the first:

- the substitution is **global**, not first-occurrence (`a|b|c|d` → `a b c d`);
- `#{?@worktree,#{@worktree},#{@git_root}}` resolves against window options and
  falls back correctly when `@worktree` is unset;
- that conditional **nested inside** the substitution —
  `#{s/[|]/ /:#{?@worktree,#{@worktree},#{@git_root}}}` — expands correctly,
  which is the exact form position 14 needs;
- an unset option through the substitution renders empty, not a literal.

**M2 — the local poller already refuses a mirror.**
`scripts/tmux-pr-enrich.sh:466`: single-target mode exits 0 when the target
window has `@bridge_win`. So `[r] refresh` in a mirror today spawns a process
that immediately exits — the "silently doing nothing" the task names.

**M3 — a mirror's `@pr_*` is only as fresh as the remote's own poll, and on a
headless remote there is no poll at all.**
`tmux-pr-enrich` is a `#()` job in `status-format[0]`, and a control-mode client
renders no status line — so a remote whose *only* client is this bridge never
runs it, and its `@pr_*` is frozen at whatever the last real client left behind.

The narrower claim matters and the wider one is false: the poller's full pass is
**server-wide**, not per-session (`scripts/tmux-pr-enrich.sh:405` iterates
`list-windows -a`, and `:416` skips only `@bridge_win` windows — which the
*remote's* own windows are not). So a remote with a real client attached to any
session does poll the bridged session's windows, on that remote's own
`prRefreshSeconds` schedule. What no remote ever has is an *on-demand* refresh
reachable from the mirror. CLAUDE.md states the same limit ("`@pr_*` is only as
fresh as the remote's own `tmux-pr-enrich` poll").

## Design

### D1 — extend `windowLabelFormat` in place; no second subscription

One format, one subscription, one backstop read, one parser — the existing
`windowLabelFormat` grows. Nothing new touches the stream, so no new
`claimSeq` path exists to get wrong.

Nine new fields, each wrapped in `#{s/[|]/ /:…}` per M1 so a pipe in any of them
cannot shift the row:

| position | source option | carried as |
|---|---|---|
| 8 | `@issue_provider` | `@bridge_issue_provider` |
| 9 | `@issue_id` | `@bridge_issue_id` |
| 10 | `@issue_url` | `@bridge_issue_url` |
| 11 | `@pr_url` | `@bridge_pr_url` |
| 12 | `@pr_draft` | `@bridge_pr_draft` |
| 13 | `@branch` | `@bridge_branch` |
| 14 | `#{?@worktree,#{@worktree},#{@git_root}}` | `@bridge_dir` |
| 15 | `@issue_title` | `@bridge_issue_title` |
| 16 | `@pr_title` | `@bridge_pr_title` |

Positions 0–7 keep their current meaning byte for byte. `@window_label_id` and
`@window_label_rest_long` move from 8/9 to **17/18** — the new fields are
inserted *before* them, so `@window_label_rest_long` stays last and keeps its
"a `|` lands inside it rather than shifting the row" property untouched. That
is deliberately the lower-risk half of M1: the new fields get the substitution,
the one field that already worked keeps working the way it already did.

Field count 10 → **19**; `strings.SplitN(line, "|", 19)`. The parser is already
positional-with-defaults (`at(i)`), so a short row still reads.

The dir field is resolved **on the remote** (`#{?@worktree,…}`), not by
re-implementing the card's `worktree || gitRoot` fallback over two carried
fields — one field, one authority.

### D2 — sanitize: display fields truncate, identity fields drop

`cleanLabelValue` truncates to a rune cap. That is right for a title and
**wrong** for a URL, a branch, a path or an id: a truncated URL opens the wrong
page, a truncated branch refreshes the wrong branch on the remote, a truncated
path names a directory that is not the one on screen. A silent wrong value is
worse than an absent one, and every consumer already handles absent (the card
renders "no issue", an inert `[o]`, and omits the dir line).

So a second cleaner: `cleanLabelValueExact(v, max)` — same `stripWindowName`,
same whole-value rejection of a leading `-` and of a bare `;`, but returns `""`
when the value exceeds the cap instead of cutting it.

| field | cleaner | cap | validator |
|---|---|---|---|
| `issue_provider` | exact | 16 | `lowerWordRe` |
| `issue_id` | exact | 64 | `^[A-Za-z0-9_-]+$` |
| `issue_url`, `pr_url` | exact | 512 | `^https?://\S+$` |
| `pr_draft` | exact | 1 | `^1$` |
| `branch` | exact | 255 | `^\S+$` |
| `dir` | exact | 4096 | `^/\S*$` |
| `issue_title`, `pr_title` | truncating | 120 | none (free-form) |

Caps are sized to their domain, not to a shared constant: 512 for a URL because
a Linear issue URL routinely passes 120, 255 for a branch because that is git's
own refname limit, 4096 for a path because that is `PATH_MAX`.

Known limit, accepted rather than worked around: `^/\S*$` rejects a path
containing a space, which is a legal worktree path. It is the same trade every
validator here makes — a value that fails is absent, and an absent dir line is
correct-but-incomplete, while a shifted row or an unquoted path reaching `sh`
is neither. Whitespace is what makes both the row and the remote body safe, so
it stays excluded.

`bridgeLabelOptions` — the one ordered list the stamp loop, the per-field diff
and `clear` all walk — gains the nine entries. That is the whole of teardown
(`clear` unsets every entry) and of the per-field diff; no new code path.

The unchanged-row check stays a struct compare on `labelRow`, and the cache
stays keyed on `(remote window id, local window)` — `retireMirror` re-adds the
same remote id against a fresh local window, and a row compare alone would
suppress the re-stamp. Nine more comparable string fields change neither.

### D3 — the card reads bridge state in a mirror, and *only* bridge state

`parseWindowOptions` learns the `@bridge_*` names plus `@bridge_win`, into
dedicated fields. A pure resolver then picks the effective `winState`:

- `@bridge_win` unset → the local values, exactly as today.
- `@bridge_win` set → the bridge values, with **no fallback to the local
  names**. A mirror's `@issue_*`/`@pr_*`/`@branch`/`@git_root` are the
  launcher's residue (cause 3); falling back to them is the bug, not a safety
  net. An older remote that stamps nothing therefore renders "no issue / no PR"
  and no branch/dir line — the documented fallback, and honest.

`@bridge_pr_number`/`_state`/`_check_state`/`_mergeable` already ship, so the PR
badge and `enrichstate.Classify` need no new fields; `@bridge_pr_draft` is the
one addition the badge needs.

Two fields stay **local on purpose**, and this is a decision rather than an
oversight: `@window_task` and `@window_claude_ago` derive from
`/tmp/claude-status/*` files that `agentShipper` already writes under *local*
pane ids, so they are correct in a mirror by a mechanism that already exists.

`@active_pane_icon` is a third case and a different one: it is stamped
**session**-scoped (`scripts/tmux-update-icons.sh:470`, `set -q -t '$s'`, and
the comment at `:193` says so), while the card's read is
`show-options -w` — window options only. So `w.paneIcon` is empty on *every*
window, mirror or not. That is a pre-existing dead field, not something this
change breaks or fixes; it is named here only so the next reader does not take
it as verified alongside the two claims above.

`detectBaseBranch` must **not run in a mirror.** It shells `git -C <dir>
symbolic-ref …` against the carried dir, which is a path on the *remote*. The
benign outcome is an error and an empty base; the malignant one is that the same
path exists locally as an unrelated repo — `/home/noams/git/lazytmux` exists on
both hosts — and the card renders a base branch read from the wrong checkout.
So `main.go` skips it when `@bridge_win` is set, and the branch line renders the
remote branch with no `→ base` arrow. Shipping the remote's base branch is a
non-goal (no remote option holds it).

### D4 — the bind: the card still launches locally; only the ctl handle is new

The task frames defect 1 as "the bind is not bridge-aware", by analogy with
`prefix + g/p/y` (`bridgedFloatTool`) and `prefix + x/&/d` (`bridgeGate`). Those
bind a *remote* action: the tool has to run where the cwd is, the kill has to
happen where the pane is. The enrich card has nothing to run on the remote — it
is a local reader of local window options, and after D1 those options carry the
remote's truth. **So the launch is not bridged, and `bridgeGate` is not added
to this bind.** The card fix is entirely inside the card.

The decisive argument is `[o]`/`[p]`: they shell `xdg-open`
(`picker/enrichcard/model.go:210`). Launching the card *on* the remote would put
that `xdg-open` on a machine with no display — breaking URL opening outright,
which is the first acceptance criterion. Keeping the launch local is not a
dodge; bridging it would fail the task.

What the bind does gain is the handle `[r]` needs (D5): `--bridge-ctl-bin`,
`--bridge-sock '#{@bridge_sock}'`, `--bridge-pane '#{@bridge_pane}'`.
`#{@bridge_sock}` is a session option and resolves through tmux's
pane→window→session lookup, the same way `bridgeCtl` already reads it
(`config/tmux.conf.nix:337`); on a non-mirror window both expand empty, which is
how the card knows it has no handle.

All three are flags rather than option reads on purpose. The card's whole
window-state read is one `show-options -w -t <target>`
(`picker/enrichcard/options.go:36`), which lists **window** options only: it
cannot see the session-scoped `@bridge_sock`, nor the global `@bridge_ctl_bin`
that `config/tmux.conf.nix:1080` already carries for `picker/remote.go:651`.
Reading either would cost an extra fork per launch, and a store path pinned at
config-generation time is exactly what `--pr-enrich-bin` already is in this same
binary — so the flag follows the local precedent instead of inventing a second
mechanism.

The flag is also more *correct*, not merely cheaper. `@bridge_ctl_bin` is
stamped at config-load time, so on a server that has not re-sourced since a nix
switch it names the previous generation's store path — the same drift
`carouselResolveScript` has to defend against with its `-x` guard on
`@carousel_bin` (#554). A flag interpolated at config-generation time travels
with the bind that carries it and cannot drift that way.

`@float_geom` and `remain-on-exit off` stay stamped (`float-conf-assertions`).

### D5 — `[r] refresh` routes to the remote via a new ctl verb

Routing, not disabling. M3 is the argument: for a bridged session the remote's
own poller has *never run*, so its `@pr_*` is frozen. A local refresh is
already a no-op (M2). Disabling the key would make that staleness permanent and
unfixable from the mirror, which is the window where it is most likely.

New verb `enrich-refresh`, following the `tool`/`carousel`/`theme` shape: a
`run-shell -b` on the remote whose POSIX body resolves the remote window's own
`@branch` and `@worktree`/`@git_root` via `tmux show-options -wqv`, then runs
the remote's `tmux-pr-enrich --target <win> --branch … --dir … --force`. The
body carries no single quote (double `tmuxQuote` only wraps), doubles every
literal `#`, spells parameter expansion `${p#*=}` only, and restores PATH from
`show-environment -g PATH` the way `toolResolveScript` does, for the same
measured reason. `args: 0`; no `windows`/`layout`/`moves` flag — the verb
changes no remote structure, and its result arrives as a
`%subscription-changed` label row rather than a reconcile — and no
`needsView`/`probe`.

**The body must guard both resolved values before invoking the poller:**
`[ -n "$b" ] && [ -n "$d" ] || exit 0`. `show-options -wqv` returns empty for an
unset option, and the poller mishandles each empty differently and badly:

- **An empty branch is not a no-op — it is a whole-server pass.** The
  single-target branch is guarded by `[[ -n $target && -n $branch ]]`
  (`tmux-pr-enrich.sh:463`); an empty branch falls *through* it into tick mode
  (`:474-489`), where `force=1` skips the age gate outright, `touch`es the
  remote's `.last-tick`, and detaches `--tick-run` → `run_full_pass`, which
  iterates `list-windows -a` across the entire remote server (`:405`). One
  keypress on one mirror window would enrich every window on that host and reset
  its tick clock, delaying the remote's own next real cycle.
- **An empty dir writes another repo's PR onto the remote window.** Single-target
  mode does run, but the `cd` is conditional —
  `if [[ -n $d ]]; then cd "$d" …; fi` (`:234`) — so `gh pr list --head "$b"`
  (`:239-247`) runs in the remote *tmux server's* cwd, which is not a repo by
  construction (the hazard CLAUDE.md's `tmux-pr-enrich` row already names). If
  that cwd happens to sit inside a git repo, that repo's PR for the branch name
  is written to the remote window's own `@pr_*` by `apply_cache_to_target`, then
  shipped home as `@bridge_pr_*` and rendered in the card — a wrong value rather
  than an absent one, persisted on the remote, in direct contradiction of D2's
  governing principle.

The local `[r] no branch` gate does **not** cover this, and the two are not
redundant: the footer gate reads the locally *carried* `@bridge_branch` while
the body re-reads the remote's own options, so they can disagree (a value the
sanitizer accepted locally that the remote has since unset), and `dir` is not
gated locally at all — a mirror with a branch but no `@worktree`/`@git_root`
still shows `[r] refresh`. Keeping the local gate inert on the bridged path is
nonetheless a real guard and not an oversight: removing it is exactly how the
whole-server fall-through above gets provoked. Do not "fix" it later.

**The target is `win`, never `<sess>:<win>`.** `Config.RemoteSession` is
documented as possibly containing spaces (`daemon/daemon.go:34`), and it would
have to cross run-shell's format expansion, two `tmuxQuote` layers and `sh` —
quoting it safely needs exactly the single quotes this body's own rule bans.
`build` already receives `win`, the remote window id `@N`, which is a complete
target-window on its own and is regex-safe by construction.

**`wire.CtlProtocolVersion` moves `"4"` → `"5"` in the same change.** This is
not optional politeness: `wire/protocol.go:29-33` requires it *"whenever the
verb table changes at all — a new verb included, since an old daemon answers
one with 'unknown verb' and the new keybind looks broken rather than stale"*.
The blast radius is real but narrower than "every live bridge breaks", and the
precise version is worth stating because the loose one is alarming and wrong:
`lztmux-remote-open.sh:415-418` already pings the daemon and, on a reply ending
`— reopen the bridge`, reaps it and recreates the mirror — matched on that
shared suffix rather than on version digits, explicitly so a later bump keeps
self-healing. So **re-opening a mirror from `prefix + s` fixes itself**, and what
is actually dead is `[r]` (and every other ctl verb) on an *already-open* mirror,
until it is reopened. That is designed behaviour, and it is why the next
paragraph matters immediately rather than eventually.

**The bridged `[r]` is synchronous and reports the ctl's real outcome.** A
fire-and-forget `.Start()` with an unconditional `refresh sent ↗` flash would
be worse than the silent no-op the task forbids — it would claim success for a
call it never checked, and the protocol bump above guarantees that is the
*common* case for the first press after this lands. `lztmux-remote-bridge-ctl`
reports failure by exit status and message: `bridge daemon unreachable` on a
dead socket, `this bridge daemon does not speak the ctl protocol — reopen the
bridge` on the version mismatch, and the daemon's own ack text (`unknown verb`,
`pane %N is not mirrored by this bridge`) otherwise
(`cmd/ctl/main.go:76-101`). So the card runs it as an ordinary `tea.Cmd` —
which is already a goroutine, so the UI never blocks — bounded by the ctl's own
`overallTimeout` of 2 s, and flashes the returned text on failure.

**Its output is captured, never inherited.** The card is a bubbletea altscreen
program; an uncaptured child writing to the float's pty paints over it. The ctl
fails by `fmt.Fprintln(os.Stderr, …)` (`cmd/ctl/main.go:117-119`), so the
command captures combined output and turns it into the flash instead. `--sock`
is passed directly and `--display-error` is deliberately **not** used: it needs
a tmux client name this process does not have, and the card owns its own
screen. (`openCmd`'s existing `xdg-open` `.Start()` has the same latent defect;
it is pre-existing and left alone, and called out in the PR body rather than
fixed here.)

Footer and flash states, so nothing is silent:

| situation | `[r]` shows | on press |
|---|---|---|
| not a mirror | `[r] refresh` | local poller, blocking spinner (unchanged) |
| mirror, no branch carried | `[r] no branch` | inert (unchanged rule) |
| mirror, no `@bridge_sock`/`@bridge_pane` | `[r] no bridge` | inert |
| mirror with a ctl handle | `[r] refresh` | ctl verb, then `refresh sent ↗` or the ctl's error text |

The bridged press needs its own re-entrancy guard. The local path has
`!m.refreshing` (`picker/enrichcard/model.go:270`), and a `tea.Cmd` is a
goroutine — without an equivalent, held presses queue remote `--force` passes,
each one a `gh` call on the remote. The bridged path therefore carries a
`sending` flag, set on dispatch and cleared by the result message. It is a
distinct flag rather than a reuse of `refreshing`, because `prBlock` renders
that one as `⧗ #N refreshing…` — which would claim the *PR* is being refreshed
when all that is in flight is a local socket write. The ctl call's completion is
deterministic (it is the synchronous half; only the remote's own poll is
fire-and-forget), so the flag has an honest clear.

**The flash needs a stated lifetime, because today it has none.** `Update`'s
`tickMsg` branch clears it unconditionally on a 1 s `tea.Tick` whose phase
relative to the keypress is arbitrary — so any message lives 0–1000 ms. A
~60-character string like `this bridge daemon does not speak the ctl protocol —
reopen the bridge` is close enough to unreadable that the "says so" acceptance
criterion would not really be met, and the protocol bump makes that exact
message the common case for the first press after this lands.

So `flash` carries a `time.Time` deadline and the tick clears it only once
passed — a deadline, never a "clear after one tick" counter, which would
reproduce the same 0–1000 ms window. Durations follow the house precedent rather
than invented numbers: **5 s for a ctl error**, matching the ctl's own
`display-message -d 5000` on its failure path (`cmd/ctl/main.go:67`), and **2 s**
for the confirmations (`opened ↗`, `refresh sent ↗`). `refreshDoneMsg` already
leaves `flash` untouched today, which is consistent with a deadline model.

The loop closes on its own: the remote poller stamps the remote window's
`@pr_*`, the `%subscription-changed` line reports it, `labelShipper` re-stamps
`@bridge_pr_*`, and the card's existing 1 s tick picks it up.

## Acceptance criteria

- A mirror window whose remote has an issue and/or PR shows the same identity
  the remote's own card would, and `[o]`/`[p]` open the remote's URLs.
- A mirror whose remote has neither still reads "no issue / no PR", and the
  branch/dir lines show the **remote's** branch and path — never the launcher's.
- A remote carrying no label state keeps working: empty card, no crash, no
  local residue.
- `[r]` in a mirror reaches the remote's poller and says so; it never silently
  no-ops.
- No change to what a non-mirror window *reads or does* — the local poller, its
  spinner and every value it renders are untouched. One deliberate exception,
  called out rather than smuggled: `flash` gains an expiry on **both** paths, so
  `opened ↗` on a non-mirror window now persists for its stated lifetime instead
  of until the next 1 s tick. Scoping the expiry to the bridged path would leave
  one field with two lifetimes and a reader unable to tell which message is
  which, for no gain.
- No regression in `tmux-reflow-windows`' grid, `picker/main.go` or
  `picker/statusline` — all three read only the nine pre-existing fields, whose
  positions 0–7 are unchanged and whose two label fields still trail the row.

## Non-goals

- A second subscription or a second shipper.
- Shipping the remote's base branch, `@window_task` or `@window_claude_ago`.
- Making `tmux-pr-enrich` poll a repo it has no checkout of.
- Any change to how reflow, the pickers or the statusline read `@bridge_*`.

## Verification

Unit: `windowlabels_test.go` (19-field parse, each new field's cleaner and
validator, drop-not-truncate on the exact fields, per-field diff, `clear`
covering all 19), `enrichcard` resolver tests (mirror / bare mirror /
non-mirror) plus the four `[r]` states above, `ctl_test.go` (the new verb's
built command — asserting it targets `win` and carries no `'` — its argument
count, and that it is reachable through `parseCtl`).

The `CtlProtocolVersion` bump needs no test of its own: `parseCtl` already
rejects a mismatch on `argv[0]`, and `ctl_test.go:150` already carries the
version-skew → "reopen the bridge" case. What it does need is the PR-body note
that live bridges must be reopened.

Two comments in `windowlabels.go` state field counts and go stale with this
change, and both are load-bearing documentation rather than decoration:
`:31-35` ("`@window_label_rest_long` is the one genuinely free-form field") must
become "the one field not pipe-stripped remotely, kept last so its own `|` lands
inside it"; `:239-240` ("the window gets nine `-u`") becomes eighteen. CLAUDE.md's
"Remote Window Labels" gains the carried set, the two cleaning policies and the
card's mirror rule, and "What the Remote Host Needs on PATH" gains
`tmux-pr-enrich` for the bridged `[r]`.

Gate: `nix build .#default`, `nix flake check`, `nix build .#lint`.

Hardware: the m2 integration checks assert dims, not content, and are
historically blind to exactly this class (#196, #181). Drive a real bridge to a
host with an enriched remote window and capture the float. State in the PR body
what was verified by hand and what was not.
