# agent-detect ForCommand nix `.foo-wrapped` normalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `manifest.ForCommand` recognize nix-wrapped agent command names (e.g. `.claude-wrapped`) so `agent-detect` actually starts scraping on a nix host, closing GitHub issue #332.

**Architecture:** `ForCommand(ms []Manifest, cmd string)` currently does a bare `c == cmd` exact match against `match_commands` entries (`"claude"`, `"codex"`, `"cursor-agent"`). On nix, `#{pane_current_command}` reports `.claude-wrapped` instead of `claude`, so the match always misses and `main.go` returns before ever writing a screen-scraper state file. Fix: strip a leading `.` and trailing `-wrapped` from `cmd` *inside* `ForCommand` before comparing, using the same regex already established in `picker/statusline/main.go` (`^\.(.*)-wrapped$`). Normalizing inside `ForCommand` makes every current and future caller correct by construction — there is no caller that should ever want the un-normalized nix wrapper name to fail to match — and keeps the one-and-only fix contained to the `manifest` package (`main.go` needs no changes).

**Tech Stack:** Go 1.25 (picker module), stdlib `regexp`, table-driven `testing`.

---

### Task 1: Add failing test coverage for wrapped command normalization

**Files:**
- Modify: `picker/agentdetect/manifest/manifest_test.go`

- [ ] **Step 1: Read the current file to confirm line numbers before editing**

Already read above — the file currently ends at line 25 with `TestForCommandUnknown`. Confirm nothing has changed since:

```bash
cat -n /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i/picker/agentdetect/manifest/manifest_test.go
```

- [ ] **Step 2: Add a table-driven test covering the wrapped and unwrapped cases**

Append this test to `picker/agentdetect/manifest/manifest_test.go`:

```go
func TestForCommandNormalizesNixWrapped(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	cases := []struct {
		name   string
		cmd    string
		wantID string
		wantOK bool
	}{
		{"wrapped claude matches", ".claude-wrapped", "claude", true},
		{"wrapped unknown agent matches nothing", ".nvim-wrapped", "", false},
		{"bare claude still matches", "claude", "claude", true},
		{"bare codex still matches", "codex", "codex", true},
		{"bare cursor-agent still matches", "cursor-agent", "cursor", true},
		{"bare unknown still matches nothing", "nvim", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := ForCommand(ms, tc.cmd)
			if ok != tc.wantOK {
				t.Fatalf("ForCommand(%q) ok = %v, want %v", tc.cmd, ok, tc.wantOK)
			}
			if ok && m.ID != tc.wantID {
				t.Fatalf("ForCommand(%q) id = %q, want %q", tc.cmd, m.ID, tc.wantID)
			}
		})
	}
}
```

Note: `wantID` values above are already verified against the real manifests (`picker/agentdetect/manifest/manifests/*.toml`): `claude.toml` and `codex.toml` set `id` equal to their bare command (`"claude"`, `"codex"`), but `cursor.toml` sets `id = "cursor"` while `match_commands = ["cursor-agent"]` — that's why the cursor case above expects `wantID: "cursor"`, not `"cursor-agent"`. If manifest content changes before this task runs, re-check with:

```bash
grep -n "^id\|^match_commands" /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i/picker/agentdetect/manifest/manifests/*.toml
```

- [ ] **Step 3: Run the test and confirm the wrapped case fails against the unfixed code**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i/picker
go test ./agentdetect/manifest/... -run TestForCommandNormalizesNixWrapped -v -race
```

Expected: FAIL — specifically the `wrapped claude matches` and `wrapped unknown agent matches nothing` subtests fail (the latter fails only if it currently returns `ok=true` some other way — expect it to already pass since `.nvim-wrapped` doesn't equal any bare `match_commands` entry; the load-bearing red case is `wrapped claude matches`). Confirm the failure output names `ForCommand(".claude-wrapped") ok = false, want true`.

- [ ] **Step 4: Commit the failing test**

```bash
git add picker/agentdetect/manifest/manifest_test.go
git commit -m "test(agentdetect): cover nix .foo-wrapped normalization in ForCommand"
```

---

### Task 2: Normalize `.foo-wrapped` inside `ForCommand`

**Files:**
- Modify: `picker/agentdetect/manifest/manifest.go:1-65`

- [ ] **Step 1: Read the current file to confirm it hasn't drifted**

```bash
cat -n /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i/picker/agentdetect/manifest/manifest.go
```

- [ ] **Step 2: Add the `regexp` import and a package-level wrapped-command regex**

Change the import block:

```go
import (
	"embed"
	"fmt"
	"regexp"
	"sort"

	"github.com/BurntSushi/toml"
)
```

Add the regex right after the `Manifest` struct (before `Load`), mirroring `picker/statusline/main.go`'s convention exactly:

```go
var wrappedRe = regexp.MustCompile(`^\.(.*)-wrapped$`)
```

- [ ] **Step 3: Normalize `cmd` inside `ForCommand` before comparing**

Replace the `ForCommand` function:

```go
func ForCommand(ms []Manifest, cmd string) (Manifest, bool) {
	if m := wrappedRe.FindStringSubmatch(cmd); m != nil {
		cmd = m[1]
	}
	for _, m := range ms {
		for _, c := range m.MatchCommands {
			if c == cmd {
				return m, true
			}
		}
	}
	return Manifest{}, false
}
```

This strips a leading `.` / trailing `-wrapped` once, then falls through to the same exact-match loop as before — bare commands (`"claude"`, `"nvim"`) are untouched since they never match `wrappedRe`, so nothing widens beyond `match_commands`.

- [ ] **Step 4: Run the full manifest package test suite with -race**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i/picker
go test ./agentdetect/manifest/... -v -race
```

Expected: PASS — all subtests of `TestForCommandNormalizesNixWrapped`, plus the pre-existing `TestLoadParsesAndSortsRules` and `TestForCommandUnknown`, all green.

- [ ] **Step 5: Run the full agentdetect package tests to confirm no regressions in callers**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i/picker
go test ./agentdetect/... -race
```

Expected: PASS across `agentdetect`, `agentdetect/manifest`, `agentdetect/screen`, `agentdetect/statefile`, `agentdetect/debounce`, `agentdetect/drainbuf`.

- [ ] **Step 6: Commit the fix**

```bash
git add picker/agentdetect/manifest/manifest.go
git commit -m "fix(agentdetect): normalize nix .foo-wrapped commands in ForCommand"
```

---

### Task 3: Full repo gate

**Files:** none (verification only)

- [ ] **Step 1: Build the wrapped tmux**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
nix build .#default
```

Expected: builds successfully, `./result/bin/tmux` exists.

- [ ] **Step 2: Run the full check suite (bats + Go)**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
nix flake check
```

Expected: all bats tests (`tests/*.bats`) and Go checks (`picker-go-tests` and friends) pass.

- [ ] **Step 3: Run lint (formatter + pre-commit hooks)**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
nix build .#lint
```

Expected: passes (alejandra, statix, deadnix, shellcheck, shfmt, typos, ...). This step is required separately — `nix flake check` does not run it (see CLAUDE.md "Build and Test", corrected in #331).

---

### Task 4: End-to-end verification on a scratch tmux server

**Files:** none (manual verification only — do not disturb other panes/sessions on this machine)

This task needs three things that Tasks 1-3 don't guarantee on their own:
1. **The shell-side arm (#333)** — `scripts/tmux-update-icons.sh`'s `pipe-pane` gating — actually present on this branch's history, otherwise `agent-detect` is never spawned regardless of Task 2's fix.
2. **A tmux server that actually loaded the freshly built config**, on its own socket so the shared/default server (and every other agent's panes on it) is never touched.
3. **A real client attached to that scratch server.** `arm_agent_detect` (the `pipe-pane ... agent-detect` call) only runs from the `#()` job tmux evaluates for `status-format[0]`, and tmux only evaluates status-line jobs while a client is attached and rendering — see `config/tmux.conf.nix:871` and CLAUDE.md's identical rule for the remote bridge ("A control-mode client renders no status line"). A server started with `new-session -d` and nothing attached will never arm the watcher, regardless of 1 and 2.

- [ ] **Step 0: Verify the shell-side normalization (#333) is present on this branch's base; rebase if not**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
git fetch origin main
git merge-base --is-ancestor 951666e HEAD
echo "951666e is ancestor of HEAD: exit=$?"
```

As of writing this plan, `951666e` (`fix(agent-detect): normalize .foo-wrapped commands before arming (#333)`) is confirmed present on `origin/main` but is **not** an ancestor of this branch's HEAD — this worktree's base (`be3d179`) was cut before #333 merged. If the check above prints a nonzero exit, rebase onto the current `origin/main` so the scratch-server build in this task actually includes the shell-side arm:

```bash
git rebase origin/main
```

Expect a clean rebase — Task 1/2's commits touch only `picker/agentdetect/manifest/*`, disjoint from `scripts/tmux-update-icons.sh`.

If `git merge-base --is-ancestor 951666e HEAD` still fails after `git fetch origin main` (i.e. #333 has not landed on `origin/main` at all), **stop here**: end-to-end evidence is unobtainable on this base. Skip Steps 1-6 below, note in Task 5's PR body that the Go unit tests (Tasks 1-2) are the only green signal obtained, and do not write or imply the "#333 landed" claim into the PR body.

- [ ] **Step 0b: Re-run the full gate against the rebased tree (Task 3 ran before the rebase)**

Task 3's gate ran against the pre-rebase tree. If Step 0 rebased onto `origin/main`, the tree that will actually ship changed (it now includes #333 and anything else upstream picked up), so `nix flake check` and `nix build .#lint` — not just the build — must be re-validated on it; otherwise a lint/format or test regression introduced by the merge would only surface in CI.

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
nix build .#default
nix flake check
nix build .#lint
```

Expected: all three pass again, and `./result/bin/tmux` now embeds both the Go fix (Task 2) and the shell-side arm (#333). (If Step 0 found no rebase was needed, this is a fast re-confirmation of a tree that hasn't changed.)

- [ ] **Step 1: Start a scratch tmux server on its own socket, loading the freshly built config, and attach a real client to it**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
./result/bin/tmux -L agentdetect-verify new-session -d -s verify -c /tmp
```

Using `-L agentdetect-verify` puts this on an isolated server/socket — the shared default server and every other agent's pane on it are never touched, and this server has definitely loaded the config built in Step 0b (unlike attaching a session to whatever server happens to already be running). Every `tmux` invocation below must use the same `-L agentdetect-verify` flag.

A detached server has zero attached clients, and `arm_agent_detect` (`scripts/tmux-update-icons.sh`) only runs from the `#()` job tmux evaluates for `status-format[0]` — a job tmux only evaluates while a client is attached and rendering the status line. With no client attached, the `pipe-pane ... agent-detect` call that arms the watcher never fires, and Steps 3-6 below would produce a false negative regardless of Task 2's fix. Attach a real client from a separate window on your own (non-scratch) tmux server — do not attach from inside the scratch session itself:

```bash
tmux new-window -d "./result/bin/tmux -L agentdetect-verify attach -t verify"
```

(If you're not already inside tmux, use `script -q /dev/null -c "./result/bin/tmux -L agentdetect-verify attach -t verify" &` instead.)

Confirm the client actually attached before proceeding — do not continue to Step 3 until this shows at least one line:

```bash
./result/bin/tmux -L agentdetect-verify list-clients
```

Capture the pane id once — both the raw `%`-prefixed id (needed later to target `kill-pane` precisely) and the stripped id agent-detect's files are keyed on — and persist both to scratch files, since each of the following code blocks is a separate shell process and a plain variable would not survive between steps:

```bash
RAW_PANE_ID=$(./result/bin/tmux -L agentdetect-verify display-message -p -t verify '#{pane_id}')
echo "$RAW_PANE_ID" > /tmp/agentdetect-verify-pane-id-raw
echo "${RAW_PANE_ID#%}" > /tmp/agentdetect-verify-pane-id
cat /tmp/agentdetect-verify-pane-id
```

- [ ] **Step 2: Confirm `/tmp/claude-status/screen/` does not yet exist (or note its current state) as a baseline**

`/tmp/claude-status/*` is shared across tmux servers (it's not scoped to `-L agentdetect-verify`), so this baseline must be recorded *before* the scratch server's agent pane starts:

```bash
ls /tmp/claude-status/screen/ 2>&1
```

Record the output — this directory has never existed on this machine before this fix, so its *appearance* after starting an agent pane is the proof.

- [ ] **Step 3: Start an agent (e.g. claude) in the scratch session's pane and let `pipe-pane` arm the watcher**

```bash
./result/bin/tmux -L agentdetect-verify send-keys -t verify 'claude' Enter
```

Wait at least 10 seconds — `arm_agent_detect`'s sweep only runs on ticks where `CLAUDE_NOW % 5 == 0` (`scripts/tmux-update-icons.sh:40`), and the status line only redraws it once a second while the attached client renders — for the agent to start, the pane's foreground command to settle, and the sweep to actually run, then check:

```bash
./result/bin/tmux -L agentdetect-verify display-message -p -t verify '#{pane_current_command}'
```

Expected on this nix host: `.claude-wrapped` (confirming the bug's precondition is real).

- [ ] **Step 4: Confirm the screen-scraper state file appears**

```bash
PANE_ID=$(cat /tmp/agentdetect-verify-pane-id)
ls -la /tmp/claude-status/screen/
ls -la /tmp/claude-status/screen/$PANE_ID
```

Expected: a file now exists at `/tmp/claude-status/screen/<PANE_ID>` (no `%`), and it did not exist in Step 2 (or its mtime is newer than before this test).

- [ ] **Step 5: Confirm exactly one watcher is registered for the pane, no duplicates**

```bash
PANE_ID=$(cat /tmp/agentdetect-verify-pane-id)
cat /tmp/claude-status/watchers/$PANE_ID
ps aux | grep "agent-detect $PANE_ID" | grep -v grep
```

Expected: the watchers file holds one PID, and exactly one live `agent-detect` process matches that PID for this pane — note the argv is the bare pane id (`agent-detect 12`, not `agent-detect %12`), matching `main.go`'s `os.Args[1] // already sans '%'`.

- [ ] **Step 6: Kill only the agent's pane (server stays up) and confirm the watcher reaps and registry entry is removed**

The registry/screen-file cleanup lives in a `pane-exited` `run-shell` hook (`config/tmux.conf.nix:975`) — it needs a live server to execute it. `kill-server` tears the whole server down first, so it can never actually exercise this hook; testing that requires the server to survive the agent pane's death. Split a second pane in the session first so the server has a reason to stay up:

```bash
./result/bin/tmux -L agentdetect-verify split-window -d -t verify
```

Now kill only the original agent pane, by its raw (`%`-prefixed) id:

```bash
RAW_PANE_ID=$(cat /tmp/agentdetect-verify-pane-id-raw)
./result/bin/tmux -L agentdetect-verify kill-pane -t "$RAW_PANE_ID"
```

Poll for cleanup rather than a fixed sleep — the watcher's own liveness backstop can take up to 30s (`picker/agentdetect/main.go:36`) even though the hook itself should fire immediately:

```bash
PANE_ID=$(cat /tmp/agentdetect-verify-pane-id)
for i in $(seq 1 30); do
  [ ! -e "/tmp/claude-status/watchers/$PANE_ID" ] && break
  sleep 1
done
ls /tmp/claude-status/watchers/$PANE_ID 2>&1
ls /tmp/claude-status/screen/$PANE_ID 2>&1
ps aux | grep "agent-detect $PANE_ID" | grep -v grep
```

Expected: watcher registry entry gone, screen state file gone (the `pane-exited` hook removes both together), and no live `agent-detect` process for that pane id.

Now tear down the scratch server entirely (pure teardown — the reaping assertion above already happened while it was alive):

```bash
./result/bin/tmux -L agentdetect-verify kill-server
```

Clean up the scratch marker files:

```bash
rm -f /tmp/agentdetect-verify-pane-id /tmp/agentdetect-verify-pane-id-raw
```

- [ ] **Step 7: Record the plain result of this verification in the PR body (do not claim success if any step above didn't produce the expected evidence)**

No command — this is a note to self for Task 5's PR write-up: state explicitly which of Steps 0–6 succeeded with real evidence (including whether Step 0 needed a rebase, and what `951666e`'s ancestor check returned), and flag plainly anything that didn't (e.g. if no nix-wrapped agent CLI was available to test with, or #333 was never found on any branch, say so rather than implying it was verified).

---

### Task 5: Final review and PR

**Files:** none

- [ ] **Step 1: Review the diff**

```bash
cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-332-fix-agent-detect-normalize-foo-wrapped-i
git fetch origin main
git log --oneline origin/main..HEAD
git diff origin/main..HEAD --stat
```

Diff against `origin/main`, not local `main` — Task 4 Step 0 may have rebased this branch onto `origin/main`, which has moved ahead of the local `main` ref. Confirm only these files changed: `picker/agentdetect/manifest/manifest.go`, `picker/agentdetect/manifest/manifest_test.go`, and this plan doc at `docs/superpowers/plans/2026-08-10-agentdetect-forcommand-normalize-wrapped.md`. Nothing under `picker/statusline/**`, `scripts/**`, or `config/` should appear.

- [ ] **Step 2: Push and open the PR**

```bash
git push -u origin feat/332-fix-agent-detect-normalize-foo-wrapped-i
gh pr create --assignee @me --title "fix(agent-detect): normalize .foo-wrapped in ForCommand so the scraper runs on nix" --body "$(cat <<'EOF'
## Summary
- `manifest.ForCommand` now strips a nix `.foo-wrapped` prefix/suffix (same regex convention as `picker/statusline`) before matching against `match_commands`, so agent-detect actually starts scraping on nix hosts where `#{pane_current_command}` reports `.claude-wrapped` instead of `claude`.
- Normalization lives inside `ForCommand` (not the call site) — every caller is correct by construction and there is exactly one `.foo-wrapped` strip in the `agentdetect` package.
- Closes #332. Go half of #328.
- FILL IN based on Task 4 Step 0's actual result: if the branch was rebased onto `origin/main` to pick up the shell-side arm, write "Shell half (#333, 951666e) was already on `origin/main`; rebased this branch onto it in Task 4 Step 0 so end-to-end verification below covers the full fix." If #333 was not found on any branch, write instead "Shell half (#333) has not landed anywhere yet — this PR covers only the Go-side `ForCommand` fix; end-to-end verification could not be run (see Test plan)." Do not assert #333 "landed" unless Task 4 Step 0's ancestor check actually confirmed it.

## Test plan
- [x] `go test ./agentdetect/manifest/... -race` — new wrapped/unwrapped table test green, confirmed red before the fix
- [x] `go test ./agentdetect/... -race`
- [x] `nix build .#default`
- [x] `nix flake check`
- [x] `nix build .#lint`
- [ ] End-to-end: `/tmp/claude-status/screen/` populated for a live agent pane on this nix host (see Task 4 verification notes — fill in actual outcome)
EOF
)"
```

Note: fill in the actual Task 4 outcome in the test plan checkbox before running this command — do not check the end-to-end box unless Task 4's steps produced real evidence.

---

## Self-review notes

- **Spec coverage:** issue's four red/green test cases (wrapped claude matches, wrapped unknown matches nothing, existing bare cases still match, bare unknown still matches nothing) are all in Task 1's table test. The "confirm red before fix" requirement is Task 1 Step 3. `-race` is used in every `go test` invocation. The "exactly one implementation of the strip in the agentdetect package" constraint is satisfied — only `manifest.go` gets a `wrappedRe`, `main.go` is untouched. The "do not touch statusline/scripts/config" scope constraint is checked in Task 5 Step 1. The plan doc itself satisfies the "commit a plan doc" repo convention.
- **Placeholder scan:** no TBD/TODO markers; every code step shows complete code; every test step shows the actual assertions.
- **Type consistency:** `ForCommand(ms []Manifest, cmd string) (Manifest, bool)` signature is unchanged across all tasks; `wrappedRe` name and regex pattern match the `picker/statusline/main.go` precedent exactly, as required by the issue.
- **Revision (post-critique):** Task 4 was rewritten to fix three issues found in review: (1) filesystem/registry/process checks now strip the `%` sigil from `#{pane_id}` before use, since `agent-detect`'s files and argv are keyed bare (verified against `tmux-update-icons.sh:58,60` and `main.go:44`), and the stripped id is persisted to a scratch file so it survives across the separate shell invocations Step 5→6 span; (2) verification now runs on a scratch tmux *server* (`./result/bin/tmux -L agentdetect-verify`) built fresh in Step 0b, not a session on the already-running shared server, so it actually exercises the Task 2 fix instead of a stale store path; (3) added Step 0, which checks (and, as of this writing, confirms it must act on) whether `951666e` (#333, the shell-side arm) is an ancestor of this branch's HEAD, rebasing onto `origin/main` when it isn't — `origin/main` already contains #333 as of 2026-08-10 — with an explicit bail-out path if #333 turns out to be unmerged anywhere. Task 5's PR body and diff-scope check were updated to match: the diff check now compares against `origin/main` (post-rebase-aware), and the "#333 landed" claim is conditioned on Step 0's actual result instead of asserted outright.
- **Revision 2 (post-critique):** Fixed three further issues: (1) Step 1's `new-session -d` left the scratch server with zero attached clients, and `arm_agent_detect` only runs from the `status-format[0]` `#()` job that tmux evaluates solely while a client renders the status line — so Steps 3-6 would have false-negatived with the watcher never arming. Step 1 now attaches a real client from a separate window on the caller's own tmux server and asserts it with `list-clients` before proceeding; Step 3's wait was bumped to 10s+ to clear the arm sweep's `CLAUDE_NOW % 5 == 0` gate. (2) Step 6 tested watcher reaping by `kill-server`, but the cleanup it was asserting is a `pane-exited` `run-shell` hook that needs a live server to execute — killing the server first means the hook never runs, so the "registry entry gone" check was unfalsifiable. Step 6 now splits a second pane to keep the server alive, `kill-pane`s only the agent's original pane (targeted via a newly-persisted raw `%`-prefixed pane id), polls up to 30s (the watcher's own liveness backstop, `main.go:36`) for the registry/screen files to clear, and only then runs `kill-server` as pure teardown. (3) Task 3's three-command gate ran before Task 4 Step 0's rebase onto `origin/main`, so a lint/test regression introduced by the merge would have shipped unchecked. Step 0b now re-runs all three gate commands (`nix build .#default`, `nix flake check`, `nix build .#lint`), not just the build, against the post-rebase tree.
