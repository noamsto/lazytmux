# kitty-pane mode polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the aeye kitty-pane carousel (a) auto-open hidden when its pane is off-screen and (b) navigable with seamless `ctrl+hjkl` across the tmux↔carousel boundary.

**Architecture:** Three changes across two repos. aeye: `launch_kitty` launches into the stash tab when the owning pane isn't on a visible window; the viewer handles `ctrl+hjkl` by calling `kitty @ action neighboring_window`. lazytmux: a `tmux-smart-nav` helper + rewritten `C-hjkl` bindings hand off to kitty at a tmux edge. No kitty config and no Home Manager option — the viewer owns the carousel→tmux direction, the tmux side self-gates on `KITTY_LISTEN_ON`. nix-config only bumps flake inputs.

**Tech Stack:** Bash (`scripts/tmux-claude-images.sh`, lazytmux `scripts/*.sh`), Go + bubbletea (`gallery.go`), Nix (flake-parts, Home Manager), bats, `go test`.

**Spec:** `docs/superpowers/specs/2026-06-29-kitty-pane-mode-polish-design.md`
**Issue:** [noamsto/aeye#103](https://github.com/noamsto/aeye/issues/103)

## Global Constraints

- **Self-gate, never regress non-kitty users.** Every new code path is inert unless `KITTY_LISTEN_ON` is set: `tmux-smart-nav` falls back to `select-pane`; `kittyNeighbor` returns early; the aeye stash-launch branch only runs in kitty mode. tmux-split-mode and non-kitty terminals must behave exactly as today.
- **Pane key retains its `%`.** `$KEY` is `${TMUX_PANE:-…}` and keeps the leading `%` (`tmux-claude-images.sh:42`). Match it verbatim in stubs and asserts.
- **Viewer uses bare `h/l/j/k`** for gallery nav; `ctrl+hjkl` are free and must not disturb those cases.
- **No new Go dependencies** — `os`/`os/exec` are stdlib, so `go.mod` is untouched and no flake `vendorHash` refresh is needed.
- **Tooling:** run `shellcheck` on every shell script; shell behavior is covered by `bats`, Go by `go test`. Run repo commands inside the worktree's direnv devshell (`direnv exec . <cmd>`).
- **Worktree isolation:** each repo's work happens in its own git worktree; never commit to `main`.

---

## Phase 0: Worktree setup

- [ ] **Step 1: aeye worktree** (issue #103)

```bash
cd /home/noams/Data/git/noamsto/aeye
wt switch -c feat/103-launch-hidden-nav -y
```

- [ ] **Step 2: nix-config worktree**

```bash
cd /home/noams/nix-config
wt switch -c feat/carousel-seamless-nav -y
```

- [ ] **Step 3: warm direnv in each fresh worktree** (so pre-commit + devshell work; see memory: precommit-worktree-direnv). Run once per worktree path, e.g. for aeye:

```bash
cd <aeye-worktree-path>
direnv allow .
direnv exec . true
```

The lazytmux worktree (`feat/kitty-pane-mode-polish`, already created, holds this plan + spec) is already direnv-warmed.

---

## Phase A — aeye

### Task A1: launch the carousel hidden when its pane is off-screen

**Files:**
- Modify: `scripts/tmux-claude-images.sh` — add `_key_on_screen()`; add the stash-launch branch inside `launch_kitty()` after the line 126-130 already-open guard.
- Test: `tests/launch-hidden.bats` (new).

**Interfaces:**
- Produces: `_key_on_screen()` — returns 0 iff `$KEY` is a pane in the active window of an attached session.

- [ ] **Step 1: Write the failing test** — create `tests/launch-hidden.bats`:

```bash
#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031  # bats wraps each @test in a subshell; export is intentional

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/state"
	mkdir -p "$CLAUDE_STATUS_DIR/images"
	APP="$(dirname "$BATS_TEST_DIRNAME")/scripts/tmux-claude-images.sh"
	export AEYE_HOST=kitty TMUX_PANE='%9'
	# Non-empty manifest for %9 so launch gets past the "no images" guard.
	echo '{"type":"image","path":"/x.png","source":"d2"}' >"$CLAUDE_STATUS_DIR/images/9.jsonl"

	STUB="$BATS_TEST_TMPDIR/bin"; mkdir -p "$STUB"
	export KITTY_LOG="$BATS_TEST_TMPDIR/kitty.log"; : >"$KITTY_LOG"
	export VISIBLE_ROWS="$BATS_TEST_TMPDIR/visible"
	# Viewer stub so VIEWER_BIN resolves on PATH.
	printf '#!/usr/bin/env bash\n:\n' >"$STUB/aeye"; chmod +x "$STUB/aeye"
	# kitty stub: bare `@ ls` = reachable (echo []); `@ ls --match ...` = no match
	# (exit 1) so launch_kitty proceeds; launch/other subcommands are logged.
	cat >"$STUB/kitty" <<'K'
#!/usr/bin/env bash
shift            # drop '@'
sub="$1"; shift  # drop subcommand
case "$sub" in
ls) [[ "${1:-}" == "--match" ]] && exit 1; echo '[]' ;;
launch) printf 'launch %s\n' "$*" >>"$KITTY_LOG" ;;
*) printf '%s %s\n' "$sub" "$*" >>"$KITTY_LOG" ;;
esac
K
	# tmux stub: list-panes emits the configured rows (ignores -a/-F shape).
	cat >"$STUB/tmux" <<'T'
#!/usr/bin/env bash
[[ "${1:-}" == list-panes ]] && cat "$VISIBLE_ROWS" 2>/dev/null
exit 0
T
	chmod +x "$STUB/kitty" "$STUB/tmux"
	export PATH="$STUB:$PATH"
}

@test "ensure-open launches stashed (and never steals focus) when off-screen" {
	printf '%%9 0 1\n' >"$VISIBLE_ROWS"   # %9 present but window_active=0
	run bash "$APP" --ensure-open
	[ "$status" -eq 0 ]
	grep -q 'launch --type=window --match var:aeye_stash=1 .*--keep-focus.*claude_img_src=%9' "$KITTY_LOG"
}

@test "ensure-open launches a visible vsplit (keep-focus) when on-screen" {
	printf '%%9 1 1\n' >"$VISIBLE_ROWS"   # %9 is the active window of an attached session
	run bash "$APP" --ensure-open
	[ "$status" -eq 0 ]
	grep -q 'launch --type=window .*--location=vsplit.*--keep-focus.*claude_img_src=%9' "$KITTY_LOG"
	! grep -q 'launch --type=window --match var:aeye_stash=1 .*claude_img_src=%9' "$KITTY_LOG"
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <aeye-worktree> && direnv exec . bats tests/launch-hidden.bats`
Expected: the off-screen test FAILS (today's code always launches the visible vsplit, so no `--match var:aeye_stash=1` launch line). The on-screen test may already pass.

- [ ] **Step 3: Add `_key_on_screen()`** to `scripts/tmux-claude-images.sh` (place it just above `launch_kitty()`):

```sh
# True iff $KEY is a pane in the active window of an attached session — i.e. the
# user is currently looking at it. Unlike reconcile's context-dependent query,
# this runs in the capturing pane's context, so it must scan all panes (-a) and
# filter explicitly rather than trust the calling pane as "current".
_key_on_screen() {
	tmux list-panes -a -F '#{pane_id} #{window_active} #{session_attached}' 2>/dev/null |
		awk -v k="$KEY" '$1==k && $2==1 && $3>=1 {f=1} END{exit !f}'
}
```

- [ ] **Step 4: Add the stash-launch branch** inside `launch_kitty()`, immediately after the already-open guard block (the `if kitty @ ls --match "var:claude_img_src=$KEY" …` block ending at line 130) and before the `local placement=()` visible launch:

```sh
	# Auto-open for an off-screen pane: create it stashed so it never appears over
	# the window the user is actually looking at; reconcile reveals it on focus.
	# Deliberately bypasses kitty_place_args (no host goto-layout side effect) —
	# placement is deferred to _carousel_unstash's `goto-layout … splits`.
	if [[ -n $ENSURE_OPEN ]] && ! _key_on_screen; then
		_ensure_stash_tab
		kitty @ launch --type=window --match "var:aeye_stash=1" --keep-focus \
			--var claude_img_src="$KEY" \
			--env AEYE_DIR="$STATE_DIR" \
			--env CLAUDE_STATUS_DIR="$STATE_DIR" \
			"$VIEWER_BIN" "$KEY" >/dev/null
		return
	fi
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd <aeye-worktree> && direnv exec . bats tests/launch-hidden.bats`
Expected: both tests PASS.

- [ ] **Step 6: Regression — run the existing carousel/launch suites**

Run: `direnv exec . bats tests/carousel-reconcile.bats tests/toggle-window.bats`
Expected: all PASS (the new branch only fires on `--ensure-open` + off-screen, which these don't exercise).

- [ ] **Step 7: shellcheck**

Run: `shellcheck scripts/tmux-claude-images.sh`
Expected: no new warnings.

- [ ] **Step 8: Commit**

```bash
git add scripts/tmux-claude-images.sh tests/launch-hidden.bats
git commit -m "fix(carousel): launch hidden when the owning pane is off-screen (#103)"
```

### Task A1b: reveal focuses the tmux host window (not the carousel)

**Files:**
- Modify: `scripts/tmux-claude-images.sh` — `_reconcile_apply()`'s focus-restore block (the `if [[ $touched -eq 1 && -n $host_tab ]]` at ~line 316).
- Test: `tests/carousel-reconcile.bats` (extend).

**Why:** verified live — `kitty @ detach-window --target-tab` makes the moved carousel the *active* window in the host tab, so the existing `focus-tab` leaves focus on the carousel, not the tmux pane. Focus the host window explicitly.

- [ ] **Step 1: Write the failing test** — append to `tests/carousel-reconcile.bats`. (The existing `kitty` stub logs every non-`ls` subcommand to `$KITTY_LOG`, and `ls` cats `$KITTY_LS_JSON`; the unstash fixture `kitty-ls-stashed-9.json` parks `%9` in the stash tab. Confirm the host (tmux) window id in that fixture — adjust the asserted id to match it.)

```bash
@test "revealing a stashed carousel focuses the tmux host window, not the carousel" {
	export AEYE_HOST=kitty
	cp "$FIX/kitty-ls-stashed-9.json" "$KITTY_LS_JSON" # %9 parked in the stash tab
	printf '%%9\n' >"$VISIBLE_PANES"                   # visible window has %9 → unstash it
	run bash "$APP" --reconcile
	[ "$status" -eq 0 ]
	# Focus must land on the host window (no claude_img_src / aeye_stash var),
	# not on a focus-tab that leaves the carousel active.
	grep -q 'focus-window --match id:' "$KITTY_LOG"
	! grep -qE 'focus-tab' "$KITTY_LOG"
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <aeye-worktree> && direnv exec . bats tests/carousel-reconcile.bats`
Expected: the new test FAILS — today's code emits `focus-tab`, not `focus-window`.

- [ ] **Step 3: Replace the focus-restore block** in `_reconcile_apply()`:

```sh
	# detach-window leaves the moved carousel active in the host tab, so focus the
	# tmux host window explicitly (the host-tab window with neither carousel nor
	# stash var) — focus-tab alone would land on the carousel. host_win comes from
	# the pre-move snapshot; the tmux host window never moves.
	if [[ $touched -eq 1 && -n $host_tab ]]; then
		local host_win
		host_win="$(jq -r --arg t "$host_tab" '
			.[].tabs[] | select((.id|tostring) == $t) | .windows[]
			| select((.user_vars.claude_img_src // "") == "" and (.user_vars.aeye_stash // "") == "")
			| .id' <<<"$ls" | head -1)"
		if [[ -n $host_win ]]; then
			kitty @ focus-window --match "id:$host_win" >/dev/null 2>&1 || true
		else
			kitty @ focus-tab --match "id:$host_tab" >/dev/null 2>&1 || true
		fi
	fi
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `direnv exec . bats tests/carousel-reconcile.bats`
Expected: all PASS (the new test + the existing stash/unstash tests; the prior tests asserted `detach-window`, not `focus-tab`, so they're unaffected).

- [ ] **Step 5: shellcheck + commit**

```bash
shellcheck scripts/tmux-claude-images.sh
git add scripts/tmux-claude-images.sh tests/carousel-reconcile.bats
git commit -m "fix(carousel): reveal focuses the tmux host window, not the carousel (#103)"
```

### Task A2: viewer handles ctrl+hjkl → kitty neighbor

**Files:**
- Modify: `gallery.go` — add `neighborForKey` + `kittyNeighbor`; add four `ctrl+hjkl` cases in the `tea.KeyPressMsg` switch (~line 235).
- Test: `gallery_nav_test.go` (new) — or append to an existing `_test.go` if the package convention prefers it.

**Interfaces:**
- Consumes: nothing from A1.
- Produces: `neighborForKey(key string) (string, bool)`; `kittyNeighbor(dir string)`.

- [ ] **Step 1: Write the failing test** — create `gallery_nav_test.go` (same `package` as `gallery.go` — confirm with `head -1 gallery.go`):

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNeighborForKey(t *testing.T) {
	cases := map[string]string{"ctrl+h": "left", "ctrl+j": "down", "ctrl+k": "up", "ctrl+l": "right"}
	for key, want := range cases {
		if got, ok := neighborForKey(key); !ok || got != want {
			t.Errorf("neighborForKey(%q) = %q,%v; want %q,true", key, got, ok, want)
		}
	}
	if _, ok := neighborForKey("h"); ok {
		t.Error("neighborForKey(\"h\") should be false (bare h is gallery nav)")
	}
}

func TestKittyNeighbor(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	stub := "#!/usr/bin/env bash\necho \"$*\" >>\"" + log + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "kitty"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// No KITTY_LISTEN_ON → must be a no-op (stub never invoked).
	t.Setenv("KITTY_LISTEN_ON", "")
	kittyNeighbor("left")
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("kittyNeighbor ran kitty without KITTY_LISTEN_ON set")
	}

	// With KITTY_LISTEN_ON → calls `kitty @ action neighboring_window <dir>`.
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-test")
	kittyNeighbor("right")
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("kitty stub not invoked: %v", err)
	}
	if got := string(out); got != "@ action neighboring_window right\n" {
		t.Errorf("kitty args = %q; want @ action neighboring_window right", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <aeye-worktree> && direnv exec . go test -run 'TestNeighborForKey|TestKittyNeighbor' ./...`
Expected: FAIL — `undefined: neighborForKey` / `undefined: kittyNeighbor`.

- [ ] **Step 3: Implement the helpers** in `gallery.go` (add near the other unexported helpers; ensure `os` and `os/exec` are imported):

```go
// neighborForKey maps a ctrl+hjkl keypress to a kitty neighboring_window
// direction. Bare h/l/j/k are gallery nav and are not handled here.
func neighborForKey(key string) (string, bool) {
	switch key {
	case "ctrl+h":
		return "left", true
	case "ctrl+j":
		return "down", true
	case "ctrl+k":
		return "up", true
	case "ctrl+l":
		return "right", true
	}
	return "", false
}

// kittyNeighbor moves kitty focus to the neighbouring window. Inert off-kitty:
// KITTY_LISTEN_ON is set only for windows kitty launched with remote control, so
// this is a no-op in tmux-split mode or any non-kitty host.
func kittyNeighbor(dir string) {
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		return
	}
	_ = exec.Command("kitty", "@", "action", "neighboring_window", dir).Run()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `direnv exec . go test -run 'TestNeighborForKey|TestKittyNeighbor' ./...`
Expected: PASS.

- [ ] **Step 5: Wire the keys into Update** — in the `switch msg.String()` block (`gallery.go:235`), add after the `case "q", "ctrl+c":` case:

```go
		case "ctrl+h", "ctrl+j", "ctrl+k", "ctrl+l":
			// Cross the tmux/kitty boundary: hand focus to the neighbouring kitty
			// window (the tmux host). Focus leaves this window, so no repaint.
			dir, _ := neighborForKey(msg.String())
			kittyNeighbor(dir)
			return m, nil
```

- [ ] **Step 6: Build + full Go test**

Run: `direnv exec . go build ./... && direnv exec . go test ./...`
Expected: build OK; all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add gallery.go gallery_nav_test.go
git commit -m "feat(carousel): viewer crosses to tmux via ctrl+hjkl (kitty neighboring_window) (#103)"
```

### Task A3: README — document that nav is automatic + the non-lazytmux tmux snippet

**Files:**
- Modify: `README.md` — in the "Enable kitty-pane mode" section (~line 169-188), add the `tmux-smart-nav` snippet and note the viewer handles the return direction itself.

- [ ] **Step 1: Add the docs** after the existing kitty/tmux prerequisite steps in the "Enable kitty-pane mode" section:

```markdown
**Seamless `Ctrl-hjkl`.** Once kitty-pane mode is on, the carousel viewer crosses
back into tmux on `Ctrl-h/j/k/l` itself (via `kitty @ action neighboring_window`)
— no kitty config needed. For the tmux→carousel direction, your tmux nav bindings
must hand off to kitty at the pane edge. lazytmux ships this; for a hand-rolled
`tmux.conf`, route `Ctrl-hjkl` through a helper that, at the edge with
`KITTY_LISTEN_ON` set, runs `kitty @ action neighboring_window <dir>` instead of
`select-pane` (see `lazytmux` `scripts/tmux-smart-nav.sh`).
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(carousel): document seamless ctrl+hjkl in kitty-pane mode (#103)"
```

- [ ] **Step 3: Push the aeye branch + open the PR**

```bash
git push -u origin feat/103-launch-hidden-nav
gh pr create --assignee @me --title "fix(carousel): launch hidden off-screen + seamless ctrl+hjkl (#103)" \
	--body "Closes #103 (aeye parts). See lazytmux spec 2026-06-29-kitty-pane-mode-polish-design.md."
```

---

## Phase B — lazytmux

### Task B1: `tmux-smart-nav` helper + bats test

**Files:**
- Create: `scripts/tmux-smart-nav.sh`.
- Modify: `config/tmux.conf.nix` — add `"tmux-smart-nav"` to `scriptNames` (line 228-249).
- Test: `tests/smart-nav.bats` (new).

**Interfaces:**
- Produces: `tmux-smart-nav <select-flag> <kitty-dir> <zoomed> <at_edge>` on PATH.

- [ ] **Step 1: Write the failing test** — create `tests/smart-nav.bats`:

```bash
#!/usr/bin/env bats
setup() {
	SCRIPT="$(dirname "$BATS_TEST_DIRNAME")/scripts/tmux-smart-nav.sh"
	STUB="$BATS_TEST_TMPDIR/bin"; mkdir -p "$STUB"
	export KITTY_LOG="$BATS_TEST_TMPDIR/kitty.log"; : >"$KITTY_LOG"
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"; : >"$TMUX_LOG"
	printf '#!/usr/bin/env bash\necho "$*" >>"%s"\n' "$KITTY_LOG" >"$STUB/kitty"
	printf '#!/usr/bin/env bash\necho "$*" >>"%s"\n' "$TMUX_LOG" >"$STUB/tmux"
	chmod +x "$STUB/kitty" "$STUB/tmux"
	export PATH="$STUB:$PATH"
}

@test "zoomed: no movement at all" {
	export KITTY_LISTEN_ON=unix:/tmp/k
	run bash "$SCRIPT" R right 1 1
	[ "$status" -eq 0 ]
	[ ! -s "$KITTY_LOG" ]; [ ! -s "$TMUX_LOG" ]
}

@test "non-edge: select-pane within tmux" {
	export KITTY_LISTEN_ON=unix:/tmp/k
	run bash "$SCRIPT" R right 0 0
	grep -q 'select-pane -R' "$TMUX_LOG"
	[ ! -s "$KITTY_LOG" ]
}

@test "edge without KITTY_LISTEN_ON: falls back to select-pane" {
	unset KITTY_LISTEN_ON
	run bash "$SCRIPT" R right 0 1
	grep -q 'select-pane -R' "$TMUX_LOG"
	[ ! -s "$KITTY_LOG" ]
}

@test "edge with kitty: hand off to neighboring_window" {
	export KITTY_LISTEN_ON=unix:/tmp/k
	run bash "$SCRIPT" R right 0 1
	grep -q '@ action neighboring_window right' "$KITTY_LOG"
	[ ! -s "$TMUX_LOG" ]
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <lazytmux-worktree> && direnv exec . bats tests/smart-nav.bats`
Expected: FAIL — `tmux-smart-nav.sh` does not exist.

- [ ] **Step 3: Create `scripts/tmux-smart-nav.sh`**:

```sh
#!/usr/bin/env bash
# Smart tmux↔kitty navigation. Called from the C-hjkl bindings (vim-tmux-navigator
# style). At a tmux edge inside kitty, hand focus to the neighbouring kitty window
# (e.g. the aeye carousel); otherwise move within tmux. Self-gates on
# KITTY_LISTEN_ON so non-kitty / tmux-split users get plain select-pane.
#   args: <select-pane-flag L|D|U|R> <kitty-dir left|down|up|right> <zoomed 0|1> <at_edge 0|1>
set -u
flag=$1 dir=$2 zoomed=$3 edge=$4
[ "$zoomed" = 1 ] && exit 0
if [ "$edge" = 1 ] && [ -n "${KITTY_LISTEN_ON:-}" ] && command -v kitty >/dev/null 2>&1; then
	kitty @ action neighboring_window "$dir" 2>/dev/null && exit 0
fi
tmux select-pane -"$flag"
```

- [ ] **Step 4: Register the script** — add `"tmux-smart-nav"` to the `scriptNames` list in `config/tmux.conf.nix` (after `"tmux-window-nav"`, line 237):

```nix
    "tmux-window-nav"
    "tmux-smart-nav"
    "tmux-reconcile-window"
```

- [ ] **Step 5: Run the test + shellcheck**

Run: `direnv exec . bats tests/smart-nav.bats && shellcheck scripts/tmux-smart-nav.sh`
Expected: all tests PASS; shellcheck clean.

- [ ] **Step 6: Commit**

```bash
git add scripts/tmux-smart-nav.sh tests/smart-nav.bats config/tmux.conf.nix
git commit -m "feat(tmux): tmux-smart-nav helper for edge handoff to kitty (#103)"
```

### Task B2: rewrite the C-hjkl bindings to use the helper

**Files:**
- Modify: `config/tmux.conf.nix:560-563` — the four `bind-key -n C-{h,j,k,l}` lines.

- [ ] **Step 1: Replace the four bindings** (currently `… "if-shell '[ #{window_zoomed_flag} -eq 0 ]' 'select-pane -X'"`) with helper calls:

```
    bind-key -n C-h if-shell "$is_vim" "send-keys C-h" "run-shell 'tmux-smart-nav L left #{window_zoomed_flag} #{pane_at_left}'"
    bind-key -n C-j if-shell "$is_vim" "send-keys C-j" "run-shell 'tmux-smart-nav D down #{window_zoomed_flag} #{pane_at_bottom}'"
    bind-key -n C-k if-shell "$is_vim" "send-keys C-k" "run-shell 'tmux-smart-nav U up #{window_zoomed_flag} #{pane_at_top}'"
    bind-key -n C-l if-shell "$is_vim" "send-keys C-l" "run-shell 'tmux-smart-nav R right #{window_zoomed_flag} #{pane_at_right}'"
```

- [ ] **Step 2: Build the flake to catch eval/syntax errors**

Run: `cd <lazytmux-worktree> && direnv exec . nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').<...>` — or simply the repo's check target. Concretely:

Run: `direnv exec . nix flake check`
Expected: evaluates and builds without error (the generated `tmux.conf` includes the new bindings; the packaged `tmux-smart-nav` resolves).

- [ ] **Step 3: Smoke-test the generated tmux.conf** contains the new binding (not the old `select-pane -R` inline):

Run: `direnv exec . nix build .#packages.$(nix eval --raw --impure --expr builtins.currentSystem).default 2>/dev/null; grep -r "tmux-smart-nav R right" result/ 2>/dev/null || echo "check the built tmux.conf path"`
Expected: the binding string appears in the built config. (If the package layout makes this awkward, defer to the Phase C live check.)

- [ ] **Step 4: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(tmux): C-hjkl hands off to the kitty carousel at the pane edge (#103)"
```

- [ ] **Step 5: Push lazytmux branch + open PR** (spec + plan + both tasks are on this branch)

```bash
git push -u origin feat/kitty-pane-mode-polish
gh pr create --assignee @me --title "feat(tmux): seamless ctrl+hjkl into the kitty carousel (#103)" \
	--body "tmux side of noamsto/aeye#103. Spec + plan in docs/superpowers/."
```

---

## Phase C — nix-config (integration + end-to-end)

### Task C1: bump flake inputs, rebuild, verify live

**Files:**
- Modify: `flake.lock` (via `nix flake update` of the aeye + lazytmux inputs) — or pin to the feature branches for testing before the PRs merge.

- [ ] **Step 1: Point the inputs at the feature branches** (temporary, for live testing before merge). In `nix-config` flake inputs for `aeye` and `lazytmux`, set the branch refs, then:

```bash
cd <nix-config-worktree>
nix flake update aeye lazytmux
```

(After the aeye + lazytmux PRs merge, re-point to the default branch and `nix flake update` again.)

- [ ] **Step 2: Build Home Manager**

Run: `direnv exec . nh home build` (or the repo's justfile build target)
Expected: builds clean — no kitty config changes are needed (the viewer + tmux helper carry the feature), so only the input bumps move.

- [ ] **Step 3: Switch + reload tmux**

```bash
nh home switch
tmux source-file ~/.config/tmux/tmux.conf   # or restart tmux/kitty per the deslop-restart caveat
```

- [ ] **Step 4: Run the manual verification checklist** (below).

- [ ] **Step 5: Commit**

```bash
git add flake.lock
git commit -m "chore(flake): bump aeye + lazytmux for carousel seamless nav"
```

---

## Manual verification checklist

Live behavior that can't be unit-tested (kitty-pane mode active: `AEYE_HOST=kitty`, in tmux, inside kitty):

- [ ] **Launch-hidden:** while focused on tmux window B, trigger a diagram capture in window A (e.g. have Claude write a `.d2` in A's pane). The carousel must **not** appear over B.
- [ ] **Reveal on focus:** switch to window A → the carousel appears as a vsplit beside it.
- [ ] **Open never steals focus:** when the carousel opens (auto or `prefix+I`), the cursor/focus stays on the tmux pane — keystrokes still go to tmux, not the carousel.
- [ ] **Reveal never steals focus:** when a stashed carousel is revealed on focus change, focus stays on the tmux pane (not the carousel window).
- [ ] **Stay-hidden idempotence:** switch back to B → carousel hides again; a second capture in A does not pop it over B.
- [ ] **tmux→carousel:** from the Claude pane (right edge), `Ctrl-l` moves focus into the carousel window.
- [ ] **carousel→tmux:** in the carousel, `Ctrl-h` moves focus back to the Claude pane.
- [ ] **No regression:** intra-tmux `Ctrl-hjkl` between ordinary panes still works; in a non-kitty terminal (or tmux-split mode) `Ctrl-hjkl` behaves exactly as before.
- [ ] **Manual toggle unaffected:** `prefix+I` from the Claude pane still opens the carousel visible immediately.

---

## Self-review

- **Spec coverage:** Part 1 (launch-hidden) → A1; Part 1 focus invariant (launch keep-focus + reveal host-window focus) → A1 asserts + A1b; Part 2 tmux side → B1+B2; Part 2 viewer side → A2; README/docs → A3; flake integration → C1. Testing section → A1/A1b bats, A2 go test, B1 bats, manual checklist. All covered.
- **Type/name consistency:** `_key_on_screen` (A1), `neighborForKey`/`kittyNeighbor` (A2), `tmux-smart-nav` arg order `<flag> <dir> <zoomed> <edge>` (B1) match their call sites (A1 launch branch, A2 Update cases, B2 bindings).
- **No placeholders:** every code step carries complete code; commands have expected output.
- **Ordering:** Phase C depends on A + B (flake bump); A and B are independent and can run in parallel.
