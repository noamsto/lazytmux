package main

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// The keepalive options are the whole of #471: without them ssh never notices a
// host that died without closing the connection, so the control stream never
// reaches EOF and the daemon never runs the teardown it already has. Nothing
// else in the suite would fail if they were dropped, hence this test.
func TestSSHControlArgsCarryKeepalives(t *testing.T) {
	args := sshControlArgs("/tmp/ctl.sock", "tp-g6", "/run/user/1000", "xterm-kitty", "lazytmux",
		[]string{"tmux"})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"ServerAliveInterval=15",
		"ServerAliveCountMax=4",
		"ControlMaster=auto",
		"ControlPath=/tmp/ctl.sock",
		"ControlPersist=no",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}

	// The keepalive options must precede the host: ssh stops parsing options at
	// the first non-option argument, so one placed after it would be handed to
	// the remote shell as a command word instead.
	hostAt := slices.Index(args, "tp-g6")
	if hostAt < 0 {
		t.Fatalf("host missing from %q", joined)
	}
	for i, a := range args {
		if strings.HasPrefix(a, "ServerAlive") && i > hostAt {
			t.Errorf("%s sits after the host at %d; ssh would pass it to the remote", a, hostAt)
		}
	}

	// The session is the attach target and must stay one token even with spaces.
	if got := args[len(args)-1]; got != shellQuote("lazytmux") {
		t.Errorf("last arg = %q, want the shell-quoted session", got)
	}
}

// TestTransportStartAfterStopEndsTheChild makes transport.start's race
// deterministic: a stop that lands before a child is published must still end
// it, or the ssh child outlives the daemon holding the ControlMaster open.
func TestTransportStartAfterStopEndsTheChild(t *testing.T) {
	requireSleep(t)
	tr := &transport{}
	tr.stop() // the handler fires with nothing yet published

	c := startChild(t, tr, exec.Command("sleep", "60"))
	waitEnded(t, c, "a transport started after stop was left running")
}

// TestTransportStopEndsTheCurrentChild is the ordinary path: the handler ends
// whatever is published when the signal arrives.
func TestTransportStopEndsTheCurrentChild(t *testing.T) {
	requireSleep(t)
	tr := &transport{}
	c := startChild(t, tr, exec.Command("sleep", "60"))
	tr.stop()
	waitEnded(t, c, "stop left the current transport running")
}

// TestChildCloseUnblocksParkedReader is the regression net for a Close that
// closed only stdin. ssh reads on until the remote closes its side, so a far
// end that accepts the connection and then answers nothing parks the control
// reader indefinitely — the case the identity deadline exists for.
//
// The helper reproduces that far end honestly: it never reads its stdin, never
// writes its stdout, and ignores SIGTERM. So neither the stdin EOF nor the
// signal can be what frees the reader, and the unblock must not wait out the
// SIGKILL grace either — a reconnect that stalls two seconds per attempt is the
// same wedge in slower clothes.
func TestChildCloseUnblocksParkedReader(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWedgedChild$")
	cmd.Env = append(os.Environ(), wedgedChildEnv+"=1")
	c := startChild(t, &transport{}, cmd)

	parked := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1))
		parked <- err
	}()
	// Without this the test would also pass on a Close that merely raced a Read
	// already on its way back.
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-parked:
		t.Fatalf("read returned before the close: %v", err)
	default:
	}

	closed := make(chan struct{})
	go func() { c.Close(); c.Close(); close(closed) }() // twice: the drop path and teardown both close
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked")
	}
	select {
	case <-parked:
	case <-time.After(time.Second):
		t.Fatal("Close left the reader parked")
	}
	waitEnded(t, c, "Close never ended the wedged child")
}

// TestHelperWedgedChild is the far end TestChildCloseUnblocksParkedReader dials,
// re-executed from this test binary so the case needs no ssh and no fixture on
// PATH.
func TestHelperWedgedChild(t *testing.T) {
	if os.Getenv(wedgedChildEnv) == "" {
		t.Skip("helper process for TestChildCloseUnblocksParkedReader")
	}
	signal.Ignore(syscall.SIGTERM)
	time.Sleep(time.Minute)
}

const wedgedChildEnv = "LZTMUX_TEST_WEDGED_CHILD"

func requireSleep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available")
	}
}

func startChild(t *testing.T, tr *transport, cmd *exec.Cmd) *child {
	t.Helper()
	c, err := newChild(cmd)
	if err != nil {
		t.Fatalf("newChild: %v", err)
	}
	if err := tr.start(c); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func waitEnded(t *testing.T, c *child, msg string) {
	t.Helper()
	select {
	case <-c.ended:
	case <-time.After(transportKillGrace + 5*time.Second):
		c.cmd.Process.Kill()
		t.Fatal(msg)
	}
}

// TestTransportStartFailureReleasesThePipes pins what reconnect needs from a
// failed dial: the child's stdio pipes are gone by the time start returns an
// error. dial() sits inside reattach's retry loop, so a failure that recurs —
// a missing ssh binary, fork/exec under EAGAIN — would otherwise cost two fds
// per attempt across the whole budget.
//
// os/exec supplies it today, which is why there is no cleanup on that path to
// read. The test guards the invariant rather than the code, so it still fails
// if newChild ever moves onto pipes it owns itself.
func TestTransportStartFailureReleasesThePipes(t *testing.T) {
	c, err := newChild(exec.Command(filepath.Join(t.TempDir(), "no-such-binary")))
	if err != nil {
		t.Fatalf("newChild: %v", err)
	}
	tr := &transport{}
	if err := tr.start(c); err == nil {
		t.Fatal("transport.start = nil for a binary that does not exist")
	}
	// A closed pipe is the only observable difference, and it is the one that
	// matters: an fd this process still holds would accept the write.
	if _, err := c.in.Write([]byte("x")); err == nil {
		t.Error("stdin pipe still writable after a failed Start; the fd leaked")
	}
	if _, err := c.out.Read(make([]byte, 1)); err == nil {
		t.Error("stdout pipe still readable after a failed Start; the fd leaked")
	}
}
