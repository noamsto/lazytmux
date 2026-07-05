package statefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWritesOnChangeOnly(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")
	now := time.Unix(1000, 0)

	changed, err := w.Update("processing", now)
	if err != nil || !changed {
		t.Fatalf("first update: changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "3"))
	if string(b) != "state=processing\ntimestamp=1000\n" {
		t.Fatalf("file = %q", b)
	}

	changed, _ = w.Update("processing", now.Add(time.Second))
	if changed {
		t.Fatal("same state should not rewrite")
	}

	changed, _ = w.Update("idle", now.Add(2*time.Second))
	if !changed {
		t.Fatal("state change should rewrite")
	}
}

func TestEmptyStateIsNoop(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")
	if changed, _ := w.Update("", time.Unix(1, 0)); changed {
		t.Fatal("empty state must be a no-op")
	}
	if _, err := os.Stat(filepath.Join(dir, "3")); !os.IsNotExist(err) {
		t.Fatal("no file should be written for empty state")
	}
}
