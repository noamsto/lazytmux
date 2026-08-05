package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAgentStatus(t *testing.T) {
	body := strings.Join([]string{
		"%1|claude|processing 1700000000 |",            // trailing empty fields trimmed away
		"%2|nvim||",                                    // mirrored pane, no agent
		"%3|claude|waiting 1700000042 1|ENG-7|fix | y", // unseen, issues, task holding a '|'
		"%4|fish|garbage|",                             // unparsable stamp reads as no agent
	}, "\n")

	got := parseAgentStatus(body)
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4 (every mirrored pane): %+v", len(got), got)
	}
	if got[0].pane != "%1" || got[0].proc != "claude" || got[0].state != "processing" || got[0].ts != 1700000000 || got[0].unseen {
		t.Errorf("row 0 = %+v", got[0])
	}
	if got[1].proc != "nvim" || got[1].state != "" {
		t.Errorf("agent-free pane must survive with its command: %+v", got[1])
	}
	w := got[2]
	if w.pane != "%3" || w.state != "waiting" || !w.unseen || w.issues != "ENG-7" || w.task != "fix | y" {
		t.Errorf("row 2 = %+v", w)
	}
	if got[3].proc != "fish" || got[3].state != "" {
		t.Errorf("unparsable stamp = %+v, want no state", got[3])
	}
}

// mirrorCfg is a Config wired to two mirror panes, capturing the local tmux
// commands the shipper issues.
func mirrorCfg(calls *[][]string) Config {
	return Config{
		LocalPanes: func() map[string]string { return map[string]string{"%1": "%7", "%2": "%8"} },
		LocalTmux: func(args ...string) error {
			*calls = append(*calls, args)
			return nil
		},
	}
}

func TestAgentShipperApply(t *testing.T) {
	dir := t.TempDir()
	a := &agentShipper{dir: dir, sess: "lab-mono", skew: 10, written: map[string]paneStatus{}}
	var calls [][]string
	cfg := mirrorCfg(&calls)

	a.apply(cfg, []paneStatus{
		{pane: "%1", proc: "claude", state: "waiting", ts: 1700000000, unseen: true, task: "ship it", issues: "ENG-7"},
		{pane: "%9", state: "done", ts: 1700000000}, // no local mirror — skipped
	})

	body, err := os.ReadFile(filepath.Join(dir, "panes", "7"))
	if err != nil {
		t.Fatalf("pane file: %v", err)
	}
	want := "state=waiting\ntimestamp=1700000010\nsession=lab-mono\nunseen=1\n"
	if string(body) != want {
		t.Errorf("pane file =\n%q\nwant\n%q", body, want)
	}
	if strings.Contains(string(body), "transcript=") {
		t.Error("transcript path names a file on the remote's disk; it must not be written")
	}
	for sub, want := range map[string]string{"tasks": "ship it\n", "issues": "ENG-7\n"} {
		got, err := os.ReadFile(filepath.Join(dir, sub, "7"))
		if err != nil || string(got) != want {
			t.Errorf("%s/7 = %q (%v), want %q", sub, got, err, want)
		}
	}

	// The agent exits: its pane stops reporting, so the files go with it.
	a.apply(cfg, nil)
	for _, sub := range []string{"panes", "tasks", "issues"} {
		if _, err := os.Stat(filepath.Join(dir, sub, "7")); !os.IsNotExist(err) {
			t.Errorf("%s/7 outlived the agent", sub)
		}
	}
}

// The local pane runs a renderer, so its own command tells the icons nothing.
// @bridge_proc carries the remote's — stamped for every mirrored pane, agent or
// not, and only when it changes (else it is a fork per pane per second).
func TestAgentShipperStampsRemoteCommand(t *testing.T) {
	a := &agentShipper{dir: t.TempDir(), sess: "lab-mono", written: map[string]paneStatus{}}
	var calls [][]string
	cfg := mirrorCfg(&calls)

	rows := []paneStatus{{pane: "%1", proc: "nvim"}, {pane: "%2", proc: "claude", state: "processing", ts: 1700000000}}
	a.apply(cfg, rows)
	want := [][]string{
		{"set-option", "-p", "-t", "%7", "@bridge_proc", "nvim"},
		{"set-option", "-p", "-t", "%8", "@bridge_proc", "claude"},
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("stamps = %v, want %v", calls, want)
	}

	a.apply(cfg, rows)
	if len(calls) != 2 {
		t.Errorf("unchanged commands re-stamped: %v", calls)
	}

	rows[0].proc = "fish"
	a.apply(cfg, rows)
	if len(calls) != 3 || calls[2][5] != "fish" {
		t.Errorf("a changed command should re-stamp: %v", calls)
	}
}

// An agent-free pane is still mirrored — it gets the icon stamp, but no state
// file, and a file left from a previous agent goes away.
func TestAgentShipperDropsFilesWhenAgentLeavesPane(t *testing.T) {
	dir := t.TempDir()
	a := &agentShipper{dir: dir, sess: "lab-mono", written: map[string]paneStatus{}}
	var calls [][]string
	cfg := mirrorCfg(&calls)

	a.apply(cfg, []paneStatus{{pane: "%1", proc: "claude", state: "done", ts: 1700000000}})
	a.apply(cfg, []paneStatus{{pane: "%1", proc: "fish"}})

	if _, err := os.Stat(filepath.Join(dir, "panes", "7")); !os.IsNotExist(err) {
		t.Error("state file outlived the agent that left the pane")
	}
	last := calls[len(calls)-1]
	if last[5] != "fish" {
		t.Errorf("last stamp = %v, want the pane's new command", last)
	}
}

// Looking at a mirror window runs the local mark-seen hook, which strips
// `unseen` from the file. The remote's stamp still says 1 until its agent writes
// again, so an unchanged row must not be written back over that.
func TestAgentShipperLeavesUnchangedRowsAlone(t *testing.T) {
	dir := t.TempDir()
	a := &agentShipper{dir: dir, sess: "lab-mono", written: map[string]paneStatus{}}
	var calls [][]string
	cfg := mirrorCfg(&calls)
	row := paneStatus{pane: "%1", state: "waiting", ts: 1700000000, unseen: true}
	a.apply(cfg, []paneStatus{row})

	path := filepath.Join(dir, "panes", "7")
	seen := "state=waiting\ntimestamp=1700000000\nsession=lab-mono\n"
	if err := os.WriteFile(path, []byte(seen), 0o644); err != nil {
		t.Fatal(err)
	}
	a.apply(cfg, []paneStatus{row})
	if got, _ := os.ReadFile(path); string(got) != seen {
		t.Errorf("unchanged row was rewritten:\n%q", got)
	}

	// A fresh remote write (new timestamp) does land, unseen and all.
	row.ts = 1700000100
	a.apply(cfg, []paneStatus{row})
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "unseen=1") {
		t.Errorf("new remote state should overwrite: %q", got)
	}
}

// A bridge that dies must not leave a mirror pane's state behind: the shell-side
// prune collects by server-start mtime and would keep it until a tmux restart.
func TestAgentShipperClear(t *testing.T) {
	dir := t.TempDir()
	a := &agentShipper{dir: dir, sess: "lab-mono", written: map[string]paneStatus{}}
	var calls [][]string
	cfg := mirrorCfg(&calls)
	a.apply(cfg, []paneStatus{{pane: "%1", state: "processing", ts: 1700000000}})

	a.clear()
	if _, err := os.Stat(filepath.Join(dir, "panes", "7")); !os.IsNotExist(err) {
		t.Error("clear left the pane file behind")
	}
	if len(a.written) != 0 {
		t.Errorf("written = %v, want empty", a.written)
	}
}

func TestAgentShipperNoLocalPanes(t *testing.T) {
	a := &agentShipper{dir: t.TempDir(), written: map[string]paneStatus{}}
	a.apply(Config{}, []paneStatus{{pane: "%1", state: "done", ts: 1}})
	if len(a.written) != 0 {
		t.Errorf("no LocalPanes seam should write nothing, got %v", a.written)
	}
}
