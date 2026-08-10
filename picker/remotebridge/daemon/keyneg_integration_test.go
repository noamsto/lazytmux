package daemon

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestKeyNegStrippedEndToEndThroughASink pins #338: a modifyOtherKeys
// negotiation sequence a remote pane's occupant wrote for itself (Claude
// Code and other agent CLIs request mode 2) must never reach the local
// mirror pane's pty, or local tmux re-encodes future keystrokes — including
// Ctrl+R — for that pane as if the renderer itself had requested it.
func TestKeyNegStrippedEndToEndThroughASink(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	s := newOutputSink(remote, nil) // nil gfx: the strip must not depend on graphics being wired in
	defer s.Close()

	s.Write([]byte("hello \x1b[>4;2mworld"))

	got := readAllFrames(t, local, 500*time.Millisecond)
	if strings.Contains(got, "\x1b[>4;2m") {
		t.Fatalf("negotiation sequence leaked through: %q", got)
	}
	if want := "hello world"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestKeyNegSplitAcrossWritesEndToEnd exercises the case a live network read
// is most likely to hit: the negotiation sequence straddling two separate
// pane-output frames.
func TestKeyNegSplitAcrossWritesEndToEnd(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	s := newOutputSink(remote, nil)
	defer s.Close()

	s.Write([]byte("hello \x1b[>4;"))
	s.Write([]byte("2mworld"))

	got := readAllFrames(t, local, 500*time.Millisecond)
	if want := "hello world"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
