# Fix #235 empty reflow_key width Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `tmux-reflow-windows` from stamping `@reflow_key` with an empty width half, and make readiness test 2 fail loudly on that signal instead of timing out.

**Architecture:** Hooks pass `#{client_width}` which is empty with no attached / size-neutral client. Backgrounded `after-new-window` reflows race the test's forced `s 36` reflow and overwrite `10:36` with `10:`. Bail before any cache write when WIDTH is empty/non-numeric; harden the bats assertion to name the empty-width probe.

**Tech Stack:** bash, bats, tmux (private `-L` socket), nix flake check

## Global Constraints

- Scope: `scripts/tmux-reflow-windows.sh`, `tests/tmux-next38-readiness.bats`, optionally `tests/reflow-fanout.bats`. Do not touch `picker/remotebridge/`.
- Never invoke bare `tmux` in tests — always `-L <socket>` / `TMUX_TMPDIR`.
- Do not raise timeouts or retry-until-green.
- shfmt tabs; shellcheck clean; commit from `nix develop`.

---

## Evidence (pre-fix)

Local loop of test 2 against built wrapper: intermittent fail with
`timed out waiting for @reflow_key=10:36, last=10:` — window count right, width empty.
Test already passes width `36` explicitly; poison comes from racing hook/update-icons
reflows that expand empty `#{client_width}` and still stamp the key.

**Root cause settled: (2)** — real reflow bug. Test harness also benefits from a louder empty-width failure.

---

### Task 1: Bail on empty/invalid WIDTH in reflow

**Files:**
- Modify: `scripts/tmux-reflow-windows.sh` (after WIDTH assignment, before cache fast-path)
- Modify: `tests/reflow-fanout.bats` (add case: empty width leaves prior `@reflow_key` untouched)

- [ ] **Step 1: Add regression test for empty WIDTH**

In `tests/reflow-fanout.bats`, after the existing lock test pattern, add:

```bash
@test "empty WIDTH exits without poisoning @reflow_key" {
	tmux set -q @reflow_key "3:200"
	run bash "$REFLOW" S "" --force
	[ "$status" -eq 0 ]
	[ "$(tmux show -v @reflow_key)" = "3:200" ]

	run bash "$REFLOW" S --force
	[ "$status" -eq 0 ]
	[ "$(tmux show -v @reflow_key)" = "3:200" ]
}
```

- [ ] **Step 2: Implement early exit when WIDTH is not a positive integer**

Right after `WIDTH=${2:-$(tmux display-message -p '#{client_width}')}` (and scratch-session skip is fine either side), add:

```bash
# Empty/non-numeric width: no attached or size-neutral client expanded
# #{client_width} to nothing. Stamping "N:" poisons the cache and lets a
# later real reflow look like a no-op hit (issue #235).
if [[ ! $WIDTH =~ ^[1-9][0-9]*$ ]]; then
	log_enabled && log_event reflow event skip_empty_width width "$WIDTH" sess "$SESSION"
	exit 0
fi
```

- [ ] **Step 3: Run the new fanout test green**

```bash
# via the flake check target or local bats with PATH tmux
nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').reflow-fanout-tests 2>/dev/null || \
  nix flake check  # scoped if available
```

Prefer running the bats file the same way CI does (see `flake.nix` `reflow-fanout-tests`).

---

### Task 2: Loud empty-width failure in readiness test 2

**Files:**
- Modify: `tests/tmux-next38-readiness.bats`

- [ ] **Step 4: Fail fast on empty width half while waiting**

Update `wait_for_option` usage for `@reflow_key` (or a dedicated waiter) so that if the current value matches `^[0-9]+:$` (empty width half), fail immediately with a message naming the empty width probe — do not burn the full timeout.

Also keep the control client attached until after the key is observed (detach after the wait), so a post-detach empty-width reflow cannot race the assertion window. The reflow bail from Task 1 is the real fix; this is defense-in-depth + clearer CI signal.

- [ ] **Step 5: Loop test 2 ≥20 times; report pass/fail counts**

```fish
set -x TMUX_BIN (nix build .#default --no-link --print-out-paths)/bin/tmux
# 25× bats --filter 'status-format 0-3 parse' …
```

Require 20/20+ consecutive passes.

---

### Task 3: Gate + ship

- [ ] **Step 6: `nix flake check` green**
- [ ] **Step 7: Commit, push, open PR with `Closes #235` and the before/after pass counts**
