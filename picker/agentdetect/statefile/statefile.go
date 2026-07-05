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

func (w *Writer) Update(state string, now time.Time) (bool, error) {
	if state == "" || state == w.last {
		return false, nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return false, err
	}
	final := filepath.Join(w.dir, w.paneID)
	tmp := final + ".tmp"
	content := fmt.Sprintf("state=%s\ntimestamp=%d\n", state, now.Unix())
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return false, err
	}
	w.last = state
	return true, nil
}
