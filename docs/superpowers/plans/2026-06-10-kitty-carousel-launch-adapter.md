# Kitty Carousel Launch Adapter (non-tmux) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the Claude image carousel open without tmux — when Claude Code runs in a bare kitty window — by spawning a viewer split via `kitty @ launch`, keyed by the CC session id.

**Architecture:** Two shell scripts change; the Go viewer is **untouched** (it already renders kitty graphics and reads `images/<key>.jsonl` by key). The capture hook (`claude-images-update.sh`) gains a `$CLAUDE_CODE_SESSION_ID` fallback key for when `$TMUX_PANE` is absent. The launcher (`tmux-claude-images.sh`) gains a `resolve_target` step that picks a host — `tmux` (split-window, keyed by pane) → `kitty` (`kitty @ launch`, keyed by session id) → `none` — and dispatches. Both ends agree on the key: pane id in tmux, `$CLAUDE_CODE_SESSION_ID` otherwise. Verified facts: `CLAUDE_CODE_SESSION_ID` reaches CC subprocesses; `kitty @` works from a terminal-less CC subprocess over `$KITTY_LISTEN_ON`; `kitty @ launch --var` populates window `user_vars`; `kitty @ ls --match var:k=v` exits 1 on no match.

**Scope (locked):** kitty terminal + kitty graphics protocol only. No sixel/foot, no ghostty (no split CLI yet), no WezTerm — those are follow-ups. Binary distribution to non-nix users is explicitly deferred; this proves the path in a nix/lazytmux setup.

**Tech Stack:** Bash, bats (tests, run via `nix flake check`), kitty remote control, Nix (`writeShellScriptBin` + placeholder substitution).

---

## File Structure

- **Modify** `scripts/claude-images-update.sh` — capture hook; key fallback `$TMUX_PANE` → `$CLAUDE_CODE_SESSION_ID`. (Not in `scriptsWithIcons`; pure shell + runtime env, no Nix placeholder.)
- **Modify** `scripts/tmux-claude-images.sh` — launcher; add `resolve_target` + `launch_tmux`/`launch_kitty` + `--resolve` test seam. (In `scriptsWithIcons`; `@picker_generate@` substituted at build time, only inside the `launch_*` functions.)
- **Modify** `tests/claude-images.bats` — update the no-`TMUX_PANE` test, add a session-id-key test, isolate `CLAUDE_CODE_SESSION_ID` in `setup()`.
- **Create** `tests/claude-images-launch.bats` — unit-test `resolve_target` via the `--resolve` seam (no tmux/kitty binaries needed).
- **Modify** `flake.nix:103` — add `bats tests/claude-images-launch.bats` to the `claude-images-tests` check.
- **Modify** `CLAUDE.md` — update the `tmux-claude-images` row to describe dual-mode (tmux split / kitty window).

The keybind (`config/tmux.conf.nix:409`, `bind I run-shell .../tmux-claude-images`) is **unchanged** — in tmux the launcher behaves identically.

---

## Task 1: Capture hook keys by session id outside tmux

**Files:**
- Modify: `scripts/claude-images-update.sh:10-12`
- Test: `tests/claude-images.bats`

- [ ] **Step 1: Isolate session id in test setup**

In `tests/claude-images.bats`, add one line at the end of `setup()` (after `APP="scripts/claude-images-update.sh"`):

```bash
	unset CLAUDE_CODE_SESSION_ID
```

- [ ] **Step 2: Replace the "no TMUX_PANE" test and add a session-id test**

Replace the existing test (currently `@test "no TMUX_PANE -> no-op, exit 0"`, lines 58-63):

```bash
@test "no TMUX_PANE and no session id -> no-op, exit 0" {
	unset TMUX_PANE
	unset CLAUDE_CODE_SESSION_ID
	run run_app hook-read-image.json
	[ "$status" -eq 0 ]
	[ ! -f "$MANIFEST" ]
}

@test "no TMUX_PANE falls back to CLAUDE_CODE_SESSION_ID key" {
	unset TMUX_PANE
	export CLAUDE_CODE_SESSION_ID="sess-abc"
	run_app hook-read-image.json
	sess_manifest="$CLAUDE_STATUS_DIR/images/sess-abc.jsonl"
	[ -f "$sess_manifest" ]
	run jq -r '.path' "$sess_manifest"
	[ "$output" = "$IMG" ]
}
```

- [ ] **Step 3: Run tests to verify the new one fails**

Run: `cd /path/to/worktree && bats tests/claude-images.bats`
Expected: `no TMUX_PANE falls back to CLAUDE_CODE_SESSION_ID key` FAILS (hook currently no-ops when `$TMUX_PANE` is unset, so `sess-abc.jsonl` is never created). The "no-op" test PASSES.

- [ ] **Step 4: Add the session-id fallback to the hook**

In `scripts/claude-images-update.sh`, replace lines 10-12:

```bash
pane_id="${TMUX_PANE:-}"
[[ -n $pane_id ]] || exit 0 # outside tmux → no-op
pane_file="${pane_id#%}"
```

with:

```bash
# Key by tmux pane when inside tmux, else the Claude Code session id so the
# carousel works in a bare terminal. No pane and no session id → no-op.
pane_id="${TMUX_PANE:-${CLAUDE_CODE_SESSION_ID:-}}"
[[ -n $pane_id ]] || exit 0
pane_file="${pane_id#%}"
```

- [ ] **Step 5: Run tests to verify all pass**

Run: `bats tests/claude-images.bats`
Expected: all tests PASS (including the dedup, screenshot, and both no-`TMUX_PANE` cases).

- [ ] **Step 6: shellcheck the hook**

Run: `shellcheck scripts/claude-images-update.sh`
Expected: no warnings.

- [ ] **Step 7: Commit**

```bash
git add scripts/claude-images-update.sh tests/claude-images.bats
git commit -m "feat(images): key capture hook by CC session id outside tmux"
```

---

## Task 2: Launcher resolves host (tmux | kitty | none) and dispatches

**Files:**
- Modify: `scripts/tmux-claude-images.sh` (full rewrite)
- Create: `tests/claude-images-launch.bats`
- Modify: `flake.nix:103`

- [ ] **Step 1: Write the failing launcher tests**

Create `tests/claude-images-launch.bats`:

```bash
#!/usr/bin/env bats

# Unit-tests resolve_target via the script's --resolve seam, which prints
# "<MODE>\t<KEY>\t<MANIFEST>" and exits before any tmux/kitty call.

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/state"
	SCRIPT="scripts/tmux-claude-images.sh"
	unset TMUX TMUX_PANE KITTY_LISTEN_ON CLAUDE_CODE_SESSION_ID
}

@test "tmux mode: keyed by TMUX_PANE" {
	export TMUX="/tmp/sock,1,0" TMUX_PANE="%7"
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "$output" = "tmux	%7	$CLAUDE_STATUS_DIR/images/7.jsonl" ]
}

@test "kitty mode: keyed by CLAUDE_CODE_SESSION_ID when not in tmux" {
	export KITTY_LISTEN_ON="unix:/tmp/kitty-1" CLAUDE_CODE_SESSION_ID="sess-abc"
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "$output" = "kitty	sess-abc	$CLAUDE_STATUS_DIR/images/sess-abc.jsonl" ]
}

@test "tmux wins over kitty when both present" {
	export TMUX="/tmp/sock,1,0" TMUX_PANE="%3" KITTY_LISTEN_ON="unix:/tmp/kitty-1"
	run bash "$SCRIPT" --resolve
	[ "${output%%	*}" = "tmux" ]
}

@test "none mode: neither tmux nor kitty" {
	run bash "$SCRIPT" --resolve
	[ "$status" -eq 0 ]
	[ "${output%%	*}" = "none" ]
}
```

(The literal tabs in the `[ "$output" = ... ]` lines are real TAB characters.)

- [ ] **Step 2: Wire the new test into the flake check**

In `flake.nix`, inside the `claude-images-tests` derivation, add a line after `bats tests/claude-images.bats` (line 103):

```nix
              bats tests/claude-images.bats
              bats tests/claude-images-launch.bats
```

- [ ] **Step 3: Run the new tests to verify they fail**

Run: `bats tests/claude-images-launch.bats`
Expected: FAIL — the current launcher has no `--resolve` seam and assumes tmux, so output won't match (it will try `tmux list-panes` and error, or print nothing).

- [ ] **Step 4: Rewrite the launcher**

Replace the entire contents of `scripts/tmux-claude-images.sh` with:

```bash
#!/usr/bin/env bash
# Open the Claude image carousel for the invoking session.
#   - Inside tmux: toggle a split pane (bound to prefix+I; also runnable by
#     Claude via a Bash call). Keyed by $TMUX_PANE.
#   - Outside tmux, in kitty with remote control: toggle a split window via
#     `kitty @ launch`. Keyed by $CLAUDE_CODE_SESSION_ID.
# The carousel binary (@picker_generate@) and manifest format are shared.
set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
IMAGES_DIR="$STATE_DIR/images"

# resolve_target sets MODE/KEY/MANIFEST from the environment.
#   MODE=tmux  + KEY=<pane id>         inside tmux
#   MODE=kitty + KEY=<cc session id>   outside tmux, kitty remote control up
#   MODE=none                          neither host available
resolve_target() {
	if [[ -n ${TMUX:-} ]]; then
		MODE=tmux
		KEY="${TMUX_PANE:-$(tmux display-message -p '#{pane_id}')}"
		MANIFEST="$IMAGES_DIR/${KEY#%}.jsonl"
	elif [[ -n ${KITTY_LISTEN_ON:-} ]]; then
		MODE=kitty
		KEY="${CLAUDE_CODE_SESSION_ID:-}"
		MANIFEST="$IMAGES_DIR/$KEY.jsonl"
	else
		MODE=none
	fi
}

launch_tmux() {
	local existing
	existing="$(tmux list-panes -F '#{pane_id} #{@claude_img_src}' |
		awk -v s="$KEY" '$2 == s {print $1; exit}')"
	if [[ -n $existing ]]; then
		tmux kill-pane -t "$existing"
		return
	fi
	local viewer
	viewer="$(tmux split-window -h -P -F '#{pane_id}' "@picker_generate@ --gallery '$KEY'")"
	tmux set-option -p -t "$viewer" @claude_img_src "$KEY"
}

launch_kitty() {
	# Toggle: a viewer window is tagged with user_var claude_img_src=$KEY.
	# `kitty @ ls --match` exits non-zero when nothing matches.
	if kitty @ ls --match "var:claude_img_src=$KEY" >/dev/null 2>&1; then
		kitty @ close-window --match "var:claude_img_src=$KEY"
		return
	fi
	kitty @ launch --type=window --var claude_img_src="$KEY" \
		--env CLAUDE_STATUS_DIR="$STATE_DIR" \
		@picker_generate@ --gallery "$KEY" >/dev/null
}

main() {
	resolve_target
	if [[ ${1:-} == --resolve ]]; then # test seam: print resolution, no launch
		printf '%s\t%s\t%s\n' "$MODE" "${KEY:-}" "${MANIFEST:-}"
		return
	fi
	case $MODE in
	none)
		echo "image carousel needs tmux or kitty remote control" >&2
		exit 0
		;;
	kitty)
		[[ -n $KEY ]] || {
			echo "no CLAUDE_CODE_SESSION_ID; cannot locate images" >&2
			exit 0
		}
		;;
	esac
	if [[ ! -s $MANIFEST ]]; then
		[[ $MODE == tmux ]] && tmux display-message "no images yet for this pane"
		exit 0
	fi
	"launch_$MODE"
}

main "$@"
```

- [ ] **Step 5: Run the launcher tests to verify they pass**

Run: `bats tests/claude-images-launch.bats`
Expected: all 4 PASS.

- [ ] **Step 6: shellcheck the launcher**

Run: `shellcheck scripts/tmux-claude-images.sh`
Expected: no warnings. (Declarations and command-substitution assignments are split to avoid SC2155; `"launch_$MODE"` is an intentional dynamic dispatch.)

- [ ] **Step 7: Build to confirm Nix substitution still applies**

Run: `nix build .#default`
Expected: builds; `@picker_generate@` inside the `launch_*` functions is replaced with the picker store path (the launcher is in `scriptsWithIcons`).

- [ ] **Step 8: Commit**

```bash
git add scripts/tmux-claude-images.sh tests/claude-images-launch.bats flake.nix
git commit -m "feat(images): open carousel via kitty @ launch when not in tmux"
```

---

## Task 3: Document the dual-mode launcher

**Files:**
- Modify: `CLAUDE.md` (the `tmux-claude-images` row of the Script Roles table)

- [ ] **Step 1: Update the table row**

Find the `tmux-claude-images` entry in the "Script Roles" table and replace its Purpose cell text with:

```
Toggle the image carousel for the invoking Claude session. In tmux: split pane keyed by `$TMUX_PANE` (bound to `prefix + I`). Outside tmux in kitty (remote control on): `kitty @ launch` window keyed by `$CLAUDE_CODE_SESSION_ID`, tagged `user_var claude_img_src`. Renderer + manifest shared across modes.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: note kitty (non-tmux) mode for the image carousel launcher"
```

---

## Task 4: Manual end-to-end verification (kitty, no tmux)

Not automatable (needs a live kitty + a CC session outside tmux). Run after `nh os switch`/rebuild deploys the new build.

- [ ] **Step 1:** In a kitty window with `allow_remote_control yes`, start Claude Code **not** inside tmux.
- [ ] **Step 2:** Have Claude `Read` an image (e.g. a `.jpg`), so the hook writes `/tmp/claude-status/images/<session-id>.jsonl`. Confirm: `ls /tmp/claude-status/images/` shows a file named with the session id (not a `%`-pane).
- [ ] **Step 3:** Run the launcher (`tmux-claude-images`, via the image-gallery skill or directly). Expected: a new kitty **window/split** opens showing the carousel with the image rendered (kitty graphics), navigable with arrows, `q` closes it.
- [ ] **Step 4:** Run the launcher again. Expected: the existing viewer window **closes** (toggle), via the `claude_img_src` user-var match.
- [ ] **Step 5:** Regression — in your normal tmux setup, `prefix + I` still toggles the split pane exactly as before.

---

## Self-Review

- **Spec coverage:** tmux-independence (Task 2 kitty mode), kitty-protocol-only (viewer untouched, no sixel), session-id key agreement (Task 1 hook + Task 2 launcher both use `$CLAUDE_CODE_SESSION_ID`), toggle parity (`launch_kitty` close-on-match), CI (Task 2 flake wiring), docs (Task 3), manual proof (Task 4). ghostty/WezTerm/distribution explicitly out of scope per the locked scope. ✓
- **Placeholder scan:** every step has concrete code/commands. ✓
- **Type/name consistency:** `resolve_target` sets `MODE`/`KEY`/`MANIFEST`; `main` dispatches `"launch_$MODE"` to `launch_tmux`/`launch_kitty`; `--resolve` prints the same three; tests assert the same key (`%7`→`7.jsonl`, `sess-abc`→`sess-abc.jsonl`) the hook writes. user-var name `claude_img_src` matches between `launch_kitty` set and match. ✓
```
