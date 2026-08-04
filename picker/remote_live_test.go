package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Live check: real sshListRemoteSessions against a fish-login remote.
// Run: LIVE_REMOTE_PROBE=tp-g6 go test -count=1 -run TestSSHListRemoteSessionsLive .
func TestSSHListRemoteSessionsLive(t *testing.T) {
	host := os.Getenv("LIVE_REMOTE_PROBE")
	if host == "" {
		t.Skip("set LIVE_REMOTE_PROBE=<ssh-host>")
	}

	seed := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=2", "-T", host, "--",
		"env TMUX_TMPDIR=/run/user/$(id -u) $(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux) -f /dev/null new-session -d -s probe-verify")
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed remote session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=2", "-T", host, "--",
			"env TMUX_TMPDIR=/run/user/$(id -u) $(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux) kill-session -t probe-verify").Run()
	})

	names, err := sshListRemoteSessions(host)
	if err != nil {
		t.Fatalf("sshListRemoteSessions(%q): %v", host, err)
	}
	found := false
	for _, n := range names {
		if n == "probe-verify" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected probe-verify in %v", names)
	}
	t.Logf("sessions: %s", strings.Join(names, ", "))
}
