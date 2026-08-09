# Restorable Remote Sessions (#268) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a remote host in the picker's Remote section is reachable but serverless, list the sessions from its newest `tmux-remux` snapshot as selectable rows, and make Enter on one of them start the remote server, restore that snapshot, and bridge in — instead of today's dead end.

**Architecture:** `picker/remote.go` gains a second, host-scoped ssh probe (`sshListRestorableSessions`) that runs only for hosts already classified `remoteProbeNoServer`. It fetches the remote's hostname plus `tmux-remux list --json`, picks the newest `snapshot` event, verifies the manifest's `Host` against the hostname that actually answered, and drops never-attached (throwaway) sessions. `collectRemoteItems` renders the survivors as extra tree rows under the host, tagged `remoteRestore: true`. Activating one of those rows now passes `LZTMUX_REMOTE_RESTORE=1` to `lztmux-remote-open`, which — only when that env var is set and the requested session isn't already live — cold-starts the server if needed and runs `tmux-remux restore` (bypassing the remote's own `restoreMode=off` gate, since this is an explicit user action) before bridging. The existing live-attach path (env var unset) is untouched byte-for-byte.

**Tech Stack:** Go (bubbletea picker, `picker/remote.go`, `picker/tui.go`), bash (`scripts/lztmux-remote-open.sh`), bats (`tests/remote-cold-start.bats`).

---

## Background you need before starting

- **#269 already shipped** the `remoteProbeNoServer` vs `remoteProbeUnreachable` distinction in `picker/remote.go`. `remoteSessionsForHost` returns `remoteProbeNoServer` when the remote answered ssh but has no tmux server (or an empty session list); `remoteProbeUnreachable` when ssh itself failed. This plan only ever activates for `remoteProbeNoServer`.
- **#287 already shipped** cold-start: when `collectRemoteItems` shows a `remoteProbeNoServer` host, its row is still selectable, and `lztmux-remote-open.sh` starts the host's `tmux-startup.service` (or launchd agent on Darwin) on demand, then re-probes — because unit state is not server state. This plan extends that logic (one shared helper function), it does not replace it.
- **#267 (`feat(module): startupSession.headless for GUI-less hosts`) is already closed/merged.** The issue's priority note said this work is "lower priority once #267 keeps a server alive on the remote." #267 makes serverless remotes *less common* (a systemd/launchd unit can now run headless without a GUI session), but it does not make them impossible — a host can still be freshly booted, mid-maintenance, or have the unit disabled. The scope in this plan is unchanged (full listing + restore path), and Task 15 sends a crew-bus FYI about this rather than silently descoping, per the issue's explicit instruction.
- **`tmux-remux`'s data model** (read from `/Users/noams/git/tmux-remux`, the pinned flake input): `tmux-remux list --json` prints one JSON object per line, newest-first, encoding Go's `store.Event` struct verbatim (no json tags, so field names are capitalized: `Ts`, `Kind`, `ManifestJSON`, etc.). An event with `Kind == "snapshot"` has a `ManifestJSON` field holding `{"host":..., "saved_at":..., "sessions":[{"name":..., "last_attached":..., ...}]}`. `saved_at` and event `Ts` are both Unix milliseconds. `last_attached` is `0` for a session tmux never saw a client attach to (confirmed against `internal/tmux/parse.go`'s handling of tmux's empty `session_last_attached` field) — this is the throwaway signal (see Task 2). `tmux-remux restore` reads the newest snapshot *before the current server's start time* and is safe to call even when a server is already up — it never duplicates a live session. But its smart filter (`internal/filter`, wired up in `RestoreCmd.Run`) is more than "skips sessions already running": by default it also drops any session whose windows are all idle plain shells (`RestoreSkipIdleShells`/`RestoreSkipIdleWindows`, both `true`), any session older than `RestoreMaxSessionAge` (14d default), and the whole snapshot if it's older than `RestoreMaxSnapshotAge` (30d default) — see `internal/config/config.go`'s `DefaultConfig`. None of that filter is importable here: it lives under `tmux-remux`'s own `internal/` tree, which Go's internal-import rule keeps out of this separate `github.com/noamsto/lazytmux/picker` module, and the remote's actual configured overrides (if any) aren't visible to the picker without yet another ssh round trip. So a session the picker lists as restorable is not guaranteed to survive `restore` — see the design doc's "Restore filter mismatch" section for how this plan handles that honestly rather than silently. `tmux-remux restore` (no `--auto`) ignores the remote's `restoreMode=off` config; only `restore --auto` respects that gate. This plan always calls the bare `restore` from the picker's explicit action.
- **The picker package is `package main`** (flat namespace across all `picker/*.go` files, including `_test.go` files) — a helper defined in one test file is visible from another in the same package.

## File Structure

- Modify `picker/remote.go`: new manifest types, `newestSnapshotManifest`, `filterThrowawaySessions`, `formatSnapshotAge`, `remoteRestorableCmd`, `restorableFromProbeOutput`, `sshListRestorableSessions`; extend `hostResult` and `collectRemoteItems`; extend `openRemoteBridge`.
- Modify `picker/remote_test.go`: unit tests for every new pure function; update existing `collectRemoteItems` call sites; new integration tests for restorable rows.
- Modify `picker/tui.go`: one new `listItem` field, one call-site update in `activateCurrent`, one call-site update in `remoteCmd`. No other tui.go changes — kept minimal because another worker (#229) is also editing this file.
- Modify `picker/tui_test.go`: update three pre-existing `collectRemoteItems` call sites (mechanical, 4th argument only).
- Modify `scripts/lztmux-remote-open.sh`: extract `start_remote_server()` (pure refactor), add the `LZTMUX_REMOTE_RESTORE`-gated restore branch.
- Modify `tests/remote-cold-start.bats`: extend the fake `ssh` stub, add 4 new `@test` cases for the restore path.
- Create `docs/superpowers/specs/2026-08-08-restorable-remote-sessions-design.md`: short design record (host-verification method, throwaway filter rationale, why `restore` bypasses `restoreMode=off`).

---

### Task 1: Manifest types + newest-snapshot selection

**Files:**
- Modify: `picker/remote.go` (add after `sshListRemoteSessions`, i.e. after line 146 in the current file)
- Test: `picker/remote_test.go`

- [ ] **Step 1: Write the failing test**

Add to `picker/remote_test.go`. No import changes needed — the file already imports `errors`, `fmt`, `os/exec`, `strings`, `testing`, and this test uses none beyond those (`"time"` isn't needed until Task 3's `TestFormatSnapshotAge`):

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./... -run TestNewestSnapshotManifest -v`
Expected: FAIL — `undefined: newestSnapshotManifest` (compile error).

- [ ] **Step 3: Implement the types and the function**

In `picker/remote.go`, add `"encoding/json"` to the import block (already has `bytes`, `context`, `errors`, `fmt`, `os`, `os/exec`, `strings`, `sync`, `time`). Then add, after `sshListRemoteSessions` (after its closing `}`):

```go
// remuxManifestSession is the subset of a tmux-remux snapshot session the
// picker needs to render a restorable row.
type remuxManifestSession struct {
	Name         string `json:"name"`
	LastAttached int64  `json:"last_attached"`
}

// remuxManifest mirrors tmux-remux's snapshot.Manifest (internal/snapshot/manifest.go
// in noamsto/tmux-remux) — only the fields the picker reads.
type remuxManifest struct {
	Host     string                 `json:"host"`
	SavedAt  int64                  `json:"saved_at"`
	Sessions []remuxManifestSession `json:"sessions"`
}

// remuxEvent mirrors the fields of tmux-remux's store.Event this package
// needs, as emitted (one JSON object per line) by `tmux-remux list --json`.
// The upstream type carries no json tags, so field names must match Go's
// default (capitalized) encoding exactly.
type remuxEvent struct {
	Ts           int64
	Kind         string
	ManifestJSON string
}

// newestSnapshotManifest scans newline-delimited tmux-remux events for the
// snapshot with the highest timestamp and decodes its manifest. A snapshot is
// a point-in-time dump of every session on the server at save time, not an
// append-only session list, so only the single newest one is ever consulted —
// unioning across snapshots would resurrect sessions closed since. Malformed
// lines are skipped: this reads over an ssh pipe, where truncated output is a
// real failure mode, not a programming error.
func newestSnapshotManifest(ndjson string) (remuxManifest, bool) {
	var best remuxEvent
	found := false
	for _, line := range strings.Split(ndjson, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev remuxEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Kind != "snapshot" {
			continue
		}
		if !found || ev.Ts > best.Ts {
			best, found = ev, true
		}
	}
	if !found {
		return remuxManifest{}, false
	}
	var m remuxManifest
	if err := json.Unmarshal([]byte(best.ManifestJSON), &m); err != nil {
		return remuxManifest{}, false
	}
	return m, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./... -run TestNewestSnapshotManifest -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add picker/remote.go picker/remote_test.go
git commit -m "feat(picker): parse tmux-remux's newest snapshot manifest"
```

---

### Task 2: Throwaway-session filter

**Files:**
- Modify: `picker/remote.go`
- Test: `picker/remote_test.go`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./... -run TestFilterThrowawaySessions -v`
Expected: FAIL — `undefined: filterThrowawaySessions`.

- [ ] **Step 3: Implement**

Add to `picker/remote.go`, after `newestSnapshotManifest`:

```go
// filterThrowawaySessions drops sessions tmux reports as never attached
// (session_last_attached == 0) — the exact signature of an ephemeral session
// like probe-verify (seeded and killed by TestSSHListRemoteSessionsLive)
// rather than one a person was actually using. This is a principled signal,
// not a name denylist: any detached-and-abandoned session is caught the same
// way.
func filterThrowawaySessions(sessions []remuxManifestSession) []remuxManifestSession {
	out := make([]remuxManifestSession, 0, len(sessions))
	for _, s := range sessions {
		if s.LastAttached == 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./... -run TestFilterThrowawaySessions -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add picker/remote.go picker/remote_test.go
git commit -m "feat(picker): filter never-attached sessions from restorable rows"
```

---

### Task 3: Snapshot-age formatting

**Files:**
- Modify: `picker/remote.go`
- Test: `picker/remote_test.go`

- [ ] **Step 1: Write the failing test**

Add `"time"` to `picker/remote_test.go`'s import block — this is the first test in the plan that uses it (Tasks 1-2 needed no import changes).

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./... -run TestFormatSnapshotAge -v`
Expected: FAIL — `undefined: formatSnapshotAge`.

- [ ] **Step 3: Implement**

Add to `picker/remote.go`, after `filterThrowawaySessions`:

```go
// formatSnapshotAge renders how long ago a snapshot was saved, coarsening
// units the further back it is (mirrors usageResetSuffix's granularity in
// picker/statusline/usage.go). A stale manifest is worse than no row, so its
// age has to be legible at a glance, not buried in a tooltip.
func formatSnapshotAge(savedAtMillis int64, now time.Time) string {
	age := now.Sub(time.UnixMilli(savedAtMillis))
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	case age < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(age/(24*time.Hour)))
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./... -run TestFormatSnapshotAge -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add picker/remote.go picker/remote_test.go
git commit -m "feat(picker): format tmux-remux snapshot age for restorable rows"
```

---

### Task 4: The ssh probe (`sshListRestorableSessions`)

**Files:**
- Modify: `picker/remote.go`
- Test: `picker/remote_test.go`

The one security-relevant rule in this feature — a wrong-host `state.db` must not produce rows — has to be unit-testable without shelling out to a real `ssh`. So `sshListRestorableSessions` is split in two: a pure `restorableFromProbeOutput` that does the hostname check, newest-snapshot selection, and throwaway filtering on an already-fetched string, and a thin ssh wrapper around it. Only the wrapper needs a live host, so only it is deferred to `remote_live_test.go` (Task 4b).

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd picker && go test ./... -run 'TestRemoteRestorableCmdFishSafe|TestRestorableFromProbeOutput' -v`
Expected: FAIL — `undefined: remoteRestorableCmd`, `undefined: restorableFromProbeOutput`.

- [ ] **Step 3: Implement**

Add to `picker/remote.go`, near `remoteListSessionsCmd` (after its declaration, before `sshConnectFailureExit`):

```go
// remoteRestorableCmd emits the remote host's own hostname (line 1, used to
// verify a fetched snapshot really belongs to this host) followed by
// tmux-remux's event log as newline-delimited JSON. No tmux server is
// required — tmux-remux reads state.db directly — so this only runs once a
// host has already probed as remoteProbeNoServer. Must stay fish-safe like
// remoteListSessionsCmd: no `var=value` shell assignments.
const remoteRestorableCmd = `hostname; $(command -v tmux-remux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux-remux) list --json 2>/dev/null`
```

Then, after `sshListRemoteSessions` (and after the manifest-parsing helpers from Tasks 1-3), add:

```go
// restorableFromProbeOutput parses remoteRestorableCmd's stdout (the remote's
// own hostname, then tmux-remux's ndjson event log), verifies the snapshot's
// Host field against the hostname that actually answered ssh, and drops
// throwaway sessions. Split out from sshListRestorableSessions so the host
// check — the one security-relevant rule here, guarding against a stale or
// wrong-host state.db producing rows — is testable without a real ssh.
func restorableFromProbeOutput(stdout string) (remuxManifest, error) {
	remoteHostname, ndjson, ok := strings.Cut(stdout, "\n")
	if !ok {
		return remuxManifest{}, errors.New("no manifest data")
	}
	m, found := newestSnapshotManifest(ndjson)
	if !found {
		return remuxManifest{}, errors.New("no snapshot to restore")
	}
	if m.Host != strings.TrimSpace(remoteHostname) {
		return remuxManifest{}, fmt.Errorf("snapshot host %q does not match %q", m.Host, remoteHostname)
	}
	m.Sessions = filterThrowawaySessions(m.Sessions)
	return m, nil
}

// sshListRestorableSessions fetches the newest tmux-remux snapshot from a
// serverless host and returns its sessions, verified and filtered by
// restorableFromProbeOutput. Only meaningful once a host has already probed
// as remoteProbeNoServer — a live server's sessions come from
// sshListRemoteSessions instead.
func sshListRestorableSessions(host string) (remuxManifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=2",
		"-T",
		host,
		"--",
		remoteRestorableCmd,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return remuxManifest{}, err
	}
	return restorableFromProbeOutput(stdout.String())
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd picker && go test ./... -run 'TestRemoteRestorableCmdFishSafe|TestRestorableFromProbeOutput' -v`
Expected: PASS

Also run the full package build to catch the new `sshListRestorableSessions` compiling cleanly (it isn't called anywhere yet, which is fine — Go doesn't error on unused package-level funcs):

Run: `cd picker && go build ./...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add picker/remote.go picker/remote_test.go
git commit -m "feat(picker): ssh probe for a serverless host's restorable sessions"
```

---

### Task 4b: Live test for the new probe

**Files:**
- Modify: `picker/remote_live_test.go`

- [ ] **Step 1: Add a live-gated test mirroring `TestSSHListRemoteSessionsLive`**

Append to `picker/remote_live_test.go`:

```go
// Live check: real sshListRestorableSessions against a host with tmux-remux
// installed and at least one snapshot on disk (no tmux server required).
// Run: LIVE_REMOTE_RESTORE=tp-g6 go test -count=1 -run TestSSHListRestorableSessionsLive .
func TestSSHListRestorableSessionsLive(t *testing.T) {
	host := os.Getenv("LIVE_REMOTE_RESTORE")
	if host == "" {
		t.Skip("set LIVE_REMOTE_RESTORE=<ssh-host> (must have tmux-remux + a snapshot, no live server)")
	}

	m, err := sshListRestorableSessions(host)
	if err != nil {
		t.Fatalf("sshListRestorableSessions(%q): %v", host, err)
	}
	t.Logf("host=%q saved_at=%d sessions=%v", m.Host, m.SavedAt, m.Sessions)
}
```

This is deliberately observational (like its sibling `TestSSHListRemoteSessionsLive`) rather than assertive — the plan's own Task 15 covers exact manual verification against `tp-g6`. It gives the implementer a quick, real, one-line way to sanity-check the probe against an actual host before wiring it into the picker.

- [ ] **Step 2: Verify it compiles and skips cleanly with no env var set**

Run: `cd picker && go test ./... -run TestSSHListRestorableSessionsLive -v`
Expected: `--- SKIP` (no `LIVE_REMOTE_RESTORE` set), overall PASS.

- [ ] **Step 3: Commit**

```bash
git add picker/remote_live_test.go
git commit -m "test(picker): add a live check for sshListRestorableSessions"
```

---

### Task 5: Wire the restore probe into `collectRemoteItems` (plumbing only)

This task changes the function signature and every call site so the code compiles, without changing rendered output yet — that comes in Task 6. Keeping plumbing and behavior in separate commits makes the tui.go/tui_test.go diff easy to review and easy for another worker to rebase past.

**Files:**
- Modify: `picker/remote.go:252` (`collectRemoteItems`), the `hostResult` type inside it
- Modify: `picker/remote_test.go` (add a shared test stub, update 5 call sites)
- Modify: `picker/tui_test.go` (update 3 call sites)
- Modify: `picker/tui.go:1292` (the one production call site)

- [ ] **Step 1: Add a shared test stub to `picker/remote_test.go`**

Add near the top of the file (after the imports, before `TestParseRemoteHosts`):

```go
// noRestore is a stub restoreProbe for tests that don't exercise the
// tmux-remux-snapshot listing. Every collectRemoteItems call needs an
// explicit one — passing nil would fall back to the real ssh implementation
// and either hang or flake in CI.
func noRestore(string) (remuxManifest, error) {
	return remuxManifest{}, errors.New("no snapshot")
}
```

- [ ] **Step 2: Update `collectRemoteItems`'s signature and internals in `picker/remote.go`**

Change the `hostResult` type and the function signature/body. The current code (inside `collectRemoteItems`, starting at the `type hostResult struct` line) is:

```go
	type hostResult struct {
		host  string
		sess  []string
		state remoteProbeState
	}
	results := make([]hostResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			sess, state := remoteSessionsForHost(h, localSessionNames, probe)
			results[i] = hostResult{host: h, sess: sess, state: state}
		}(i, h)
	}
	wg.Wait()
```

Replace it with:

```go
	type hostResult struct {
		host            string
		sess            []string
		state           remoteProbeState
		restorable      []remuxManifestSession
		manifestSavedAt int64
	}
	results := make([]hostResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			sess, state := remoteSessionsForHost(h, localSessionNames, probe)
			res := hostResult{host: h, sess: sess, state: state}
			if state == remoteProbeNoServer {
				if m, err := restoreProbe(h); err == nil && len(m.Sessions) > 0 {
					res.restorable = m.Sessions
					res.manifestSavedAt = m.SavedAt
				}
			}
			results[i] = res
		}(i, h)
	}
	wg.Wait()
```

Then change the function signature (currently `func collectRemoteItems(tmuxOpts map[string]string, localSessionNames map[string]bool, probe func(string) ([]string, error)) []listItem {`) to:

```go
func collectRemoteItems(tmuxOpts map[string]string, localSessionNames map[string]bool, probe func(string) ([]string, error), restoreProbe func(string) (remuxManifest, error)) []listItem {
```

And add the default assignment right after the existing `if probe == nil { probe = sshListRemoteSessions }` block:

```go
	if restoreProbe == nil {
		restoreProbe = sshListRestorableSessions
	}
```

- [ ] **Step 3: Update the 5 pre-existing call sites in `picker/remote_test.go`**

- `TestCollectRemoteItems` (line ~73): `collectRemoteItems(opts, local, probe)` → `collectRemoteItems(opts, local, probe, noRestore)`
- `TestCollectRemoteItemsNoServerRow` (line ~109): `collectRemoteItems(opts, nil, probe)` → `collectRemoteItems(opts, nil, probe, noRestore)`
- `TestCollectRemoteItemsAllBridged` (line ~180): `collectRemoteItems(opts, map[string]bool{"lab-mono": true}, probe)` → `collectRemoteItems(opts, map[string]bool{"lab-mono": true}, probe, noRestore)`
- `TestCollectRemoteItemsEmptyHosts` (line ~193): `collectRemoteItems(nil, nil, nil)` → `collectRemoteItems(nil, nil, nil, nil)` (the empty-hosts early return never touches either probe, so `nil` here is fine and matches the existing style of that one test)
- `TestRemoteItemsRowCountStableAcrossProbe` (line ~276): `collectRemoteItems(opts, nil, probe)` → `collectRemoteItems(opts, nil, probe, noRestore)`

- [ ] **Step 4: Update the 3 pre-existing call sites in `picker/tui_test.go`**

All three (lines ~645, ~674, ~699) are `collectRemoteItems(opts, nil, probe)` → `collectRemoteItems(opts, nil, probe, noRestore)`.

- [ ] **Step 5: Update the production call site in `picker/tui.go`**

Line 1292, inside `remoteCmd`:

```go
			return remoteMsg{items: collectRemoteItems(opts, local, nil)}
```

becomes:

```go
			return remoteMsg{items: collectRemoteItems(opts, local, nil, nil)}
```

- [ ] **Step 6: Run the full picker test suite to confirm it compiles and every existing test still passes**

Run: `cd picker && go build ./... && go test ./... -v`
Expected: all PASS, no behavior change (no restorable rows appear yet — `noRestore`/production `sshListRestorableSessions` never finds sessions unless a real host answers, and `TestCollectRemoteItemsNoServerRow` still asserts exactly 2 items / no restorable rows).

- [ ] **Step 7: Commit**

```bash
git add picker/remote.go picker/remote_test.go picker/tui_test.go picker/tui.go
git commit -m "refactor(picker): thread a restoreProbe through collectRemoteItems"
```

---

### Task 6: Render restorable rows

**Files:**
- Modify: `picker/remote.go` (the render loop inside `collectRemoteItems`)
- Test: `picker/remote_test.go`

- [ ] **Step 1: Write the failing tests**

```go
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
	// restore; see Task 7.
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd picker && go test ./... -run TestCollectRemoteItemsRestorableSessions -v`
Expected: FAIL — `TestCollectRemoteItemsRestorableSessions` gets `len(items) == 2`, not 3 (no restorable rows rendered yet); `row.remoteRestore` build error is avoided because the field doesn't exist yet either — this will actually be a **compile** failure (`row.remoteRestore` undefined) until Task 7 adds the field. Do Step 2a below first.

- [ ] **Step 2a: Add the `remoteRestore` field now (pulled forward from Task 7) so this test can compile**

In `picker/tui.go`, the `listItem` struct currently ends with:

```go
	displayEnd      string // remote session row: display with the closing tree glyph
	plainEnd        string // remote session row: plain with the closing tree glyph
}
```

Change to:

```go
	displayEnd      string // remote session row: display with the closing tree glyph
	plainEnd        string // remote session row: plain with the closing tree glyph
	remoteRestore   bool   // remote bridge row: sourced from a tmux-remux snapshot, not a live probe — bridging must restore it first
}
```

Run: `cd picker && go build ./...` — expected success (field is unused by production logic until Task 6 Step 3, which is fine, it's a struct field, not a func).

Now run: `cd picker && go test ./... -run TestCollectRemoteItemsRestorableSessions -v`
Expected: FAIL — assertion failures (`len(items) != 3`, `remoteRestore` false, no "ago"/"restores" substrings), not a compile error.

- [ ] **Step 3: Implement the render loop change**

In `picker/remote.go`, inside `collectRemoteItems`, the current per-host render loop is:

```go
	for _, r := range results {
		note := ""
		switch r.state {
		case remoteProbeUnreachable:
			// The host may be back up by the time it is picked.
			note = "(unreachable — open default)"
		case remoteProbeNoServer:
			// The launcher cold-starts the host's own startup session (#287).
			note = "(no server — Enter starts one)"
		default:
			if len(r.sess) == 0 {
				note = "(all open)"
			}
		}
		items = append(items, remoteHostRowItem(tmuxOpts, r.host, note))
		for _, sess := range r.sess {
			items = append(items, listItem{
				isRemoteRow: true,
				target:      "remote:" + r.host + ":" + sess,
				remoteHost:  r.host,
				remoteSess:  sess,
				display:     cDim + remoteTreeMid + reset + " " + sess,
				displayEnd:  cDim + remoteTreeEnd + reset + " " + sess,
				plain:       remoteTreeMid + " " + sess,
				plainEnd:    remoteTreeEnd + " " + sess,
				searchText:  r.host + "/" + sess + " " + r.host + " " + sess,
			})
		}
	}
	return items
}
```

Replace it with:

```go
	for _, r := range results {
		note := ""
		switch r.state {
		case remoteProbeUnreachable:
			// The host may be back up by the time it is picked.
			note = "(unreachable — open default)"
		case remoteProbeNoServer:
			// The launcher cold-starts the host's own startup session (#287).
			// The host row itself never restores — it carries no
			// remoteSess/remoteRestore either way — so its note doesn't
			// change when restorable child rows exist below it; those rows
			// carry their own "(restore — saved …)" suffix instead.
			note = "(no server — Enter starts one)"
		default:
			if len(r.sess) == 0 {
				note = "(all open)"
			}
		}
		items = append(items, remoteHostRowItem(tmuxOpts, r.host, note))
		for _, sess := range r.sess {
			items = append(items, listItem{
				isRemoteRow: true,
				target:      "remote:" + r.host + ":" + sess,
				remoteHost:  r.host,
				remoteSess:  sess,
				display:     cDim + remoteTreeMid + reset + " " + sess,
				displayEnd:  cDim + remoteTreeEnd + reset + " " + sess,
				plain:       remoteTreeMid + " " + sess,
				plainEnd:    remoteTreeEnd + " " + sess,
				searchText:  r.host + "/" + sess + " " + r.host + " " + sess,
			})
		}
		for _, s := range r.restorable {
			suffix := "  (restore — saved " + formatSnapshotAge(r.manifestSavedAt, time.Now()) + ")"
			items = append(items, listItem{
				isRemoteRow:   true,
				target:        "remote:" + r.host + ":" + s.Name,
				remoteHost:    r.host,
				remoteSess:    s.Name,
				remoteRestore: true,
				display:       cDim + remoteTreeMid + reset + " " + s.Name + cDim + suffix + reset,
				displayEnd:    cDim + remoteTreeEnd + reset + " " + s.Name + cDim + suffix + reset,
				plain:         remoteTreeMid + " " + s.Name + suffix,
				plainEnd:      remoteTreeEnd + " " + s.Name + suffix,
				searchText:    r.host + "/" + s.Name + " " + r.host + " " + s.Name,
			})
		}
	}
	return items
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd picker && go test ./... -v`
Expected: all PASS, including both new tests and every pre-existing test in the package.

- [ ] **Step 5: Commit**

```bash
git add picker/remote.go picker/remote_test.go picker/tui.go
git commit -m "feat(picker): render restorable sessions from a serverless host's snapshot"
```

---

### Task 7: Thread the restore flag through activation

**Files:**
- Modify: `picker/remote.go` (`openRemoteBridge`)
- Modify: `picker/tui.go` (`activateCurrent`)

The `remoteRestore` field already exists (added in Task 6 Step 2a). This task makes activating such a row actually request the restore path.

- [ ] **Step 1: Update `openRemoteBridge`'s signature and body**

In `picker/remote.go`, the current function is:

```go
func openRemoteBridge(host, sess string) error {
	args := []string{host}
	if sess != "" {
		args = append(args, sess)
	}
	cmd := exec.Command("lztmux-remote-open", args...)
	cmd.Stdout = os.Stderr
	// Captured, not inherited: the picker owns the screen, so a failure has to
	// come back as a string for the hint line rather than paint over the popup.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := lastNonEmptyLine(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}
```

Change to:

```go
// openRemoteBridge shells out to lztmux-remote-open, which does the actual
// ssh + bridge-daemon work. restore signals a row built from a tmux-remux
// snapshot rather than a live probe (#268): the session doesn't exist on the
// remote yet, so the launcher must restore it before there's anything to
// bridge into.
func openRemoteBridge(host, sess string, restore bool) error {
	args := []string{host}
	if sess != "" {
		args = append(args, sess)
	}
	cmd := exec.Command("lztmux-remote-open", args...)
	if restore {
		cmd.Env = append(os.Environ(), "LZTMUX_REMOTE_RESTORE=1")
	}
	cmd.Stdout = os.Stderr
	// Captured, not inherited: the picker owns the screen, so a failure has to
	// come back as a string for the hint line rather than paint over the popup.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := lastNonEmptyLine(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}
```

- [ ] **Step 2: Update the one call site in `picker/tui.go`**

In `activateCurrent` (around line 949):

```go
	if item.remoteHost != "" {
		if err := openRemoteBridge(item.remoteHost, item.remoteSess); err != nil {
```

becomes:

```go
	if item.remoteHost != "" {
		if err := openRemoteBridge(item.remoteHost, item.remoteSess, item.remoteRestore); err != nil {
```

- [ ] **Step 3: Run the full test suite**

Run: `cd picker && go build ./... && go test ./... -v`
Expected: all PASS. (`openRemoteBridge` has no direct unit test in this package — like `sshListRemoteSessions`, it shells out — so there is nothing new to assert here beyond compiling and the existing suite staying green.)

- [ ] **Step 4: Commit**

```bash
git add picker/remote.go picker/tui.go
git commit -m "feat(picker): pass the restore flag from a snapshot row to lztmux-remote-open"
```

---

### Task 8: Extract `start_remote_server()` in the launcher (pure refactor)

This isolates the one piece of cold-start logic that Task 9 needs to reuse, with **zero behavior change** — verified by running the existing bats suite before touching the new restore branch.

**Files:**
- Modify: `scripts/lztmux-remote-open.sh`

- [ ] **Step 1: Run the existing bats suite to record the baseline**

Run: `cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-268-feat-picker-list-restorable-remote-sessi && bats tests/remote-cold-start.bats`
Expected: all existing tests PASS (this is your regression baseline — re-run after Step 2).

- [ ] **Step 2: Extract the function**

The current block in `scripts/lztmux-remote-open.sh` (right after `first_remote_session`'s definition) is:

```bash
if [[ -z $sess ]]; then
	sess="$(first_remote_session)"
fi

# Nothing to bridge: start the host's OWN startup session rather than inventing
# a name here — the remote's tmux-startup unit carries its configured session
# name and directory. Starting it blind is safe: the systemd unit is
# Type=forking with RemainAfterExit, the launchd agent is RunAtLoad, and both
# scripts exact-match `has-session` before creating anything. Then re-probe,
# because unit state is not server state — a live server can sit behind an
# `inactive` unit (#287).
if [[ -z $sess ]]; then
	if [[ $remote_os == Darwin ]]; then
		# The launchd agent mirrors tmux-startup.service on macOS; kickstart
		# runs a RunAtLoad agent on demand.
		start_cmd=(launchctl kickstart "gui/$remote_uid/org.nix-community.home.tmux-startup")
		start_desc="tmux-startup launchd agent"
	else
		start_cmd=(systemctl --user start tmux-startup.service)
		start_desc="tmux-startup.service"
	fi
	if ! ssh "$host" -- "${start_cmd[@]}"; then
		echo "lztmux-remote-open: $host has no tmux server, and no $start_desc to start one" >&2
		exit 1
	fi
	sess="$(first_remote_session)"
	if [[ -z $sess ]]; then
		echo "lztmux-remote-open: started $start_desc on $host but no session appeared" >&2
		exit 1
	fi
fi
```

Replace it with:

```bash
# Starts the host's OWN startup session — the remote's tmux-startup unit
# carries its configured session name and directory, so nothing is invented
# here. Starting it blind is safe: the systemd unit is Type=forking with
# RemainAfterExit, the launchd agent is RunAtLoad, and both scripts
# exact-match `has-session` before creating anything. Callers must always
# re-probe with first_remote_session afterwards rather than trust this
# returning cleanly — unit state is not server state, a live server can sit
# behind an `inactive` unit (#287). Exits the whole script on failure: a cold
# start is a fatal precondition for every caller.
start_remote_server() {
	if [[ $remote_os == Darwin ]]; then
		# The launchd agent mirrors tmux-startup.service on macOS; kickstart
		# runs a RunAtLoad agent on demand.
		start_cmd=(launchctl kickstart "gui/$remote_uid/org.nix-community.home.tmux-startup")
		start_desc="tmux-startup launchd agent"
	else
		start_cmd=(systemctl --user start tmux-startup.service)
		start_desc="tmux-startup.service"
	fi
	if ! ssh "$host" -- "${start_cmd[@]}"; then
		echo "lztmux-remote-open: $host has no tmux server, and no $start_desc to start one" >&2
		exit 1
	fi
}

if [[ -z $sess ]]; then
	sess="$(first_remote_session)"
fi

if [[ -z $sess ]]; then
	start_remote_server
	sess="$(first_remote_session)"
	if [[ -z $sess ]]; then
		echo "lztmux-remote-open: started $start_desc on $host but no session appeared" >&2
		exit 1
	fi
fi
```

(`start_desc` is set as a plain, non-`local` variable inside `start_remote_server`, exactly like the original inline code — it stays visible to the caller's error message afterward, matching this script's existing convention of not using `local`.)

- [ ] **Step 3: Re-run the bats suite to confirm zero behavior change**

Run: `bats tests/remote-cold-start.bats`
Expected: identical PASS results to Step 1 — same test count, same outcomes. In particular `"cold start: no server -> starts the unit, re-probes, bridges what it finds"` must still see exactly 2 `list-sessions` probes, and `"cold start: an explicit session argument skips both the probe and the unit"` must still see zero `list-sessions`/`systemctl` calls.

- [ ] **Step 4: shellcheck + shfmt**

Run: `shellcheck scripts/lztmux-remote-open.sh` and `shfmt -d scripts/lztmux-remote-open.sh` (this repo's shfmt uses tabs).
Expected: no warnings; no diff (or apply `shfmt -w scripts/lztmux-remote-open.sh` if it reports a diff).

- [ ] **Step 5: Commit**

```bash
git add scripts/lztmux-remote-open.sh
git commit -m "refactor(remote-open): extract start_remote_server for reuse"
```

---

### Task 9: The restore branch in the launcher

**Files:**
- Modify: `scripts/lztmux-remote-open.sh`

- [ ] **Step 1: Add the new branch**

Immediately after the cold-start block from Task 8 (still before the `if [[ -z $win ]]; then` block that resolves the window), add:

```bash
# The picker's row came from a tmux-remux snapshot, not a live probe (#268):
# the named session may not exist on the remote yet. Only entered when the
# caller explicitly asked for a restore — a plain live-session attach (the
# common case) takes none of these extra round trips.
if [[ -n ${LZTMUX_REMOTE_RESTORE:-} && -n $sess ]]; then
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux has-session -t $(shell_quote "=$sess")" 2>/dev/null; then
		if [[ -z "$(first_remote_session)" ]]; then
			start_remote_server
		fi
		remote_remux="$(ssh "$host" 'command -v tmux-remux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux-remux')"
		# Bypasses the remote's own restoreMode=off gate (config/tmux.conf.nix's
		# `restore --auto`) on purpose: the user directly asked for this
		# session, not merely for the server to start.
		# tmux-remux shells out to the bare `tmux` binary name (it doesn't know
		# the store path we just resolved), so it needs that directory on its
		# PATH — the same non-interactive-ssh-PATH problem $remote_tmux above
		# already had to work around.
		# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
		if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir PATH=$(dirname "$remote_tmux"):\$PATH $remote_remux restore"; then
			echo "lztmux-remote-open: tmux-remux restore failed on $host" >&2
			exit 1
		fi
		# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
		if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux has-session -t $(shell_quote "=$sess")" 2>/dev/null; then
			# tmux-remux restore can exit 0 having restored nothing: its own
			# smart filter (idle-shells-only sessions, or sessions/snapshots
			# past its age ceiling) runs regardless of what the picker listed
			# (see the design doc's "Restore filter mismatch" section) — name
			# that as the likely cause instead of a bare "not found".
			echo "lztmux-remote-open: session '$sess' was not restored on $host — tmux-remux's restore filter may have skipped it (idle shells / stale age)" >&2
			exit 1
		fi
	fi
fi
```

- [ ] **Step 2: shellcheck + shfmt**

Run: `shellcheck scripts/lztmux-remote-open.sh` and `shfmt -d scripts/lztmux-remote-open.sh`
Expected: no warnings; no diff (or apply `shfmt -w`).

- [ ] **Step 3: Commit**

```bash
git add scripts/lztmux-remote-open.sh
git commit -m "feat(remote-open): restore a tmux-remux snapshot before bridging a restorable row"
```

(Tests for this branch come next, in Task 10 — kept as a separate commit so a reviewer can read the implementation and its tests independently.)

---

### Task 10: Bats tests for the restore path

**Files:**
- Modify: `tests/remote-cold-start.bats`

- [ ] **Step 1: Extend the fake `ssh` stub's fixtures**

In `setup()`, add one more exported variable alongside the existing `REMOTE_SERVER`/`REMOTE_SESSION`:

```bash
	export RESTORE_MARKER="$BATS_TEST_TMPDIR/restored"
```

(placed right after `export REMOTE_SESSION="workstation"`)

- [ ] **Step 2: Add new `case` branches to the fake `ssh` script, ahead of the existing `command -v tmux` branch**

The existing fake `ssh` script's `case "$*" in` currently starts with:

```bash
			case "$*" in
			*"command -v tmux"*) echo /usr/bin/tmux ;;
			*"uname -s; id -u"*) printf '%s\n%s\n' "${FAKE_UNAME:-Linux}" 1000 ;;
```

**This must change to put the more specific `tmux-remux` pattern first** — `"command -v tmux-remux"` contains `"command -v tmux"` as a substring, so the existing generic branch would otherwise swallow it:

```bash
			case "$*" in
			*"command -v tmux-remux"*) echo /usr/bin/tmux-remux ;;
			*"tmux-remux restore"*)
				if [ -n "${FAKE_RESTORE_FAILS:-}" ]; then
					echo "restore: boom" >&2
					exit 1
				fi
				if [ -z "${RESTORE_TARGET_MISMATCH:-}" ]; then
					touch "$RESTORE_MARKER"
				fi
				;;
			*"has-session -t '=workstation'"*)
				[ -f "$REMOTE_SERVER" ] && exit 0
				exit 1
				;;
			*"has-session -t '=work'"*)
				[ -f "$RESTORE_MARKER" ] && exit 0
				exit 1
				;;
			*"command -v tmux"*) echo /usr/bin/tmux ;;
			*"uname -s; id -u"*) printf '%s\n%s\n' "${FAKE_UNAME:-Linux}" 1000 ;;
```

(the rest of the `case` — `systemctl --user start`, `launchctl kickstart`, `list-sessions`, `list-windows` — is unchanged)

- [ ] **Step 3: Add the new test cases at the end of the file**

```bash
@test "restore: requested session isn't live -> cold starts, restores, bridges" {
	export LZTMUX_REMOTE_RESTORE=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 0 ]

	grep -q 'systemctl --user start tmux-startup.service' "$SSH_LOG"
	grep -q "has-session -t '=work'" "$SSH_LOG"
	grep -q 'tmux-remux restore' "$SSH_LOG"
	# Guards against dropping the PATH= that lets tmux-remux find the bare
	# `tmux` binary it execs — the fake `command -v tmux` above resolves to
	# /usr/bin/tmux, so the restore command must carry /usr/bin on PATH.
	grep -q 'PATH=/usr/bin:.*tmux-remux restore' "$SSH_LOG"
	grep -q 'new-session -d -s tp-g6-work' "$TMUX_LOG"
	grep -q 'switch-client -t =tp-g6-work' "$TMUX_LOG"
}

@test "restore: server already running but session missing -> restores without a cold start" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 0 ]

	run grep -c systemctl "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q "has-session -t '=work'" "$SSH_LOG"
	grep -q 'tmux-remux restore' "$SSH_LOG"
	grep -q 'switch-client -t =tp-g6-work' "$TMUX_LOG"
}

@test "restore: tmux-remux restore failing surfaces an error and bridges nothing" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1
	export FAKE_RESTORE_FAILS=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 1 ]
	[[ $output == *"restore failed"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "restore: session absent even after a successful restore fails loudly" {
	touch "$REMOTE_SERVER"
	export LZTMUX_REMOTE_RESTORE=1
	export RESTORE_TARGET_MISMATCH=1

	run bash "$LAUNCHER" tp-g6 work
	[ "$status" -eq 1 ]
	[[ $output == *"tmux-remux's restore filter may have skipped it"* ]]

	run grep -c new-session "$TMUX_LOG"
	[ "$status" -ne 0 ]
}

@test "restore: a live session attach with the flag unset is unaffected" {
	touch "$REMOTE_SERVER"
	# LZTMUX_REMOTE_RESTORE intentionally unset.

	run bash "$LAUNCHER" tp-g6 workstation
	[ "$status" -eq 0 ]

	run grep -cE 'has-session|tmux-remux' "$SSH_LOG"
	[ "$status" -ne 0 ]
	grep -q 'switch-client -t =tp-g6-workstation' "$TMUX_LOG"
}
```

- [ ] **Step 4: Run the full bats suite**

Run: `bats tests/remote-cold-start.bats`
Expected: all tests PASS, including the 9 pre-existing ones (unmodified) and the 5 new ones.

- [ ] **Step 5: shellcheck the test file**

Run: `shellcheck tests/remote-cold-start.bats`
Expected: no new warnings (the file already carries `# shellcheck disable=SC2030,SC2031` at the top for bats' subshell semantics).

- [ ] **Step 6: Commit**

```bash
git add tests/remote-cold-start.bats
git commit -m "test(remote-open): cover the restore path in lztmux-remote-open"
```

---

### Task 11: Design record

**Files:**
- Create: `docs/superpowers/specs/2026-08-08-restorable-remote-sessions-design.md`

- [ ] **Step 1: Write the design doc**

```markdown
# Restorable Remote Sessions — Design Notes (#268)

## Host verification

`tmux-remux`'s `Manifest.Host` (and the mirrored `store.Event.Host` column) is
whatever `os.Hostname()` returned on the machine that wrote the snapshot — not
necessarily the ssh `Host` alias used to reach it (`@remote_bridge_hosts` can
alias, tunnel, or shadow a real hostname). `sshListRestorableSessions` fetches
the remote's own `hostname` output in the same ssh round trip as the manifest
and rejects a mismatch outright, rather than trusting that whichever `state.db`
answered belongs to the host the picker thinks it's talking to.

## Throwaway-session filter

Chosen signal: `session_last_attached == 0` (tmux's own "no client ever
attached" value, confirmed in `internal/tmux/parse.go` of `noamsto/tmux-remux`).
This is principled rather than a name denylist (`probe-verify` was the specific
symptom, not the rule) — any session created and abandoned without a human
ever attaching reads the same way, regardless of what it happens to be called.

## Why `tmux-remux restore` bypasses `restoreMode=off`

`RestoreCmd.Run` only respects the `off` gate when invoked with `--auto`
(`if cfg.RestoreMode == config.RestoreOff && c.Auto { return nil }`). That gate
exists to keep *automatic* restore-on-server-start opt-in. Activating a
restorable row from the picker is an explicit, one-off user action, not the
server's own boot sequence — so `lztmux-remote-open` calls the bare `restore`
(no `--auto`), which always attempts the restore regardless of the remote's
configured mode.

## Why the restore branch is env-var-gated, not always-on

`tests/remote-cold-start.bats`'s `"cold start: an explicit session argument
skips both the probe and the unit"` test locks in that a live-session attach
(the common case: the picker already knows the session is live from its own
prior probe) takes zero extra ssh round trips. Since the script alone cannot
tell "this name came from a live probe" from "this name came from a snapshot"
without probing — and probing unconditionally would violate that test's
invariant — the picker signals it explicitly via `LZTMUX_REMOTE_RESTORE=1`,
set only on rows built from `sshListRestorableSessions` (`remoteRestore: true`
in `picker/tui.go`'s `listItem`).

## Restore filter mismatch (known limitation)

The picker lists every non-throwaway session in the newest snapshot
(`last_attached != 0`). It does **not** replicate `tmux-remux restore`'s own
smart filter (`internal/filter.Filter`, wired up in `RestoreCmd.Run`): by
default that filter also drops sessions whose windows are all idle plain
shells (`RestoreSkipIdleShells`/`RestoreSkipIdleWindows`), sessions older than
`RestoreMaxSessionAge` (14d default), and entire snapshots older than
`RestoreMaxSnapshotAge` (30d default).

This isn't an oversight: `internal/filter` lives under `tmux-remux`'s own
`internal/` tree, so Go's internal-import rule makes it unimportable from this
repo's separate `github.com/noamsto/lazytmux/picker` module. Reimplementing
the filter here would duplicate upstream logic that can silently drift
(defaults, new flags), and even a faithful copy couldn't see the remote's
*actual* configured overrides without another ssh round trip (`tmux-remux`
has no `config show`/`restore --dry-run` to query them). Given that, this plan
does not attempt to pre-filter rows to match what `restore` would keep.

The consequence: pressing Enter on a listed session can restore nothing if
`tmux-remux restore`'s own filter would have skipped it. `lztmux-remote-open`
makes this honest rather than silent — when the session is still missing
after a successful `restore` call, it fails with a message naming the filter
as the likely cause (`tmux-remux's restore filter may have skipped it (idle
shells / stale age)`) instead of a bare "not found".

## #267 and scope

#267 (`feat(module): startupSession.headless for GUI-less hosts`) shipped
before this issue was picked up. It reduces how often a remote goes fully
serverless (a systemd/launchd unit can now stay up without a GUI session) but
doesn't eliminate the case — a fresh boot before the unit starts, a disabled
persist module, or manual maintenance all still produce a serverless remote
with snapshots on disk. Scope was kept at the full listing + restore path
(not trimmed to listing-only); see the crew-bus message sent alongside this
work for the explicit call-out.
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-08-08-restorable-remote-sessions-design.md
git commit -m "docs: design notes for restorable remote sessions (#268)"
```

---

### Task 12: Crew-bus note about #267

**Files:** none (communication only)

- [ ] **Step 1: Send the FYI message**

Per the issue's explicit instruction ("If you think the scope should shrink, ask on the crew bus rather than deciding alone"), post a status update rather than silently deciding. Run (adjust `<crew-id>` / `<branch>` to the actual values from `WORKER_TASK.md` — `crew_id: 1786128840-73323`, branch `feat/268-feat-picker-list-restorable-remote-sessi`):

```bash
crew msg worker:feat/268-feat-picker-list-restorable-remote-sessi dispatcher:1786128840-73323 "268: #267 (startupSession.headless) is closed/merged already. Keeping full scope (listing + restore path, not listing-only) — a headless unit reduces how often a remote goes fully serverless but doesn't eliminate it (fresh boot before the unit starts, disabled persist module, manual maintenance). Noted in docs/superpowers/specs/2026-08-08-restorable-remote-sessions-design.md and will call it out in the PR body. Flag if you want listing-only instead."
```

- [ ] **Step 2: No commit for this step** (communication, not a file change).

---

### Task 13: Full verification gate

**Files:** none (verification only)

- [ ] **Step 1: Run the full Go test suite**

Run: `cd picker && go test ./... -v`
Expected: all PASS.

- [ ] **Step 2: Run the full bats suite**

Run: `cd /Users/noams/Data/git/.worktrees/git/lazytmux/feat-268-feat-picker-list-restorable-remote-sessi && bats tests/remote-cold-start.bats`
Expected: all PASS (14 tests: 9 pre-existing + 5 new).

- [ ] **Step 3: Run the three-command build/lint/check gate**

Run, in order:

```bash
nix build .#default
nix flake check
nix build .#lint
```

Expected: all three succeed. If `nix build .#default` fails on something unrelated to this change, stop and investigate before proceeding — don't paper over an unrelated break.

- [ ] **Step 4: Manual verification against `tp-g6` — best effort, report honestly**

Attempt this only if `tp-g6` is actually reachable from this machine right now (`ssh tp-g6 true`). If it is:

1. Ensure `tp-g6` has no live tmux server (`ssh tp-g6 "TMUX_TMPDIR=/run/user/\$(id -u) tmux list-sessions"` should fail) and has at least one attached-to session in its newest snapshot (`ssh tp-g6 tmux-remux list --json | tail -5` — look for a `snapshot` event with a session whose `last_attached` is non-zero; if the only snapshot on disk is the `probe-verify` one, seed a real one first: attach a session on `tp-g6`, do something in it, then kill the server so it's serverless again — `tmux-remux` saves on structural change and via its own timer).
2. Build and open the picker (`nix build .#default`, run the wrapped `tmux`, `prefix + s`).
3. Confirm the Remote section shows `tp-g6` with a tree row for the real (non-throwaway) session, tagged with its age.
4. Press Enter on that row; confirm it lands you on a live, bridged mirror of that session on `tp-g6` with its restored panes/cwd.

If `tp-g6` is not reachable, or you don't perform this check, **say so explicitly in the PR body** — state exactly which of Steps 1-4 above you ran versus reasoned about from the code and the bats/Go tests. Do not claim a manual repro that wasn't actually run, and do not fabricate a serverless remote to "verify" against.

- [ ] **Step 5: No commit for this task** (verification only — any fixes it surfaces get their own commit against the relevant task above).

---

### Task 14: Open the PR

**Files:** none (git/GitHub operations only)

- [ ] **Step 1: Push the branch**

```bash
git push -u origin feat/268-feat-picker-list-restorable-remote-sessi
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create --assignee @me --title "feat(picker): list restorable remote sessions from tmux-remux snapshots" --body "$(cat <<'EOF'
Closes #268

## What

When a remote host in the picker's Remote section is reachable but serverless (#269), its newest tmux-remux snapshot is now read (`tmux-remux list --json`, no tmux server needed) and rendered as selectable "restore" rows — one per non-throwaway session, tagged with how long ago it was saved. Pressing Enter on one starts the remote's server if needed, runs `tmux-remux restore` (bypassing the remote's own `restoreMode=off` gate — this is an explicit user action, not the server's own boot sequence), and bridges in.

## Design decisions (see docs/superpowers/specs/2026-08-08-restorable-remote-sessions-design.md)

- Only the single **newest** snapshot event is consulted — a snapshot is point-in-time state, not an append-only list.
- The manifest's `Host` field is verified against the hostname the ssh probe actually reached, so a stale/wrong-host `state.db` can't produce rows.
- Throwaway sessions (never attached — `session_last_attached == 0`, tmux's own signal) are filtered out. This is principled, not a `probe-verify` denylist.
- `lztmux-remote-open` only takes the new restore path when the picker explicitly signals it (`LZTMUX_REMOTE_RESTORE=1`) — the existing live-session attach path (the common case) is unchanged, verified by the pre-existing "skips both the probe and the unit" bats test staying green untouched.
- **Known limitation:** listed rows aren't pre-filtered by `tmux-remux restore`'s own smart filter (idle-shell sessions, `RestoreMaxSessionAge`/`RestoreMaxSnapshotAge`) — that logic lives in an internal package of a separate Go module and isn't importable here. Enter can restore nothing if the filter would have skipped the session; the launcher names the filter as the likely cause instead of failing silently. See the design doc's "Restore filter mismatch" section.

## #267 status

#267 (`feat(module): startupSession.headless for GUI-less hosts`) is already closed/merged. It reduces how often a remote goes fully serverless but doesn't eliminate the case (fresh boot before the unit starts, disabled persist module, manual maintenance). Scope was kept at the full listing + restore path rather than trimmed to listing-only; flagged on the crew bus alongside this PR.

## Testing

- New Go unit tests: manifest parsing (`newestSnapshotManifest`), the throwaway filter, age formatting, host verification (`restorableFromProbeOutput`'s table test — matching host, mismatched host, no newline, snapshot-free log), and `collectRemoteItems` rendering restorable rows.
- New bats tests in `tests/remote-cold-start.bats` covering the restore branch: cold-start-then-restore, restore-without-cold-start, restore failure, post-restore session still missing, and confirming the unrelated live-attach path takes zero extra ssh calls.
- `nix build .#default`, `nix flake check`, `nix build .#lint` all green.
- Manual verification against `tp-g6`: <fill in exactly what you ran per Task 13 Step 4 — do not claim more than you actually did>.
EOF
)"
```

- [ ] **Step 2: Report the PR URL** and post the `pr_open` status per the worker protocol (`crew msg worker:feat/268-feat-picker-list-restorable-remote-sessi dispatcher:1786128840-73323 ...` per the existing `status`/`pr_open` convention visible in `.git/crew/events.jsonl`, if this run is orchestrated by the dispatcher).

---

## Self-review notes

- **Spec coverage:** newest-snapshot-only (Task 1), host verification unit-tested against a pure function rather than only the live-gated ssh path (Task 4's `restorableFromProbeOutput` table test, design doc), snapshot age surfaced (Task 3/6), throwaway filter (Task 2), new restore path extending #287's cold start without duplicating it (Task 8/9), live-attach path unaffected (Task 8 Step 3, Task 10's last test), #267 status handled via crew-bus rather than silent descope (Task 12), testing requirements from `WORKER_TASK.md` (Go unit tests in Tasks 1-4/6 — including host matching, not just the env-gated live test — bats in Task 10, shellcheck/shfmt in Tasks 8-9, full three-command gate in Task 13, honest manual-verification reporting in Task 13 Step 4), plan doc committed alongside the code (this file) plus a design doc (Task 11).
- **Scope boundary respected:** touches only `picker/remote.go`, `picker/remote_test.go`, `picker/remote_live_test.go`, `scripts/lztmux-remote-open.sh`, `tests/remote-cold-start.bats`, plus the minimum possible in `picker/tui.go` (one struct field, two one-line call-site updates) and `picker/tui_test.go` (three mechanical call-site updates) — both called out explicitly as coordination touchpoints with the #229 worker. Never touches `picker/render_list.go`, `picker/agentdetect/**`, `scripts/lib-claude.sh`, or `scripts/tmux-update-icons.sh`.
- **Known limitation, stated rather than hidden:** the picker's restorable-row list isn't pre-filtered by `tmux-remux restore`'s own smart filter (idle-shell sessions, age ceilings) — that filter is in an unimportable internal package of a separate Go module, and duplicating it risks drift plus can't see the remote's actual config overrides anyway. Enter can therefore restore nothing for a listed row; the launcher's failure message names the filter as the likely cause (Task 9), the design doc explains why (Task 11), and the PR body calls it out (Task 14) instead of the Background bullet's earlier, incomplete "skips sessions already running" characterization.
- **Host row vs. child rows:** the no-server host row's note stays "(no server — Enter starts one)" even when restorable child rows exist beneath it (Task 6) — the host row itself carries no `remoteSess`/`remoteRestore` and would silently take the plain cold-start path if its note claimed otherwise (Task 4 finding). Only the child rows built from `r.restorable` carry `remoteRestore: true` and the "(restore — saved …)" suffix.
