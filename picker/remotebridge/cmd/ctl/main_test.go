package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

func TestRunReportsUnreachableDaemon(t *testing.T) {
	err := run(filepath.Join(t.TempDir(), "missing.sock"), []string{"split-h", "%1"})
	if err == nil || !strings.Contains(err.Error(), "bridge daemon unreachable") {
		t.Fatalf("run() error = %v, want unreachable daemon", err)
	}
}

func TestRunReportsDaemonErrorAck(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, err = wire.ReadFrame(conn)
		}
		if err == nil {
			err = wire.WriteFrame(conn, wire.FrameCtlAck, []byte("carousel: pane %99 is not mirrored by this bridge"))
		}
		done <- err
	}()

	err = run(sock, []string{"split-h", "%1"})
	if err == nil || err.Error() != "carousel: pane %99 is not mirrored by this bridge" {
		t.Fatalf("run() error = %v, want daemon ack", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShowErrorPassesExactCtlErrorToTmux(t *testing.T) {
	original := runTmux
	t.Cleanup(func() { runTmux = original })
	var got []string
	runTmux = func(args ...string) error {
		got = args
		return nil
	}

	err := errors.New("carousel: pane %99 is not mirrored by this bridge")
	if err := showError("/dev/pts/1", err); err != nil {
		t.Fatal(err)
	}
	want := []string{"display-message", "-d", "5000", "-t", "/dev/pts/1", errorPrefix + err.Error()}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux argv = %#v, want %#v", got, want)
	}
}

func TestShowErrorWithTmuxServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not available")
	}

	tmpdir, err := os.MkdirTemp("", "lz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpdir) })
	const socket = "s"
	env := append(os.Environ(), "TMUX_TMPDIR="+tmpdir)

	start := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", "ctl-error", "sleep 30")
	start.Env = env
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() {
		stop := exec.Command("tmux", "-L", socket, "kill-server")
		stop.Env = env
		_ = stop.Run()
	})

	original := runTmux
	t.Cleanup(func() { runTmux = original })
	runTmux = func(args ...string) error {
		cmd := exec.Command("tmux", append([]string{"-L", socket}, args...)...)
		cmd.Env = env
		return cmd.Run()
	}

	want := "bridge daemon unreachable: connection refused"
	if err := showError("ctl-error:0.0", errors.New(want)); err != nil {
		t.Fatalf("show error through tmux: %v", err)
	}
	show := exec.Command("tmux", "-L", socket, "show-messages", "-t", "ctl-error:0.0")
	show.Env = env
	out, err := show.CombinedOutput()
	if err != nil {
		t.Fatalf("show tmux messages: %v: %s", err, out)
	}
	if !strings.Contains(string(out), want) {
		t.Fatalf("tmux messages = %q, want %q", out, want)
	}
}
