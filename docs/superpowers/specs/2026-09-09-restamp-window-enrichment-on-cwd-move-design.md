# Re-stamp window enrichment when a pane moves to another repo (#596)

## Problem

`@worktree` / `@branch` / `@git_root` — and, downstream, `@issue_*` and
`@pr_*` — are written once, against the cwd a window was *created* in. The
writers are:

- `after-new-window[10]` / `after-new-session[10]` → `tmux-reconcile-window`
  (cwd mode);
- worktrunk's `post-switch` hook → `tmux-reconcile-window` (explicit mode);
- the #137 in-place branch-transition poll inside `tmux-update-icons`.

None of them fires when a pane simply `cd`s from repo A into repo B. tmux has
no cwd-change hook, so the stamps go stale silently and the status-line label,
the window-picker row and the `prefix + i` enrich card all describe the wrong
repository, forever.

The #137 poll is the closest existing mechanism but does not close the gap:

1. it runs only for the **invoking session's active window** (plus a one-shot
   seed for any window with no `@branch` yet), so a background window or a
   window in another session never re-polls;
2. it keys on the **branch name**, so a move between two repos that are both on
   `main` is invisible to it;
3. it never rewrites `@worktree` — only `@branch` and `@git_root` — so
   `tmux-pr-enrich`, `tmux-worktree-match` and the enrich card keep the old
   worktree;
4. it has no `@bridge_win` guard.

## Goals

1. A window whose active pane moves into a different repository ends up with
   `@worktree`, `@git_root` and `@branch` describing the new repository, within
   a bounded, stated latency.
2. The same move re-fires issue detection **and the PR fetch**, so `@issue_*`
   and `@pr_*` describe the new repository too — including when the new branch
   carries no issue id at all, which is the reported case.
3. Steady state — a window whose cwd has not changed — costs **zero additional
   processes per tick**.
4. A `@bridge_win` mirror window is never touched by the new path.

## Non-goals

- Detecting a repo change that happens with **no cwd change**.
- Detecting a move into a repository **nested inside** the current `@worktree`
  (a submodule, a vendored clone). `under()` reports it as inside, so rule 4
  short-circuits and the window is never re-stamped. Catching it would require
  writing the memo on the seed path for every window, which trades away the
  provably-free no-move case that acceptance criterion 4 demands.
- Reacting faster than the existing 1s status tick.
- Fixing anything about `gh` fork/PR resolution. Measured already correct.
- Reworking the #137 branch-transition poll. It stays; the new path is additive.

## Where the re-derive goes, and why

**Chosen: the batched `list-panes -a` read in `tmux-update-icons`, gated on the
active pane's `pane_current_path` differing from a per-window memo option, and
delegating the actual work to the existing `tmux-reconcile-window`.**

| candidate | verdict |
| --- | --- |
| `tmux-update-icons` batched read | **chosen** — the read already happens every tick and already carries `pane_current_path`, `@bridge_win` and `@branch`; adding format fields makes the change-detection fork-free, and it already owns the sibling #137 re-stamp trigger. |
| `tmux-pr-enrich` full pass | rejected — it *consumes* `@worktree`/`@git_root` to group windows by repo (`tmux-pr-enrich.sh:405`). Making the consumer of a tag also its producer inverts the dependency, and its pass is gated behind `prRefreshSeconds` (default 120s). |
| explicit stamping by `dispatch` and friends | rejected — fixes the reported instance and leaves a plain `cd` broken, which is the actual defect. |
| a `pane-focus-in` hook | rejected — `config/tmux.conf.nix:1214-1225` records this being considered and declined for #100: it is the hottest per-interaction path in the config and `tmux-reconcile-window` forks ~7 subprocesses even on its idempotent branch. It is also only correct for windows the user visits. |

`tmux-reconcile-window` is reused rather than reimplemented: it is already "the
single place that defines HOW a window gets tagged", already idempotent, already
bails on `@bridge_win`, and already kicks `tmux-issue-stamp`.

## Mechanism

### The memo option

A new window option `@window_cwd_seen` records the active pane's cwd as of the
last time this path evaluated it. It is a shadow of an external value no hook
reports — the same shape as `@crew_seen`, which shadows `@crew_name` a few lines
away in the same file.

It is not carried across a `tmux-remux` restore (restored windows are freshly
created), so a restored window re-enters the seed short-circuit below.

### The window's cwd — one authority, used by both writers

`tmux-update-icons` already has a second writer of `@branch`/`@git_root`: the
#137 in-place branch poll, which reads `win_pane_path` — first-pane-wins. The
reconciler derives its cwd from `display-message -t <target>`, which for a window
target resolves the **active** pane. Left alone those are two writers of one
option keyed on two different panes: for a window with pane 1 in repo A and the
active pane 2 in repo B, the new gate fires and the reconciler stamps B, then the
next tick the poll recomputes A from pane 1 and rewrites `@branch`/`@git_root`
back to A. The window settles with `@worktree` = B, `@git_root` = A, `@branch` =
A, and `tmux-pr-enrich`, which groups on `@worktree`, queries A's branch inside B.

So the spec defines **one** authority:

> **the window's cwd** is the current path of its first **non-floating** pane, in
> `list-panes` order.

and makes every consumer read it:

- the new gate compares it against the memo;
- the #137 branch poll's `git -C` uses it instead of `win_pane_path`;
- the reconcile is targeted at **that pane's `%id`**, so `display-message -t %N`
  resolves that pane's path rather than the window's active one. Verified on a
  live server: `set-option -t %N -w` and `show-options -t %N -wqv` both resolve
  the window containing the pane, and `display-message -t %N` expands against the
  pane itself. This is why no `#{window_id}` field is added — `#{pane_id}` is
  already field 1, is equally stable under `renumber-window on`, and additionally
  pins the fork to the pane whose path was memoised.

**First pane, not active pane, and that is the load-bearing choice.** Keying on
the active pane would make pane *selection* a re-tag trigger: `prefix + o`
between a pane in repo A and a pane in repo B would fire a full re-tag each way —
options rewritten, eight `@pr_*` unset, a forced `gh` query with
`statusCheckRollup`, two reflows — on a keystroke that changed nothing about the
repository. First-pane is stable under focus, matches the first-pane-wins
convention already in that loop, and is identical to active-pane for a
single-pane window, which is the reported case and the overwhelming majority.

The consequence, stated: a split window whose *other* pane moves to a different
repo is not re-stamped. The window's identity is its first tiled pane.

Floats are excluded from that selection. A focused float *is* the window's active
pane and a float can hold any index, so a `prefix + b` shell float or a file
manager that chdirs could otherwise become the window's identity.
`#{pane_floating_flag}` makes the exclusion fork-free; `tmux-float-refit.sh:26`
already uses it.

The authority lives in a **new** per-window map, not a redefinition of
`win_pane_path`: that array's key set drives the entire per-window loop and its
first-pane-wins guard also captures nine other window values, so rewriting its
population would change far more than the cwd.

### The three new format fields

The batched `list-panes -a` format gains exactly three fields:
`#{pane_floating_flag}`, `#{@worktree}` and `#{@window_cwd_seen}`. They are placed
immediately **before** `#{pane_active}` — downstream of every field the existing
script already parses, so a `|` inside `@worktree` cannot newly break a window's
icons or branch poll, while still sitting upstream of the poison detector below.

### The gate

A per-window **poison flag** is recorded during the read loop: if any of a
window's rows has a `pane_active` field outside `{0,1}`, the window is poisoned.
`pane_active` is a closed set, so testing it is free, and a value outside it means
a `|` inside a directory name shifted the row. A shifted memo can never compare
equal, which would be a reconcile fork every tick forever for that window —
strictly worse than the field shift the rest of the script already tolerates.
Failing closed here is the discipline `tmux-worktree-match.sh:58-61` applies with
its `NF != 7` check. The flag is recorded per row and consumed per window, since
the evidence exists only inside the read loop.

Then, per window, fire the reconcile iff **all** of:

1. the window is not poisoned;
2. `@bridge_win != 1` — a mirror's labels are daemon-owned, and a local re-stamp
   is the two-writer race CLAUDE.md warns about;
3. the window's cwd is non-empty;
4. the cwd is **not under** `@worktree` (an empty `@worktree` counts as not
   under);
5. the cwd differs from `@window_cwd_seen`.

Firing writes `@window_cwd_seen` and backgrounds `tmux-reconcile-window %<pane id>`.

Conditions 4 and 5 do different jobs and both are needed.

**Condition 4 is the free path.** A window whose stamps already describe its cwd
is short-circuited without writing anything — which is what keeps a 40-window
`tmux-remux` restore from firing 40 reconciles, what makes the no-move case
provably free, and what makes a `cd` deeper into the same worktree cost nothing.
It also means an already-stale window (cwd outside its `@worktree`) self-heals on
the first tick after this ships, rather than only on its next move.

**Condition 5 bounds everything condition 4 cannot settle.** After a move into a
directory the reconciler refuses — a non-git tree, where it exits before writing
anything — `@worktree` still names the old repo, so condition 4 stays true
forever. The memo is what stops that from being one fork per tick: the same path
is not re-fired. It is written by `tmux-update-icons` synchronously rather than by
the reconciler, because a reconciler that exits before writing would never
memoise at all.

**"under" is `tmux-worktree-match.sh`'s `under()`, not a prefix match**: equal, or
prefixed by `<base>/` while *not* prefixed by `<base>/.worktrees/`. A plain prefix
test would read `/x/lazytmux-old` as inside `/x/lazytmux`, and would read a nested
`.worktrees/` checkout — which belongs to a different branch — as inside its
parent. A false "inside" permanently suppresses the re-stamp for that window, the
hardest failure to notice.

No path canonicalisation is attempted. `@worktree` may hold worktrunk's raw
spelling (explicit mode) or git's physical one (cwd mode). Where they differ, the
gate fires once and the reconcile rewrites `@worktree` to the physical spelling;
that demotes the window from rank 0 to rank 2 in `tmux-worktree-match` (`:74`,
`:79`), which that script documents as degrade-not-lie. Stated, not hidden: the
re-stamp is still correct and the match is still found, only its rank changes.

The memo is written with **direct argv**, never through the `tmux_cmds` /
`tmux source -` batch: that batch interpolates into single quotes and tmux
reparses it, so a `'` in the value injects arbitrary tmux commands, and a
directory name may legally contain one. This is the hazard
`tmux-update-icons.sh:396-401` already documents for branch names.

**Assumption, stated:** `pane_current_path` is taken to move only on a deliberate
`cd`. A tiled file manager (`yazi`, `lf`) or a build script that chdirs moves it
continuously. Condition 4 makes all such movement inside the worktree free;
movement that repeatedly leaves it costs one reconcile per distinct destination.

### Making the PR actually arrive

Reusing the reconciler gives goal 1 for free but **not** goal 2. The chain
`reconcile → tmux-issue-stamp → tmux-pr-enrich --force` only holds on the
stamp's *id-found* branch. Its no-id branch unsets `@issue_*`, forces a reflow
and `exit 0`s before ever reaching the PR kick.

That is precisely the reported case: `clamp-floating-panes` matches neither
`branch_to_linear_key` (needs `<letters>-<digits>`) nor
`branch_to_gh_issue_number` (needs digits), so there is no issue id, so no PR
fetch is kicked and PR 5582 would arrive only whenever `tmux-pr-enrich`'s own
full pass next ran — up to `prRefreshSeconds`, 120s by default.

So `tmux-issue-stamp`'s no-id branch gains the same
`@pr_enrich@ --target --branch --dir --force` kick its id-found branch already
makes. A branch with no issue is still a branch with a PR. Two consequences,
stated rather than inherited:

- the kick is **guarded on a non-empty worktree**. With `--dir ""`,
  `fetch_pr_cached` skips its `cd` (`tmux-pr-enrich.sh:235`) and `gh` runs in the
  tmux server's cwd — the original wrong-repo bug that file's header calls out.
  `tmux-update-icons.sh:404` can hand the stamp an empty `git_root` when
  `rev-parse --show-toplevel` times out while `branch --show-current` succeeded.
- `--force` also pulls `statusCheckRollup` (`tmux-pr-enrich.sh:237`), the
  expensive query the full pass gates behind `prCheckRefreshSeconds`. The kick
  fires from every caller of the stamp, not only a re-tag: `after-new-window` for
  each window created on a non-issue branch, the #137 poll, and `wt switch`. A
  `tmux-remux` restore of N such windows is N forced queries. This generalises a
  cost the id-found branch already pays — a restore of N *issue*-branch windows
  already fires N forced fetches today — rather than introducing a new class of
  it.

### Clearing what the old repo left behind

A re-tag must not leave the previous repository's data in place:

- **`@branch` on a detached HEAD.** The reconciler writes `@branch` only when the
  derived branch is non-empty, so a move into repo B at a detached HEAD leaves
  repo A's branch beside repo B's `@worktree`. `tmux-pr-enrich` then groups that
  window under B and queries A's branch name against it — actively wrong data,
  not merely stale. On a re-tag the reconciler therefore **unsets** `@branch`
  when there is no current branch. Its cost is stated under Known limits.
- **`@pr_*` from the old repo.** Only a successful fetch overwrites them
  (`@pr_branch` is written but read by nothing — not reflow's `FMT`, not the
  statusline, not the enrich card — so nothing hides them either). Until a fetch
  lands, the window shows repo B's branch with repo A's PR number and URL. On a
  re-tag that changes `@worktree`, the reconciler unsets `@pr_number`,
  `@pr_title`, `@pr_state`, `@pr_check_state`, `@pr_url`, `@pr_mergeable`,
  `@pr_draft` and `@pr_branch` — symmetric with what the issue stamp already does
  for `@issue_*` on its no-id branch, and with the same `2>/dev/null` shape, since
  unsetting an already-unset user option is noisy. The forced fetch repopulates
  them; where it cannot reach `gh` at all, `apply_cache_to_target` returns without
  writing (`tmux-pr-enrich.sh:262`) and the window carries no `@pr_*` rather than
  the wrong repo's. Unsetting `@pr_state`/`@pr_check_state` specifically is also
  what suppresses a false "PR merged" / "checks failed" toast on the next fetch:
  `notify_pr_change` (`tmux-pr-enrich.sh:118-131`) reads an empty prior field as
  discovery rather than a transition. Keep the unset; a plain overwrite would
  re-arm the toast.

### Reflow

The reconciler relies on `tmux-issue-stamp` to force a reflow, which only runs
when enrich is enabled *and* the branch is non-empty. On a re-tag with enrich off,
or on a detached HEAD, the grid would keep the old label. The reconciler therefore
forces `tmux-reflow-windows --force` itself on a **re-tag in cwd mode**, where
"re-tag" means the `@worktree` read back before the write was non-empty. Both
halves are load-bearing and on different axes: the re-tag test excludes window
creation (cwd mode *is* the creation path, and `after-new-window` already
backgrounds a reflow), and the cwd-mode test excludes worktrunk's `post-switch`,
which also already reflows — without it every `wt switch` would pay an extra
forced reflow. Reflow takes a
session *name*, so the reconciler resolves one with `display-message`. With enrich
on and a non-empty branch this and the issue stamp's own reflow both fire on the
same re-tag; both are `--force` and backgrounded, and a repo move is rare.

### Wiring

Neither new invocation may use a bare command name:
`config/tmux.conf.nix:415-419` states why — "a bare name resolves against the
tmux server's frozen PATH and stays stale until a full server restart".

- `mkScriptIcons` gains `@reconcile@` →
  `${script.tmux-reconcile-window}/bin/tmux-reconcile-window`. No cycle: the
  reconciler never references `tmux-update-icons`.
- `mkScriptReconcile`, which today substitutes only `@issue_stamp@`, gains
  `@reflow@` → `${script.tmux-reflow-windows}/bin/tmux-reflow-windows`.
- `tmux-update-icons` carries `RECONCILE_BIN="${RECONCILE_BIN:-@reconcile@}"`
  beside the existing `ISSUE_STAMP_BIN` seam, with the same
  `[[ $BIN == @* ]]` unsubstituted-placeholder guard. That env override is what
  a counting fake binds to, and without it acceptance criterion 4 is not
  demonstrable — `tests/update-icons-enrich-trigger.bats` works exactly this way.

### Cost

| situation | added cost |
| --- | --- |
| steady state, any number of windows | three extra `\|`-delimited fields on a format string already fetched; no fork, no tmux write |
| first tick per server, window already correct | nothing at all — rule 4 short-circuits without writing the memo |
| a `cd` deeper into the same worktree, or any pane-focus change | nothing — condition 4 short-circuits |
| a genuine move out of the worktree | one `set-option` + one backgrounded `tmux-reconcile-window`, **per attached client**: `status-format[0]` is evaluated per client, and unlike the #137 poll the gate has no one-window-per-tick cap |
| a move into a non-git directory | one `set-option` + one reconcile that exits at `is-inside-work-tree`; the memo then bounds it to that single fork |

Latency: one `status-interval` tick (`status-interval 1`) to fire, plus the
reconciler's own git calls. Stated bound: **the window options are correct within
~2s of the `cd`, and `@issue_*`/`@pr_*` within the issue stamp's and the forced
PR fetch's own time — seconds, not the 120s `prRefreshSeconds` floor.**

## Known limits

- `tmux-update-icons` runs from `status-format[0]`, which tmux evaluates only for
  a client drawing a status line. A server whose only clients are control-mode —
  which is exactly the recorded bridge topology, where agents run on a host
  viewed only through mirrors — never ticks, so the re-derive never fires there.
  Same condition every existing 1s poller has; #580 widened the *attached*
  server's pass to `list-panes -a`, which does not help a server with no status
  client at all.
- **A window left on a detached HEAD by the `@branch` unset costs one
  `git branch --show-current` per tick.** `tmux-update-icons.sh:391` re-polls any
  window whose `@branch` is empty, and a detached HEAD yields empty forever, so
  the "polled once, then trusted" seed never latches; `tmux-branch-display` adds
  a second fork per render. This is not new behaviour — it is exactly the
  standing cost of any window *created* on a detached HEAD — and it is outside
  acceptance criterion 4, which scopes the no-fork guarantee to a window that has
  **not** moved. Fixing the underlying seed predicate is a separate concern from
  this change.
- If the backgrounded reconcile dies, the memo has already been written, so that
  cwd is not retried until the next move. Self-correcting on the next `cd`, but
  not retried in place.
- A `|` in a directory name is not repaired, only prevented from making things
  worse (the poison flag). That window's icons, branch poll and labels are
  already broken by the field shift today.

## Acceptance criteria

1. A window created in repo A whose pane then `cd`s into repo B reports
   `@worktree`, `@git_root` and `@branch` for B within one status tick.
2. The same window then carries `@issue_*` / `@pr_*` for B.
3. Demonstrated on a real reproduction: a window on `clamp-floating-panes` in a
   tmux checkout reports that branch and PR 5582.
4. A window that has not moved fires no `tmux-reconcile-window` and performs no
   tmux write across repeated ticks — demonstrated by a counting fake.
5. A `@bridge_win` mirror window is never passed to the new path — demonstrated
   by a test.
6. `nix build .#default`, `nix flake check` and `nix build .#lint` all pass.
