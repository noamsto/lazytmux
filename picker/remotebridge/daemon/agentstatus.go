package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// agentStatusPollInterval is the floor between two polls. There is no timer:
// rt reads the stream, so only the main loop may poll. An agent that changes
// state redraws its pane first, which is what wakes that loop.
const agentStatusPollInterval = time.Second

// agentStatusFormat reads what claude-status-update stamped on each remote pane.
// @claude_status is "<state> <epoch> <unseen>"; the free-form task goes last so
// a '|' inside it lands in the final field instead of shifting the row.
const agentStatusFormat = "'#{pane_id}|#{@claude_status}|#{@claude_issues}|#{@claude_task}'"

// paneStatus is one remote pane's agent state, as stamped by the hook writer.
type paneStatus struct {
	pane   string // remote pane id, with %
	state  string
	ts     int64 // epoch on the REMOTE's clock
	unseen bool
	issues string
	task   string
}

// parseAgentStatus turns an agentStatusFormat reply body into the panes that
// actually reported a state. A pane with no @claude_status has no agent.
func parseAgentStatus(body string) []paneStatus {
	var out []paneStatus
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		// Trailing empty fields may or may not survive the trip, so read them
		// positionally rather than demanding all four.
		fields := strings.SplitN(line, "|", 4)
		if len(fields) < 2 || fields[1] == "" {
			continue
		}
		st := strings.Fields(fields[1])
		if len(st) < 2 {
			continue
		}
		ts, err := strconv.ParseInt(st[1], 10, 64)
		if err != nil {
			continue
		}
		row := paneStatus{
			pane:   fields[0],
			state:  st[0],
			ts:     ts,
			unseen: len(st) > 2 && st[2] == "1",
		}
		if len(fields) > 2 {
			row.issues = fields[2]
		}
		if len(fields) > 3 {
			row.task = fields[3]
		}
		out = append(out, row)
	}
	return out
}

// agentShipper writes the remote's agent state into the local claude-status
// tree under the LOCAL pane ids, which is what every local consumer — window
// icons, the session tint, the status aggregate, the pickers — reads.
type agentShipper struct {
	dir      string                // claude-status root
	sess     string                // local mirror session, recorded in each pane file
	skew     int64                 // localNow - remoteNow, so a remote stamp lands on our clock
	written  map[string]paneStatus // local pane id (no %) -> the row last written for it
	lastPoll time.Time
}

// poll re-reads the remote's stamped state and applies it, throttled to
// agentStatusPollInterval. Main loop only: rt is not safe to share.
func (a *agentShipper) poll(cfg Config, rt roundTrip) {
	if time.Since(a.lastPoll) < agentStatusPollInterval {
		return
	}
	a.lastPoll = time.Now()
	l, ok := rt(fmt.Sprintf("list-panes -s -t %s -F %s", tmuxQuote(cfg.RemoteSession), agentStatusFormat))
	if !ok || l.Kind == controlmode.Error {
		return
	}
	a.apply(cfg, parseAgentStatus(string(l.Data)))
}

func newAgentShipper(localSess string, skew int64) *agentShipper {
	dir := os.Getenv("CLAUDE_STATUS_DIR")
	if dir == "" {
		dir = "/tmp/claude-status"
	}
	return &agentShipper{dir: dir, sess: localSess, skew: skew, written: map[string]paneStatus{}}
}

// apply writes a file per reporting pane and drops the ones that stopped
// reporting (the agent exited, or its pane is gone).
func (a *agentShipper) apply(cfg Config, rows []paneStatus) {
	if cfg.LocalPanes == nil {
		return
	}
	local := cfg.LocalPanes()
	live := make(map[string]bool, len(rows))

	for _, r := range rows {
		id, ok := local[r.pane]
		if !ok {
			continue
		}
		id = strings.TrimPrefix(id, "%")
		live[id] = true
		// Write only on a real change: rewriting an unchanged row would undo the
		// local mark-seen hook, which clears `unseen` the moment you look at the
		// mirror window while the remote's stamp still says 1.
		if prev, seen := a.written[id]; seen && prev == r {
			continue
		}
		a.written[id] = r

		body := fmt.Sprintf("state=%s\ntimestamp=%d\nsession=%s\n", r.state, r.ts+a.skew, a.sess)
		if r.unseen {
			body += "unseen=1\n"
		}
		// No transcript= line: it names a path on the remote's disk, and the
		// interrupt detector that reads it would tail a local file that either
		// doesn't exist or belongs to someone else's session.
		writeStatusFile(filepath.Join(a.dir, "panes", id), body)
		writeStatusFile(filepath.Join(a.dir, "tasks", id), lineOrEmpty(r.task))
		writeStatusFile(filepath.Join(a.dir, "issues", id), lineOrEmpty(r.issues))
	}

	for id := range a.written {
		if live[id] {
			continue
		}
		a.forget(id)
	}
}

// clear drops every file this bridge wrote. The shell-side prune collects by
// server-start mtime, so nothing else would ever reap them.
func (a *agentShipper) clear() {
	for id := range a.written {
		a.forget(id)
	}
}

func (a *agentShipper) forget(id string) {
	for _, sub := range []string{"panes", "tasks", "issues"} {
		os.Remove(filepath.Join(a.dir, sub, id))
	}
	delete(a.written, id)
}

func lineOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return s + "\n"
}

// writeStatusFile writes body, or removes the file when body is empty — an
// empty tasks/issues file would read back as a blank task rather than none.
func writeStatusFile(path, body string) {
	if body == "" {
		os.Remove(path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	os.WriteFile(path, []byte(body), 0o644)
}

// remoteClockSkew measures localNow - remoteNow so a stamp made on the remote
// lands on our clock. Ages drive the staleness fade and the "last active"
// readout, and two hosts' clocks are never exactly equal. Measured once: NTP
// drift over a session is far below the fade's resolution.
func remoteClockSkew(rt roundTrip) int64 {
	l, ok := rt("display-message -p '%s'")
	if !ok || l.Kind == controlmode.Error {
		return 0
	}
	remote, err := strconv.ParseInt(strings.TrimSpace(string(l.Data)), 10, 64)
	if err != nil {
		return 0
	}
	return time.Now().Unix() - remote
}
