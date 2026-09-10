# Plan — re-stamp window enrichment when a pane moves to another repo (#596)

Design spec: `docs/superpowers/specs/2026-09-09-restamp-window-enrichment-on-cwd-move-design.md`

`tmux-update-icons`' existing batched `list-panes -a` read gains three format
fields, notices that a window's **first non-floating** pane has moved out of the
repository its stamps describe, and hands that pane's `%id` to the existing
`tmux-reconcile-window`. The reconciler is extended so a re-tag converges — it
clears what the old repo left behind and forces a reflow — and
`tmux-issue-stamp` is extended so a branch with no issue id still triggers the
PR fetch.

**The window's cwd** is the current path of its first non-floating pane. The new
gate, the #137 branch poll and the reconcile target all read that one pane.

Fire the reconcile iff **all** of: not poisoned; `@bridge_win != 1`; cwd
non-empty; cwd **not** `under(@worktree)`; cwd != `@window_cwd_seen`. Firing
writes the memo (direct argv) and backgrounds
`tmux-reconcile-window %<pane id>`.

---

- [ ] **Step 1: `tmux-issue-stamp` kicks the PR fetch on its no-id branch**

  `scripts/tmux-issue-stamp.sh`: the no-id branch (which unsets `@issue_*`,
  forces a reflow and exits) never reaches the `@pr_enrich@ … --force` call the
  id-found branch makes. Add the same kick there, **guarded on a non-empty
  `$worktree`** — with `--dir ""` the poller skips its `cd` and `gh` runs in the
  tmux server's cwd, the original wrong-repo bug that file's header calls out.

  This is what makes acceptance criterion 3 reachable at all:
  `clamp-floating-panes` matches neither issue regex, so the reported case always
  takes this branch.

  Extend `tests/issue-stamp.bats`: the no-id branch fires the PR kick (the fake
  and its `prlog` already exist), and does not when the worktree is empty.

- [ ] **Step 2: `tmux-reconcile-window` converges a re-tag**

  `scripts/tmux-reconcile-window.sh`. "Re-tag" = the `@worktree` read back before
  the write was non-empty.

  - on a re-tag, unset `@branch` when the derived branch is empty (detached
    HEAD), instead of leaving the previous repo's branch beside the new
    `@worktree`;
  - on a re-tag where `@worktree` actually changes, unset `@pr_number`,
    `@pr_title`, `@pr_state`, `@pr_check_state`, `@pr_url`, `@pr_mergeable`,
    `@pr_draft`, `@pr_branch` — with the `2>/dev/null` shape
    `tmux-issue-stamp.sh:97-101` uses, since unsetting an unset user option is
    noisy;
  - force `@reflow@ "<session name>" --force` in the background on a **re-tag in
    cwd mode**. Both halves matter and are on different axes: re-tag excludes
    window creation (which already reflows), cwd mode excludes worktrunk's
    `post-switch` (which also already reflows).

  Wire `@reflow@` into `mkScriptReconcile`'s `builtins.replaceStrings` in
  `config/tmux.conf.nix` — store path, not a bare name, per the `mkScriptIcons`
  comment above it.

- [ ] **Step 3: cover Step 2 in `tests/reconcile.bats`**

  Add `@reflow@` → a recording fake to that file's `sed`. Assert:
  - a re-tag in cwd mode fires the reflow fake once with `--force`; the creation
    seed and the idempotent early-exit do not;
  - explicit mode does not fire it even on a re-tag;
  - a re-tag onto a detached HEAD unsets `@branch`;
  - a re-tag that changes `@worktree` unsets the eight `@pr_*` options;
  - a same-worktree re-tag (branch change only) leaves `@pr_*` alone.

- [ ] **Step 4: the cwd authority and the move detector in `tmux-update-icons`**

  `scripts/tmux-update-icons.sh`:
  - add `RECONCILE_BIN="${RECONCILE_BIN:-@reconcile@}"` beside the existing
    `ISSUE_STAMP_BIN` seam, same `@*` guard and comment shape;
  - add `#{pane_floating_flag}`, `#{@worktree}` and `#{@window_cwd_seen}` to the
    batched format, immediately **before** `#{pane_active}` — downstream of every
    field the script already parses, upstream of the poison detector. Extend the
    `read` variable list and the `|`-delimiter comment block;
  - add a **new** map `win_cwd` (first non-floating pane wins) plus `win_cwd_pane`
    holding that pane's id. Do not redefine `win_pane_path`: its key set drives
    the whole per-window loop and its guard captures nine other values;
  - record a per-window poison flag when a row's `pane_active` is outside
    `{0,1}`;
  - point the #137 branch poll's `git -C` at `win_cwd` instead of
    `win_pane_path`, so the two writers of `@branch`/`@git_root` read one pane;
  - implement the five conditions inside the existing per-window loop, ahead of
    the branch poll, with an `under()` helper matching
    `tmux-worktree-match.sh:50-55` (equal, or `<base>/` prefixed and not
    `<base>/.worktrees/` prefixed);
  - write `@window_cwd_seen` with direct argv, **not** via `tmux_cmds` /
    `tmux source -` (a directory name may contain `'`);
  - do **not** set `sess_need_reflow` here — the reconciler owns its reflow, and
    one fired from here would race ahead of the stamps it must render.

- [ ] **Step 5: wire `@reconcile@` into the icons build**

  `config/tmux.conf.nix`: add `"@reconcile@"` to `mkScriptIcons`'
  `replaceStrings` source list and
  `"${script.tmux-reconcile-window}/bin/tmux-reconcile-window"` to the target
  list. No cycle: the reconciler never references `tmux-update-icons`.

- [ ] **Step 6: new bats file `tests/update-icons-cwd-move.bats`**

  Modelled on `tests/update-icons-enrich-trigger.bats`: real script, private
  config-less tmux server, fakes injected by env. A counting fake reconciler
  records its argv per invocation. Cases:
  1. **moves repo** — a window whose first pane ends up in repo B fires the fake
     exactly once, targeted by `%<pane id>`, and `@window_cwd_seen` lands on B.
  2. **does not move** — repeated ticks on a settled window fire the fake zero
     times *and* leave `@window_cwd_seen` unwritten. Criterion 4's evidence:
     assert the counter and the option, not a property.
  3. **already stale** — a window whose cwd is outside its `@worktree` fires once
     on the first tick, then settles.
  4. **subdirectory `cd`** — a `cd` deeper inside `@worktree` fires zero times.
  5. **bridge mirror** — `@bridge_win 1` on a window sitting outside its
     `@worktree` fires zero times (criterion 5).
  6. **non-git cwd** — a move into a non-repo directory fires once and settles,
     rather than forking every tick.
  7. **`.worktrees/` boundary** — a cwd at `<repo>/.worktrees/x` is not treated as
     inside `<repo>`, so it fires.
  8. **float** — a floating pane in another repo does not become the window's
     cwd, so it fires zero times.
  9. **pane focus** — selecting a second pane in a different directory fires zero
     times, since the authority is the first non-floating pane.

- [ ] **Step 7: register the new check in `flake.nix`**

  Add `update-icons-cwd-move-tests` to the `checks` attrset, copying the
  `update-icons-enrich-trigger-tests` shape (`bats coreutils gnused git tmux`,
  `cp -r ./scripts ./tests`).

- [ ] **Step 8: documentation**

  - `CLAUDE.md`: extend the `tmux-update-icons` row in the Script Roles table;
    add a Key Conventions bullet for `@window_cwd_seen` covering its shadow
    nature (`@crew_seen` precedent), the fork-free `under(@worktree)` condition,
    the first-non-floating-pane authority, and that the memo bounds a non-git or
    repeated move to one fork. Note the no-status-client limit.
  - `config/tmux.conf.nix`: update the comment (~line 1214) recording why
    `pane-focus-in` was rejected, so it names where the residual gap is closed.

- [ ] **Step 9: verification**

  1. `nix build .#default`
  2. `nix flake check`
  3. `nix build .#lint`
  4. Live reproduction on a **private** tmux server. Use the plain tmux binary,
     not the `tmux` on PATH: the wrapper prepends `-f <store>/tmux.conf`, tmux
     accepts multiple `-f`, so a wrapper-started server loads lazytmux's hooks and
     auto-restores the user's sessions from the shared tmux-remux state db.
     Isolate `TMUX_TMPDIR` and `XDG_DATA_HOME`; never touch the user's server.
     Create a window in this worktree, `cd` into `/home/noams/git/tmux`
     (`clamp-floating-panes`), run the built scripts, and paste before/after
     `show-options -w` plus `@pr_number` showing 5582.

## Notes

- No step carries subtle concurrency, security-sensitive logic, or a wide blast
  radius, so none is tagged for an escalated implement rung.
- `tmux-issue-stamp` already serialises every stamp trigger through a per-window
  lock, so the new trigger cannot corrupt a concurrent `post-switch` stamp.
