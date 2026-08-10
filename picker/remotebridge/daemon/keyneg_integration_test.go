package daemon

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/graphics"
)

// TestKeyNegStrippedEndToEndThroughASink: a modifyOtherKeys negotiation
// sequence must never reach a mirror pane's pty, or local tmux re-encodes
// future keystrokes for it — including Ctrl+R — as if the renderer itself
// had requested it (#338).
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

// TestKeyNegAndGraphicsFlushOrderOnClose: kn only ever holds back the
// newest unprocessed tail of the stream, so on close its leftover must be
// threaded through gfx before gfx's own held bytes are flushed, preserving
// the original byte order of the stream's tail.
func TestKeyNegAndGraphicsFlushOrderOnClose(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	s := newOutputSink(remote, graphics.New(nil, nil))

	// An incomplete kitty APC (held by gfx), then — in a separate write, so
	// it lands in a later pump iteration — an incomplete modifyOtherKeys
	// sequence (held by kn) that never reaches gfx during normal operation.
	s.Write([]byte("AAA\x1b_Ga=t,f=100;"))
	time.Sleep(20 * time.Millisecond)
	s.Write([]byte("BBB\x1b[>4;"))
	time.Sleep(20 * time.Millisecond)

	s.Close()

	got := readAllFrames(t, local, 500*time.Millisecond)
	if want := "AAA\x1b_Ga=t,f=100;BBB\x1b[>4;"; got != want {
		t.Fatalf("got %q, want %q (original byte order preserved)", got, want)
	}
}
