package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestParseRemoteHosts(t *testing.T) {
	got := parseRemoteHosts("  tp-g6   lab\ttp-g6 ")
	want := []string{"tp-g6", "lab"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
	if parseRemoteHosts("") != nil {
		t.Errorf("empty should be nil")
	}
}

func TestRemoteSessionsForHost(t *testing.T) {
	probe := func(host string) ([]string, error) {
		switch host {
		case "down":
			return nil, errors.New("ssh failed")
		case "serverless":
			return nil, errRemoteNoServer
		case "empty":
			return nil, nil
		}
		return []string{"mono", "nix-config", ""}, nil
	}
	local := map[string]bool{"lab-mono": true}

	sess, state := remoteSessionsForHost("lab", local, probe)
	if state != remoteProbeOK {
		t.Fatalf("state = %v, want remoteProbeOK", state)
	}
	if len(sess) != 1 || sess[0] != "nix-config" {
		t.Fatalf("got %v, want [nix-config] (mono suppressed)", sess)
	}

	if _, state := remoteSessionsForHost("down", local, probe); state != remoteProbeUnreachable {
		t.Fatalf("down host state = %v, want remoteProbeUnreachable", state)
	}

	// A reachable host whose tmux server is down must not read as unreachable.
	if _, state := remoteSessionsForHost("serverless", local, probe); state != remoteProbeNoServer {
		t.Fatalf("serverless host state = %v, want remoteProbeNoServer", state)
	}

	if _, state := remoteSessionsForHost("empty", local, probe); state != remoteProbeNoServer {
		t.Fatalf("empty probe state = %v, want remoteProbeNoServer", state)
	}
}

func TestCollectRemoteItems(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	probe := func(host string) ([]string, error) {
		if host == "dead" {
			return nil, errors.New("unreachable")
		}
		return []string{"mono", "other"}, nil
	}
	local := map[string]bool{"lab-mono": true}

	items := collectRemoteItems(opts, local, probe)
	if len(items) != 4 {
		t.Fatalf("expected header + lab + lab's other + dead, got %d: %+v", len(items), items)
	}
	if !items[0].isRemoteHeader {
		t.Fatalf("first row should be remote header")
	}
	if items[1].remoteHost != "lab" || items[1].remoteSess != "" {
		t.Fatalf("host row should precede its sessions; got %+v", items[1])
	}

	var labels []string
	for _, it := range items[1:] {
		if it.remoteHost == "" {
			t.Fatalf("row missing remoteHost: %+v", it)
		}
		labels = append(labels, it.plain)
	}
	joined := strings.Join(labels, " | ")
	if !strings.Contains(joined, remoteTreeMid+" other") {
		t.Errorf("missing tree row for other in %q", joined)
	}
	if strings.Contains(joined, "mono") {
		t.Errorf("bridged lab-mono should be suppressed; got %q", joined)
	}
	if !strings.Contains(joined, "dead") || !strings.Contains(joined, "unreachable") {
		t.Errorf("dead host should appear as bare unreachable row; got %q", joined)
	}
}

// A reachable host with no tmux server gets its own row: honest label, and
// selectable, because the launcher cold-starts the host's startup session (#287).
func TestCollectRemoteItemsNoServerRow(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "tp-g6"}
	probe := func(string) ([]string, error) { return nil, errRemoteNoServer }

	items := collectRemoteItems(opts, nil, probe)
	if len(items) != 2 {
		t.Fatalf("expected header + one row, got %d: %+v", len(items), items)
	}
	row := items[1]
	if !strings.Contains(row.plain, "no server") {
		t.Errorf("row should say the host has no server; got %q", row.plain)
	}
	if strings.Contains(row.plain, "unreachable") {
		t.Errorf("reachable host must not be labelled unreachable; got %q", row.plain)
	}
	if row.remoteHost != "tp-g6" || row.remoteSess != "" {
		t.Errorf("row must open the host with no session (cold start); got remoteHost=%q remoteSess=%q", row.remoteHost, row.remoteSess)
	}
	if row.target == "" {
		t.Error("row must be selectable so Enter can start the remote's server")
	}
	if row.searchText != "tp-g6" {
		t.Errorf("row should still be searchable by host; got %q", row.searchText)
	}
}

func TestLastNonEmptyLine(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"\n  \n":                       "",
		"only":                         "only",
		"ssh noise\nthe real reason\n": "the real reason",
		"trailing blanks\n\n   \n":     "trailing blanks",
	}
	for in, want := range cases {
		if got := lastNonEmptyLine(in); got != want {
			t.Errorf("lastNonEmptyLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// Exit 255 is the only status that means the host is out of reach.
func TestClassifyProbeErr(t *testing.T) {
	exitErr := func(code int) error {
		return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	}

	if got := classifyProbeErr(exitErr(255), false); !errors.Is(got, errRemoteUnreachable) {
		t.Errorf("exit 255 => %v, want errRemoteUnreachable", got)
	}
	if got := classifyProbeErr(exitErr(1), false); !errors.Is(got, errRemoteNoServer) {
		t.Errorf("exit 1 => %v, want errRemoteNoServer", got)
	}
	// tmux missing entirely on the remote: still the host answering, not a
	// connection failure.
	if got := classifyProbeErr(exitErr(127), false); !errors.Is(got, errRemoteNoServer) {
		t.Errorf("exit 127 => %v, want errRemoteNoServer", got)
	}
	// A timeout beats the exit status: the process was killed, so its code is
	// meaningless.
	if got := classifyProbeErr(exitErr(1), true); !errors.Is(got, errRemoteUnreachable) {
		t.Errorf("timeout => %v, want errRemoteUnreachable", got)
	}
	// Non-ExitError (ssh binary missing) is not a host that answered.
	if got := classifyProbeErr(errors.New("exec: \"ssh\": not found"), false); !errors.Is(got, errRemoteUnreachable) {
		t.Errorf("non-exit error => %v, want errRemoteUnreachable", got)
	}
}

// A host whose sessions are all bridged keeps its row, so attaching the last
// one can't make the section disappear.
func TestCollectRemoteItemsAllBridged(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab"}
	probe := func(string) ([]string, error) { return []string{"mono"}, nil }

	items := collectRemoteItems(opts, map[string]bool{"lab-mono": true}, probe)
	if len(items) != 2 {
		t.Fatalf("expected header + lab host row, got %d: %+v", len(items), items)
	}
	if items[1].remoteHost != "lab" || items[1].remoteSess != "" {
		t.Fatalf("row 1 should be the bare lab host row: %+v", items[1])
	}
	if !strings.Contains(items[1].plain, "(all open)") {
		t.Errorf("host row should note everything is open; got %q", items[1].plain)
	}
}

func TestCollectRemoteItemsEmptyHosts(t *testing.T) {
	if items := collectRemoteItems(nil, nil, nil); items != nil {
		t.Fatalf("no hosts => nil, got %v", items)
	}
}

func TestLocalBridgeSession(t *testing.T) {
	if got := localBridgeSession("tp-g6", "mono"); got != "tp-g6-mono" {
		t.Fatalf("got %q", got)
	}
}

// Fish login shells reject `td=...; t=...` assignments (exit 127), which made
// reachable remotes show as "(unreachable — open default)". Guard the probe
// command against regressing to bash-only assignment syntax.
func TestRemoteListSessionsCmdFishSafe(t *testing.T) {
	if strings.Contains(remoteListSessionsCmd, "td=") || strings.Contains(remoteListSessionsCmd, "; t=") {
		t.Fatalf("probe must not use shell assignments (fish-incompatible): %q", remoteListSessionsCmd)
	}
	if !strings.Contains(remoteListSessionsCmd, "env TMUX_TMPDIR=") {
		t.Fatalf("probe should set TMUX_TMPDIR via env(1): %q", remoteListSessionsCmd)
	}
	if !strings.Contains(remoteListSessionsCmd, "list-sessions") {
		t.Fatalf("probe should list sessions: %q", remoteListSessionsCmd)
	}
	// macOS remotes keep their server at tmux's default /tmp/tmux-<uid>; the
	// probe must try that socket dir too or a darwin host always reads as
	// "no server".
	if !strings.Contains(remoteListSessionsCmd, "TMUX_TMPDIR=/tmp/tmux-$(id -u)") {
		t.Fatalf("probe should fall back to the macOS socket dir: %q", remoteListSessionsCmd)
	}
}
