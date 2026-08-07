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
	// Isolates this test's tmux server on its own default socket (no -L
	// needed on either side: tmux resolves the default socket under
	// TMUX_TMPDIR) so it can never reach the developer's real interactive
	// server or its panes, and paneAlive — which always shells out to plain
	// "tmux" — talks to the same isolated server without needing its own
	// socket flag.
	t.Setenv("TMUX_TMPDIR", t.TempDir())

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
