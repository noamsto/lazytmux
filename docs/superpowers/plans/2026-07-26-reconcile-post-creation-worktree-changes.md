# Reconcile Post-Creation Worktree Changes (#100) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Determine, with evidence, which of the two gaps GitHub issue #100 raised in the creation-time window reconciler (#95/#96) still exist against current `main` — Gap A (stale `@worktree` after a post-creation worktree change) and Gap B (no reconcile-all sweep after a tmux-remux state restore) — and implement only what genuinely survives.

**Result of the investigation: neither gap survives in a form that needs new code.** Gap A survives only in a narrow, already-tolerated form (closed for the mainline case by #199/PR #205). Gap B does **not** survive at all: it was empirically verified, against the actual pinned `tmux-remux` binary, that every restore/undo/pick path already positions each pane's cwd via `-c` *synchronously at window/session creation*, so the **existing** `after-new-window[10]`/`after-new-session[10]` creation hooks (#95/#96) already tag every restored window correctly. **This plan adds no new hooks, no new scripts, and no new tests.** It documents both findings at the point in the source where a future reader would otherwise reach for exactly the hooks the issue proposed, and updates the PR to recommend closing #100 as already resolved.

**Architecture:** Two source comments (Gap A at `config/tmux.conf.nix`'s creation-hook block, Gap B at `modules/home-manager.nix`'s `tmuxStateConf`), one `CLAUDE.md` clarification, and a verification pass. No scripts, no Nix wiring, no bats tests, no flake.nix changes.

**Tech Stack:** bash (comment-only touch, no logic changes), Nix docs, Markdown (`CLAUDE.md`), tmux hooks (read, not modified).

---

## Gap Survival Analysis (this section is the deliverable — the acceptance criterion is "a clear written statement of which gaps survive #199, with evidence, before any code")

### What changed on `main` since the issue was filed

Commit `67cf160` (PR #205, closing #199) rewrote the worktrunk post-switch navigation hook. Before #199, a window's `@worktree` tag was trusted at face value. After #199 (`scripts/tmux-worktree-match.sh`):

- Every `wt switch` runs **one** `tmux list-panes -a` and ranks *all* windows (not just the switch target) by whether a pane's cwd corroborates the window's `@worktree` tag (`under()`/`validates()` in the awk program, `tmux-worktree-match.sh:50-83`).
- A tag with a corroborating pane wins (rank 0-2); an **untagged** window whose active pane's cwd matches wins at rank **2** (`tmux-worktree-match.sh:79`, rank 3 — untagged + background pane — is explicitly excluded at `:82`); a **tagged** window with no corroborating pane anywhere and a readable cwd elsewhere is **unset** — `CLEAR\t<window_id>` (`tmux-worktree-match.sh:92-98`, applied at `:115`).
- The matched window (`MATCH`) is then re-tagged unconditionally in explicit mode by the post-switch hook's tail (`modules/home-manager.nix:938-940`, `tmux-reconcile-window "$STAMP_TARGET" "{{ worktree_path }}" "{{ branch }}"`), regardless of whether its prior tag was right, wrong, or absent.
- `pane-focus-in` is **not** an active hook today — it only appears as a `set-hook -gu pane-focus-in` cleanup line (`config/tmux.conf.nix:718`) clearing a stale hook from an older config version. `focus-events on` is already set globally (`:492`).

### Gap A — post-creation worktree changes (split-window, late `cd`, focus move)

**Verdict: survives only in a narrow form. Do not add `after-split-window` or `pane-focus-in` reconcile hooks.**

Evidence:

1. **The self-heal is real, not aspirational.** Every `wt switch` — to *any* target, not just the affected window — clears any `@worktree` tag that no pane corroborates, and correctly retags whichever window the switch actually lands on. A window whose worktree changed via split/focus/cd *and which the user later `wt switch`es back into* is fixed for free, with zero new hooks.
2. **The residual gap is exactly: a window's worktree changes via raw tmux primitives (split, focus move, manual `cd`), and the user never again runs `wt switch` targeting that window's new location.** That window's `@worktree`/`@branch` window options stay stale indefinitely — nothing else re-derives `@worktree` specifically.
3. **The residual gap is narrower than "the window looks stale," because two independent mechanisms already re-derive the other fields.** `tmux-branch-display.sh:8-12` falls back to a live `git branch --show-current` whenever `@branch` is unset. Stronger still, `tmux-update-icons.sh:250-282` already re-derives `@branch`/`@git_root` on its 1s tick for the **active** window (or any window with no `@branch` yet — "polled once to seed it, then trusted"), forking `git branch --show-current`/`git rev-parse --show-toplevel` directly against the pane's live cwd. So once a window with a changed worktree becomes active again — the normal way a user would even notice it — its `@branch`/`@git_root` self-correct within one tick, no `wt switch` required. What neither poller ever re-derives is `@worktree` itself (nothing but `wt switch`'s matcher or a creation hook ever writes it) or `@issue_*` (the update-icons re-stamp at `:271-280` requires a **previous non-empty** branch, so it only fires on a genuine transition, never an initial seed). The residual gap is specifically "`@worktree`/`@issue_*` stay stale," and only for a window that was tagged at some point and never becomes the active window with a subsequent `wt switch` reaching it.
4. **`wt switch` is this repo's primary worktree-navigation path** (see this project's `CLAUDE.md`'s "Worktree Isolation" section). A user who changes a window's worktree without ever using `wt switch` again to reach it is going around the tool's own workflow.
5. **`pane-focus-in` is a genuinely hot path** — it fires on every pane focus change (arrow-key pane nav, mouse click, popup close, a script's `select-pane`), far more frequently than window creation or `wt switch`. `tmux-reconcile-window` is not cheap even in its idempotent branch: cwd mode forks **~7 subprocesses** before it can early-exit — a `@bridge_win` `show-options`, a `display-message`, two `git rev-parse` calls, a `git branch --show-current`, and two more `show-options` (`scripts/tmux-reconcile-window.sh:22,38,40,41,45,50-51`) — not "2 cheap show-options reads." Adding that cost to the single hottest per-interaction path in the whole config, for a payoff that only matters in the narrow case above, is not worth it.
6. The dispatcher notes are explicit that a hook redundant with the matcher's validation should not be added, and that "adding hooks to the 1s-adjacent path for a problem that no longer exists is a regression, not a feature." `pane-focus-in` is that 1s-adjacent path.

**Decision: do not add `after-split-window` or `pane-focus-in` reconcile hooks.** Document this at the `after-new-window[10]`/`after-new-session[10]` hook block in `config/tmux.conf.nix` (Task 1) and restate it in the PR body.

### Gap B — no server-start / state-restore sweep

**Verdict: does not survive. No new sweep, no new script.**

The first plan draft for this issue proposed a new `tmux-reconcile-all` sweep chained after tmux-remux's `restore --auto`/`undo --pop`/`pick --kind=close`/`pick --kind=snapshot`, on the premise (taken from the issue body) that "a tmux-state restore drops `@worktree` on every restored window." **That premise does not hold against the currently pinned `tmux-remux` and was falsified empirically, not just by reading source:**

1. **Source evidence.** `internal/restore/apply.go` in the pinned `tmux-remux` revision (`648c22f6f6893771cb2a5d1bda9c871b3f5a9eeb`, matching this repo's `flake.lock` — narHash verified identical: `sha256-7m0TibwCESZSqJT1PkqN6mQ65VRQ7wqOZ2cYDW85aVI=`) builds every window-creating action with the historical cwd passed via `-c`:
   ```
   args = []string{"new-session", "-d", "-s", v.Name, "-c", v.Cwd}
   args = []string{"new-window", "-t", fmt.Sprintf("%s:%d", v.Session, v.Index), "-n", v.Name, "-c", v.Cwd}
   args = []string{"split-window", "-t", v.Target, "-c", v.Cwd}
   ```
   All four recovery commands — `RestoreCmd` (`restore --auto`), `UndoCmd` (`undo --pop`), and `PickCmd` (`pick --kind=close`/`pick --kind=snapshot`) — build a plan via the same `restore.BuildPlan`/`restore.Apply` functions (`cmd/tmux-remux/main.go:239-240` for undo; the restore/pick commands share the identical call shape). There is no separate, `-c`-less code path for any of them.
2. **Empirical reproduction (not just reading code).** In an isolated tmux server (private `TMUX_TMPDIR`/`XDG_DATA_HOME`/`XDG_STATE_HOME`, a scratch git repo on a feature branch, the real `after-new-window[10]`/`after-new-session[10]` hooks pointed at the actual `scripts/tmux-reconcile-window.sh`), a window was created, tagged (confirming the harness), saved via the real `tmux-remux save`, the server killed, a fresh server started with the same hooks pre-registered (mirroring config-load order), and `tmux-remux restore --auto` run for real. The restored window's `@worktree`/`@branch`/`@git_root` were **already correctly set immediately** — no lag, no missing tag, no sweep needed. (The one artifact of the test harness itself — a detached `-d` session has `last_attached = 0`, which the smart filter's `stale` classification would otherwise reject; that's a property of never attaching a real client in a scripted test, not of restore's tagging behavior, and was worked around by patching the saved snapshot's `last_attached` field before restoring — it does not affect the tagging conclusion.)
3. **Why this makes sense mechanically:** `after-new-window`/`after-new-session` fire whenever tmux executes `new-window`/`new-session`, *regardless of who issued the command or with what flags* — including a `-c`-carrying command issued by an external binary. Since the pane's cwd is set via `-c` at the moment of process spawn (before the shell/program in the pane ever runs), `tmux-reconcile-window`'s `pane_current_path` read — pinned to the just-created window's id — sees the correct, final cwd on the very first read. There is no async "create, then cd later" step in tmux-remux's restore path (unlike, e.g., the worktrunk post-switch hook's own reused-window branch, which genuinely does `send-keys "cd ..."` after the fact — a real case of the async pattern the issue was worried about, but not tmux-remux's).
4. **`split-window` restoring additional panes is not a gap either.** `split-window` doesn't fire `after-new-window` (that's a separate tmux event class), but it doesn't need to: `@worktree` is a *window* option, already set correctly by the window's first pane (created via `new-window -c`, which does fire the hook); a restored split pane landing in a different directory doesn't retag the window, matching ordinary (non-restore) split behavior, which is exactly Gap A's residual, already covered above.

**Decision: add no `tmux-reconcile-all` script, no new Nix wiring, no `bind`-chained sweep.** It would be a mechanism added for a problem that does not exist against the currently pinned `tmux-remux` — precisely what the issue's own acceptance criteria and the dispatcher notes warn against ("if a proposed trigger turns out to be redundant... do not add it — report that instead"). Document this at `modules/home-manager.nix`'s `tmuxStateConf` (Task 2), and recommend closing #100 in the PR body (Task 4).

**Caveat, stated plainly for the PR body:** this conclusion is tied to the *currently pinned* `tmux-remux` revision. If a future `tmux-remux` bump changes `apply.go` to stop passing `-c` (or to defer cwd-positioning to a post-creation step), Gap B could reopen. That's a property of the dependency, not of this repo's own hooks, and isn't something to defend against speculatively here — worth a one-line note in `CLAUDE.md`'s Persist section so a future reader knows what to re-check if restored windows ever stop getting tagged.

---

## File Structure

- Modify: `config/tmux.conf.nix` — add the Gap A decision comment near the `after-new-window[10]`/`after-new-session[10]` hooks (~line 753).
- Modify: `modules/home-manager.nix` — add the Gap B decision comment near `tmuxStateConf`'s `restoreMode == "auto"` block (~line 94).
- Modify: `CLAUDE.md` — add one sentence to the "Persist (tmux-remux)" section noting restored windows are already tagged by the existing creation hooks, and what would have to change upstream for that to stop being true.
- No new files. No `flake.nix` changes. No new scripts. No new tests (no new pure logic to cover).

---

### Task 1: Document the Gap A decision in `config/tmux.conf.nix`

**Files:**
- Modify: `config/tmux.conf.nix:753-757`

- [ ] **Step 1: Add the decision comment after the existing reconcile-window hook registration**

Read `config/tmux.conf.nix` around lines 744-757 to confirm current content matches (it should show the `after-new-window[10]`/`after-new-session[10]` hooks followed by the `pane-exited` claude-status cleanup hook). Then use Edit with:

old_string:
```
    set-hook -g after-new-window[10]  'run-shell -b "${script.tmux-reconcile-window}/bin/tmux-reconcile-window #{window_id}"'
    set-hook -g after-new-session[10] 'run-shell -b "${script.tmux-reconcile-window}/bin/tmux-reconcile-window #{window_id}"'

    # Clean up claude status file when a pane closes (pane_id is %N, files are just N)
```

new_string:
```
    set-hook -g after-new-window[10]  'run-shell -b "${script.tmux-reconcile-window}/bin/tmux-reconcile-window #{window_id}"'
    set-hook -g after-new-session[10] 'run-shell -b "${script.tmux-reconcile-window}/bin/tmux-reconcile-window #{window_id}"'

    # Post-#199 (issue #100): after-split-window / pane-focus-in reconcile hooks
    # were considered here and deliberately NOT added. Since #199
    # (tmux-worktree-match.sh), every `wt switch` already treats a stale
    # @worktree tag as untrusted: it unsets a tag no pane corroborates and
    # retags whichever window the switch actually lands on. The residual gap —
    # a window's worktree changes via a raw split/focus/cd and is never
    # `wt switch`ed into again — is real but narrow (wt switch is this repo's
    # primary navigation path), while pane-focus-in fires on every pane focus
    # change, the hottest per-interaction path in this config, and
    # tmux-reconcile-window forks ~7 subprocesses even in its idempotent
    # branch. Not worth it.

    # Clean up claude status file when a pane closes (pane_id is %N, files are just N)
```

- [ ] **Step 2: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "docs(reconcile): record why post-creation split/focus hooks were not added (#100)"
```

---

### Task 2: Document the Gap B finding in `modules/home-manager.nix`

**Files:**
- Modify: `modules/home-manager.nix:72-96`

- [ ] **Step 1: Read the current block to confirm exact content**

Read `modules/home-manager.nix:60-108` to confirm `tmuxStateBin`/`tmuxStateConf` still match (they should — nothing else in this plan touches this file before this task).

- [ ] **Step 2: Add the decision comment above the `restoreMode == "auto"` block**

old_string:
```
      set-hook -g pane-exited[99]           'run-shell -b "${tmuxStateBin} capture-event pane-died          --pane=#{hook_pane}    --window=#{hook_window} --session=#{hook_session}"'
      set-hook -g window-unlinked[99]       'run-shell -b "${tmuxStateBin} capture-event window-unlinked    --window=#{hook_window} --session=#{hook_session}"'
      set-hook -g session-closed[99]        'run-shell -b "${tmuxStateBin} capture-event session-closed     --session=#{hook_session}"'

      ${lib.optionalString (cfg.persist.restoreMode == "auto") ''
        run-shell -b '${tmuxStateBin} restore --auto'
      ''}
```

new_string:
```
      set-hook -g pane-exited[99]           'run-shell -b "${tmuxStateBin} capture-event pane-died          --pane=#{hook_pane}    --window=#{hook_window} --session=#{hook_session}"'
      set-hook -g window-unlinked[99]       'run-shell -b "${tmuxStateBin} capture-event window-unlinked    --window=#{hook_window} --session=#{hook_session}"'
      set-hook -g session-closed[99]        'run-shell -b "${tmuxStateBin} capture-event session-closed     --session=#{hook_session}"'

      # Issue #100 asked for a reconcile-all sweep here, on the premise that a
      # restored window has no @worktree until the user navigates into it. That
      # premise doesn't hold for the currently pinned tmux-remux: restore/undo/
      # pick all build their plan via the same restore.Apply (internal/restore/
      # apply.go), which passes -c <historical cwd> directly to new-session/
      # new-window/split-window — so the after-new-window[10]/after-new-session[10]
      # creation hooks (config/tmux.conf.nix) already tag every restored window
      # correctly, synchronously, with no async cd to race. Verified empirically
      # (isolated tmux server, real tmux-remux save/restore, tag present
      # immediately), not just by reading source. No sweep added. If a future
      # tmux-remux bump ever stops passing -c at creation, this would need
      # revisiting (see CLAUDE.md's Persist section).
      ${lib.optionalString (cfg.persist.restoreMode == "auto") ''
        run-shell -b '${tmuxStateBin} restore --auto'
      ''}
```

- [ ] **Step 3: Commit**

```bash
git add modules/home-manager.nix
git commit -m "docs(reconcile): record why no reconcile-all sweep was added for tmux-remux restore (#100)"
```

---

### Task 3: Update `CLAUDE.md`'s Persist section

**Files:**
- Modify: `CLAUDE.md` (the "Persist (tmux-remux)" section, currently ~line 93-107)

- [ ] **Step 1: Add a bullet noting restored windows are already tagged**

Read the "Persist (tmux-remux)" section first (`grep -n "### Persist (tmux-remux)" CLAUDE.md`, then read the following ~15 lines) to confirm content still matches, then Edit:

old_string:
```
- `restoreMode` defaults to `"off"` (manual `prefix + R` only). Set to `"auto"`
  to apply the smart filter on tmux server start.
```

new_string:
```
- `restoreMode` defaults to `"off"` (manual `prefix + R` only). Set to `"auto"`
  to apply the smart filter on tmux server start.
- Restored windows already get `@worktree`/`@branch`/`@issue_*` from the
  ordinary `after-new-window`/`after-new-session` creation hooks — tmux-remux's
  restore/undo/pick all create windows via `new-window -c`/`new-session -c`
  with the historical cwd, so the hook's cwd read never races an async `cd`
  (#100). This depends on tmux-remux continuing to pass `-c` at creation; if a
  future tmux-remux bump stops doing that, this would need revisiting.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: record that tmux-remux restore already tags windows via the creation hooks (#100)"
```

---

### Task 4: Verification and PR

**Files:** none (verification + PR body only)

- [ ] **Step 1: Confirm nothing else changed**

Run: `cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-100-reconcile-post-creation-worktree-changes && git diff --stat`
Expected: only `config/tmux.conf.nix`, `modules/home-manager.nix`, `CLAUDE.md` — comment/doc-only diffs, no logic lines changed.

- [ ] **Step 2: Full flake check (from inside the dev shell, per this repo's `CLAUDE.md`)**

Run: `nix develop -c nix flake check`
Expected: green — this is a comment-only change, but running the full check is cheap and confirms the Nix string edits didn't break anything (an unbalanced `#` inside a Nix multi-line string, for instance, would still be worth catching).

- [ ] **Step 3: Confirm the full tmux package still builds**

Run: `nix build .#default`
Expected: succeeds.

- [ ] **Step 4: Write the PR body with the full evidence trail**

The PR description must include:
- The Gap Survival Analysis above (or a tight summary linking to it), including the empirical reproduction steps for Gap B (isolated tmux server + real `tmux-remux save`/`restore --auto`, tag present immediately).
- An explicit recommendation to close #100 as already resolved by #199 (Gap A) and by tmux-remux's own `-c`-at-creation design (Gap B) — not by new code in this repo.
- The caveat that Gap B's closure is contingent on tmux-remux continuing to pass `-c` at creation time, noted in `CLAUDE.md` for future reference.

---

## Self-Review Notes

- **Spec coverage:** Gap A → Task 1 (decision + comment, no hooks added). Gap B → Task 2 (decision + comment, no sweep added, empirically verified). Written gap-survival statement before code → the analysis section above. `nix flake check` green → Task 4. Bats coverage requirement is moot — no new pure logic was added (the acceptance criterion "pure logic gets bats coverage" has nothing to cover when no pure logic is written).
- **No placeholders:** every step has literal file content, exact commands, and expected output.

---

## Revision Log

**Revision 1 (plan-critic pass 1, 3 blocking findings, all fixed at the time):** the original draft added a `tmux-reconcile-all` script chained after tmux-remux's restore/pick/undo. A `plan-critic` pass found: (1) the Nix-laziness verification for the resulting `tmuxStateConf ↔ tmuxConfig.script` reference was unfalsifiable, (2) the chain landed on two foreground/interactive paths despite `tmux-reconcile-window` costing ~7 forks per window, and (3) the Gap B evidence overstated what breaks after a restore (`tmux-update-icons.sh` already self-heals `@branch`/`@git_root`). All three were fixed in that revision.

**Revision 2 (plan-critic pass 2, blocking finding, led to a full rewrite):** a second `plan-critic` pass, asked to re-verify the revised plan, raised one more blocking finding: **Gap B's central premise — that tmux-remux's restore leaves windows untagged — was never actually verified**, only assumed from the issue body, and in fact `tmux-update-icons.sh`'s own comment ("tmux-remux restore creates windows with `new-window -n`") plus this repo's own precedent comment for `new-window`-based tagging (`modules/home-manager.nix:927-931`, "A bare `new-window` is enough...") both point the other way. Acting on that finding: `internal/restore/apply.go` in the pinned `tmux-remux` revision was read directly (confirming `-c <cwd>` on every window-creating action) and then **empirically reproduced** end-to-end in an isolated tmux server against the real, currently-pinned `tmux-remux` binary — a window was tagged, saved, the server killed and restarted with the same creation hooks, and `restore --auto` run for real. The restored window was tagged correctly immediately, with no sweep. This **fully falsified the plan's Gap B premise** rather than merely needing a wording fix, so the plan was rewritten from a 6-task, script-adding design down to a 4-task, documentation-only one. This is the plan now on file; a third adversarial critic pass was not run given the strength of the empirical evidence (a live reproduction against the actual pinned dependency, not a static-analysis inference) and the revision cap already reached — the evidence itself, not another critic's read of prose, is what should be checked before execution.
