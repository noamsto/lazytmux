package statefile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Writer struct {
	dir, paneID, last string
}

func New(dir, paneID string) *Writer { return &Writer{dir: dir, paneID: paneID} }

// Update records a newly observed agent state. An empty state is manifest
// matching's positive verdict that no rule matched the current screen, i.e.
// the agent is gone — not "nothing to report" — so it delegates to Clear.
func (w *Writer) Update(state string, now time.Time) (bool, error) {
	if state == "" {
		return w.Clear()
	}
	if state == w.last {
		return false, nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return false, err
	}
	tmp := w.path() + ".tmp"
	content := fmt.Sprintf("state=%s\ntimestamp=%d\n", state, now.Unix())
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, w.path()); err != nil {
		return false, err
	}
	w.last = state
	return true, nil
}

// Clear removes the state file and forgets the last written state, so a
// later Update always writes even if the next real state matches whatever
// was just cleared. It always goes to disk rather than trusting w.last as a
// proxy for "is there a file": a freshly constructed Writer starts with
// w.last == "", even when an earlier Writer instance for the same pane left
// a file behind. Idempotent: a missing file is success, not an error.
func (w *Writer) Clear() (bool, error) {
	switch err := os.Remove(w.path()); {
	case err == nil:
		w.last = ""
		return true, nil
	case os.IsNotExist(err):
		w.last = ""
		return false, nil
	default:
		return false, err
	}
}

func (w *Writer) path() string { return filepath.Join(w.dir, w.paneID) }
