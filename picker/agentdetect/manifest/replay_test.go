package manifest

import (
	"os"
	"testing"

	"github.com/noamsto/lazytmux/picker/agentdetect/screen"
)

// replayMatch feeds raw capture-pane bytes through the VT emulator (same path as
// agent-detect's pipe-pane watcher) and returns Match's verdict.
func replayMatch(t *testing.T, capture []byte, cols, rows int) (string, bool) {
	t.Helper()
	scr := screen.New(cols, rows)
	scr.Feed(capture)
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ForCommand(ms, "cursor-agent")
	if !ok {
		t.Fatal("no manifest for cursor-agent")
	}
	return Match(m, scr.Text(), scr.Title(), scr.AltScreen())
}

func TestCursorCaptureReplay(t *testing.T) {
	working, err := os.ReadFile("testdata/cursor_working_capture.txt")
	if err != nil {
		t.Fatal(err)
	}
	idle, err := os.ReadFile("testdata/cursor_idle_capture.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Pane geometry from the live captures (100×30 tmux pane).
	const cols, rows = 100, 30

	got, ok := replayMatch(t, working, cols, rows)
	if !ok || got != "processing" {
		t.Fatalf("working capture replay = (%q,%v), want (processing,true)", got, ok)
	}

	got, ok = replayMatch(t, idle, cols, rows)
	if !ok || got != "idle" {
		t.Fatalf("idle capture replay = (%q,%v), want (idle,true)", got, ok)
	}
}
