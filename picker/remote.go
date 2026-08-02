package main

import (
	"bytes"
	"context"
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

// remoteSessionsForHost probes one host for live tmux session names.
// ok is false on probe failure (caller shows a bare host row). When ok is true,
// the returned list is the remote sessions not already bridged locally as
// <host>-<sess> (may be empty when every session is already open).
func remoteSessionsForHost(host string, localSessions map[string]bool, probe func(string) ([]string, error)) (sessions []string, ok bool) {
	names, err := probe(host)
	if err != nil || len(names) == 0 {
		return nil, false
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
	return out, true
}

// sshListRemoteSessions runs the same path/tmpdir resolution as
// lztmux-remote-open so a remote without tmux on the non-interactive PATH still
// lists. Returns session names or an error.
func sshListRemoteSessions(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteProbeTimeout)
	defer cancel()

	// Mirrors scripts/lztmux-remote-open.sh: resolve uid-scoped TMUX_TMPDIR and
	// a usable tmux binary, then list session names.
	remoteCmd := `td=/run/user/$(id -u); t=$(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux); env TMUX_TMPDIR="$td" "$t" list-sessions -F '#{session_name}' 2>/dev/null`
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=2",
		"-T",
		host,
		"--",
		remoteCmd,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
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
		host   string
		sess   []string
		failed bool
	}
	results := make([]hostResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			sess, ok := remoteSessionsForHost(h, localSessionNames, probe)
			results[i] = hostResult{host: h, sess: sess, failed: !ok}
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
		if r.failed {
			// Bare host: open defaults to the remote's most-recent session.
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
		}
		if len(r.sess) == 0 {
			// Probe ok but every session already bridged (or remote empty) — omit.
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
