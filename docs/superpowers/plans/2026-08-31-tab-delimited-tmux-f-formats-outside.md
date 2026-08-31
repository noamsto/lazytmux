# Plan: Fix tab-delimited tmux -F formats outside scripts/ (#378)

Follow-up to #373. Mechanism: tmux rewrites non-printable bytes in `-F` output to `_` when the querying client lacks a UTF-8 locale, so a tab (or US/`0x1f`) delimiter collapses the row to one field.

## Approach

Swap every remaining control-byte delimiter for `|`. Where a field can legally contain `|` (`pane_current_path`, free-form titles), fail closed on wrong field count — never act on a wrongly parsed field. Widen the build-time guard so the regression cannot return.

## Steps

1. **`modules/home-manager.nix` (post-switch + post-remove)**
   Change `list-windows -F '…\t…'` → `|` and `awk -F'\t'` → `awk -F'|'`. Add `NF != 3 { next }` (same fail-closed shape as `tmux-worktree-match.sh:58-61`). Wrong count ⇒ empty `DUP`/`WIN` ⇒ no takeover / no kill.

2. **`picker/main.go`**
   Replace `\t` joins and `strings.SplitN(..., "\t", N)` with `|` at:
   - `collectPanesSnapshot` (+ `sessions` / `paneMap` parsers)
   - `collectSessionActivity`
   - `collectWindows`
   Fail closed when field count ≠ expected (skip the row).

3. **`picker/statusline/main.go`**
   Replace US (`0x1f`) join/split with `|`. On wrong field count, return `ok=false` (same degraded-fetch path as a tmux error). Do not keep US — it is a control byte and mangles the same way.

4. **Dead code: `scripts/tmux-reflow-windows.sh`**
   Confirm `win_procs` is unread outside this file (update-icons has its own array). Delete the declare, the `list-panes` collection loop, and `win_seen`.

5. **Widen the guard**
   - Extend `flake.nix` `tmux-format-delimiter-assertions` to also scan `${./modules}` (keep scripts + config + fixture self-test).
   - Add a Go equivalent over `picker/**` (test or check that rejects `\t` / `\n` / `\x09` / `\x1f` / raw C0 in `#{…}` format strings; allow multi-byte UTF-8). Wire it into `nix flake check`.
   - Delete the scope caveat from `CLAUDE.md` (Key Conventions) and the check comment in `flake.nix`.

6. **Validate**
   - Prove the widened guard fails on a deliberately reintroduced tab (capture output for the PR).
   - `nix build .#default`, `nix flake check`, `nix build .#lint`.

## Acceptance

- [ ] home-manager post-switch/post-remove use `|` + fail-closed NF
- [ ] picker main.go and statusline use `|` (no tab/US)
- [ ] `win_procs` loop removed from reflow
- [ ] Guard covers `scripts/`, `modules/*.nix`, `picker/**`; caveat gone from CLAUDE.md + flake.nix
- [ ] All three nix gates green; guard failure demonstrated in PR body

## Out of scope

- Re-touching already-fixed scripts/ sites from #373 (except dead `win_procs` deletion)
- PR #425 remux-timer region in home-manager.nix (different lines; rebase if it merges first)
