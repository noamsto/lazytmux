package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// remoteProbeTimeout bounds each per-host ssh list-sessions probe so a down
// host cannot stall the session picker's async remote section.
const remoteProbeTimeout = 3 * time.Second

// remoteListSessionsCmd lists remote tmux sessions under the same TMUX_TMPDIR /
// binary resolution as lztmux-remote-open. Must stay fish-safe: no `var=value`
// shell assignments — fish login shells reject them and the picker would mark
// a reachable host unreachable.
const remoteListSessionsCmd = `env TMUX_TMPDIR=/run/user/$(id -u) $(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux) list-sessions -F '#{session_name}' 2>/dev/null`

// sshConnectFailureExit is ssh's own exit status when it cannot reach the host;
// any other non-zero status is the remote command's, passed through verbatim.
const sshConnectFailureExit = 255

// A probe failure means one of two very different things, and rendering both as
// "unreachable" sends you debugging the network when the host is fine (#266).
var (
	errRemoteUnreachable = errors.New("remote unreachable")
	errRemoteNoServer    = errors.New("remote tmux server not running")
)

// remoteProbeState is what one host's probe established: reachable with
// sessions, reachable with no tmux server to bridge, or not reachable at all.
type remoteProbeState int

const (
	remoteProbeOK remoteProbeState = iota
	remoteProbeNoServer
	remoteProbeUnreachable
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
// own, so the host answered and only its tmux server is missing.
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
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// collectRemoteItems builds the "Remote" suggestion rows (header + hosts /
// sessions). Runs off the first-paint path; merges via remoteMsg.
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

	cPeach := ansiFg(envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387"))
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)
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
	rule := "── Remote " + strings.Repeat("─", 220)
	items = append(items, listItem{
		display:        cDim + rule + reset,
		plain:          rule,
		isHeader:       true,
		isRemoteHeader: true,
	})

	anyRow := false
	for _, r := range results {
		switch r.state {
		case remoteProbeUnreachable:
			// Bare host: open defaults to the remote's most-recent session — the
			// host may be back up by the time it is picked.
			display := fmt.Sprintf("%s %s  %s", cPeach+iSess+reset, r.host, cDim+"(unreachable — open default)"+reset)
			plain := fmt.Sprintf("%s %s  (unreachable — open default)", iSess, r.host)
			items = append(items, listItem{
				target:     "remote:" + r.host,
				remoteHost: r.host,
				display:    display,
				plain:      plain,
				searchText: r.host,
			})
			anyRow = true
			continue
		case remoteProbeNoServer:
			// Reachable, nothing to bridge. Left unselectable (empty target and
			// remoteHost) because opening it would resolve an empty session name.
			display := fmt.Sprintf("%s%s %s  (no tmux server)%s", cDim, iSess, r.host, reset)
			plain := fmt.Sprintf("%s %s  (no tmux server)", iSess, r.host)
			items = append(items, listItem{
				display:    display,
				plain:      plain,
				searchText: r.host,
			})
			anyRow = true
			continue
		}
		if len(r.sess) == 0 {
			// Every session already bridged locally — omit.
			continue
		}
		for _, sess := range r.sess {
			label := r.host + "/" + sess
			display := fmt.Sprintf("%s %s", cPeach+iSess+reset, label)
			plain := fmt.Sprintf("%s %s", iSess, label)
			items = append(items, listItem{
				target:     "remote:" + r.host + ":" + sess,
				remoteHost: r.host,
				remoteSess: sess,
				display:    display,
				plain:      plain,
				searchText: label + " " + r.host + " " + sess,
			})
			anyRow = true
		}
	}
	if !anyRow {
		return nil
	}
	return items
}
