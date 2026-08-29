package statefile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Writer struct {
	dir, paneID, last string
}

func New(dir, paneID string) *Writer { return &Writer{dir: dir, paneID: paneID} }

// Update records a newly observed agent state and its counted flags. An empty
// state is manifest matching's positive verdict that no rule matched the
// current screen, i.e. the agent is gone — not "nothing to report" — so it
// delegates to Clear.
//
// The write is skipped only when state *and* flags are both unchanged: a
// background shell finishing moves no state, and a pane that kept reporting
// the stale count would never lose the badge.
func (w *Writer) Update(state string, flags map[string]int, now time.Time) (bool, error) {
	if state == "" {
		return w.Clear()
	}
	key := stateKey(state, flags)
	if key == w.last {
		return false, nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return false, err
	}
	tmp := w.path() + ".tmp"
	content := fmt.Sprintf("state=%s\ntimestamp=%d\n%s", state, now.Unix(), flagLines(flags))
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, w.path()); err != nil {
		return false, err
	}
	w.last = key
	return true, nil
}

// flagLines renders flags as sorted key=value lines, so an unchanged set
// always produces byte-identical content and stateKey stays a valid identity.
func flagLines(flags map[string]int) string {
	if len(flags) == 0 {
		return ""
	}
	names := make([]string, 0, len(flags))
	for n := range flags {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s=%d\n", n, flags[n])
	}
	return b.String()
}

func stateKey(state string, flags map[string]int) string {
	return state + "\x00" + flagLines(flags)
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
