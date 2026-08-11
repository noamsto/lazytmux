package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// remoteProbeTimeout bounds each per-host ssh list-sessions probe so a down
// host cannot stall the session picker's async remote section.
const remoteProbeTimeout = 3 * time.Second

// remoteListSessionsCmd lists remote tmux sessions under the same TMUX_TMPDIR /
// binary resolution as lztmux-remote-open. The socket dir is OS-dependent:
// /run/user/<uid> on Linux, tmux's default /tmp/tmux-<uid> on macOS (no
// $XDG_RUNTIME_DIR) — try Linux first, then macOS; a missing socket dir fails
// fast, so the wrong guess costs a local stat. Must stay fish-safe: no
// `var=value` shell assignments — fish login shells reject them and the picker
// would mark a reachable host unreachable.
const remoteListSessionsCmd = `env TMUX_TMPDIR=/run/user/$(id -u) $(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux) list-sessions -F '#{session_name}' 2>/dev/null || env TMUX_TMPDIR=/tmp/tmux-$(id -u) $(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux) list-sessions -F '#{session_name}' 2>/dev/null`

// remoteRestorableCmd emits the remote host's own hostname (line 1, used to
// verify a fetched snapshot really belongs to this host) followed by
// tmux-remux's event log as newline-delimited JSON. No tmux server is
// required — tmux-remux reads state.db directly — so this only runs once a
// host has already probed as remoteProbeNoServer. Must stay fish-safe like
// remoteListSessionsCmd: no `var=value` shell assignments.
const remoteRestorableCmd = `hostname; $(command -v tmux-remux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux-remux) list --json 2>/dev/null`

const sshConnectFailureExit = 255

var (
	errRemoteUnreachable    = errors.New("remote unreachable")
	errRemoteNoServer       = errors.New("remote tmux server not running")
	errRemoteNeedsAuth      = errors.New("remote needs interactive authentication")
	errRemoteHostKeyChanged = errors.New("remote host key changed")
)

type remoteProbeState int

const (
	remoteProbeOK remoteProbeState = iota
	remoteProbeNoServer
	remoteProbeUnreachable
	remoteProbeNeedsAuth
	remoteProbeHostKeyChanged
)

// Tree prefixes for a host's session rows; markRemoteTreeEnds decides which
// prefix each row ends up with.
const (
	remoteTreeMid = "├─"
	remoteTreeEnd = "╰─"
)

// parseRemoteHosts splits a whitespace-separated @remote_bridge_hosts value
// into ssh Host aliases. Empty tokens are dropped.
func parseRemoteHosts(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, h := range fields {
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// localBridgeSession is the local mirror name for a remote host+session pair.
func localBridgeSession(host, sess string) string {
	return host + "-" + sess
}

// remoteSessionsForHost probes one host for live tmux session names. On
// remoteProbeOK the returned list is the remote sessions not already bridged
// locally as <host>-<sess> (may be empty when every session is already open).
// An empty list with no error means the remote has no server to list.
func remoteSessionsForHost(host string, localSessions map[string]bool, probe func(string) ([]string, error)) ([]string, remoteProbeState) {
	names, err := probe(host)
	if err != nil {
		switch {
		case errors.Is(err, errRemoteNoServer):
			return nil, remoteProbeNoServer
		case errors.Is(err, errRemoteNeedsAuth):
			return nil, remoteProbeNeedsAuth
		case errors.Is(err, errRemoteHostKeyChanged):
			return nil, remoteProbeHostKeyChanged
		}
		return nil, remoteProbeUnreachable
	}
	if len(names) == 0 {
		return nil, remoteProbeNoServer
	}
	out := make([]string, 0, len(names))
	for _, sess := range names {
		if sess == "" {
			continue
		}
		if localSessions[localBridgeSession(host, sess)] {
			continue
		}
		out = append(out, sess)
	}
	return out, remoteProbeOK
}

// authFailurePatterns are the ssh stderr signatures meaning a human at a real
// terminal could fix this by answering a prompt. Only consulted on exit 255,
// where the failure is ssh's own.
var authFailurePatterns = []string{
	"Host key verification failed",
	// The "(" anchors on ssh's own "Permission denied (publickey,password)."
	// rather than the bare phrase, which also appears in a local firewall's
	// "ssh: connect to host X port 22: Permission denied" (EACCES) — a
	// genuinely down host, not one a prompt could fix.
	"Permission denied (",
	"Too many authentication failures",
	"keyboard-interactive",
}

// hostKeyChangedPattern is ssh's warning that the host key no longer matches
// known_hosts. Kept out of authFailurePatterns deliberately: this is the
// signature of a MITM as much as of a reinstalled host, so it must never reach
// a flow that invites the user to connect. ssh prints it alongside "Host key
// verification failed", so it is matched first.
const hostKeyChangedPattern = "REMOTE HOST IDENTIFICATION HAS CHANGED"

// revokedHostKeyPattern is ssh's refusal banner for a key listed in a
// RevokedHostKeys file. ssh refuses it unconditionally — nothing unsafe can be
// accepted — but it prints no hostKeyChangedPattern alongside it and instead
// follows with a bare "Host key verification failed.", which would otherwise
// fall into authFailurePatterns and invite exactly the "Enter to connect"
// action a revoked key must never offer.
const revokedHostKeyPattern = "REVOKED HOST KEY DETECTED"

// classifyProbeErr decides which failure a non-zero probe was. ssh exits 255
// when it could not reach the host; any other status is the remote command's
// own, so the host answered and only its tmux server is missing (#266). Within
// 255, stderr distinguishes a host that merely wants an interactive answer from
// one that is genuinely down (#357) — an unrecognised 255 stays unreachable, so
// being wrong costs a stale label rather than a pointless password prompt.
func classifyProbeErr(err error, stderr string, timedOut bool) error {
	if timedOut {
		return fmt.Errorf("%w: probe timed out", errRemoteUnreachable)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() != sshConnectFailureExit {
		return fmt.Errorf("%w: %w", errRemoteNoServer, err)
	}
	if strings.Contains(stderr, hostKeyChangedPattern) || strings.Contains(stderr, revokedHostKeyPattern) {
		return fmt.Errorf("%w: %w", errRemoteHostKeyChanged, err)
	}
	for _, p := range authFailurePatterns {
		if strings.Contains(stderr, p) {
			return fmt.Errorf("%w: %w", errRemoteNeedsAuth, err)
		}
	}
	return fmt.Errorf("%w: %w", errRemoteUnreachable, err)
}

// remoteAuthStartFailure classifies the tea.ExecProcess callback error for the
// auth handshake popup. *exec.ExitError means lztmux-remote-auth ran and
// already explained itself — it pauses on any failure it prints before
// returning the pty — so only a *exec.Error (the process never started at
// all, most often a stale PATH after a lazytmux bump until the server
// restarts) has nothing on screen to explain, and that is the only case worth
// surfacing into the status line.
func remoteAuthStartFailure(err error) (string, bool) {
	var startErr *exec.Error
	if errors.As(err, &startErr) {
		return err.Error(), true
	}
	return "", false
}

// sshListRemoteSessions runs the same path/tmpdir resolution as
// lztmux-remote-open so a remote without tmux on the non-interactive PATH still
// lists. Returns session names, or an error wrapping errRemoteUnreachable /
// errRemoteNoServer.
func sshListRemoteSessions(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=2",
		"-T",
		host,
		"--",
		remoteListSessionsCmd,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, classifyProbeErr(err, stderr.String(), ctx.Err() != nil)
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return remuxManifest{}, classifyProbeErr(err, stderr.String(), ctx.Err() != nil)
	}
	return restorableFromProbeOutput(stdout.String())
}

// openRemoteBridge launches lztmux-remote-open for host[/sess] and returns any
// start error (the script switches the client itself on success). restore
// signals a row built from a tmux-remux snapshot rather than a live probe
// (#268): the session doesn't exist on the remote yet, so the launcher must
// restore it before there's anything to bridge into.
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

// lastNonEmptyLine picks the launcher's most specific complaint: ssh and
// systemctl noise comes first, the script's own message last.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// remoteHeaderItem is the "── Remote ──" divider row. Shared by the
// synchronous pending render (Task 2) and the probed result so both agree on
// layout — a pending header must look identical to a resolved one.
func remoteHeaderItem(tmuxOpts map[string]string) listItem {
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	reset := "\033[0m"
	rule := "── Remote " + strings.Repeat("─", 220)
	return listItem{
		display:        cDim + rule + reset,
		plain:          rule,
		isHeader:       true,
		isRemoteHeader: true,
	}
}

// remoteHostRowItem renders one host's row with the given trailing note —
// either a resolved annotation ("(no server — Enter starts one)", …) or
// remotePendingNote before the probe has run. The host row is always
// selectable: it opens the remote's most-recent session and keeps the
// section alive once every session is bridged.
func remoteHostRowItem(tmuxOpts map[string]string, host, note string) listItem {
	cPeach := ansiFg(envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387"))
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)
	reset := "\033[0m"

	display := fmt.Sprintf("%s %s", cPeach+iSess+reset, host)
	plain := fmt.Sprintf("%s %s", iSess, host)
	if note != "" {
		display += "  " + cDim + note + reset
		plain += "  " + note
	}
	return listItem{
		isRemoteRow: true,
		target:      "remote:" + host,
		remoteHost:  host,
		display:     display,
		plain:       plain,
		searchText:  host,
	}
}

// remotePendingNote is the placeholder annotation on a host row before its
// ssh probe returns — a dim ellipsis, since no state (unreachable / no
// server / session list) is known yet (#312).
const remotePendingNote = "…"

// pendingRemoteItems builds the Remote section synchronously from
// @remote_bridge_hosts alone — no ssh, so it belongs on the first-paint
// path. Each host gets exactly the row collectRemoteItems would give it,
// with remotePendingNote standing in for whatever annotation the probe will
// resolve. remoteMsg (collectRemoteItems's result) replaces this slice
// wholesale once every host's probe returns.
func pendingRemoteItems(tmuxOpts map[string]string) []listItem {
	hosts := parseRemoteHosts(envOrMap("REMOTE_BRIDGE_HOSTS", tmuxOpts, "@remote_bridge_hosts", ""))
	if len(hosts) == 0 {
		return nil
	}
	items := make([]listItem, 0, len(hosts)+1)
	items = append(items, remoteHeaderItem(tmuxOpts))
	for _, h := range hosts {
		items = append(items, remoteHostRowItem(tmuxOpts, h, remotePendingNote))
	}
	return items
}

// collectRemoteItems builds the "Remote" suggestion rows (header + hosts /
// sessions) by probing every configured host over ssh. Runs off the
// first-paint path; the result merges via remoteMsg, replacing
// pendingRemoteItems's synchronous placeholder (#312).
func collectRemoteItems(tmuxOpts map[string]string, localSessionNames map[string]bool, probe func(string) ([]string, error), restoreProbe func(string) (remuxManifest, error)) []listItem {
	hosts := parseRemoteHosts(envOrMap("REMOTE_BRIDGE_HOSTS", tmuxOpts, "@remote_bridge_hosts", ""))
	if len(hosts) == 0 {
		return nil
	}
	if probe == nil {
		probe = sshListRemoteSessions
	}
	if restoreProbe == nil {
		restoreProbe = sshListRestorableSessions
	}
	if localSessionNames == nil {
		localSessionNames = map[string]bool{}
	}

	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	reset := "\033[0m"

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

	items := make([]listItem, 0, len(hosts)+1)
	items = append(items, remoteHeaderItem(tmuxOpts))

	for _, r := range results {
		note := ""
		switch r.state {
		case remoteProbeUnreachable:
			// The host may be back up by the time it is picked.
			note = "(unreachable — open default)"
		case remoteProbeNeedsAuth:
			// ssh wants an answer a batch-mode probe can never give (#357).
			note = "(auth needed — Enter to connect)"
		case remoteProbeHostKeyChanged:
			note = "(host key changed — verify manually)"
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
		hostRow := remoteHostRowItem(tmuxOpts, r.host, note)
		switch r.state {
		case remoteProbeNeedsAuth:
			hostRow.remoteNeedsAuth = true
		case remoteProbeHostKeyChanged:
			hostRow.remoteInert = true
		}
		items = append(items, hostRow)
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

// bridgePIDFromFile parses a bridge daemon pidfile's raw contents into a
// validated PID. Pure logic split out for unit testing; malformed or empty
// content returns ok=false rather than erroring, matching how
// scripts/lib-remote.sh's remote_daemon_alive treats an unreadable pidfile.
func bridgePIDFromFile(raw string) (pid int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// stopBridgeDaemon best-effort SIGTERMs the remote-bridge daemon mirroring
// sess, found via the @bridge_sock session option the daemon stamps on
// itself. All failures are swallowed: this is opportunistic cleanup, not a
// required step — lztmux-remote-open.sh's stale-daemon check self-heals on
// the next reopen regardless of whether this signal lands. The daemon's own
// SIGTERM handler already tears itself down (removes its socket/pidfile,
// exits), so this only needs to deliver the signal.
func stopBridgeDaemon(sess string) {
	logEvent("picker", "event", "stop_bridge_daemon", "target", sess)
	out, err := exec.Command("tmux", "display-message", "-p", "-t", sess, "#{@bridge_sock}").Output()
	if err != nil {
		return
	}
	sock := strings.TrimSpace(string(out))
	if sock == "" {
		return
	}
	raw, err := os.ReadFile(sock + ".pid")
	if err != nil {
		return
	}
	pid, ok := bridgePIDFromFile(string(raw))
	if !ok {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	proc.Signal(syscall.SIGTERM) //nolint:errcheck
}
