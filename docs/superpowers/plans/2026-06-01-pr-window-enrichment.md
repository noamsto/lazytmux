# PR + Issue-Tracker Window Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface per-worktree Linear/GitHub issue identity and PR check-state in the tmux window-list and status line, with keybinds to open the issue/PR in a browser.

**Architecture:** Tmux **window options** are the single source of truth (`@issue_*`, `@pr_*`). A one-shot dispatcher (`tmux-issue-stamp`) writes issue identity on window creation; a fire-and-forget poller (`tmux-pr-enrich`) refreshes PR state every ~30s via a hidden `#()` tick in `status-format[0]`. Display formats and keybinds only read these options. All shell-out logic (gh/linear) is isolated behind provider scripts; pure logic lives in `lib-enrich.sh` and is unit-tested with bats.

**Tech Stack:** Bash (tmux scripts), Nix (flake + home-manager module), `gh` CLI, optional `linear` CLI, `jq`, `bats-core` (tests).

**Design reference:** `docs/superpowers/specs/2026-05-28-pr-linear-window-enrichment-design.md`

**Keybind decision (overrides spec's `g` chord, which collides with lazygit on `prefix + g`):** a dedicated `enrich` key-table on `prefix + i` — `i` open issue, `p` open PR, `r` force refresh.

---

## File Structure

| File | Responsibility |
|---|---|
| `scripts/lib-enrich.sh` | **new** — pure helpers (REPLY convention): branch→key regexes, title sanitize, sha1 cache key, check-rollup collapse, provider priority. Sourced by all enrich scripts. |
| `scripts/tmux-issue-stamp.sh` | **new** — dispatcher: tries providers in priority order, writes `@issue_*`, kicks an immediate PR fetch. |
| `scripts/tmux-issue-stamp-linear.sh` | **new** — Linear provider: `linear` CLI or branch regex → `id\ntitle\nurl`. |
| `scripts/tmux-issue-stamp-github.sh` | **new** — GitHub Issues provider: branch regex + `gh issue view` → `id\ntitle\nurl`. |
| `scripts/tmux-pr-enrich.sh` | **new** — background poller (cache + flock + daemonize), `--tick`/`--force`/`--target`/mock-mode; writes `@pr_*`. |
| `config/tmux.conf.nix` | modify — register 5 scripts, add icon placeholders, window-list + line-0 segments, hidden tick, `prefix + i` key-table. |
| `modules/home-manager.nix` | modify — `programs.lazytmux.enrich` option block; gate scripts into closure; worktrunk `post-switch` hook tail call. |
| `flake.nix` | modify — add `bats-core` to devShell; add `enrich-tests` + `enrich-display` to `checks`. |
| `tests/enrich.bats` | **new** — unit tests for pure lib logic. |
| `tests/fixtures/*.json` | **new** — gh `statusCheckRollup` JSON fixtures. |
| `tests/test-display.sh` | **new** — mock-mode window-list regression test on a throwaway tmux server. |
| `tests/helper.bash` | **new** — bats helper that sources `lib-enrich.sh` with placeholders stubbed. |
| `CLAUDE.md` | modify — add enrich scripts to script table; note window-options as truth. |

**Cache layout** (`/tmp/lazytmux-pr/`): `<branch-sha1>.json` (gh output + `fetched_at`), `<branch-sha1>.lock` (flock), `.last-tick` (last ticker pass).

**Window options written:** `@issue_provider @issue_id @issue_title @issue_url @pr_number @pr_title @pr_state @pr_check_state @pr_url`.

---

## Task 1: `lib-enrich.sh` — branch→key regex extractors

**Files:**
- Create: `scripts/lib-enrich.sh`
- Create: `tests/helper.bash`
- Create: `tests/enrich.bats`

- [ ] **Step 1: Create the bats helper that sources the lib**

`tests/helper.bash`:

```bash
# Sources lib-enrich.sh for bats with Nix placeholders stubbed to defaults.
# Run from repo root: bats tests/enrich.bats
setup_lib_enrich() {
	local tmp
	tmp="$(mktemp)"
	# Substitute the @providers@ placeholder so the un-built script is sourceable.
	sed 's/@providers@/linear github/g' scripts/lib-enrich.sh >"$tmp"
	# shellcheck disable=SC1090
	source "$tmp"
	rm -f "$tmp"
}
```

- [ ] **Step 2: Write the failing tests for `branch_to_linear_key`**

Append to `tests/enrich.bats`:

```bash
#!/usr/bin/env bats

load helper

setup() {
	setup_lib_enrich
}

@test "branch_to_linear_key: lowercase team-num" {
	branch_to_linear_key "noa-123-foo"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: already uppercase" {
	branch_to_linear_key "NOA-123-foo"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: with slash prefix" {
	branch_to_linear_key "feature/noa-123-foo"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: bare key no suffix" {
	branch_to_linear_key "noa-123"
	[ "$REPLY" = "NOA-123" ]
}

@test "branch_to_linear_key: pure-numeric prefix is not a linear key" {
	branch_to_linear_key "123-foo"
	[ -z "$REPLY" ]
}

@test "branch_to_linear_key: plain branch name yields empty" {
	branch_to_linear_key "main"
	[ -z "$REPLY" ]
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `nix develop -c bats tests/enrich.bats`
Expected: FAIL — `command not found: branch_to_linear_key` (or source error if `scripts/lib-enrich.sh` does not exist yet).

- [ ] **Step 4: Create `lib-enrich.sh` with `branch_to_linear_key`**

`scripts/lib-enrich.sh`:

```bash
#!/usr/bin/env bash
# Shared issue/PR enrichment utilities for tmux scripts.
# Sourced (not executed) — provides constants and functions.
# Functions use the REPLY convention (set REPLY instead of echoing) to avoid
# subshell forks, matching lib-icons.sh / lib-claude.sh.

# shellcheck disable=SC2034  # used by scripts that source this library

ENRICH_CACHE_DIR="/tmp/lazytmux-pr"

# branch_to_linear_key BRANCH
# Extracts a Linear issue key (TEAM-123) from a branch name.
# Requires letters before the dash (pure-numeric prefixes are GitHub issues).
# Sets REPLY to the uppercased key, or empty if no match.
branch_to_linear_key() {
	local branch="$1"
	REPLY=""
	# Take the last path segment, then match <letters>-<digits> at its start.
	local slug="${branch##*/}"
	if [[ $slug =~ ^([A-Za-z]+-[0-9]+) ]]; then
		REPLY="${BASH_REMATCH[1]^^}"
	fi
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `nix develop -c bats tests/enrich.bats`
Expected: PASS (6 tests).

- [ ] **Step 6: Add failing tests for `branch_to_gh_issue_number`**

Append to `tests/enrich.bats`:

```bash
@test "branch_to_gh_issue_number: leading number" {
	branch_to_gh_issue_number "247-fix-bug"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: gh- prefix" {
	branch_to_gh_issue_number "gh-247-fix"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: issue- prefix" {
	branch_to_gh_issue_number "issue-247"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: slash then number" {
	branch_to_gh_issue_number "feature/247-foo"
	[ "$REPLY" = "247" ]
}

@test "branch_to_gh_issue_number: linear-style branch is not a gh issue" {
	branch_to_gh_issue_number "noa-123-foo"
	[ -z "$REPLY" ]
}

@test "branch_to_gh_issue_number: plain branch yields empty" {
	branch_to_gh_issue_number "main"
	[ -z "$REPLY" ]
}
```

- [ ] **Step 7: Run to verify the new tests fail**

Run: `nix develop -c bats tests/enrich.bats`
Expected: FAIL — `command not found: branch_to_gh_issue_number`.

- [ ] **Step 8: Implement `branch_to_gh_issue_number`**

Append to `scripts/lib-enrich.sh`:

```bash
# branch_to_gh_issue_number BRANCH
# Extracts a GitHub issue number from a branch name. Matches:
#   ^<digits>-         247-fix-bug
#   /<digits>-         feature/247-foo
#   ^gh-<digits>       gh-247
#   ^issue-<digits>    issue-247
# Sets REPLY to the number, or empty if no match.
branch_to_gh_issue_number() {
	local branch="$1"
	REPLY=""
	local slug="${branch##*/}"
	if [[ $slug =~ ^(gh|issue)-([0-9]+) ]]; then
		REPLY="${BASH_REMATCH[2]}"
	elif [[ $slug =~ ^([0-9]+)- ]]; then
		REPLY="${BASH_REMATCH[1]}"
	fi
}
```

- [ ] **Step 9: Run to verify all tests pass**

Run: `nix develop -c bats tests/enrich.bats`
Expected: PASS (12 tests).

- [ ] **Step 10: Lint and commit**

```bash
shellcheck scripts/lib-enrich.sh
git add scripts/lib-enrich.sh tests/helper.bash tests/enrich.bats
git commit -m "feat(enrich): add branch→issue-key regex extractors with tests"
```

---

## Task 2: `lib-enrich.sh` — title sanitize + cache key

**Files:**
- Modify: `scripts/lib-enrich.sh`
- Modify: `tests/enrich.bats`

- [ ] **Step 1: Add failing tests for `sanitize_title` and `truncate_ellipsis`**

Append to `tests/enrich.bats`:

```bash
@test "sanitize_title: strips CR/LF and truncates to 50" {
	sanitize_title "$(printf 'Add foo\r\nbar baz')"
	[ "$REPLY" = "Add foobar baz" ]
}

@test "sanitize_title: hard-truncates long titles to 50 chars" {
	local long="123456789012345678901234567890123456789012345678901234567890"
	sanitize_title "$long"
	[ "${#REPLY}" -eq 50 ]
}

@test "truncate_ellipsis: short string is unchanged" {
	truncate_ellipsis "short" 25
	[ "$REPLY" = "short" ]
}

@test "truncate_ellipsis: long string gets ellipsis at limit" {
	truncate_ellipsis "this title is definitely longer than twenty-five" 25
	[ "${#REPLY}" -eq 25 ]
	[ "${REPLY: -1}" = "…" ]
}
```

- [ ] **Step 2: Run to verify failure**

Run: `nix develop -c bats tests/enrich.bats`
Expected: FAIL — `command not found: sanitize_title`.

- [ ] **Step 3: Implement `sanitize_title` and `truncate_ellipsis`**

Append to `scripts/lib-enrich.sh`:

```bash
# sanitize_title RAW
# Strips CR, LF, and ESC control chars, then hard-truncates to 50 chars.
# Sets REPLY to the cleaned title.
sanitize_title() {
	local clean="${1//$'\r'/}"
	clean="${clean//$'\n'/}"
	clean="${clean//$'\033'/}"
	REPLY="${clean:0:50}"
}

# truncate_ellipsis STR MAX
# If STR exceeds MAX display chars, truncate to MAX-1 and append "…".
# Sets REPLY to the (possibly shortened) string.
truncate_ellipsis() {
	local str="$1" max="$2"
	if ((${#str} > max)); then
		REPLY="${str:0:max-1}…"
	else
		REPLY="$str"
	fi
}
```

- [ ] **Step 4: Run to verify pass**

Run: `nix develop -c bats tests/enrich.bats`
Expected: PASS (16 tests).

- [ ] **Step 5: Add failing test for `branch_sha1`**

Append to `tests/enrich.bats`:

```bash
@test "branch_sha1: stable 40-char hex for a branch" {
	branch_sha1 "feat/2-pr-window-enrichment"
	[ "${#REPLY}" -eq 40 ]
	[[ "$REPLY" =~ ^[0-9a-f]{40}$ ]]
}

@test "branch_sha1: same branch yields same key" {
	branch_sha1 "main"
	local first="$REPLY"
	branch_sha1 "main"
	[ "$REPLY" = "$first" ]
}
```

- [ ] **Step 6: Run to verify failure**

Run: `nix develop -c bats tests/enrich.bats`
Expected: FAIL — `command not found: branch_sha1`.

- [ ] **Step 7: Implement `branch_sha1`**

Append to `scripts/lib-enrich.sh`:

```bash
# branch_sha1 BRANCH
# Computes a stable cache key (sha1 hex) for a branch name.
# Sets REPLY to the 40-char hex digest.
branch_sha1() {
	local out
	out="$(printf '%s' "$1" | sha1sum)"
	REPLY="${out%% *}"
}
```

- [ ] **Step 8: Run to verify pass**

Run: `nix develop -c bats tests/enrich.bats`
Expected: PASS (18 tests).

- [ ] **Step 9: Lint and commit**

```bash
shellcheck scripts/lib-enrich.sh
git add scripts/lib-enrich.sh tests/enrich.bats
git commit -m "feat(enrich): add title sanitize, ellipsis, and sha1 cache key"
```

---

## Task 3: `lib-enrich.sh` — collapse check rollup + provider priority

**Files:**
- Modify: `scripts/lib-enrich.sh`
- Modify: `tests/enrich.bats`
- Create: `tests/fixtures/rollup-success.json`, `rollup-failure.json`, `rollup-pending.json`, `rollup-empty.json`, `rollup-mixed.json`

- [ ] **Step 1: Create JSON fixtures**

`tests/fixtures/rollup-success.json`:

```json
[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"COMPLETED","conclusion":"NEUTRAL"}]
```

`tests/fixtures/rollup-failure.json`:

```json
[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}]
```

`tests/fixtures/rollup-pending.json`:

```json
[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":null},{"__typename":"CheckRun","status":"QUEUED","conclusion":null}]
```

`tests/fixtures/rollup-empty.json`:

```json
[]
```

`tests/fixtures/rollup-mixed.json`:

```json
[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":null},{"__typename":"CheckRun","status":"COMPLETED","conclusion":"NEUTRAL"}]
```

- [ ] **Step 2: Add failing tests for `collapse_check_rollup`**

Append to `tests/enrich.bats`:

```bash
@test "collapse_check_rollup: all success/neutral → success" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-success.json)"
	[ "$REPLY" = "success" ]
}

@test "collapse_check_rollup: any failure → failure" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-failure.json)"
	[ "$REPLY" = "failure" ]
}

@test "collapse_check_rollup: any pending → pending" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-pending.json)"
	[ "$REPLY" = "pending" ]
}

@test "collapse_check_rollup: empty array → none" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-empty.json)"
	[ "$REPLY" = "none" ]
}

@test "collapse_check_rollup: pending + neutral → pending" {
	collapse_check_rollup "$(cat tests/fixtures/rollup-mixed.json)"
	[ "$REPLY" = "pending" ]
}
```

- [ ] **Step 3: Run to verify failure**

Run: `nix develop -c bats tests/enrich.bats`
Expected: FAIL — `command not found: collapse_check_rollup`.

- [ ] **Step 4: Implement `collapse_check_rollup`**

Append to `scripts/lib-enrich.sh` (uses `jq`; available in tmux's PATH via the wrapper):

```bash
# collapse_check_rollup ROLLUP_JSON
# Maps a gh `statusCheckRollup` array to a single state.
# Priority: any FAILURE/ERROR/CANCELLED/TIMED_OUT → failure;
#   else any PENDING/IN_PROGRESS/QUEUED (or null conclusion) → pending;
#   else all SUCCESS/NEUTRAL/SKIPPED → success; empty array → none.
# Sets REPLY to one of: failure | pending | success | none.
collapse_check_rollup() {
	local json="$1"
	REPLY="$(jq -r '
		if (. | length) == 0 then "none"
		elif any(.[]; (.conclusion // "") | ascii_upcase
			| . == "FAILURE" or . == "ERROR" or . == "CANCELLED" or . == "TIMED_OUT") then "failure"
		elif any(.[]; ((.status // "") | ascii_upcase
			| . == "IN_PROGRESS" or . == "QUEUED" or . == "PENDING")
			or ((.conclusion // "") == "")) then "pending"
		else "success"
		end
	' <<<"$json" 2>/dev/null)"
	[[ -z $REPLY ]] && REPLY="none"
}
```

- [ ] **Step 5: Run to verify pass**

Run: `nix develop -c bats tests/enrich.bats`
Expected: PASS (23 tests).

- [ ] **Step 6: Add failing tests for `provider_priority_list`**

Append to `tests/enrich.bats`:

```bash
@test "provider_priority_list: default order from substituted placeholder" {
	provider_priority_list
	[ "$REPLY" = "linear github" ]
}
```

- [ ] **Step 7: Run to verify failure**

Run: `nix develop -c bats tests/enrich.bats`
Expected: FAIL — `command not found: provider_priority_list`.

- [ ] **Step 8: Implement `provider_priority_list`**

Append to `scripts/lib-enrich.sh`. The `@providers@` placeholder is substituted at Nix build time (and stubbed to `linear github` by `tests/helper.bash`):

```bash
# provider_priority_list
# Returns the configured issue-tracker providers in priority order.
# The @providers@ placeholder is substituted at Nix build time from
# programs.lazytmux.enrich.providers. Sets REPLY to a space-separated list.
provider_priority_list() {
	REPLY="@providers@"
}
```

- [ ] **Step 9: Run to verify pass**

Run: `nix develop -c bats tests/enrich.bats`
Expected: PASS (24 tests).

- [ ] **Step 10: Lint and commit**

```bash
shellcheck scripts/lib-enrich.sh
git add scripts/lib-enrich.sh tests/enrich.bats tests/fixtures
git commit -m "feat(enrich): add check-rollup collapse and provider priority"
```

---

## Task 4: `tmux-issue-stamp-linear.sh` provider

**Files:**
- Create: `scripts/tmux-issue-stamp-linear.sh`

This script shells out to the `linear` CLI / git and is verified manually + by an offline regex test (no live CLI in CI).

- [ ] **Step 1: Write the Linear provider script**

`scripts/tmux-issue-stamp-linear.sh`:

```bash
#!/usr/bin/env bash
# Linear issue provider for tmux-issue-stamp.
# Usage: tmux-issue-stamp-linear <worktree-path> <branch>
# Output: three lines on stdout — id, title, url (empty lines for unset fields).
# Errors → empty output, exit 0.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

worktree="${1:-}"
branch="${2:-}"
id="" title="" url=""

# Resolve the Linear key from the branch first; bail early if none.
branch_to_linear_key "$branch"
key="$REPLY"
if [[ -z $key ]]; then
	printf '\n\n\n'
	exit 0
fi
id="$key"

# If the linear CLI is available, enrich title + url from within the worktree.
if command -v linear >/dev/null 2>&1 && [[ -d $worktree ]]; then
	title="$(cd "$worktree" && linear issue title 2>/dev/null)" || title=""
	url="$(cd "$worktree" && linear issue url 2>/dev/null)" || url=""
	# Prefer the CLI's canonical id when present.
	cli_id="$(cd "$worktree" && linear issue id 2>/dev/null)" || cli_id=""
	[[ -n $cli_id ]] && id="$cli_id"
fi

if [[ -n $title ]]; then
	sanitize_title "$title"
	title="$REPLY"
fi

printf '%s\n%s\n%s\n' "$id" "$title" "$url"
exit 0
```

- [ ] **Step 2: Shellcheck the script**

Run: `shellcheck -e SC1091 scripts/tmux-issue-stamp-linear.sh`
Expected: no warnings (SC1091 excluded — `@lib_enrich@` is a build-time path).

- [ ] **Step 3: Manual offline smoke test (regex fallback path)**

Run (simulating no `linear` CLI by sourcing the lib + the key logic directly):

```bash
nix develop -c bash -c '
  source <(sed "s/@providers@/linear github/g" scripts/lib-enrich.sh)
  branch_to_linear_key "noa-123-foo"; echo "key=$REPLY"
'
```
Expected: `key=NOA-123`

- [ ] **Step 4: Commit**

```bash
git add scripts/tmux-issue-stamp-linear.sh
git commit -m "feat(enrich): add Linear issue provider"
```

---

## Task 5: `tmux-issue-stamp-github.sh` provider

**Files:**
- Create: `scripts/tmux-issue-stamp-github.sh`

- [ ] **Step 1: Write the GitHub provider script**

`scripts/tmux-issue-stamp-github.sh`:

```bash
#!/usr/bin/env bash
# GitHub Issues provider for tmux-issue-stamp.
# Usage: tmux-issue-stamp-github <worktree-path> <branch>
# Output: three lines on stdout — id, title, url (empty for unset fields).
# Errors → empty output, exit 0.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

worktree="${1:-}"
branch="${2:-}"
id="" title="" url=""

# Only a github.com origin qualifies.
if [[ -d $worktree ]]; then
	origin="$(git -C "$worktree" remote get-url origin 2>/dev/null)" || origin=""
else
	origin=""
fi
if [[ $origin != *github.com* ]]; then
	printf '\n\n\n'
	exit 0
fi

branch_to_gh_issue_number "$branch"
num="$REPLY"
if [[ -z $num ]]; then
	printf '\n\n\n'
	exit 0
fi
id="#$num"

# Fetch title + url via gh from within the worktree (repo context).
if command -v gh >/dev/null 2>&1; then
	json="$(cd "$worktree" && gh issue view "$num" --json number,title,url 2>/dev/null)" || json=""
	if [[ -n $json ]]; then
		title="$(jq -r '.title // ""' <<<"$json" 2>/dev/null)"
		url="$(jq -r '.url // ""' <<<"$json" 2>/dev/null)"
	fi
fi

if [[ -n $title ]]; then
	sanitize_title "$title"
	title="$REPLY"
fi

printf '%s\n%s\n%s\n' "$id" "$title" "$url"
exit 0
```

- [ ] **Step 2: Shellcheck**

Run: `shellcheck -e SC1091 scripts/tmux-issue-stamp-github.sh`
Expected: no warnings.

- [ ] **Step 3: Manual offline smoke test (regex path)**

```bash
nix develop -c bash -c '
  source <(sed "s/@providers@/linear github/g" scripts/lib-enrich.sh)
  branch_to_gh_issue_number "247-fix-bug"; echo "num=$REPLY"
'
```
Expected: `num=247`

- [ ] **Step 4: Commit**

```bash
git add scripts/tmux-issue-stamp-github.sh
git commit -m "feat(enrich): add GitHub Issues provider"
```

---

## Task 6: `tmux-issue-stamp.sh` dispatcher

**Files:**
- Create: `scripts/tmux-issue-stamp.sh`

- [ ] **Step 1: Write the dispatcher script**

`scripts/tmux-issue-stamp.sh`. Provider impls are resolved by build-time placeholders `@issue_stamp_linear@` / `@issue_stamp_github@`:

```bash
#!/usr/bin/env bash
# One-shot issue-identity dispatcher. Runs from the worktrunk post-switch hook
# tail. Iterates configured providers in priority order; first complete
# (id) wins. Writes @issue_* window options on the target. Always exits 0.
#
# Usage: tmux-issue-stamp <target> <worktree-path> <branch>
#   <target> is a tmux target (e.g. "$session:$window" or "$N" session id form).
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

target="${1:-}"
worktree="${2:-}"
branch="${3:-}"

[[ -z $target || -z $branch ]] && exit 0

provider_priority_list
read -r -a providers <<<"$REPLY"

run_provider() {
	case "$1" in
	linear) @issue_stamp_linear@ "$worktree" "$branch" ;;
	github) @issue_stamp_github@ "$worktree" "$branch" ;;
	*) printf '\n\n\n' ;;
	esac
}

chosen_provider="" id="" title="" url=""
for p in "${providers[@]}"; do
	mapfile -t out < <(run_provider "$p")
	if [[ -n ${out[0]:-} ]]; then
		chosen_provider="$p"
		id="${out[0]:-}"
		title="${out[1]:-}"
		url="${out[2]:-}"
		break
	fi
done

if [[ -z $id ]]; then
	# No provider matched: leave options unset; display falls back to branch.
	exit 0
fi

tmux set-option -t "$target" -w @issue_provider "$chosen_provider"
tmux set-option -t "$target" -w @issue_id "$id"
tmux set-option -t "$target" -w @issue_title "$title"
tmux set-option -t "$target" -w @issue_url "$url"

# Kick an immediate PR fetch for this branch (likely "none" for a fresh branch).
@pr_enrich@ --target "$target" --branch "$branch" --force >/dev/null 2>&1 &

exit 0
```

- [ ] **Step 2: Shellcheck**

Run: `shellcheck -e SC1091 scripts/tmux-issue-stamp.sh`
Expected: no warnings.

- [ ] **Step 3: Commit**

```bash
git add scripts/tmux-issue-stamp.sh
git commit -m "feat(enrich): add issue-stamp dispatcher"
```

---

## Task 7: `tmux-pr-enrich.sh` poller (mock mode first)

**Files:**
- Create: `scripts/tmux-pr-enrich.sh`

Build mock mode first (it's the testable path), then the live `gh` poller.

- [ ] **Step 1: Write the script with arg parsing + mock mode + window-write helper**

`scripts/tmux-pr-enrich.sh`:

```bash
#!/usr/bin/env bash
# Background PR enrichment poller. Three entry modes:
#   --tick                  cheap gate; daemonize a full pass if .last-tick stale
#   --target T --branch B   enrich one window's branch (with --force to bypass TTL)
#   --mock-* ...            write mock @pr_* options directly (no gh), for tests
# Always exits 0. Writes @pr_number @pr_title @pr_state @pr_check_state @pr_url.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_enrich@

REFRESH_SECONDS="@pr_refresh_seconds@"
TTL=60

mkdir -p "$ENRICH_CACHE_DIR" 2>/dev/null

# --- arg parse ---
mode="tick"
target="" branch="" force=0
mock_number="" mock_state="" mock_check="" mock_title="" mock_url=""
while (($#)); do
	case "$1" in
	--tick) mode="tick" ;;
	--target) target="$2"; shift ;;
	--branch) branch="$2"; shift ;;
	--force) force=1 ;;
	--mock-pr-number) mock_number="$2"; mode="mock"; shift ;;
	--mock-pr-state) mock_state="$2"; shift ;;
	--mock-check-state) mock_check="$2"; shift ;;
	--mock-pr-title) mock_title="$2"; shift ;;
	--mock-pr-url) mock_url="$2"; shift ;;
	*) ;;
	esac
	shift
done

# write_pr_options TARGET NUMBER TITLE STATE CHECK URL
write_pr_options() {
	tmux set-option -t "$1" -w @pr_number "$2"
	tmux set-option -t "$1" -w @pr_title "$3"
	tmux set-option -t "$1" -w @pr_state "$4"
	tmux set-option -t "$1" -w @pr_check_state "$5"
	tmux set-option -t "$1" -w @pr_url "$6"
}

if [[ $mode == "mock" ]]; then
	[[ -z $target ]] && exit 0
	sanitize_title "$mock_title"
	write_pr_options "$target" "$mock_number" "$REPLY" "$mock_state" "$mock_check" "$mock_url"
	exit 0
fi
```

- [ ] **Step 2: Shellcheck the partial script**

Run: `shellcheck -e SC1091 scripts/tmux-pr-enrich.sh`
Expected: no warnings.

- [ ] **Step 3: Add the per-branch fetch + cache function**

Append to `scripts/tmux-pr-enrich.sh`:

```bash
# fetch_branch_pr BRANCH  → echoes cache JSON path, refreshing via gh if stale.
fetch_branch_pr() {
	local b="$1"
	branch_sha1 "$b"
	local cache="$ENRICH_CACHE_DIR/$REPLY.json"
	local lock="$ENRICH_CACHE_DIR/$REPLY.lock"

	# Skip if fresh and not forced.
	if ((force == 0)) && [[ -f $cache ]]; then
		local age=$(($(date +%s) - $(stat -c %Y "$cache" 2>/dev/null || echo 0)))
		((age < TTL)) && { printf '%s' "$cache"; return; }
	fi

	# Only one process per branch fetches at a time.
	exec {lockfd}>"$lock"
	if ! flock -n "$lockfd"; then
		printf '%s' "$cache"
		return
	fi

	command -v gh >/dev/null 2>&1 || { printf '%s' "$cache"; return; }

	local json
	json="$(gh pr list --head "$b" --state open --limit 1 \
		--json number,title,url,state,statusCheckRollup 2>/dev/null)" || json="[]"
	if [[ $json == "[]" || -z $json ]]; then
		json="$(gh pr list --head "$b" --state all --limit 1 \
			--json number,title,url,state,statusCheckRollup 2>/dev/null)" || json="[]"
	fi
	printf '%s' "$json" >"$cache"
	printf '%s' "$cache"
}

# apply_cache_to_target TARGET CACHE_PATH
apply_cache_to_target() {
	local tgt="$1" cache="$2"
	local json="[]"
	[[ -f $cache ]] && json="$(cat "$cache")"
	if [[ $json == "[]" || -z $json ]]; then
		write_pr_options "$tgt" "none" "" "" "" ""
		return
	fi
	local number title url state rollup
	number="$(jq -r '.[0].number // ""' <<<"$json")"
	title="$(jq -r '.[0].title // ""' <<<"$json")"
	url="$(jq -r '.[0].url // ""' <<<"$json")"
	state="$(jq -r '(.[0].state // "") | ascii_downcase' <<<"$json")"
	rollup="$(jq -c '.[0].statusCheckRollup // []' <<<"$json")"
	collapse_check_rollup "$rollup"
	local check="$REPLY"
	sanitize_title "$title"
	write_pr_options "$tgt" "$number" "$REPLY" "$state" "$check" "$url"
}
```

- [ ] **Step 4: Add the single-target and tick (daemonizing) entry logic**

Append to `scripts/tmux-pr-enrich.sh`:

```bash
# --- single-target mode (from dispatcher / force refresh) ---
if [[ -n $target && -n $branch ]]; then
	cache="$(fetch_branch_pr "$branch")"
	apply_cache_to_target "$target" "$cache"
	exit 0
fi

# --- tick mode: cheap gate, then daemonize a full pass ---
last_tick="$ENRICH_CACHE_DIR/.last-tick"
if ((force == 0)) && [[ -f $last_tick ]]; then
	tick_age=$(($(date +%s) - $(stat -c %Y "$last_tick" 2>/dev/null || echo 0)))
	((tick_age < REFRESH_SECONDS)) && exit 0
fi
touch "$last_tick"

# Daemonize: detach so the status refresh returns immediately.
(
	exec >/dev/null 2>&1
	setsid bash -c '
		'"$(declare -f write_pr_options fetch_branch_pr apply_cache_to_target collapse_check_rollup branch_sha1 sanitize_title)"'
		ENRICH_CACHE_DIR="'"$ENRICH_CACHE_DIR"'"; TTL='"$TTL"'; force=0
		declare -A seen
		while IFS="|" read -r tgt wt br; do
			[[ -z $br ]] && continue
			cache="$(fetch_branch_pr "$br")"
			apply_cache_to_target "$tgt" "$cache"
		done < <(tmux list-windows -a -F "#{session_id}:#{window_id}|#{@worktree}|#{@branch}" \
			| awk -F"|" "NF==3 && \$3!=\"\"" | head -n 30)
	' &
) &
exit 0
```

> Note for the implementer: the `declare -f` inlining keeps the detached child self-contained without re-sourcing. If this proves brittle under shellcheck, an acceptable alternative is to re-`source @lib_enrich@` inside the `setsid` child and call a dedicated `--run-pass` flag on this same script. Pick whichever passes `shellcheck` cleanly; the observable behavior (one detached pass over all windows, dedup-by-branch via per-branch cache TTL, cap 30) must match.

- [ ] **Step 5: Shellcheck**

Run: `shellcheck -e SC1091 scripts/tmux-pr-enrich.sh`
Expected: no warnings. If the `declare -f` inlining triggers warnings, switch to the `--run-pass` re-source alternative noted above and re-run until clean.

- [ ] **Step 6: Commit**

```bash
git add scripts/tmux-pr-enrich.sh
git commit -m "feat(enrich): add PR poller with mock mode, cache, and tick daemon"
```

---

## Task 8: Register scripts + icons in `config/tmux.conf.nix`

**Files:**
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Add `lib-enrich` and enrich icon defaults**

In the `let` block, after `lib-claude = mkLib "lib-claude";` (line 82), add:

```nix
  lib-enrich = mkLib "lib-enrich";
```

`mkLib` currently substitutes only `@ICON_MAP@`/`@FALLBACK_ICON@`. `lib-enrich.sh` instead needs `@providers@`. Add a dedicated builder beside `mkLib` (after line 82):

```nix
  # lib-enrich needs the provider-priority substitution rather than the icon map.
  lib-enrich = let
    raw = builtins.readFile ../scripts/lib-enrich.sh;
    patched = builtins.replaceStrings ["@providers@"] [enrichProvidersStr] raw;
  in
    pkgs.writeShellScript "lib-enrich" patched;
```

(Replace the bare `lib-enrich = mkLib "lib-enrich";` line — do not keep both.)

- [ ] **Step 2: Add enrich config params to the function signature**

At the top of `config/tmux.conf.nix`, extend the argument set (after `extraConfText ? "",` on line 12):

```nix
  # Issue/PR enrichment config (threaded from the home-manager module).
  enrichProviders ? ["linear" "github"],
  enrichPrRefreshSeconds ? 30,
  enrichIcons ? {},
```

Then in the `let` block (near the `icons` attrset, after line 47), add derived values:

```nix
  enrichProvidersStr = lib.concatStringsSep " " enrichProviders;
  enrichIconDefaults = {
    linear = ""; # nerd: nf-md-alpha-l-circle
    github = ""; # nerd: nf-md-github
    pending = ""; # nerd: nf-md-progress-clock
    success = ""; # nerd: nf-md-check-circle
    failure = ""; # nerd: nf-md-alert-circle
    merged = ""; # nerd: nf-md-source-merge
  };
  enrichIconSet = enrichIconDefaults // enrichIcons;
```

- [ ] **Step 3: Add the new scripts to `scriptNames` and `scriptsWithIcons`**

Extend `scriptNames` (line 109-120) with the four executables:

```nix
    "tmux-issue-stamp"
    "tmux-issue-stamp-linear"
    "tmux-issue-stamp-github"
    "tmux-pr-enrich"
```

These scripts need `@lib_enrich@` and cross-script path substitution, not the icon map. Add a dedicated builder + name list after `scriptsWithIcons` (line 123):

```nix
  scriptsWithEnrich = ["tmux-issue-stamp" "tmux-issue-stamp-linear" "tmux-issue-stamp-github" "tmux-pr-enrich"];

  # Build issue-stamp providers + pr-enrich first so the dispatcher can
  # reference them by full store path.
  enrich-linear-bin = mkScriptEnrich "tmux-issue-stamp-linear";
  enrich-github-bin = mkScriptEnrich "tmux-issue-stamp-github";
  enrich-pr-bin = mkScriptEnrich "tmux-pr-enrich";

  mkScriptEnrich = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      [
        "@lib_enrich@"
        "@providers@"
        "@pr_refresh_seconds@"
        "@issue_stamp_linear@"
        "@issue_stamp_github@"
        "@pr_enrich@"
        "@ICON_LINEAR@"
        "@ICON_GITHUB@"
        "@ICON_PEND@"
        "@ICON_OK@"
        "@ICON_FAIL@"
        "@ICON_MERGED@"
      ]
      [
        "${lib-enrich}"
        enrichProvidersStr
        (toString enrichPrRefreshSeconds)
        "${enrich-linear-bin}/bin/tmux-issue-stamp-linear"
        "${enrich-github-bin}/bin/tmux-issue-stamp-github"
        "${enrich-pr-bin}/bin/tmux-pr-enrich"
        enrichIconSet.linear
        enrichIconSet.github
        enrichIconSet.pending
        enrichIconSet.success
        enrichIconSet.failure
        enrichIconSet.merged
      ]
      raw;
  in
    pkgs.writeShellScriptBin name patched;
```

> Note: `enrich-linear-bin`/`enrich-github-bin`/`enrich-pr-bin` are defined via `mkScriptEnrich`, so `mkScriptEnrich` must appear textually before them is NOT required (Nix `let` bindings are lazy and order-independent). Keep them grouped for readability.

Then update the `script` genAttrs (line 136-141) so enrich scripts use `mkScriptEnrich`:

```nix
  script = lib.genAttrs scriptNames (name:
    if builtins.elem name scriptsWithEnrich
    then mkScriptEnrich name
    else if builtins.elem name scriptsWithIcons
    then mkScriptFull name
    else if name == "claude-status"
    then claude-status-pkg
    else mkScript name);
```

- [ ] **Step 4: Verify the flake builds**

Run: `nix build .#default 2>&1 | tail -20`
Expected: builds successfully; `./result/bin/tmux` exists. (Functional display wiring comes in Task 9.)

- [ ] **Step 5: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(enrich): register enrich scripts and icons in tmux config"
```

---

## Task 9: Window-list + status-line display segments

**Files:**
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Update `automatic-rename-format` to show issue id + PR check**

Replace the current `automatic-rename-format` (line ~396):

```nix
    set -g automatic-rename-format "#{?#{@branch},#{=30:@branch}#{?#{==:#{=30:@branch},#{@branch}},,…},#{b:pane_current_path}} #{@window_icon_display}"
```

with an issue-aware version. When `@issue_id` is set, show provider icon + id + (PR check icon + number when a PR exists); otherwise fall back to the current branch behavior:

```nix
    set -g automatic-rename-format "#{?#{@issue_id},#{?#{==:#{@issue_provider},linear},${enrichIconSet.linear},${enrichIconSet.github}} #{@issue_id}#{?#{&&:#{@pr_number},#{!=:#{@pr_number},none}}, #{?#{==:#{@pr_check_state},failure},${enrichIconSet.failure},#{?#{==:#{@pr_check_state},pending},${enrichIconSet.pending},#{?#{==:#{@pr_state},merged},${enrichIconSet.merged},${enrichIconSet.success}}}} ##{@pr_number},},#{?#{@branch},#{=30:@branch}#{?#{==:#{=30:@branch},#{@branch}},,…},#{b:pane_current_path}}} #{@window_icon_display}"
```

> Implementer note: `##{@pr_number}` escapes the `#` so the literal `#N` renders. Test rendering in Task 11 (`test-display.sh`) — tmux's format DSL is finicky; if a nested ternary misbehaves, simplify by precomputing a `@pr_check_icon` window option in `tmux-pr-enrich.sh` and referencing that single var here instead of the nested `#{?...}` chain. Prefer the precomputed-var approach if the inline version fails the display test.

- [ ] **Step 2: Add issue + PR segments to `status-format[0]` for the focused window**

In `status-format[0]` (line 338), the branch segment is:

```
#[fg=#{@thm_blue},bold]#{@icon_branch} #(${script.tmux-branch-display}/bin/tmux-branch-display '#{@branch}' '#{pane_current_path}')
```

Replace that segment with a conditional: when `@issue_id` is set, show provider icon + id + truncated title, and append a PR segment colored by check state; otherwise keep the branch display unchanged:

```
#{?#{@issue_id},#[fg=#{@thm_blue},bold]#{?#{==:#{@issue_provider},linear},${enrichIconSet.linear},${enrichIconSet.github}} #{@issue_id} #[fg=#{@thm_text},nobold]#{=25:@issue_title}#{?#{&&:#{@pr_number},#{!=:#{@pr_number},none}},  #{?#{==:#{@pr_check_state},failure},#[fg=#{@thm_red}],#{?#{==:#{@pr_check_state},pending},#[fg=#{@thm_peach}],#{?#{==:#{@pr_state},merged},#[fg=#{@thm_mauve}],#[fg=#{@thm_green}]}}}#{?#{==:#{@pr_check_state},failure},${enrichIconSet.failure},#{?#{==:#{@pr_check_state},pending},${enrichIconSet.pending},#{?#{==:#{@pr_state},merged},${enrichIconSet.merged},${enrichIconSet.success}}}} ##{@pr_number} #{=25:@pr_title},},#[fg=#{@thm_blue},bold]#{@icon_branch} #(${script.tmux-branch-display}/bin/tmux-branch-display '#{@branch}' '#{pane_current_path}')}
```

> Implementer note: keep the surrounding `  ` (two-space) separators consistent with the existing line-0 spacing. As in Step 1, if the nested PR-color ternary is hard to get right, precompute `@pr_color` + `@pr_check_icon` window options in `tmux-pr-enrich.sh` and reference them here.

- [ ] **Step 3: Build and eyeball with mock data**

```bash
nix build .#default
./result/bin/tmux -L enrichdev new-session -d -s test
./result/bin/tmux -L enrichdev set-option -t test -w @issue_provider linear
./result/bin/tmux -L enrichdev set-option -t test -w @issue_id NOA-123
./result/bin/tmux -L enrichdev set-option -t test -w @issue_title "Add foo bar"
./result/bin/tmux -L enrichdev set-option -t test -w @pr_number 247
./result/bin/tmux -L enrichdev set-option -t test -w @pr_state open
./result/bin/tmux -L enrichdev set-option -t test -w @pr_check_state pending
./result/bin/tmux -L enrichdev set-option -t test -w @pr_title "Implement enrichment"
./result/bin/tmux -L enrichdev list-windows -t test -F '#{window_name}'
./result/bin/tmux -L enrichdev kill-server
```
Expected: window name shows the Linear icon + `NOA-123` + pending icon + `#247`.

- [ ] **Step 4: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(enrich): show issue id + PR check state in window list and status line"
```

---

## Task 10: Keybinds (`prefix + i` enrich table) + hidden tick

**Files:**
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Add the `enrich` key-table and its trigger**

After the existing pickers block (after line 287, `bind a ...`), add:

```nix
    # === Issue/PR enrichment ===
    # prefix + i enters the enrich table: i open issue, p open PR, r refresh.
    bind-key i switch-client -T enrich
    bind-key -T enrich i run-shell 'url="#{@issue_url}"; [ -n "$url" ] && xdg-open "$url" >/dev/null 2>&1'
    bind-key -T enrich p run-shell 'url="#{@pr_url}"; [ -n "$url" ] && xdg-open "$url" >/dev/null 2>&1'
    bind-key -T enrich r run-shell '${enrich-pr-bin}/bin/tmux-pr-enrich --target "#{session_id}:#{window_id}" --branch "#{@branch}" --force'
```

> Note: use `#{session_id}:#{window_id}` (the `$N`/`@N` id form), not names — avoids the numeric-session-name targeting ambiguity documented in `CLAUDE.md`.

- [ ] **Step 2: Add the hidden `#()` tick to `status-format[0]`**

At the very start of `status-format[0]` (line 338), there is already a leading `#(${script.tmux-update-icons}...)` call whose output is consumed silently. Add the enrich tick immediately after it (it prints nothing):

```
#(${script.tmux-update-icons}/bin/tmux-update-icons '#{session_name}')#(${enrich-pr-bin}/bin/tmux-pr-enrich --tick)#[align=left,bg=#{@thm_bg}]...
```

- [ ] **Step 3: Build and verify the keytable + tick load without error**

```bash
nix build .#default
./result/bin/tmux -L enrichdev new-session -d -s test
./result/bin/tmux -L enrichdev list-keys -T enrich
./result/bin/tmux -L enrichdev kill-server
```
Expected: lists the `i`, `p`, `r` binds under table `enrich`; no config-load errors.

- [ ] **Step 4: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(enrich): add prefix+i keytable and background PR tick"
```

---

## Task 11: Mock-mode display regression test

**Files:**
- Create: `tests/test-display.sh`
- Create: `tests/fixtures/window-list.expected`

- [ ] **Step 1: Write the display test script**

`tests/test-display.sh`:

```bash
#!/usr/bin/env bash
# Mock-mode window-list regression test.
# Spawns a throwaway tmux server using the built wrapper, sets @issue_*/@pr_*
# options for the documented display states, and diffs window names against a
# golden file. Run after `nix build .#default`.
set -euo pipefail

TMUX_BIN="${TMUX_BIN:-./result/bin/tmux}"
SOCKET="enrichtest-$$"
EXPECTED="tests/fixtures/window-list.expected"

cleanup() { "$TMUX_BIN" -L "$SOCKET" kill-server 2>/dev/null || true; }
trap cleanup EXIT

t() { "$TMUX_BIN" -L "$SOCKET" "$@"; }

t new-session -d -s s -n w0   # state: no provider (branch fallback)
t set-option -t s:0 -w @branch "feat/plain-branch"

t new-window -t s -n w1       # Linear + open PR pending
t set-option -t s:1 -w @issue_provider linear
t set-option -t s:1 -w @issue_id NOA-123
t set-option -t s:1 -w @pr_number 247
t set-option -t s:1 -w @pr_state open
t set-option -t s:1 -w @pr_check_state pending

t new-window -t s -n w2       # Linear + open PR passing
t set-option -t s:2 -w @issue_provider linear
t set-option -t s:2 -w @issue_id NOA-124
t set-option -t s:2 -w @pr_number 248
t set-option -t s:2 -w @pr_state open
t set-option -t s:2 -w @pr_check_state success

t new-window -t s -n w3       # GitHub + open PR passing
t set-option -t s:3 -w @issue_provider github
t set-option -t s:3 -w @issue_id "#142"
t set-option -t s:3 -w @pr_number 249
t set-option -t s:3 -w @pr_state open
t set-option -t s:3 -w @pr_check_state success

t new-window -t s -n w4       # Linear + no PR yet
t set-option -t s:4 -w @issue_provider linear
t set-option -t s:4 -w @issue_id NOA-125
t set-option -t s:4 -w @pr_number none

# Force a rename pass and capture the resolved window names.
sleep 1
got="$(t list-windows -t s -F '#{window_name}')"

if [[ "${UPDATE_GOLDEN:-0}" == "1" ]]; then
	printf '%s\n' "$got" >"$EXPECTED"
	echo "golden updated"
	exit 0
fi

diff <(printf '%s\n' "$got") "$EXPECTED"
echo "display test passed"
```

- [ ] **Step 2: Make it executable and generate the golden file**

```bash
chmod +x tests/test-display.sh
nix build .#default
UPDATE_GOLDEN=1 ./tests/test-display.sh
```
Expected: `tests/fixtures/window-list.expected` is created. **Inspect it** — confirm each line matches the Display state matrix in the spec (provider icon, id, check icon, `#number`). If wrong, fix the format string from Task 9 before locking the golden.

- [ ] **Step 3: Run the test against the golden**

Run: `./tests/test-display.sh`
Expected: `display test passed` (no diff).

- [ ] **Step 4: Shellcheck and commit**

```bash
shellcheck tests/test-display.sh
git add tests/test-display.sh tests/fixtures/window-list.expected
git commit -m "test(enrich): add mock-mode window-list display regression test"
```

---

## Task 12: home-manager `enrich` option block + worktrunk hook

**Files:**
- Modify: `modules/home-manager.nix`

- [ ] **Step 1: Add the `enrich` option block**

In `modules/home-manager.nix`, after the `persist` option block (after line ~166), add:

```nix
    enrich = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          PR + issue-tracker window enrichment: stamps tmux windows with the
          Linear/GitHub issue id and PR check-state for their worktree's branch,
          and adds `prefix + i` keybinds to open the issue/PR or force refresh.
        '';
      };

      providers = lib.mkOption {
        type = lib.types.listOf (lib.types.enum ["linear" "github"]);
        default = ["linear" "github"];
        description = "Issue-tracker providers, tried in priority order. First match wins.";
      };

      prRefreshSeconds = lib.mkOption {
        type = lib.types.ints.between 10 300;
        default = 30;
        description = "Background PR enrichment cadence in seconds (clamped 10–300).";
      };

      icons = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = {};
        example = lib.literalExpression ''{ linear = ""; github = ""; }'';
        description = ''
          Override enrichment icon glyphs (keys: linear, github, pending,
          success, failure, merged). Unset keys fall back to nerd-font defaults.
        '';
      };
    };
```

- [ ] **Step 2: Thread enrich options into the `tmux.conf.nix` import**

Find where `config/tmux.conf.nix` is imported in `modules/home-manager.nix` (the `tmuxConfig = import ../config/tmux.conf.nix {...}` call) and pass the enrich params:

```nix
  tmuxConfig = import ../config/tmux.conf.nix {
    inherit pkgs lib;
    # ... existing args (extraProcessIcons, terminalTerm, extraConfText) ...
    enrichProviders = cfg.enrich.providers;
    enrichPrRefreshSeconds = cfg.enrich.prRefreshSeconds;
    enrichIcons = cfg.enrich.icons;
  };
```

> Implementer note: read the existing import call first; preserve every existing argument. Only add the three `enrich*` lines.

- [ ] **Step 3: Gate enrich scripts into the closure**

In the `config` block's `home.packages` list (line ~274+), add:

```nix
        ++ lib.optionals cfg.enrich.enable [
          tmuxConfig.script.tmux-issue-stamp
          tmuxConfig.script.tmux-issue-stamp-linear
          tmuxConfig.script.tmux-issue-stamp-github
          tmuxConfig.script.tmux-pr-enrich
          pkgs.jq
        ]
```

> Note: `gh` is assumed already on the user's PATH (it is a hard dependency of the GitHub provider). Do not add it to the closure — the script no-ops when `gh` is absent (spec error-handling table).

- [ ] **Step 4: Add the issue-stamp tail call to the worktrunk post-switch hook**

In the worktrunk `[post-switch]` `tmux` heredoc (lines ~347-380), after BOTH the matched-window branch and the new-window branch have set `@worktree` + `@branch`, add a single tail call before the closing `"""`. Because the hook sets the target differently in each branch, capture the resolved target into `STAMP_TARGET` in both branches, then stamp once at the end:

In the matched branch (after `tmux set-option -t "$SESS:$WIN" -w @branch ...`):
```bash
  STAMP_TARGET="$SESS:$WIN"
```
In the new-window branch (after `tmux set-option -t "$CUR_SESSION" -w @branch ...`):
```bash
  STAMP_TARGET="$CUR_SESSION"
```
Then, before the closing `"""`:
```bash
if [ -n "${STAMP_TARGET:-}" ]; then
  tmux-issue-stamp "$STAMP_TARGET" "{{ worktree_path }}" "{{ branch | sanitize }}" >/dev/null 2>&1 &
fi
```

> Note: this tail is only emitted when `cfg.enrich.enable`. Wrap the worktrunk-config string assembly so the stamp lines are included via `lib.optionalString cfg.enrich.enable`. Read the surrounding worktrunk config generation to match its string-interpolation style before editing.

- [ ] **Step 5: Build the home-manager module in isolation (eval check)**

Run: `nix flake check 2>&1 | tail -30`
Expected: no evaluation errors from the module. (Full check including new tests is wired in Task 13; if `checks` for tests are not yet added, this step just confirms the module evaluates.)

- [ ] **Step 6: Commit**

```bash
git add modules/home-manager.nix
git commit -m "feat(enrich): add enrich home-manager options and worktrunk stamp hook"
```

---

## Task 13: Wire tests into `flake.nix` checks

**Files:**
- Modify: `flake.nix`

- [ ] **Step 1: Add `bats-core` to the dev shell**

In `flake.nix` `devShells.default.packages`, add `pkgs.bats` (the `bats-core` package attr in nixpkgs is `bats`):

```nix
          packages =
            config.pre-commit.settings.enabledPackages
            ++ [
              pkgs.go
              pkgs.gopls
              pkgs.gotools
              pkgs.bats
              pkgs.jq
            ];
```

- [ ] **Step 2: Add a `checks.enrich-tests` derivation**

In `perSystem`, add a `checks` attribute (alongside `packages`):

```nix
        checks = {
          enrich-tests = pkgs.runCommand "enrich-tests" {
            nativeBuildInputs = [pkgs.bats pkgs.jq];
          } ''
            cp -r ${./.}/scripts scripts
            cp -r ${./.}/tests tests
            cd .
            bats tests/enrich.bats
            touch $out
          '';
        };
```

> Implementer note: `tests/helper.bash` stubs `@providers@` via `sed`, so the unit suite runs against the raw `scripts/lib-enrich.sh` without needing the Nix build. Confirm the sandbox has `sha1sum` (coreutils — it does) for `branch_sha1`.

- [ ] **Step 3: Run the flake checks**

Run: `nix flake check 2>&1 | tail -30`
Expected: `enrich-tests` passes (24 bats assertions) plus the existing pre-commit hooks.

> Note on the display test: `tests/test-display.sh` needs the built wrapper and a writable `/tmp` + tmux server, which is awkward inside the `nix flake check` sandbox. Keep it as a **manual** smoke test (documented in CLAUDE.md) rather than a flake check, matching the spec's "tmux option I/O — high cost, low ROI" stance. If desired later, wire it as a separate `checks.enrich-display` that runs `${tmuxConfig.tmux-wrapped}/bin/tmux` — defer unless it proves reliable.

- [ ] **Step 4: Commit**

```bash
git add flake.nix
git commit -m "test(enrich): wire bats unit suite into flake checks and dev shell"
```

---

## Task 14: Documentation

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add the enrich scripts to the Script Roles table**

In `CLAUDE.md` under "### Script Roles", add rows:

```markdown
| `tmux-issue-stamp` | worktrunk `post-switch` tail (one-shot) | Detects Linear/GitHub issue for the new window's branch, writes `@issue_*` options. |
| `tmux-issue-stamp-linear` / `-github` | called by dispatcher | Provider impls: branch regex (+ `linear`/`gh` CLI) → `id\ntitle\nurl`. |
| `tmux-pr-enrich` | hidden `#()` in status-format[0] (`--tick`); `prefix + i r` (`--force`) | Background PR poller; writes `@pr_*` from `gh pr list`. Cache at `/tmp/lazytmux-pr/`. |
```

- [ ] **Step 2: Add a lib-enrich entry and window-options note**

Under "### Shared Libraries", add:

```markdown
- **`lib-enrich.sh`** — `branch_to_linear_key`, `branch_to_gh_issue_number`, `sanitize_title`, `truncate_ellipsis`, `branch_sha1`, `collapse_check_rollup`, `provider_priority_list`. Sourced by issue-stamp + pr-enrich. Unit-tested in `tests/enrich.bats`.
```

Add a one-line note that window options (`@issue_*`, `@pr_*`) are the single source of truth for enrichment, and the manual display smoke test is `./tests/test-display.sh` after `nix build .#default`.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(enrich): document enrichment scripts and window-options model"
```

---

## Task 15: End-to-end manual verification

**Files:** none (verification only)

- [ ] **Step 1: Full build + flake check**

```bash
nix build .#default
nix flake check
```
Expected: build succeeds; all checks pass.

- [ ] **Step 2: Display smoke test**

```bash
./tests/test-display.sh
```
Expected: `display test passed`.

- [ ] **Step 3: Live keytable + tick (real tmux)**

In a scratch tmux server using the wrapper, verify:
- `list-keys -T enrich` shows `i`/`p`/`r`.
- Setting `@issue_url`/`@pr_url` then pressing `prefix i i` / `prefix i p` opens the URL (or no-ops cleanly when unset).
- `tmux-pr-enrich --tick` returns immediately and (after `prRefreshSeconds`) spawns a detached pass that writes `@pr_*` on windows with a `@branch`.

- [ ] **Step 4: Update the spec status**

Change the spec header `**Status:** Draft` → `**Status:** Implemented` in `docs/superpowers/specs/2026-05-28-pr-linear-window-enrichment-design.md`.

- [ ] **Step 5: Final commit + push + PR**

```bash
git add docs/superpowers/specs/2026-05-28-pr-linear-window-enrichment-design.md
git commit -m "docs(spec): mark PR/issue enrichment implemented"
git push -u origin feat/2-pr-window-enrichment
gh pr create --assignee @me --title "feat: PR + issue-tracker window enrichment" --body "Closes #2"
```

---

## Self-Review

**Spec coverage:**
- Goals (per-window issue identity, PR + check state, full titles on line 0, open-in-browser keybinds, two providers) → Tasks 4–6 (providers + dispatcher), 7 (PR state), 9 (display), 10 (keybinds). ✅
- Components (`tmux-issue-stamp`, `-linear`, `-github`, `tmux-pr-enrich`, `lib-enrich.sh`) → Tasks 1–7. ✅
- Window-options-as-truth → enforced throughout; documented in Task 14. ✅
- Cache layout (`<sha1>.json`, `.lock`, `.last-tick`, 60s TTL, flock, cap 30) → Task 7. ✅
- Config block (`enable`, `providers`, `prRefreshSeconds`, `icons`) → Task 12. **Deviation:** spec also lists `linearCli` (bundle schpet/linear-cli as a flake input). **Deferred** — the Linear provider uses whatever `linear` is on PATH and falls back to branch-regex when absent, so v1 ships without the flake input. Documented here as the one intentional scope cut; add the input + option later if regex-only proves insufficient (matches the spec's own "Open questions" on packaging linear-cli).
- Testing (bats unit suite, mock-mode display test, flake checks) → Tasks 1–3, 11, 13. ✅
- Error handling (gh absent, auth, no PR, sanitize) → providers + poller no-op/`none` paths; `sanitize_title`. **Partial:** sticky-`unauthed` state handling is simplified (poller writes `none` rather than a distinct `@pr_state=unauthed`). Acceptable for v1; flagged for follow-up.
- Keybinds → **Deviation (user-approved):** `prefix + i` key-table instead of spec's `prefix + g` (which collides with lazygit). ✅

**Placeholder scan:** No "TBD"/"handle edge cases" placeholders. Build-time `@...@` tokens are intentional Nix substitutions, defined in Task 8's substitution lists. Two display tasks (9 steps 1–2) carry an explicit *fallback strategy* (precompute `@pr_color`/`@pr_check_icon` vars) rather than a vague placeholder — the primary inline approach is fully specified.

**Type/name consistency:** Lib function names are consistent across tasks (`branch_to_linear_key`, `branch_to_gh_issue_number`, `sanitize_title`, `truncate_ellipsis`, `branch_sha1`, `collapse_check_rollup`, `provider_priority_list`). Window option names consistent (`@issue_provider/_id/_title/_url`, `@pr_number/_title/_state/_check_state/_url`). Script placeholders consistent (`@lib_enrich@`, `@issue_stamp_linear@`, `@issue_stamp_github@`, `@pr_enrich@`, `@providers@`, `@pr_refresh_seconds@`, `@ICON_*@`).

**Known deferrals (documented, non-blocking):** `linearCli` flake input; distinct `unauthed` PR state; `enrich-display` as a flake check (kept manual). All match the spec's own "Out of scope / Open questions".
