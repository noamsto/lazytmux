# agent-detect Watcher Leak Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `agent-detect` watcher processes from surviving their pane's death (#239), and stop a re-armed watcher from leaving its predecessor running alongside it.

**Architecture:** tmux's own `pipe-pane` teardown is not a reliable exit signal for the watcher (that is the root leak: EOF never reliably arrives — see "Why we don't chase the exact tmux mechanism" below). So instead of depending on EOF alone, `picker/agentdetect/main.go` gains two independent, cheap exit checks layered onto the existing `select` loop:

1. **Ownership check** (fixes the re-arm leak): every watcher claims its pane in a tiny on-disk registry (`/tmp/claude-status/watchers/<paneID>`) at startup. A re-arm is a *new* process for the same pane, so its registration overwrites the file. The old process notices the mismatch on its existing 40ms debounce ticker (a local file read — no fork, so riding the hot ticker is free) and exits **without deleting the registry file** — the file it would be deleting is by then the *new* watcher's registration, and deleting it would make the new watcher spuriously fail its own next `stillOwner` check and self-evict. Leaving stale registrations behind is intentional here, exactly the way the existing `panes/`, `screen/`, `issues/`, `tasks/`, `names/`, `interrupt/` directories already do — which is why this needs the next point.
2. **Pane-liveness backstop** (fixes the pane-death leak, and is the guaranteed convergence path regardless of what's actually wrong with EOF): a new coarse 30s ticker asks tmux directly (`tmux capture-pane -p -t %pane`) whether the pane still exists, and exits if not — including when tmux itself is unreachable. Deliberately not `tmux display -t %pane`: `display-message`'s target lookup is declared `CMD_FIND_CANFAIL` in tmux's own source (`cmd-display-message.c`), so it resolves a missing target silently and still exits 0 against a dead pane — verified empirically against tmux 3.7b (the exact binary this repo wraps): `tmux display -p -F "" -t %<killed-pane>` returns exit 0 with no error. `capture-pane`'s target flag is not CANFAIL (`cmd-capture-pane.c`), so it correctly exits 1 ("can't find pane") against the same dead pane — verified the same way, and already the pattern this file uses for `seededScreen`.
3. **Registry pruning** (bounds the registry's own growth): a normal EOF exit doesn't delete its registration either — no exit path does, by design (point 1) — so every pane that ever ran an agent leaves a permanent file in `/tmp/claude-status/watchers`. `scripts/lib-claude.sh`'s `claude_prune_stale_state` is this codebase's existing, already-wired mechanism for exactly this: it mtime-sweeps every pane-id-keyed directory against the current tmux server's start time and drops anything older, once per server (re)start. `watchers/` gets added to its directory list alongside the six it already prunes.

Both watcher-side checks emit the same final-snapshot-then-return the EOF path already does, and because Go's `select` only runs one case per loop iteration, there is no way for two exit paths to race or double-emit.

**Tech Stack:** Go 1.25 (`picker` module), `go test`, `nix build`/`nix flake check`; one small Bash addition to `scripts/lib-claude.sh`.

---

## Why we don't chase the exact tmux mechanism

The issue asks to fix the actual mechanism *if it can be identified*, but is explicit that the backstop is required regardless because it's "the only thing that guarantees convergence." `picker/agentdetect/main.go`'s read/close path is already correct by inspection: `readStdin` blocks on `bufio.Reader.Read`, and on any error (including EOF) calls `buf.Close()` and returns; `drainbuf.Buffer.Close` sets `closed` and pulses `Notify()`; the main loop's `buf.Notify()` case calls `buf.Take()`, sees `closed`, emits, and returns. There's no goroutine leak, no unreceived channel send, and `statefile.Writer.Update` does a plain non-blocking file write. The failure to observe EOF is therefore on tmux's side of the pipe (its `pipe-pane` teardown), which this repo does not patch — it wraps stock tmux via Nix. So this plan does not attempt a C-level tmux fix; it makes the Go watcher self-sufficient instead, which is both the required backstop and, by construction, also the fix for the re-arm case (a documented instance of the same "close doesn't reach the child" behavior).

This keeps the *arming* side of the change out of the picture entirely — no changes to `config/tmux.conf.nix` or `scripts/tmux-update-icons.sh` are needed, since the registry is populated by the watcher itself at every startup (which happens once per `pipe-pane` arm call already). It does not, however, keep the change entirely inside `picker/agentdetect/**`: the registry directory this plan introduces is a new instance of the same pane-id-keyed-file shape `claude_prune_stale_state` (`scripts/lib-claude.sh`) already exists to garbage-collect, so that function's directory list needs the one-line addition described above — otherwise the registry itself becomes an unbounded-file-count leak, just with a longer fuse than the process leak this PR fixes.

---

## File Structure

- Modify: `picker/agentdetect/main.go` — add the registry (`registerWatcher`, `stillOwner`, `ownerMatches`), the liveness probe (`paneAlive`, `aliveFromProbe`), two new constants, and wire both into `main()`'s loop.
- Create: `picker/agentdetect/main_test.go` — unit tests for the four pure/local-I/O functions above, plus one test that shells out to a real scratch tmux session to exercise `paneAlive` itself rather than only the pure `aliveFromProbe` decision it wraps (`package main`, matching how other `agentdetect` files are tested in-package, e.g. `drainbuf/drainbuf_test.go`, `statefile/statefile_test.go`).
- Modify: `scripts/lib-claude.sh` — add a `CLAUDE_WATCHERS_DIR` constant and include it in `claude_prune_stale_state`'s directory list, so the registry directory is pruned on tmux server restart the same way `panes/`, `screen/`, `issues/`, `tasks/`, `names/`, `interrupt/` already are.
- Modify: `CLAUDE.md` — document the new `watchers/<pane_id>` directory in Key Conventions, matching the style of the existing per-directory bullets.

No other files change.

---

### Task 1: Write failing tests for the new decision functions

**Files:**
- Create: `picker/agentdetect/main_test.go`

- [ ] **Step 1: Write the test file**

```go
package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestAliveFromProbe(t *testing.T) {
	if !aliveFromProbe(nil) {
		t.Error("nil probe error should mean alive")
	}
	if aliveFromProbe(errors.New("can't find pane")) {
		t.Error("any probe error should mean gone, including an unreachable tmux server")
	}
}

// TestPaneAliveRealTmux exercises paneAlive's actual shell-out, not just the
// pure aliveFromProbe decision it wraps. This is deliberate: an earlier draft
// of paneAlive shelled out to `tmux display -t %pane`, whose target lookup is
// CMD_FIND_CANFAIL in tmux's own source (cmd-display-message.c) — it resolves
// a missing pane silently and still exits 0, so aliveFromProbe(nil) reads a
// dead pane as alive and the whole liveness backstop this task exists to add
// would never fire on ordinary pane death. aliveFromProbe alone can't catch
// that class of bug; only actually invoking the tmux command can.
func TestPaneAliveRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	session := "agentdetect-test-" + strconv.Itoa(os.Getpid())
	if err := exec.Command("tmux", "new-session", "-d", "-s", session, "-x", "80", "-y", "24").Run(); err != nil {
		t.Skipf("could not start a scratch tmux session: %v", err)
	}
	defer exec.Command("tmux", "kill-session", "-t", session).Run()

	out, err := exec.Command("tmux", "list-panes", "-t", session, "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	paneID := strings.TrimPrefix(strings.TrimSpace(string(out)), "%")

	if !paneAlive(paneID) {
		t.Fatalf("paneAlive(%q) = false for a live scratch pane", paneID)
	}

	if err := exec.Command("tmux", "kill-session", "-t", session).Run(); err != nil {
		t.Fatalf("kill-session: %v", err)
	}

	if paneAlive(paneID) {
		t.Fatalf("paneAlive(%q) = true for a pane whose session was just killed (this is exactly the CANFAIL false-positive #239's review caught — paneAlive must not shell out to a tmux subcommand whose target resolution silently tolerates a missing target)", paneID)
	}
}

func TestOwnerMatches(t *testing.T) {
	cases := []struct {
		name       string
		registered string
		pid        int
		want       bool
	}{
		{"exact match", "1234", 1234, true},
		{"trailing newline", "1234\n", 1234, true},
		{"different pid", "5678", 1234, false},
		{"empty registry", "", 1234, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ownerMatches(c.registered, c.pid); got != c.want {
				t.Errorf("ownerMatches(%q, %d) = %v, want %v", c.registered, c.pid, got, c.want)
			}
		})
	}
}

func TestRegisterWatcherAndStillOwner(t *testing.T) {
	dir := t.TempDir()

	if !registerWatcher(dir, "42", 100) {
		t.Fatal("registerWatcher should succeed against a writable temp dir")
	}
	if !stillOwner(dir, "42", 100) {
		t.Error("the pid that just registered should still be the owner")
	}
	if stillOwner(dir, "42", 999) {
		t.Error("a different pid must not read as the owner")
	}

	// A later registration (simulating a re-arm) supersedes the first —
	// this is the exact scenario that leaked in #239: old watcher still
	// running, new watcher starts for the same pane.
	if !registerWatcher(dir, "42", 200) {
		t.Fatal("re-registration should succeed")
	}
	if stillOwner(dir, "42", 100) {
		t.Error("the superseded pid must no longer be the owner")
	}
	if !stillOwner(dir, "42", 200) {
		t.Error("the new pid should be the owner after re-registration")
	}
}

func TestStillOwnerMissingRegistry(t *testing.T) {
	dir := t.TempDir()
	if stillOwner(dir, "no-such-pane", 100) {
		t.Error("a pane with no registry entry must not read as owned")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run (from the `picker` module directory):
```bash
cd picker
go test ./agentdetect/... -run 'TestAliveFromProbe|TestPaneAliveRealTmux|TestOwnerMatches|TestRegisterWatcherAndStillOwner|TestStillOwnerMissingRegistry' -v
```
Expected: build failure — `undefined: aliveFromProbe`, `undefined: paneAlive`, `undefined: ownerMatches`, `undefined: registerWatcher`, `undefined: stillOwner`.

- [ ] **Step 3: Commit the test file**

```bash
git add picker/agentdetect/main_test.go
git commit -m "test(agentdetect): add coverage for watcher-registry and liveness decisions"
```

---

### Task 2: Implement the registry and liveness decision functions

**Files:**
- Modify: `picker/agentdetect/main.go`

- [ ] **Step 1: Add the new import**

`main.go` currently imports (in order): `"bufio"`, `"os"`, `"os/exec"`, `"strconv"`, `"strings"`, `"time"`, plus the four local packages. Add `"path/filepath"` alongside the standard-library imports:

```go
import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/agentdetect/debounce"
	"github.com/noamsto/lazytmux/picker/agentdetect/drainbuf"
	"github.com/noamsto/lazytmux/picker/agentdetect/manifest"
	"github.com/noamsto/lazytmux/picker/agentdetect/screen"
	"github.com/noamsto/lazytmux/picker/agentdetect/statefile"
)
```

- [ ] **Step 2: Add the two new constants**

Add to the existing `const` block, after `maxBufferedBytes`:

```go
	// Leak-reaper interval (#239): coarse because it forks `tmux capture-pane`.
	// Anything faster would fork per tick across every live watcher for no
	// benefit — a dead pane isn't time-sensitive the way animation is; tens
	// of seconds is fine for a backstop that only exists to bound the worst
	// case.
	livenessInterval = 30 * time.Second
	watcherRegDir     = "/tmp/claude-status/watchers"
```

- [ ] **Step 3: Add the four functions**

Add near the bottom of the file, after `paneInfo`:

```go
// registerWatcher atomically claims paneID for pid, replacing whatever a
// previous watcher wrote. Returns false only if the filesystem itself is
// unusable (can't mkdir/write/rename) — in that case the caller can't
// guarantee it's the sole watcher for this pane, so it should not become a
// long-lived process that might duplicate one (#239).
func registerWatcher(dir, paneID string, pid int) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	final := filepath.Join(dir, paneID)
	tmp := final + "." + strconv.Itoa(pid) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return false
	}
	return os.Rename(tmp, final) == nil
}

// stillOwner reports whether pid is still the registered watcher for paneID.
// A re-arm overwrites the registry with the new watcher's pid; once that
// happens the old process reads a mismatch here and exits, which is what
// closes the re-arm leak (#239) without any tmux-side changes.
func stillOwner(dir, paneID string, pid int) bool {
	content, err := os.ReadFile(filepath.Join(dir, paneID))
	if err != nil {
		return false
	}
	return ownerMatches(string(content), pid)
}

// ownerMatches is the pure comparison stillOwner delegates to, split out so
// the decision is testable without touching the filesystem.
func ownerMatches(registered string, pid int) bool {
	return strings.TrimSpace(registered) == strconv.Itoa(pid)
}

// paneAlive reports whether the pane still exists, by asking tmux directly
// rather than trusting any cached state. Deliberately not `tmux display-message
// -t <pane>`: display-message's target lookup is declared CMD_FIND_CANFAIL in
// tmux's own source (cmd-display-message.c), so it tolerates a missing target
// and exits 0 even against a dead pane — verified empirically against tmux
// 3.7b, the exact binary this repo wraps. capture-pane's target flag is not
// CANFAIL (cmd-capture-pane.c), so it correctly errors "can't find pane" and
// exits nonzero on a dead one; it's also the pattern seededScreen already uses
// elsewhere in this file, so no new probing style is introduced.
func paneAlive(paneID string) bool {
	err := exec.Command("tmux", "capture-pane", "-p", "-t", "%"+paneID).Run()
	return aliveFromProbe(err)
}

// aliveFromProbe is the pure decision behind the leak-reaper backstop: a nil
// error means the pane answered, anything else — pane gone, session gone, or
// tmux server unreachable entirely — means exit. An unreachable tmux must
// never be read as "assume alive" (#239): a dead server can't tell us
// otherwise, so silence has to mean gone.
func aliveFromProbe(err error) bool { return err == nil }
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd picker
go test ./agentdetect/... -run 'TestAliveFromProbe|TestPaneAliveRealTmux|TestOwnerMatches|TestRegisterWatcherAndStillOwner|TestStillOwnerMissingRegistry' -v
```
Expected: `PASS` for all five tests (`TestOwnerMatches` reports per-subtest; `TestPaneAliveRealTmux` reports `SKIP` instead of `PASS` if `tmux` isn't on `PATH` in this environment — that's fine, but if it does run, it must `PASS`, not silently no-op).

- [ ] **Step 5: Format and vet**

The constant block and code above were hand-typed for readability in this plan; run `gofmt -w` to make the actual file canonical (it may reformat the `const` block's alignment), then confirm.

```bash
cd picker
gofmt -w agentdetect/main.go
gofmt -l agentdetect/main.go
go vet ./agentdetect/...
```
Expected: `gofmt -l` prints nothing after the `-w` pass; `go vet` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add picker/agentdetect/main.go
git commit -m "feat(agentdetect): add watcher-registry and pane-liveness decision helpers"
```

---

### Task 3: Wire the two checks into main()'s loop

**Files:**
- Modify: `picker/agentdetect/main.go`

- [ ] **Step 1: Replace `main()`**

Current `main()`:

```go
func main() {
	if len(os.Args) < 2 {
		return
	}
	paneID := os.Args[1] // already sans '%'

	cols, rows, cmd := paneInfo(paneID)
	ms, err := manifest.Load()
	if err != nil {
		return
	}
	m, ok := manifest.ForCommand(ms, cmd)
	if !ok {
		return // pane isn't running a known agent; nothing to watch
	}

	scr := seededScreen(paneID, cols, rows)
	deb := debounce.New(debounceWindow, sampleCeiling)
	w := statefile.New(stateDir, paneID)
	emit(scr, m, w) // report what is already on screen, before any new output

	buf := drainbuf.New(maxBufferedBytes)
	go readStdin(buf)

	ticker := time.NewTicker(debounceWindow / 2)
	defer ticker.Stop()

	for {
		select {
		case <-buf.Notify():
			data, truncated, closed := buf.Take()
			if truncated {
				// Dropped bytes broke VT continuity; re-seed so stale rows
				// can't linger. Seeding rather than blanking matters for an
				// agent that only ever repaints a few cells — a blank screen
				// would never be filled back in.
				scr = seededScreen(paneID, cols, rows)
			}
			if len(data) > 0 {
				scr.Feed(data)
				deb.Mark(time.Now())
			}
			if closed {
				emit(scr, m, w) // final snapshot on EOF
				return
			}
		case <-ticker.C:
			if deb.Due(time.Now()) {
				emit(scr, m, w)
			}
		}
	}
}
```

Replace with:

```go
func main() {
	if len(os.Args) < 2 {
		return
	}
	paneID := os.Args[1] // already sans '%'

	cols, rows, cmd := paneInfo(paneID)
	ms, err := manifest.Load()
	if err != nil {
		return
	}
	m, ok := manifest.ForCommand(ms, cmd)
	if !ok {
		return // pane isn't running a known agent; nothing to watch
	}

	myPID := os.Getpid()
	if !registerWatcher(watcherRegDir, paneID, myPID) {
		// Can't guarantee we're the sole watcher for this pane, so don't
		// become a long-lived process that might duplicate one (#239).
		return
	}

	scr := seededScreen(paneID, cols, rows)
	deb := debounce.New(debounceWindow, sampleCeiling)
	w := statefile.New(stateDir, paneID)
	emit(scr, m, w) // report what is already on screen, before any new output

	buf := drainbuf.New(maxBufferedBytes)
	go readStdin(buf)

	ticker := time.NewTicker(debounceWindow / 2)
	defer ticker.Stop()
	liveness := time.NewTicker(livenessInterval)
	defer liveness.Stop()

	for {
		select {
		case <-buf.Notify():
			data, truncated, closed := buf.Take()
			if truncated {
				// Dropped bytes broke VT continuity; re-seed so stale rows
				// can't linger. Seeding rather than blanking matters for an
				// agent that only ever repaints a few cells — a blank screen
				// would never be filled back in.
				scr = seededScreen(paneID, cols, rows)
			}
			if len(data) > 0 {
				scr.Feed(data)
				deb.Mark(time.Now())
			}
			if closed {
				emit(scr, m, w) // final snapshot on EOF
				return
			}
		case <-ticker.C:
			// Cheap (a local file read, no fork) so it rides the existing
			// hot ticker: catches a re-arm within one tick instead of
			// waiting on the coarse liveness probe below (#239).
			//
			// Deliberately does not remove the registry file on this exit:
			// by the time we're here it's the new watcher's registration,
			// not ours, and deleting it would make the new watcher fail
			// its own next stillOwner check and self-evict. Stale entries
			// from any exit path (this one and EOF) are swept later by
			// claude_prune_stale_state instead.
			if !stillOwner(watcherRegDir, paneID, myPID) {
				emit(scr, m, w)
				return
			}
			if deb.Due(time.Now()) {
				emit(scr, m, w)
			}
		case <-liveness.C:
			// Forking `tmux capture-pane` isn't free, so this stays on its own
			// coarse ticker — a leak reaper, not a liveness signal. This is
			// the backstop for the case EOF never arrives at all (#239):
			// dead pane, or the tmux server gone outright.
			if !paneAlive(paneID) {
				emit(scr, m, w)
				return
			}
		}
	}
}
```

Note `select` only ever runs one case per loop iteration and every exit path `return`s immediately after its `emit`, so the two new checks cannot race the EOF path or each other, and none of them can double-emit.

- [ ] **Step 2: Build**

```bash
cd picker
go build ./agentdetect/...
```
Expected: no output, exit 0.

- [ ] **Step 3: Run the full agentdetect test suite**

```bash
cd picker
go test ./agentdetect/...
```
Expected: `ok` for `agentdetect` (now has tests) and all its existing subpackages (`debounce`, `drainbuf`, `manifest`, `screen`, `statefile`).

- [ ] **Step 4: Format and vet**

```bash
cd picker
gofmt -w agentdetect/main.go
gofmt -l agentdetect/main.go
go vet ./agentdetect/...
```
Expected: `gofmt -l` and `go vet` both silent.

- [ ] **Step 5: Commit**

```bash
git add picker/agentdetect/main.go
git commit -m "fix(agentdetect): exit on pane death or supersession by a re-arm"
```

---

### Task 4: Prune the watcher registry directory

**Files:**
- Modify: `scripts/lib-claude.sh`
- Modify: `CLAUDE.md`

No exit path in Task 3 deletes a pane's registry entry — not even the ordinary EOF exit — because a delete on the superseded watcher's exit would race the new watcher's own registration (see the Architecture section above). That means every pane that ever ran an agent leaves its registry file behind forever unless something else prunes it. `claude_prune_stale_state` already exists for exactly this shape of file; this task adds `watchers/` to it.

- [ ] **Step 1: Add the `CLAUDE_WATCHERS_DIR` constant**

In `scripts/lib-claude.sh`, add alongside the other `CLAUDE_STATUS_DIR`-relative constants near the top of the file:

```bash
CLAUDE_WATCHERS_DIR="$CLAUDE_STATUS_DIR/watchers"
```

Place it after `CLAUDE_INTERRUPT_DIR="$CLAUDE_STATUS_DIR/interrupt"` (the last of that group).

- [ ] **Step 2: Add it to `claude_prune_stale_state`'s directory list**

In the same file, extend the `for dir in ...` loop inside `claude_prune_stale_state`:

```bash
for dir in "$CLAUDE_PANES_DIR" "$CLAUDE_SCREEN_DIR" "$CLAUDE_ISSUES_DIR" "$CLAUDE_TASKS_DIR" "$CLAUDE_NAMES_DIR" "$CLAUDE_INTERRUPT_DIR" "$CLAUDE_WATCHERS_DIR"; do
```

This directory holds only `<pane_id>` files (one PID per line, written by `registerWatcher` in `picker/agentdetect/main.go`), so it fits the loop's existing `[[ -f $f ]]` / mtime-vs-`server_start` sweep with no special-casing.

- [ ] **Step 3: Document the new directory in CLAUDE.md**

Add a bullet to the Key Conventions section of `CLAUDE.md`, next to the existing `**Issue self-report files**` / `**Task self-report files**` / `**Name self-report files**` bullets:

```markdown
- **Watcher registry files** at `/tmp/claude-status/watchers/<pane_id>`: one line holding the PID of the `agent-detect` process currently watching that pane, written by `registerWatcher` (`picker/agentdetect/main.go`) at every watcher startup, including re-arms. A re-arm's registration supersedes the prior watcher's; the superseded watcher notices the mismatch on its own debounce ticker and exits, but never deletes the file itself — deleting would remove the *new* watcher's registration and cause it to spuriously self-evict on its own next ownership check. Pruned by `claude_prune_stale_state` like the other pane-id-keyed directories.
```

- [ ] **Step 4: Lint the shell change**

```bash
shellcheck scripts/lib-claude.sh
shfmt -d scripts/lib-claude.sh
```

Expected: both silent. (`nix flake check` in Task 5 also covers this via the pre-commit hooks, but checking it now catches a typo before the full flake run.)

- [ ] **Step 5: Commit**

```bash
git add scripts/lib-claude.sh CLAUDE.md
git commit -m "fix(claude-status): prune the agent-detect watcher registry on server restart"
```

---

### Task 5: Full flake verification

**Files:** none (verification only)

- [ ] **Step 1: Build the default package**

```bash
nix build .#default
```
Expected: succeeds, produces `./result`.

- [ ] **Step 2: Run all flake checks**

```bash
nix flake check
```
Expected: succeeds (this exercises `go test ./agentdetect/...` inside `pickerChecked`'s `checkPhase`, per `flake.nix`, plus every other check — Nix lints, shellcheck/shfmt, `tests/enrich.bats`, etc.). `TestPaneAliveRealTmux` is expected to report `SKIP` here specifically, not `PASS` — the sandboxed `checkPhase` has no `tmux` on `PATH` (`picker/default.nix` doesn't list it as a build input), so this is where its `exec.LookPath` skip exists for. It still runs for real in Task 1/2's local `go test` and gets exercised end-to-end manually in Task 6.

If either command fails, read the error output before changing anything further — do not guess at a fix blind.

---

### Task 6: Manual verification against a live tmux server

**Files:** none (manual verification only)

This step is optional if there is no live, shared tmux server available to test against safely, but do it if there is one — it's the only way to directly confirm the fix against the real leak, not just the unit tests. **Only touch a scratch pane you create yourself; do not kill or pipe any pane you didn't create — other agents may be working in this tmux server.**

- [ ] **Step 1: Rebuild and reload the wrapped tmux**

```bash
nix build .#default
```
Then, inside a running tmux session, reload config with `prefix + r` (sources `~/.config/tmux/tmux.conf`, which is symlinked to the newly built config by the home-manager module — a plain `nix build` alone does not update the live binary until the home-manager activation or an explicit re-link happens; if this repo isn't the one driving the live `~/.config/tmux/tmux.conf` symlink, skip to Step 2 and test the built binary directly instead).

- [ ] **Step 2: Create a scratch pane running an agent command and confirm a watcher arms**

```bash
tmux new-window -n agent-detect-leak-test
tmux send-keys -t agent-detect-leak-test 'claude' Enter
sleep 6
tmux list-panes -a -F '#{pane_id} #{pane_current_command} #{pane_pipe}' | grep agent-detect-leak-test
```
(The window name filter above is illustrative — adjust to however you locate the pane id tmux assigned; `agent-detect` arms on a 5-tick sweep, so allow a few seconds.)

Note the pane id, then confirm a watcher process exists for it:
```bash
pgrep -af agent-detect
```
Expected: one `agent-detect <paneID>` line for the pane id you just created.

- [ ] **Step 3: Kill the scratch pane and confirm the watcher exits within ~30s**

```bash
tmux kill-window -t agent-detect-leak-test
sleep 32
pgrep -af agent-detect
```
Expected: no line for the pane id from Step 2 (the liveness backstop caught it within one `livenessInterval` tick).

- [ ] **Step 4: (Optional) exercise the re-arm path**

Create another scratch pane the same way, confirm a watcher arms (Step 2), then manually clear and re-establish its pipe:
```bash
tmux pipe-pane -t <paneID>
sleep 1
tmux pipe-pane -o -t <paneID> "$(tmux show-options -gv @agent_detect_bin 2>/dev/null || echo agent-detect) <paneID-without-percent>"
```
If you don't have `@agent_detect_bin` handy, simplest is to just wait for the next automatic 5-tick arm sweep after clearing the pipe, since `pane_pipe` will read `0` and `arm_agent_detect` re-arms it itself.
```bash
pgrep -af "agent-detect <paneID-without-percent>"
```
Expected: exactly one process for that pane id (not two) within ~40ms of the new one starting — the ownership check on the hot ticker should have already killed the old one by the time you check.

- [ ] **Step 5: Clean up**

```bash
tmux kill-window -t <any-remaining-scratch-window>
```

---

### Task 7: Wrap up

**Files:**
- Delete (not commit): `WORKER_TASK.md` — this is a dispatcher artifact for this run, not part of the change.

- [ ] **Step 1: Confirm the tree is clean apart from intended files**

```bash
git status
```
Expected: `picker/agentdetect/main.go`, `picker/agentdetect/main_test.go`, `scripts/lib-claude.sh`, and `CLAUDE.md` are the only tracked changes (already committed across Tasks 1–4); `WORKER_TASK.md` and `docs/superpowers/plans/2026-08-07-agent-detect-watcher-leak.md` are untracked/plan artifacts, not part of the PR diff.

- [ ] **Step 2: Push and open the PR**

```bash
git push -u origin feat/239-fix-agent-detect-watcher-processes-leak
```

```bash
gh pr create --assignee @me --title "fix(agentdetect): watcher processes leak after their pane dies" --body "$(cat <<'EOF'
## Summary
- `agent-detect` watchers no longer depend solely on stdin EOF (which tmux's `pipe-pane` teardown does not reliably deliver) to exit.
- Each watcher registers itself for its pane at startup in `/tmp/claude-status/watchers/<paneID>`; a re-armed watcher's registration supersedes the old one, and the old process notices the mismatch on its existing 40ms ticker (no extra fork) and exits — this closes the re-arm leak. Neither this exit nor the ordinary EOF exit deletes the registry file (deleting on the superseded-watcher path would delete the *new* watcher's registration instead), so the registry itself is pruned the same way the other pane-id-keyed status directories already are: `claude_prune_stale_state` now also sweeps `watchers/` on tmux server restart.
- A new coarse 30s ticker independently confirms the pane still exists via `tmux capture-pane`, and exits (including when tmux itself is unreachable) if not — this is the required backstop that guarantees convergence regardless of the exact tmux-side mechanism. (Not `tmux display-message`: its target lookup is CMD_FIND_CANFAIL in tmux's own source, so it silently reports a dead pane as alive — verified against tmux 3.7b before landing on `capture-pane`, whose target flag is not CANFAIL.)

## Design choice: watcher self-detects supersession, not the arming script
Considered killing the previous watcher from the arming side (`tmux-update-icons.sh`'s `arm_agent_detect`), e.g. via `pkill -f`. Chose to have the watcher self-register instead: it needs no new external tool dependency (no `pkill`/`pgrep` in the wrapped tmux's PATH), needs no changes to the arming script itself (registration happens once per process startup, which already happens once per arm), and is unit-testable as pure logic. Smaller and more robust than teaching the arming script to track and kill PIDs. The one shell-side change this PR does make is unrelated to arming: wiring the new registry directory into the existing `claude_prune_stale_state` GC path, the same way every other pane-id-keyed status directory already is.

Closes #239

## Test plan
- [x] `go test ./agentdetect/...` — new unit tests for `aliveFromProbe`, `ownerMatches`, `registerWatcher`, `stillOwner`, including a re-registration test that reproduces the exact re-arm race; plus `TestPaneAliveRealTmux`, which shells out to a real scratch tmux session/pane and asserts `paneAlive` returns `false` after it's killed — catching the CANFAIL false-positive class of bug directly, not just the pure decision function.
- [x] `shellcheck`/`shfmt` on `scripts/lib-claude.sh`
- [x] `nix build .#default`
- [x] `nix flake check`
- [ ] Manual verification against a live tmux server (scratch pane created/killed by the author only)
EOF
)"
```

- [ ] **Step 3: Report the PR URL**

The `gh pr create` command prints the PR URL on success — surface it back to whoever is waiting on this task.

---

## Self-Review Notes

- **Spec coverage:** pane-death leak → Task 3's `liveness.C` case, backed by `paneAlive` shelling out to `tmux capture-pane` rather than `tmux display-message` — the latter's CANFAIL target resolution would have silently defeated this exact backstop (verified empirically against tmux 3.7b, and re-verified automatically by `TestPaneAliveRealTmux` in Task 1, not just the pure `aliveFromProbe` unit tests); re-arm leak → Task 3's `stillOwner` check on `ticker.C`; "must not fork per tick at a hot rate" → the fork-based check (`paneAlive`) is on the coarse 30s ticker, the hot 40ms ticker only does a local file read; "must not race the normal EOF exit path or double-emit" → addressed structurally by `select`'s one-case-per-iteration + immediate `return` after every `emit`; "must handle tmux unreachable → exit, not assume alive" → `aliveFromProbe` treats any non-nil error (including a dead server) as gone; the registry file itself must not accumulate unboundedly → Task 4 wires `watchers/` into `claude_prune_stale_state`, and the superseded-watcher exit path deliberately never deletes its own registry entry (Task 3) since that entry is by then the new watcher's, not its own; unit tests for pure logic → Task 1; `nix build`/`nix flake check` → Task 5; manual live-server verification without disturbing other panes → Task 6, explicitly scoped to self-created scratch panes; PR body explaining the arming-vs-watcher design choice → Task 7.
- **Placeholder scan:** no TBD/TODO markers; every step has literal commands or literal code.
- **Type/name consistency:** `registerWatcher(dir, paneID string, pid int) bool`, `stillOwner(dir, paneID string, pid int) bool`, `ownerMatches(registered string, pid int) bool`, `paneAlive(paneID string) bool`, `aliveFromProbe(err error) bool`, `livenessInterval`, `watcherRegDir`, `TestPaneAliveRealTmux` — used identically in Task 1's tests, Task 2's implementation, and Task 3's `main()` wiring.
