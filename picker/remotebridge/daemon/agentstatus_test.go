package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentStatus(t *testing.T) {
	body := strings.Join([]string{
		"%1|processing 1700000000 |",            // trailing empty fields trimmed away
		"%2||",                                  // pane with no agent — dropped
		"%3|waiting 1700000042 1|ENG-7|fix | y", // unseen, issues, task holding a '|'
		"%4|garbage|",                           // no timestamp — dropped
	}, "\n")

	got := parseAgentStatus(body)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	if got[0].pane != "%1" || got[0].state != "processing" || got[0].ts != 1700000000 || got[0].unseen {
		t.Errorf("row 0 = %+v", got[0])
	}
	w := got[1]
	if w.pane != "%3" || w.state != "waiting" || !w.unseen || w.issues != "ENG-7" || w.task != "fix | y" {
		t.Errorf("row 1 = %+v", w)
	}
}

func TestAgentShipperApply(t *testing.T) {
	dir := t.TempDir()
	a := &agentShipper{dir: dir, sess: "lab-mono", skew: 10, written: map[string]paneStatus{}}
	cfg := Config{LocalPanes: func() map[string]string {
		return map[string]string{"%1": "%7", "%2": "%8"}
	}}

	a.apply(cfg, []paneStatus{
		{pane: "%1", state: "waiting", ts: 1700000000, unseen: true, task: "ship it", issues: "ENG-7"},
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

// Looking at a mirror window runs the local mark-seen hook, which strips
// `unseen` from the file. The remote's stamp still says 1 until its agent writes
// again, so an unchanged row must not be written back over that.
func TestAgentShipperLeavesUnchangedRowsAlone(t *testing.T) {
	dir := t.TempDir()
	a := &agentShipper{dir: dir, sess: "lab-mono", written: map[string]paneStatus{}}
	cfg := Config{LocalPanes: func() map[string]string { return map[string]string{"%1": "%7"} }}
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
	cfg := Config{LocalPanes: func() map[string]string { return map[string]string{"%1": "%7"} }}
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
