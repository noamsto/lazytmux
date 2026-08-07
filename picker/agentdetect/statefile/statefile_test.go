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

func TestEmptyStateClearsFile(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")
	now := time.Unix(1000, 0)

	if changed, err := w.Update("processing", now); err != nil || !changed {
		t.Fatalf("seed write: changed=%v err=%v", changed, err)
	}

	changed, err := w.Update("", now.Add(time.Second))
	if err != nil {
		t.Fatalf("empty-state update: %v", err)
	}
	if !changed {
		t.Fatal("agent going away should be reported as a change")
	}
	if _, err := os.Stat(filepath.Join(dir, "3")); !os.IsNotExist(err) {
		t.Fatal("state file should be removed once the agent is gone")
	}
}

func TestUpdateAfterClearRewritesSameState(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")
	now := time.Unix(1000, 0)

	if _, err := w.Update("processing", now); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if _, err := w.Update("", now.Add(time.Second)); err != nil {
		t.Fatalf("clear via empty state: %v", err)
	}

	// Same state as before the clear must still write — if Clear left
	// w.last set, this would look like a no-op change and the file would
	// stay missing.
	changed, err := w.Update("processing", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("post-clear update: %v", err)
	}
	if !changed {
		t.Fatal("a real state after a clear must write, even if it repeats the pre-clear state")
	}
	b, err := os.ReadFile(filepath.Join(dir, "3"))
	if err != nil {
		t.Fatalf("state file should exist after post-clear update: %v", err)
	}
	if string(b) != "state=processing\ntimestamp=1002\n" {
		t.Fatalf("file = %q", b)
	}
}

func TestClearWithNoFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "3")

	changed, err := w.Clear()
	if err != nil {
		t.Fatalf("clearing a writer that never wrote should not error: %v", err)
	}
	if changed {
		t.Fatal("clearing a writer that never wrote should report no change")
	}
	if _, err := os.Stat(filepath.Join(dir, "3")); !os.IsNotExist(err) {
		t.Fatal("no file should exist")
	}

	// Clearing again, and clearing via an empty Update, must also stay quiet.
	if changed, err := w.Clear(); err != nil || changed {
		t.Fatalf("second clear: changed=%v err=%v", changed, err)
	}
	if changed, err := w.Update("", time.Unix(1, 0)); err != nil || changed {
		t.Fatalf("empty update on a never-written writer: changed=%v err=%v", changed, err)
	}
}

func TestClearRemovesFileWrittenByAnotherWriter(t *testing.T) {
	dir := t.TempDir()

	// Simulate a file left behind by a different Writer instance — e.g. a
	// prior watcher process for the same pane — that this Writer's own
	// w.last knows nothing about.
	path := filepath.Join(dir, "3")
	if err := os.WriteFile(path, []byte("state=processing\ntimestamp=1000\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	w := New(dir, "3")
	changed, err := w.Clear()
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !changed {
		t.Fatal("clearing a file this Writer didn't itself write should still report a change")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file left by another Writer instance should be removed")
	}
}
