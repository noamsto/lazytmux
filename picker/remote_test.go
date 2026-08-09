package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// noRestore is a stub restoreProbe for tests that don't exercise the
// tmux-remux-snapshot listing. Every collectRemoteItems call needs an
// explicit one — passing nil would fall back to the real ssh implementation
// and either hang or flake in CI.
func noRestore(string) (remuxManifest, error) {
	return remuxManifest{}, errors.New("no snapshot")
}

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

	items := collectRemoteItems(opts, local, probe, noRestore)
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

	items := collectRemoteItems(opts, nil, probe, noRestore)
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

func TestCollectRemoteItemsRestorableSessions(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "tp-g6"}
	probe := func(string) ([]string, error) { return nil, errRemoteNoServer }
	savedAt := time.Now().Add(-2 * time.Hour).UnixMilli()
	restoreProbe := func(host string) (remuxManifest, error) {
		if host != "tp-g6" {
			return remuxManifest{}, errors.New("wrong host")
		}
		return remuxManifest{
			Host:    "tp-g6",
			SavedAt: savedAt,
			Sessions: []remuxManifestSession{
				{Name: "work", LastAttached: 123},
			},
		}, nil
	}

	items := collectRemoteItems(opts, nil, probe, restoreProbe)
	if len(items) != 3 {
		t.Fatalf("expected header + host row + one restorable row, got %d: %+v", len(items), items)
	}
	row := items[2]
	if row.remoteHost != "tp-g6" || row.remoteSess != "work" {
		t.Fatalf("got %+v", row)
	}
	if !row.remoteRestore {
		t.Errorf("row must be flagged remoteRestore so activation knows to restore first")
	}
	if !strings.Contains(row.plain, "ago") {
		t.Errorf("row should surface snapshot age; got %q", row.plain)
	}
	// The host row itself stays a plain cold-start row — it carries no
	// remoteSess/remoteRestore, so activating it takes #287's cold-start path,
	// not a restore. Only the child row(s) built from restorable sessions
	// restore.
	if !strings.Contains(items[1].plain, "no server") || strings.Contains(items[1].plain, "restores") {
		t.Errorf("host row must keep the plain no-server note even when restorable rows exist; got %q", items[1].plain)
	}
}

func TestCollectRemoteItemsNoServerRowUnchangedWhenNoManifest(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "tp-g6"}
	probe := func(string) ([]string, error) { return nil, errRemoteNoServer }

	items := collectRemoteItems(opts, nil, probe, noRestore)
	if len(items) != 2 {
		t.Fatalf("expected header + bare host row, got %d: %+v", len(items), items)
	}
	if !strings.Contains(items[1].plain, "no server") || strings.Contains(items[1].plain, "restores") {
		t.Errorf("host row should keep the plain no-server note; got %q", items[1].plain)
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

	items := collectRemoteItems(opts, map[string]bool{"lab-mono": true}, probe, noRestore)
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
	if items := collectRemoteItems(nil, nil, nil, nil); items != nil {
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

func TestPendingRemoteItems(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	items := pendingRemoteItems(opts)
	if len(items) != 3 {
		t.Fatalf("expected header + 2 host rows, got %d: %+v", len(items), items)
	}
	if !items[0].isRemoteHeader {
		t.Fatalf("first row should be remote header")
	}
	for i, host := range []string{"lab", "dead"} {
		row := items[i+1]
		if row.remoteHost != host || row.remoteSess != "" {
			t.Errorf("row %d: got remoteHost=%q remoteSess=%q, want host=%q", i, row.remoteHost, row.remoteSess, host)
		}
		if row.target == "" {
			t.Errorf("row %d: must be selectable before the probe returns", i)
		}
		if !row.isRemoteRow {
			t.Errorf("row %d: must be flagged isRemoteRow so claude/scratch toggles can't hide it", i)
		}
		if row.searchText != host {
			t.Errorf("row %d: searchText=%q, want %q", i, row.searchText, host)
		}
		if !strings.Contains(row.plain, remotePendingNote) {
			t.Errorf("row %d: plain=%q missing pending note %q", i, row.plain, remotePendingNote)
		}
	}
}

func TestPendingRemoteItemsNoHosts(t *testing.T) {
	if items := pendingRemoteItems(nil); items != nil {
		t.Fatalf("no hosts => nil, got %v", items)
	}
}

// The row count for a host with nothing new to report must not change
// between the pending render and the resolved one — that stability is what
// stops the whole section from reflowing under the user (#312). A host that
// turns out to have unbridged sessions still gains those child rows once the
// probe returns; restoreCursor (tui.go) keeps that growth from moving the
// selection.
func TestBridgePIDFromFile(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantPID int
		wantOK  bool
	}{
		{"valid", "12345\n", 12345, true},
		{"empty", "", 0, false},
		{"whitespace only", "   \n", 0, false},
		{"non-numeric", "abc", 0, false},
		{"zero", "0", 0, false},
		{"negative", "-5", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pid, ok := bridgePIDFromFile(c.raw)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && pid != c.wantPID {
				t.Errorf("pid = %d, want %d", pid, c.wantPID)
			}
		})
	}
}

func TestRemoteItemsRowCountStableAcrossProbe(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	pending := pendingRemoteItems(opts)

	probe := func(host string) ([]string, error) {
		if host == "dead" {
			return nil, errors.New("unreachable")
		}
		return nil, nil // lab: reachable, nothing new to show
	}
	resolved := collectRemoteItems(opts, nil, probe, noRestore)

	if len(pending) != len(resolved) {
		t.Fatalf("row count changed: pending=%d resolved=%d", len(pending), len(resolved))
	}
	for i := range pending {
		if pending[i].remoteHost != resolved[i].remoteHost || pending[i].remoteSess != resolved[i].remoteSess {
			t.Errorf("row %d identity changed: pending=%+v resolved=%+v", i, pending[i], resolved[i])
		}
	}
}

func TestNewestSnapshotManifest(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"Ts":100,"Kind":"snapshot","ManifestJSON":"{\"host\":\"tp-g6\",\"saved_at\":100,\"sessions\":[{\"name\":\"old\",\"last_attached\":5}]}"}`,
		`{"Ts":200,"Kind":"close","ManifestJSON":"{}"}`,
		`{"Ts":300,"Kind":"snapshot","ManifestJSON":"{\"host\":\"tp-g6\",\"saved_at\":300,\"sessions\":[{\"name\":\"work\",\"last_attached\":10}]}"}`,
	}, "\n")

	m, ok := newestSnapshotManifest(ndjson)
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if m.SavedAt != 300 || len(m.Sessions) != 1 || m.Sessions[0].Name != "work" {
		t.Fatalf("got %+v, want the newer (Ts=300) snapshot's manifest", m)
	}
}

func TestNewestSnapshotManifestNoneFound(t *testing.T) {
	if _, ok := newestSnapshotManifest(`{"Ts":1,"Kind":"close","ManifestJSON":"{}"}`); ok {
		t.Fatal("a log with no snapshot event should yield ok=false")
	}
	if _, ok := newestSnapshotManifest(""); ok {
		t.Fatal("empty input => ok=false")
	}
	if _, ok := newestSnapshotManifest("not json at all"); ok {
		t.Fatal("garbage line => ok=false, not a panic")
	}
}

func TestFilterThrowawaySessions(t *testing.T) {
	in := []remuxManifestSession{
		{Name: "probe-verify", LastAttached: 0},
		{Name: "work", LastAttached: 1745700000},
	}
	got := filterThrowawaySessions(in)
	if len(got) != 1 || got[0].Name != "work" {
		t.Fatalf("got %+v, want only the attached session", got)
	}
}

func TestFilterThrowawaySessionsAllThrowaway(t *testing.T) {
	in := []remuxManifestSession{{Name: "probe-verify", LastAttached: 0}}
	if got := filterThrowawaySessions(in); len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestFormatSnapshotAge(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		savedAt int64
		want    string
	}{
		{now.Add(-30 * time.Second).UnixMilli(), "just now"},
		{now.Add(-5 * time.Minute).UnixMilli(), "5m ago"},
		{now.Add(-3 * time.Hour).UnixMilli(), "3h ago"},
		{now.Add(-72 * time.Hour).UnixMilli(), "3d ago"},
	}
	for _, c := range cases {
		if got := formatSnapshotAge(c.savedAt, now); got != c.want {
			t.Errorf("formatSnapshotAge(%d) = %q, want %q", c.savedAt, got, c.want)
		}
	}
}

func TestRemoteRestorableCmdFishSafe(t *testing.T) {
	if strings.Contains(remoteRestorableCmd, "td=") {
		t.Fatalf("probe must not use shell assignments (fish-incompatible): %q", remoteRestorableCmd)
	}
	if !strings.Contains(remoteRestorableCmd, "hostname") {
		t.Fatalf("probe should print the remote hostname first: %q", remoteRestorableCmd)
	}
	if !strings.Contains(remoteRestorableCmd, "tmux-remux") {
		t.Fatalf("probe should call tmux-remux: %q", remoteRestorableCmd)
	}
}

func TestRestorableFromProbeOutput(t *testing.T) {
	snapshotLine := `{"Ts":100,"Kind":"snapshot","ManifestJSON":"{\"host\":\"tp-g6\",\"saved_at\":100,\"sessions\":[{\"name\":\"work\",\"last_attached\":10}]}"}`
	closeOnlyLine := `{"Ts":1,"Kind":"close","ManifestJSON":"{}"}`

	cases := []struct {
		name    string
		stdout  string
		wantErr bool
	}{
		{"matching host", "tp-g6\n" + snapshotLine, false},
		{"mismatched host", "wrong-host\n" + snapshotLine, true},
		{"no newline in output", "tp-g6", true},
		{"snapshot-free log", "tp-g6\n" + closeOnlyLine, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := restorableFromProbeOutput(c.stdout)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got manifest %+v", m)
				}
				if len(m.Sessions) != 0 {
					t.Fatalf("error path must not return sessions, got %+v", m.Sessions)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(m.Sessions) != 1 || m.Sessions[0].Name != "work" {
				t.Fatalf("got %+v, want the one attached session", m.Sessions)
			}
		})
	}
}
