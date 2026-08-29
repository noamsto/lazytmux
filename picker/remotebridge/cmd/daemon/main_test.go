package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReflowRunShellArgsSurvivesFormatInjection exercises a #(...)-bearing
// local session name through a real tmux run-shell call. run-shell
// format-expands its whole shell-command string before /bin/sh sees it, so
// concatenating shellQuote(reflowBin)+" --force "+shellQuote(localSess) (the
// pre-#368 construction) let tmux's format scanner consume the embedded
// "#(touch marker)" as a job — the recorder script received "zzx", not the
// literal session name. The argv-equality check below is the load-bearing
// assertion: it deterministically fails against that construction. The
// marker-absence check is best-effort only — run-shell's #(...) job spawns
// asynchronously, so it isn't a reliable pre-fix/post-fix discriminator on
// its own.
func TestReflowRunShellArgsSurvivesFormatInjection(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		// LAZYTMUX_REQUIRE_TMUX is set by pickerChecked's checkPhase in flake.nix,
		// which also adds pkgs.tmux to nativeBuildInputs — so under `nix flake
		// check` a missing tmux means that input was pruned, not that this is a
		// dev machine. Fail instead of silently skipping this regression check.
		if os.Getenv("LAZYTMUX_REQUIRE_TMUX") != "" {
			t.Fatal("tmux is required (LAZYTMUX_REQUIRE_TMUX set) but not on PATH — check pickerChecked's nativeBuildInputs in flake.nix")
		}
		t.Skip("tmux is not available")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "injected")
	record := filepath.Join(dir, "record")

	// Stands in for tmux-reflow-windows: records the argv it was invoked
	// with so the test can assert the malicious session name arrived intact.
	script := filepath.Join(dir, "record.sh")
	scriptBody := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(record) + "\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	maliciousSess := "zz#(touch " + marker + ")x"

	socket := "daemon-368-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	if out, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", "t1").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	args := reflowRunShellArgs(script, maliciousSess)
	if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("run-shell: %v: %s", err, out)
	}

	// run-shell's job is asynchronous and this waits on a real tmux server, so
	// the budget is a stall detector, not a race to beat: a parallel `go test
	// ./...` on a loaded builder starves it well past 3s (the recorder then
	// never appears and the failure reads as a broken fix, not a slow one).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(record); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("recorder script never ran: %v", err)
	}
	want := "--force\n" + maliciousSess + "\n"
	if string(got) != want {
		t.Fatalf("recorder argv = %q, want %q", got, want)
	}

	// Best-effort: see the doc comment above on why this isn't the
	// discriminating assertion.
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("embedded #(...) job executed — format-layer injection not blocked")
	}
}
