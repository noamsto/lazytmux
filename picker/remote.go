package main

import (
	"bytes"
	"context"
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

const sshConnectFailureExit = 255

var (
	errRemoteUnreachable = errors.New("remote unreachable")
	errRemoteNoServer    = errors.New("remote tmux server not running")
)

type remoteProbeState int

const (
	remoteProbeOK remoteProbeState = iota
	remoteProbeNoServer
	remoteProbeUnreachable
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
		if errors.Is(err, errRemoteNoServer) {
			return nil, remoteProbeNoServer
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

// classifyProbeErr decides which failure a non-zero probe was. ssh exits 255
// when it could not reach the host; any other status is the remote command's
// own, so the host answered and only its tmux server is missing (#266).
func classifyProbeErr(err error, timedOut bool) error {
	if timedOut {
		return fmt.Errorf("%w: probe timed out", errRemoteUnreachable)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() != sshConnectFailureExit {
		return fmt.Errorf("%w: %w", errRemoteNoServer, err)
	}
	return fmt.Errorf("%w: %w", errRemoteUnreachable, err)
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
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, classifyProbeErr(err, ctx.Err() != nil)
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

// openRemoteBridge launches lztmux-remote-open for host[/sess] and returns any
// start error (the script switches the client itself on success).
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
func collectRemoteItems(tmuxOpts map[string]string, localSessionNames map[string]bool, probe func(string) ([]string, error)) []listItem {
	hosts := parseRemoteHosts(envOrMap("REMOTE_BRIDGE_HOSTS", tmuxOpts, "@remote_bridge_hosts", ""))
	if len(hosts) == 0 {
		return nil
	}
	if probe == nil {
		probe = sshListRemoteSessions
	}
	if localSessionNames == nil {
		localSessionNames = map[string]bool{}
	}

	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	reset := "\033[0m"

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

	items := make([]listItem, 0, len(hosts)+1)
	items = append(items, remoteHeaderItem(tmuxOpts))

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
