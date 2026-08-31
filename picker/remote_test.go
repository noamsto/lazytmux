package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func probeWithSessions(sessions ...string) remoteProbeResult {
	return remoteProbeResult{Sessions: sessions}
}

func probeWithIdentity(id remoteIdentity, sessions ...string) remoteProbeResult {
	return remoteProbeResult{Identity: id, Sessions: sessions}
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
	probe := func(host string) (remoteProbeResult, error) {
		switch host {
		case "down":
			return remoteProbeResult{}, errors.New("ssh failed")
		case "serverless":
			return remoteProbeResult{}, errRemoteNoServer
		case "empty":
			return remoteProbeResult{}, nil
		}
		return probeWithSessions("mono", "nix-config", ""), nil
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
	probe := func(host string) (remoteProbeResult, error) {
		if host == "dead" {
			return remoteProbeResult{}, errors.New("unreachable")
		}
		return probeWithSessions("mono", "other"), nil
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
	probe := func(string) (remoteProbeResult, error) { return remoteProbeResult{}, errRemoteNoServer }

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
	probe := func(string) (remoteProbeResult, error) { return remoteProbeResult{}, errRemoteNoServer }
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
	probe := func(string) (remoteProbeResult, error) { return remoteProbeResult{}, errRemoteNoServer }

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

// Exit 255 means ssh itself failed; stderr says whether a human could fix it.
func TestClassifyProbeErr(t *testing.T) {
	exitErr := func(code int) error {
		return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	}

	const hostKeyChangedStderr = "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n" +
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n" +
		"Host key verification failed."

	// ssh's refusal banner for a RevokedHostKeys match: no "IDENTIFICATION HAS
	// CHANGED" line, just this warning followed by the same bare
	// "Host key verification failed." that authFailurePatterns matches.
	const revokedHostKeyStderr = "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n" +
		"@    WARNING: REVOKED HOST KEY DETECTED!               @\n" +
		"The ECDSA host key for tp-g6 is marked as revoked.\n" +
		"Host key verification failed."

	cases := []struct {
		name     string
		err      error
		stderr   string
		timedOut bool
		want     error
	}{
		// Baselines: the pre-#357 behaviour, unchanged.
		{"bare 255", exitErr(255), "", false, errRemoteUnreachable},
		{"exit 1 is the remote command's own", exitErr(1), "", false, errRemoteNoServer},
		{"tmux missing on the remote", exitErr(127), "", false, errRemoteNoServer},
		{"timeout beats the exit status", exitErr(1), "", true, errRemoteUnreachable},
		{"ssh binary missing", errors.New(`exec: "ssh": not found`), "", false, errRemoteUnreachable},

		// New: 255 plus a stderr signature a prompt could fix.
		{"unknown host key", exitErr(255), "Host key verification failed.", false, errRemoteNeedsAuth},
		{"password refused", exitErr(255), "noams@mbp: Permission denied (publickey,password).", false, errRemoteNeedsAuth},
		{"agent exhausted", exitErr(255), "Received disconnect: Too many authentication failures", false, errRemoteNeedsAuth},
		{"2fa offered", exitErr(255), "Authentications that can continue: keyboard-interactive", false, errRemoteNeedsAuth},

		// New: a changed key outranks the auth patterns ssh prints alongside it.
		{"host key changed", exitErr(255), hostKeyChangedStderr, false, errRemoteHostKeyChanged},
		// New: a revoked key must land on the same inert row as a changed one —
		// ssh refuses it unconditionally, so nothing unsafe can be accepted, but
		// the row must never invite the "Enter to connect" action regardless.
		{"revoked host key", exitErr(255), revokedHostKeyStderr, false, errRemoteHostKeyChanged},

		// Genuinely down hosts must not be dragged into the auth flow.
		{"refused", exitErr(255), "ssh: connect to host lab port 22: Connection refused", false, errRemoteUnreachable},
		{"no route", exitErr(255), "ssh: connect to host lab port 22: No route to host", false, errRemoteUnreachable},
		{"unknown name", exitErr(255), "ssh: Could not resolve hostname lab: Name or service not known", false, errRemoteUnreachable},
		// A local firewall's EACCES prints the same words as ssh's own auth
		// refusal but with no "(publickey,...)" reason list — a genuinely down
		// host, not one a prompt could fix.
		{"firewall EACCES", exitErr(255), "ssh: connect to host lab port 22: Permission denied", false, errRemoteUnreachable},

		// Precedence: a non-255 exit is the remote command's, whatever it printed.
		{"remote command printed Permission denied", exitErr(1), "cat: /etc/shadow: Permission denied", false, errRemoteNoServer},
		// Precedence: a killed process has no meaningful stderr verdict.
		{"timeout beats an auth signature", exitErr(255), "Host key verification failed.", true, errRemoteUnreachable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProbeErr(tc.err, tc.stderr, tc.timedOut); !errors.Is(got, tc.want) {
				t.Errorf("classifyProbeErr(%v, %q, %v) = %v, want %v", tc.err, tc.stderr, tc.timedOut, got, tc.want)
			}
		})
	}
}

// The auth popup's script explains and pauses on any failure it causes, so
// only a start failure (the exec.Command never ran) has nothing on screen to
// explain and needs surfacing into the status line.
func TestRemoteAuthStartFailure(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		wantOK bool
	}{
		{"nil: normal exit, nothing to surface", nil, false},
		{"ExitError: the script already explained and paused", exec.Command("sh", "-c", "exit 1").Run(), false},
		{"exec.Error: PATH stale, the process never started", exec.Command("lztmux-remote-auth-does-not-exist").Run(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := remoteAuthStartFailure(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("remoteAuthStartFailure(%v) ok = %v, want %v", tc.err, ok, tc.wantOK)
			}
			if ok && msg == "" {
				t.Errorf("remoteAuthStartFailure(%v) returned ok with an empty message", tc.err)
			}
		})
	}
}

// A host whose sessions are all bridged keeps its row, so attaching the last
// one can't make the section disappear.
func TestCollectRemoteItemsAllBridged(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab"}
	probe := func(string) (remoteProbeResult, error) { return probeWithSessions("mono"), nil }

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
	for _, cmd := range []string{remoteIdentityPreamble, remoteListSessionsCmd} {
		if strings.Contains(cmd, "td=") || strings.Contains(cmd, "; t=") {
			t.Fatalf("probe must not use shell assignments (fish-incompatible): %q", cmd)
		}
	}
	if !strings.Contains(remoteListSessionsCmd, "cat /etc/machine-id") {
		t.Fatalf("probe should print machine-id first: %q", remoteListSessionsCmd)
	}
	if !strings.Contains(remoteListSessionsCmd, "id -un") {
		t.Fatalf("probe should print username: %q", remoteListSessionsCmd)
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
			t.Errorf("row %d: must be flagged isRemoteRow so agent/scratch toggles can't hide it", i)
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

	probe := func(host string) (remoteProbeResult, error) {
		if host == "dead" {
			return remoteProbeResult{}, errors.New("unreachable")
		}
		return remoteProbeResult{}, nil // lab: reachable, nothing new to show
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

// A host that only wants an interactive answer is not "unreachable": it gets
// its own note and an actionable row (#357).
func TestCollectRemoteItemsNeedsAuth(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "mbp"}
	probe := func(string) (remoteProbeResult, error) {
		return remoteProbeResult{}, fmt.Errorf("%w: exit status 255", errRemoteNeedsAuth)
	}
	items := collectRemoteItems(opts, nil, probe, nil)

	if len(items) != 2 {
		t.Fatalf("got %d items, want header + one host row", len(items))
	}
	row := items[1]
	if !strings.Contains(row.plain, "(auth needed — Enter to connect)") {
		t.Errorf("row = %q, want the auth-needed note", row.plain)
	}
	if !row.remoteNeedsAuth {
		t.Error("remoteNeedsAuth = false, want true so Enter runs the handshake")
	}
	if row.remoteInert {
		t.Error("remoteInert = true, want false — this row must be actionable")
	}
}

// A changed host key is a MITM signature. The row must say so and Enter must
// have nothing to act on: offering "Enter to connect" here would train the user
// to accept key changes without checking a fingerprint.
func TestCollectRemoteItemsHostKeyChanged(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "mbp"}
	probe := func(string) (remoteProbeResult, error) {
		return remoteProbeResult{}, fmt.Errorf("%w: exit status 255", errRemoteHostKeyChanged)
	}
	items := collectRemoteItems(opts, nil, probe, nil)

	row := items[1]
	if !strings.Contains(row.plain, "(host key changed — verify manually)") {
		t.Errorf("row = %q, want the host-key-changed note", row.plain)
	}
	if !row.remoteInert {
		t.Error("remoteInert = false, want true so Enter refuses to act")
	}
	if row.remoteNeedsAuth {
		t.Error("remoteNeedsAuth = true, want false — a key change is not an auth prompt")
	}
}

// The two new states map through remoteSessionsForHost, not just through
// classifyProbeErr.
func TestRemoteSessionsForHostNewStates(t *testing.T) {
	cases := map[string]struct {
		err  error
		want remoteProbeState
	}{
		"needs auth":       {fmt.Errorf("%w: x", errRemoteNeedsAuth), remoteProbeNeedsAuth},
		"host key changed": {fmt.Errorf("%w: x", errRemoteHostKeyChanged), remoteProbeHostKeyChanged},
		"unreachable":      {fmt.Errorf("%w: x", errRemoteUnreachable), remoteProbeUnreachable},
		"no server":        {fmt.Errorf("%w: x", errRemoteNoServer), remoteProbeNoServer},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probe := func(string) (remoteProbeResult, error) { return remoteProbeResult{}, tc.err }
			if _, got := remoteSessionsForHost("h", nil, probe); got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
		})
	}
}

// The launcher never reaches PATH, so only @remote_open_bin resolves it.
func TestOpenRemoteBridgeUsesConfiguredBin(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "fake-open")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := map[string]string{"@remote_open_bin": bin}
	if err := openRemoteBridge(opts, "lab", "work", false); err != nil {
		t.Fatalf("openRemoteBridge: %v", err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("launcher did not run: %v", err)
	}
	if want := "lab\nwork\n"; string(got) != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestParseRemoteProbeOutput(t *testing.T) {
	stdout := "abc123\nnoams\nmono\nother\n"
	got := parseRemoteProbeOutput(stdout)
	if got.Identity.MachineID != "abc123" || got.Identity.User != "noams" {
		t.Fatalf("identity = %+v", got.Identity)
	}
	if len(got.Sessions) != 2 || got.Sessions[0] != "mono" || got.Sessions[1] != "other" {
		t.Fatalf("sessions = %v", got.Sessions)
	}
}

func TestCollectRemoteItemsDropsSelfHost(t *testing.T) {
	const selfID = "test-machine-id-self"
	local := remoteIdentity{MachineID: selfID, User: "noams"}
	readLocalRemoteIdentity = func() remoteIdentity { return local }
	t.Cleanup(func() { readLocalRemoteIdentity = func() remoteIdentity {
		return remoteIdentity{MachineID: readLocalMachineID(), User: localUsername()}
	} })

	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	opts := map[string]string{"@remote_bridge_hosts": "localhost lab"}
	probe := func(host string) (remoteProbeResult, error) {
		switch host {
		case "localhost":
			return probeWithIdentity(local), nil
		case "lab":
			return probeWithIdentity(remoteIdentity{MachineID: "other-id", User: "noams"}, "work"), nil
		}
		return remoteProbeResult{}, errors.New("unexpected host")
	}

	items := collectRemoteItems(opts, nil, probe, noRestore)
	if len(items) != 3 {
		t.Fatalf("expected header + lab + work, got %d: %+v", len(items), items)
	}
	for _, it := range items {
		if it.remoteHost == "localhost" {
			t.Fatalf("self alias should be dropped; got %+v", it)
		}
	}
	if !isCachedRemoteSelfAlias("localhost") {
		t.Fatal("self alias should be cached after probe")
	}
}

func TestCollectRemoteItemsKeepsSameMachineDifferentUser(t *testing.T) {
	const sharedID = "shared-machine-id"
	readLocalRemoteIdentity = func() remoteIdentity {
		return remoteIdentity{MachineID: sharedID, User: "noams"}
	}
	t.Cleanup(func() { readLocalRemoteIdentity = func() remoteIdentity {
		return remoteIdentity{MachineID: readLocalMachineID(), User: localUsername()}
	} })

	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	opts := map[string]string{"@remote_bridge_hosts": "root-local"}
	probe := func(string) (remoteProbeResult, error) {
		return probeWithIdentity(remoteIdentity{MachineID: sharedID, User: "root"}, "admin"), nil
	}

	items := collectRemoteItems(opts, nil, probe, noRestore)
	if len(items) != 3 {
		t.Fatalf("expected header + host + session, got %d: %+v", len(items), items)
	}
	if items[1].remoteHost != "root-local" {
		t.Fatalf("same machine, different user must be kept: %+v", items[1])
	}
}

func TestCollectRemoteItemsDropsSelfOnNoServer(t *testing.T) {
	const selfID = "test-machine-id-noserver"
	local := remoteIdentity{MachineID: selfID, User: "noams"}
	readLocalRemoteIdentity = func() remoteIdentity { return local }
	t.Cleanup(func() { readLocalRemoteIdentity = func() remoteIdentity {
		return remoteIdentity{MachineID: readLocalMachineID(), User: localUsername()}
	} })

	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	opts := map[string]string{"@remote_bridge_hosts": "localhost"}
	probe := func(string) (remoteProbeResult, error) {
		return probeWithIdentity(local), fmt.Errorf("%w: no server", errRemoteNoServer)
	}

	items := collectRemoteItems(opts, nil, probe, noRestore)
	if items != nil {
		t.Fatalf("self host with no server should be dropped entirely, got %+v", items)
	}
}

func TestPendingRemoteItemsSkipsCachedSelfAlias(t *testing.T) {
	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	markCachedRemoteSelfAlias("localhost")
	opts := map[string]string{"@remote_bridge_hosts": "localhost lab"}

	items := pendingRemoteItems(opts)
	if len(items) != 2 {
		t.Fatalf("expected header + lab only, got %d: %+v", len(items), items)
	}
	if items[1].remoteHost != "lab" {
		t.Fatalf("cached self alias should be omitted from first paint; got %+v", items[1])
	}
}

func TestCollectRemoteItemsRevalidatesCachedSelfAlias(t *testing.T) {
	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	markCachedRemoteSelfAlias("localhost")
	if !isCachedRemoteSelfAlias("localhost") {
		t.Fatal("setup: localhost should be cached")
	}

	readLocalRemoteIdentity = func() remoteIdentity {
		return remoteIdentity{MachineID: "local-id", User: "noams"}
	}
	t.Cleanup(func() { readLocalRemoteIdentity = func() remoteIdentity {
		return remoteIdentity{MachineID: readLocalMachineID(), User: localUsername()}
	} })

	probed := make(map[string]bool)
	opts := map[string]string{"@remote_bridge_hosts": "localhost lab"}
	probe := func(host string) (remoteProbeResult, error) {
		probed[host] = true
		switch host {
		case "localhost":
			return probeWithIdentity(remoteIdentity{MachineID: "repointed-id", User: "noams"}, "work"), nil
		case "lab":
			return probeWithSessions("mono"), nil
		}
		return remoteProbeResult{}, errors.New("unexpected host")
	}

	items := collectRemoteItems(opts, nil, probe, noRestore)
	if !probed["localhost"] {
		t.Fatal("collect must probe cached self aliases, not skip them")
	}
	if isCachedRemoteSelfAlias("localhost") {
		t.Fatal("stale self cache should be cleared when alias resolves elsewhere")
	}
	if len(items) != 5 {
		t.Fatalf("expected header + localhost + work + lab + mono, got %d: %+v", len(items), items)
	}
	if items[1].remoteHost != "localhost" {
		t.Fatalf("repointed alias should appear after revalidation; got %+v", items[1])
	}
}

func TestCollectRemoteItemsKeepsCachedSelfAliasWhenProbeFails(t *testing.T) {
	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	markCachedRemoteSelfAlias("localhost")

	opts := map[string]string{"@remote_bridge_hosts": "localhost"}
	probe := func(string) (remoteProbeResult, error) {
		return remoteProbeResult{}, errors.New("unreachable")
	}

	items := collectRemoteItems(opts, nil, probe, noRestore)
	if items == nil || len(items) != 2 {
		t.Fatalf("expected header + unreachable row, got %+v", items)
	}
	if !isCachedRemoteSelfAlias("localhost") {
		t.Fatal("cache must stay when probe yields no identity to revalidate against")
	}
}

func TestIsCachedRemoteSelfAliasRejectsUntrustedMarker(t *testing.T) {
	cacheDir := t.TempDir()
	remoteSelfCacheDir = cacheDir
	t.Cleanup(func() { remoteSelfCacheDir = "/tmp/lazytmux-remote-self" })

	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cacheDir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := remoteSelfCachePath("localhost")
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isCachedRemoteSelfAlias("localhost") {
		t.Fatal("world-writable cache dir must not count as cached")
	}
}
