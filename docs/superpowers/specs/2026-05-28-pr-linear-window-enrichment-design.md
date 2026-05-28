# PR + Issue-Tracker Window Enrichment

**Status:** Draft
**Date:** 2026-05-28
**Owner:** Noam Stolero

## Problem

When running multiple Claude Code instances on parallel git worktrees, tmux windows are visually indistinguishable beyond their branch name. There is no indication of which Linear/GitHub issue a window owns, whether a PR exists, or what state the PR's checks are in. This forces context-switching to `gh pr view` / Linear UI just to identify which window holds which work.

## Goals

- Show **per-window issue identity** (Linear or GitHub Issues) in the tmux window-list at a glance.
- Show **PR number + check state** when a PR exists for the window's branch.
- Show **full issue/PR titles** on the global status line for the focused window.
- Provide **keybinds to open the issue and PR in a browser** without leaving tmux.
- Support **two issue providers** (Linear, GitHub Issues) auto-detected per worktree.

## Non-goals

- Editing issues or PRs from tmux (no comment, no review, no state change).
- Notification on PR state changes (no toasts, no sound).
- Multi-issue per window (one issue per window).
- Jira or other providers in v1 — extension path documented, not built.
- Replacing `gh` or `linear` CLIs — we shell out to them rather than re-implementing.

## Architecture

Tmux **window options** are the single source of truth for enrichment data. Three populating mechanisms write to them; display and keybinds only read.

```
                       ┌─────────────────────────────────────┐
                       │ Window options (per-window state)   │
                       │   @branch        @worktree          │
                       │   @issue_provider                   │
                       │   @issue_id      @issue_title       │
                       │   @issue_url                        │
                       │   @pr_number     @pr_title          │
                       │   @pr_state      @pr_check_state    │
                       │   @pr_url                           │
                       └────────────▲────────────────────────┘
                                    │
       ┌─────────────────┬──────────┴──────────┬─────────────────┐
       │                 │                     │                 │
 worktrunk          tmux-issue-stamp      tmux-pr-enrich    prefix + g R
 post-switch        (one-shot, on         (background,      (manual force
 (creates window)   window creation)      ~30s per branch)   refresh)
       │                 │                     │                 │
       └─ sets @branch,  └─ sets @issue_*     └─ sets @pr_*    └─ both
          @worktree
```

Consumers (read-only):

- `automatic-rename-format` → compact window-list entry per tab
- `status-format[0]` → full context line for the focused window
- Keybinds (`prefix + g L` / `g P` / `g R`) → open URLs, force refresh

### Why window options as state

- Already the lazytmux convention (`@branch`, `@worktree`, `@window_icon_display`, etc.)
- Per-window scoping with no extra plumbing
- Cheap reads from tmux format strings
- Survives reflow, theme changes, and pane operations without re-fetch

## Components

### `tmux-issue-stamp` (one-shot dispatcher)

**When it runs:** Tail of the worktrunk `post-switch` hook (defined in `modules/home-manager.nix`), immediately after `@worktree` and `@branch` are set on the new/matched window.

**Signature:** `tmux-issue-stamp <target> <worktree-path> <branch>`

**Behavior:** Iterates configured providers in priority order; first to return a complete `(id, title, url)` triple wins. Writes `@issue_provider`, `@issue_id`, `@issue_title`, `@issue_url` on the target window. If no provider matches, writes nothing — display falls back to current branch-name behavior.

**Exit:** Always 0 (errors silently logged to stderr only).

### `tmux-issue-stamp-linear` (provider impl)

**Inputs:** `<worktree-path> <branch>` on argv, runs from within `<worktree-path>`.

**Detection signal (any one):**

- `linear` CLI present AND `linear issue id` returns non-empty
- `.linear.toml` exists in repo root (CLI present implied)
- Branch matches `(?i)[a-z]+-[0-9]+` AND `linear` provider is on the priority list

**Resolution:**

- If `linear` CLI present: shell out to `linear issue id`, `linear issue title`, `linear issue url`. Total cost ~1–2s; acceptable because it runs once per worktree, off the hot path.
- Else: regex `(?i)[a-z]+-[0-9]+` against branch slug, uppercase result. `id` set; `title` and `url` left empty.

**Output:** Three lines on stdout — `id\ntitle\nurl\n`. Empty lines for unset fields. Errors → empty output, exit 0.

### `tmux-issue-stamp-github` (provider impl)

**Inputs:** `<worktree-path> <branch>` on argv.

**Detection signal:** `git -C <worktree-path> remote get-url origin` matches `github.com/*` AND branch matches one of:

- `^(\d+)-` (e.g. `247-fix-bug`)
- `/(\d+)-` (e.g. `feature/247-fix-bug`)
- `^gh-(\d+)` (e.g. `gh-247`)
- `^issue-(\d+)` (e.g. `issue-247`)

**Resolution:** First matching capture group → `gh issue view <n> --json number,title,url` (in worktree dir for repo context). Parse JSON for title, url.

**Output:** Same format as Linear stamp. Errors (no match, gh fails) → empty, exit 0.

### `tmux-pr-enrich` (background poller)

**When it runs:** A hidden `#()` call in `status-format[0]` invokes `tmux-pr-enrich --tick` on every tmux status refresh (~1s). The script gates real work behind an mtime check on `/tmp/lazytmux-pr/.last-tick`: cheap exit (~10ms) when fresh; when stale (>= configured `prRefreshSeconds`), it daemonizes (`exec >&- 2>&- ; setsid`) and the parent returns immediately. This matches the fire-and-forget pattern in `claude-status` and avoids new systemd units. Also called directly by `prefix + g R --force --target <session:window>` to bypass cache for a single window.

**Per-call logic:**

1. Iterate `tmux list-windows -a -F '#{session_id}:#{window_id}|#{@worktree}|#{@branch}'`.
2. Deduplicate by branch. Cap at 30 unique branches per cycle.
3. For each branch:
   - Check `/tmp/lazytmux-pr/<sha1>.json` mtime; skip if `<60s` and not `--force`.
   - `flock -n /tmp/lazytmux-pr/<sha1>.lock` — skip if another process holds the lock.
   - Shell out: `gh pr list --head $branch --state open --limit 1 --json number,title,url,state,statusCheckRollup --timeout 5`
   - If empty → `gh pr list --head $branch --state all --limit 1 --json ...` (fallback to closed/merged).
   - Write JSON + `fetched_at` to cache file.
4. For each window touching the branch, set window options from cache:
   - `@pr_number`, `@pr_title`, `@pr_url`, `@pr_state` (open/closed/merged), `@pr_check_state` (pending/success/failure/none)
5. On no PR for branch: set `@pr_number=none`, clear other `@pr_*`.

**Auth failure:** First `unauthed` response sets `@pr_state=unauthed` on all windows and bumps cache TTL to "never auto-refresh until force." User unblocks via `gh auth refresh` + `prefix + g R`.

**Mock mode:** `--mock-pr-number=247 --mock-pr-state=open --mock-check-state=pending --mock-pr-title="..." --mock-pr-url=...` short-circuits the `gh` call and writes options directly. Used for display testing and screenshots.

### `lib-enrich.sh` (shared helpers)

Sourced by all scripts above. Functions (using the `REPLY` convention to avoid subshells):

- `sanitize_title <raw>` → strips `\r\n\033`, truncates to 50 chars, sets `REPLY`.
- `branch_sha1 <branch>` → stable cache key, sets `REPLY`.
- `collapse_check_rollup <json>` → maps `statusCheckRollup` array to one of `pending|success|failure|none`, sets `REPLY`. Priority: any FAILURE/ERROR/CANCELLED/TIMED_OUT → failure; else any PENDING/IN_PROGRESS/QUEUED → pending; all SUCCESS/NEUTRAL/SKIPPED → success; empty array → none.
- `provider_priority_list` → reads `programs.lazytmux.enrich.providers` (passed via Nix-time substitution), sets `REPLY` as space-separated.

## Data flow

### Window creation

1. User runs `wt switch -c noa-123-foo`.
2. Worktrunk creates the worktree at `<repo>/.worktrees/noa-123-foo`.
3. `post-switch` hook fires: navigates tmux, sets `@worktree`, `@branch`.
4. **New tail step:** hook runs `tmux-issue-stamp $TARGET $WORKTREE $BRANCH &` (backgrounded — non-blocking).
5. Stamp script tries Linear (CLI in worktree dir), gets `NOA-123`, `Add foo bar`, URL → writes `@issue_*`.
6. Stamp script also fires `tmux-pr-enrich --target $TARGET --force` to attempt immediate PR fetch (likely no PR yet for a fresh branch, sets `@pr_number=none`).

### Background refresh (every 30s)

1. `status-format[0]` includes a hidden `#(tmux-pr-enrich --tick)` call.
2. Script reads `/tmp/lazytmux-pr/.last-tick` mtime. If newer than `prRefreshSeconds` (default 30), exits ~10ms with no output.
3. Otherwise touches `.last-tick`, daemonizes (closes stdio, `setsid`), parent exits to tmux immediately. Detached child iterates all windows, dedups branches, hits `gh` only for stale per-branch cache entries.

### Force refresh (`prefix + g R`)

1. Resolves current window's branch.
2. Deletes `/tmp/lazytmux-pr/<sha1>.json` and re-runs `tmux-issue-stamp` + `tmux-pr-enrich --force` for that window.
3. Both scripts are invoked synchronously; small visible delay (~1s) is acceptable for a manual action.

### Cache layout

```
/tmp/lazytmux-pr/
  <branch-sha1>.json    ← gh output + fetched_at timestamp
  <branch-sha1>.lock    ← flock file
  .last-tick            ← timestamp of last full ticker pass
```

- TTL: 60s
- Per-branch (not per-window) — multiple windows on same branch share cache
- No eviction: entries older than 24h are removed when touched. `/tmp` clears on boot.
- Path is hardcoded; can move to `$XDG_RUNTIME_DIR/lazytmux-pr/` in a follow-up if needed.

## Display

### Window-list (compact, per-tab)

Each tab carries: **provider icon · issue id · PR check icon · PR number · claude icon**. Branch name is omitted when an issue is detected (issue ID is the canonical identifier; branch is still on line 0).

Icon variables (Nix-time string substitution, user-overridable via module options):

```
ICON_LINEAR  = # nerd: nf-md-alpha-l-circle
ICON_GITHUB  = # nerd: nf-md-github
ICON_PEND    = # nerd: nf-md-progress-clock   (thm_peach)
ICON_OK      = # nerd: nf-md-check-circle     (thm_green)
ICON_FAIL    = # nerd: nf-md-alert-circle     (thm_red)
ICON_MERGED  = # nerd: nf-md-source-merge     (thm_mauve)
```

State matrix:

| State | Tab content |
|---|---|
| Linear + open PR, pending | `<ICON_LINEAR> NOA-123  <ICON_PEND> #247 <claude>` |
| Linear + open PR, passing | `<ICON_LINEAR> NOA-123  <ICON_OK> #247 <claude>` |
| Linear + open PR, failing | `<ICON_LINEAR> NOA-123  <ICON_FAIL> #247 <claude>` |
| Linear + merged PR | `<ICON_LINEAR> NOA-123  <ICON_MERGED> #247 <claude>` (dimmed) |
| Linear + no PR yet | `<ICON_LINEAR> NOA-123 <claude>` |
| GH + open PR, passing | `<ICON_GITHUB> #142  <ICON_OK> #247 <claude>` |
| No provider | `<branch-30char> <claude>` (current behavior, unchanged) |

Width budget: worst case ~22 display cells; fits inside tmux's ~25-cell tab budget before truncation.

### Status line 0 (focused window)

When an issue is detected, the existing **branch segment** in `status-format[0]` is replaced by an issue segment. A PR segment is appended when present.

Today:
```
 󰓩 lazytmux  <branch-icon> feat/add-foo  <dir-icon> ./scripts  <claude>  …
```

With enrichment:
```
 󰓩 lazytmux  <ICON_LINEAR> NOA-123 Add foo bar  <ICON_PEND> #247 Implement enrichment   <dir-icon> ./scripts  <claude>  …
```

- Issue title truncated to 25 chars + ellipsis
- PR title truncated to 25 chars + ellipsis
- PR segment color matches check state
- When no issue: line 0 is unchanged from today

### Color encoding

Reuses existing `@thm_*` Catppuccin variables (light/dark theme-aware via `tmux-apply-theme-colors.sh`):

| Element | Color |
|---|---|
| Provider icon | `@thm_blue` |
| Issue ID | `@thm_text` |
| PR pending | `@thm_peach` |
| PR success | `@thm_green` |
| PR failure | `@thm_red` |
| PR merged | `@thm_mauve` |
| PR closed (not merged) | `@thm_overlay_0` |

### Keybinds

| Key | Action |
|---|---|
| `prefix + g L` | `xdg-open` value of `@issue_url` (no-op if unset) |
| `prefix + g P` | `xdg-open` value of `@pr_url` (no-op if unset) |
| `prefix + g R` | Force-refresh enrichment for current window (cache bypass) |

The `g` chord prefix avoids collision with existing `prefix + R` (snapshot picker), `prefix + L` (last-window in some configs).

## Error handling & edge cases

| Scenario | Behavior |
|---|---|
| `gh` not on PATH | Enrich script no-ops silently. PR options never set. |
| `gh` auth expired | First failed call sets `@pr_state=unauthed`; no retries until `prefix + g R`. Status line shows `⚠ gh`. |
| `linear` CLI absent / unauthed | Linear stamp falls back to branch regex. Title stays unset; tab shows `<ICON_LINEAR> NOA-123` only. |
| Branch has no PR | `@pr_number=none`; window-list omits PR segment. Re-checked each cycle (cache-cheap). |
| Multiple PRs on same branch | `gh pr list --head $branch --state open --limit 1` prefers open. Falls back to most recent closed/merged. |
| Branch renamed via `git branch -m` | Enrich script reads `git branch --show-current` per cycle; if it differs from `@branch`, re-stamps everything. |
| Detached HEAD | Enrichment skipped; tab falls back to branch-display behavior. |
| Window with no `@worktree` (manual `new-window`) | Enrichment skipped entirely. |
| Network down / `gh` timeout | 5s hard timeout; cache retained; `⚠ gh` shown only if no cache. |
| Rate limit hit | Cache TTL extends to 10min until next success. Worst case under normal load stays well under GH/Linear limits. |
| PR check state changes mid-cache | Stale up to 60s; `prefix + g R` resolves. |
| Branch's PR replaced (e.g. #247 closed, #248 opened) | Next 30s cycle catches via `--state open` filter. |
| Title with control chars / newlines | Sanitized via `tr -d '\r\n\033'`; hard-truncated to 50 chars. |
| Concurrent enrichment, same branch, N windows | `flock` on per-branch lockfile; one `gh` call wins, others skip-if-fresh. |
| Refresh missing some windows | Ticker iterates all windows in all sessions, dedup by branch, cap 30 per cycle. |
| Worktree deleted on disk | `git -C $worktree` fails; options stay stale until window closes. |
| Theme change | Colors update automatically via `@thm_*` vars. |

**Sticky-unauthed decision:** Once `gh` auth fails we stop auto-retrying (each call burns ~300ms even on failure). User explicitly unblocks via `gh auth refresh` + `prefix + g R`. Alternative (exponential backoff) is a follow-up if this proves annoying.

## Configuration

New home-manager option block, mirroring the style of `programs.lazytmux.persist`:

```nix
programs.lazytmux.enrich = {
  enable = lib.mkEnableOption "PR + issue enrichment in tmux window names" // { default = true; };

  providers = lib.mkOption {
    type = lib.types.listOf (lib.types.enum [ "linear" "github" ]);
    default = [ "linear" "github" ];
    description = "Issue-tracker providers, tried in priority order. First match wins.";
  };

  linearCli = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      Bundle schpet/linear-cli on PATH (built from flake input).
      When false, the script still uses `linear` if found elsewhere on PATH.
    '';
  };

  prRefreshSeconds = lib.mkOption {
    type = lib.types.ints.between 10 300;
    default = 30;
    description = "Background PR enrichment cadence in seconds (clamped 10–300).";
  };

  icons = lib.mkOption {
    type = lib.types.attrsOf lib.types.str;
    default = {
      linear   = "ICON_LINEAR_PLACEHOLDER";   # set by user, default is nerd-font glyph
      github   = "ICON_GITHUB_PLACEHOLDER";
      pending  = "ICON_PEND_PLACEHOLDER";
      success  = "ICON_OK_PLACEHOLDER";
      failure  = "ICON_FAIL_PLACEHOLDER";
      merged   = "ICON_MERGED_PLACEHOLDER";
    };
    description = "Override icon glyphs. Defaults are nerd-font; per CLAUDE.md user sets exact glyphs.";
  };
};
```

`enable = false` removes the worktrunk hook tail step, the status-format hidden tick, and the keybinds. Scripts are not built into the closure.

## Testing strategy

The project has no unit tests today (per `CLAUDE.md`). This feature adds a minimal `bats-core` suite for **pure-logic regressions only**.

### `tests/enrich.bats` (new, ~30–50 cases)

Tested in isolation:

- **`branch_to_linear_key`** — regex extraction with edge cases:
  - `noa-123-foo` → `NOA-123`
  - `NOA-123-foo` → `NOA-123`
  - `feature/noa-123-foo` → `NOA-123`
  - `noa-123` → `NOA-123`
  - `123-foo` → empty
  - `main` → empty
- **`branch_to_gh_issue_number`** — regex extraction:
  - `247-fix-bug` → `247`
  - `gh-247-fix` → `247`
  - `feature/247-foo` → `247`
  - `noa-123-foo` → empty (so Linear matches it, not GH)
- **`collapse_check_rollup`** — JSON fixture in, single state out:
  - All success → `success`
  - One failure among successes → `failure`
  - All pending → `pending`
  - Empty array → `none`
  - Mixed pending + neutral → `pending`
- **`sanitize_title`** — strips CR/LF/ESC, truncates 50 chars, ellipsizes
- **`provider_priority_resolution`** — given mocked detection signals, picks the right provider:
  - Linear CLI present + GH origin → linear wins
  - No Linear, GH origin + numeric branch → github wins
  - Both fail → none

### Mock-mode display test

Shell script `tests/test-display.sh`:

1. Spawns a fresh tmux server on a temp socket.
2. Creates 5 windows with `@worktree` set to fake paths.
3. Runs each script in `--mock` mode to populate options for the 5 states from "Display" section.
4. Captures `tmux list-windows -F '#{window_name}'` output.
5. Diffs against `tests/fixtures/window-list.expected`.

This catches regressions in the rename-format string (tmux format DSL is finicky) without needing live GH/Linear access.

### Pre-commit & CI

- `shellcheck` and `shfmt` on all new scripts (project convention)
- `bats-core` added to flake `devShell` and to `flake.checks` so `nix flake check` runs the suite
- `tests/test-display.sh` also runs in `flake.checks` (requires tmux at build time — already in scope)
- Manual smoke test in real tmux session after `nix build .`

### What's not tested

- Network I/O paths (`gh`, `linear` calls) — verified manually
- tmux option I/O — would need a tmux harness; high cost, low ROI

## File changes

| File | Change |
|---|---|
| `scripts/tmux-issue-stamp.sh` | **new** — dispatcher |
| `scripts/tmux-issue-stamp-linear.sh` | **new** — Linear provider |
| `scripts/tmux-issue-stamp-github.sh` | **new** — GH Issues provider |
| `scripts/tmux-pr-enrich.sh` | **new** — background poller, mock-mode |
| `scripts/lib-enrich.sh` | **new** — shared helpers |
| `config/tmux.conf.nix` | modify — register scripts, hidden `#()` tick, update window-list format, line 0 segments, `g L`/`g P`/`g R` keybinds |
| `modules/home-manager.nix` | modify — add `enrich` option block; extend worktrunk `post-switch` hook with `tmux-issue-stamp` tail call |
| `flake.nix` | modify — add `schpet-linear-cli` flake input (when `linearCli = true`); derive `linearCli` package |
| `tests/enrich.bats` | **new** — unit tests for pure logic |
| `tests/fixtures/*.json` | **new** — gh JSON fixtures |
| `tests/test-display.sh` | **new** — mock-mode display regression test |
| `flake.nix` | modify — extend `checks` with bats suite + display test; add `bats-core` to `devShell` |
| `CLAUDE.md` | modify — add enrichment scripts to script table; note window-options as truth |

## Out of scope (deferred / future)

- **Jira / other providers.** Extension path: new `tmux-issue-stamp-jira.sh`, add `"jira"` to providers enum, ship icon. ~1–2h of work.
- **Linear title fetch via GraphQL** when CLI not present. Currently fallback is regex-only; full fetch would need `LINEAR_API_KEY` secret management (agenix). Add only if regex-fallback proves insufficient.
- **PR notifications** (toast on check failure, success). Out of scope — file a follow-up if desired.
- **Issue/PR creation from tmux.** Out of scope — use `gh pr create` / Linear UI.
- **Per-repo provider override** via `.lazytmux.toml`. Auto-detection in v1 is good enough; revisit if a repo's branch shape misleads detection.
- **Reading from `$XDG_RUNTIME_DIR` instead of `/tmp`.** Cosmetic; `/tmp` is fine for v1.

## Open questions

None blocking. To be resolved during implementation:

- Exact nerd-font glyphs for `ICON_LINEAR`, `ICON_GITHUB`, etc. — user sets in module config per the project's icon convention.
- Whether to package schpet/linear-cli via `buildNpmPackage` (npm release) or fetch their prebuilt GitHub release tarball. Both viable; pick at implementation time.
