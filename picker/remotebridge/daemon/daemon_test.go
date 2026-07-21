package daemon

import (
	"bytes"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// capBuf is a tiny io.Writer that captures what it's given, for asserting
// routed output.
type capBuf struct{ bytes.Buffer }

func newTestReader(stream string) *controlmode.Reader {
	return controlmode.NewReader(strings.NewReader(stream))
}

// TestLoopRoutesAndExits, TestLoopStopsOnWindowClose, and
// TestLoopReturnsFalseOnEOF (M2.1) drove the extracted runLoop/handleLine
// helpers, which are gone: Task 4 deletes them as dead code (Run's real main
// loop already had its own inline switch, never called them) and flips the
// stop-semantics they encoded — %window-close no longer ends the daemon, only
// %exit/EOF/an emptied registry do (see TestTranslateWindowNotification for the
// B2-filtered translation, and tests/remote-m2-integration.bats for live
// add/close/rename coverage).

// TestReadReplyRoutingRoutesSiblingOutput: while awaiting one command's
// %begin..%end reply, standalone %output for another pane (NOT inside the
// %begin guard) must be routed, not dropped. Reader.Next absorbs guarded lines
// into the reply body, so pane-B output is emitted between reply blocks.
func TestReadReplyRoutingRoutesSiblingOutput(t *testing.T) {
	stream := strings.Join([]string{
		"%output %2 live-B",
		"%begin 1 1 0",
		"cursor-and-capture-reply",
		"%end 1 1 0",
	}, "\n") + "\n"
	reader := newTestReader(stream)
	router := NewRouter()
	var sink capBuf
	router.Register("%2", &sink)

	l, ok := readReplyRouting(reader, router)
	if !ok || l.Kind != controlmode.End {
		t.Fatalf("readReplyRouting returned %+v ok=%v, want End", l, ok)
	}
	if sink.String() != "live-B" {
		t.Errorf("sibling pane-B output %q was dropped, want %q", sink.String(), "live-B")
	}
}

// fakeSink is a Close()-tracking sink, for asserting closeWindow unregisters
// (and thereby closes) every pane it tears down.
type fakeSink struct{ closed bool }

func (s *fakeSink) Write(p []byte) (int, error) { return len(p), nil }
func (s *fakeSink) Close()                      { s.closed = true }

// TestCloseWindowTearsDownOnlyItsWindow pins the stop-semantics flip: closing
// one remote window must remove it from the registry, unregister/close every
// one of its panes' sinks, close its renderer conns, and kill-window the local
// mirror — without touching any other registered window.
func TestCloseWindowTearsDownOnlyItsWindow(t *testing.T) {
	reg := newRegistry(1)
	mw := reg.add("@1", "h-s:1")
	mw.remotePanes = []string{"%1", "%2"}
	other := reg.add("@2", "h-s:2")
	router := NewRouter()
	sink1, sink2 := &fakeSink{}, &fakeSink{}
	router.Register("%1", sink1)
	router.Register("%2", sink2)
	c1, c1peer := net.Pipe()
	c2, c2peer := net.Pipe()
	defer c1peer.Close()
	defer c2peer.Close()
	mw.conns["%1"] = c1
	mw.conns["%2"] = c2

	var gotArgs []string
	cfg := Config{LocalTmux: func(args ...string) error { gotArgs = args; return nil }}

	closeWindow(cfg, router, reg, "@1")

	if !sink1.closed || !sink2.closed {
		t.Fatal("closeWindow must unregister (and close) every pane's sink")
	}
	if _, ok := reg.byRemoteID("@1"); ok {
		t.Fatal("closeWindow must remove the closed window from the registry")
	}
	if _, ok := reg.byRemoteID("@2"); !ok || other.localWin != "h-s:2" {
		t.Fatal("closeWindow must not touch an unrelated registered window")
	}
	want := []string{"kill-window", "-t", "h-s:1"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("LocalTmux called with %v, want %v", gotArgs, want)
	}
	if _, err := c1.Write([]byte("x")); err == nil {
		t.Fatal("closeWindow must close each pane's conn")
	}
}

// TestCloseWindowOutOfRegistryIsNoop is the B2 filter: a %window-close for a
// window this daemon doesn't own must never touch tmux or the registry.
func TestCloseWindowOutOfRegistryIsNoop(t *testing.T) {
	reg := newRegistry(1)
	reg.add("@1", "h-s:1")
	router := NewRouter()
	called := false
	cfg := Config{LocalTmux: func(args ...string) error { called = true; return nil }}

	closeWindow(cfg, router, reg, "@9")

	if called {
		t.Fatal("closeWindow must no-op for an out-of-registry window (B2)")
	}
	if _, ok := reg.byRemoteID("@1"); !ok {
		t.Fatal("closeWindow must not remove an unrelated registry entry")
	}
}

// TestPauseContinueReseedsBeforeResumingOutput pins I1: a %pause %N marks the
// sink paused (output dropped), and a %continue %N captures a fresh screen —
// routing sibling %output while that round-trip is in flight (B3) — and writes
// it as a FrameSeed BEFORE any resumed output reaches the pane's conn.
func TestPauseContinueReseedsBeforeResumingOutput(t *testing.T) {
	// %1 -> a real outputSink over one end of a pipe (so router.sink() finds
	// it and the test can read its frames off the peer); %2 -> a capBuf, to
	// assert sibling output isn't dropped during the re-seed round-trip.
	oneLocal, onePeer := net.Pipe()
	defer oneLocal.Close()
	defer onePeer.Close()

	router := NewRouter()
	router.Register("%1", newOutputSink(oneLocal))
	var two capBuf
	router.Register("%2", &two)

	// PaneSeed issues two commands (cursor display-message + capture-pane), so
	// the %continue round-trip carries two reply blocks; the sibling %output
	// lands mid-round-trip, exercising the routing-aware reply reader.
	stream := strings.Join([]string{
		"%pause %1",
		"%output %1 dropped-while-paused", // paused: must never reach %1's conn
		"%continue %1",
		"%output %2 sibling", // routed by readReplyRouting during the round-trip
		"%begin 1 1 0",
		"0 0 0 0",
		"%end 1 1 0",
		"%begin 1 2 0",
		"FRESH-CAPTURE",
		"%end 1 2 0",
		"%output %1 after-continue", // must arrive AFTER the seed frame
		"%exit",
	}, "\n") + "\n"

	reader := newTestReader(stream)
	send := func(string) {}
	for {
		l, ok := reader.Next()
		if !ok {
			break
		}
		switch l.Kind {
		case controlmode.Output:
			router.Route(l.Pane, l.Data)
		case controlmode.Pause:
			handlePause(router, send, l.Args[0])
		case controlmode.Continue:
			handleContinue(reader, router, send, l.Args[0])
		case controlmode.Exit:
			// stop below
		}
		if l.Kind == controlmode.Exit {
			break
		}
	}

	// %1's conn must see FrameSeed(FRESH-CAPTURE) first, then FrameOutput.
	first, err := wire.ReadFrame(onePeer)
	if err != nil {
		t.Fatalf("read seed frame: %v", err)
	}
	if first.Type != wire.FrameSeed {
		t.Fatalf("first frame type = %d, want FrameSeed(%d)", first.Type, wire.FrameSeed)
	}
	if !bytes.Contains(first.Payload, []byte("FRESH-CAPTURE")) {
		t.Errorf("seed frame %q does not contain the fresh capture", first.Payload)
	}
	second, err := wire.ReadFrame(onePeer)
	if err != nil {
		t.Fatalf("read output frame: %v", err)
	}
	if second.Type != wire.FrameOutput {
		t.Fatalf("second frame type = %d, want FrameOutput(%d)", second.Type, wire.FrameOutput)
	}
	if string(second.Payload) != "after-continue" {
		t.Errorf("output frame = %q, want %q", second.Payload, "after-continue")
	}
	if two.String() != "sibling" {
		t.Errorf("sibling pane %%2 recorded %q, want %q (dropped during re-seed)", two.String(), "sibling")
	}
}

// TestCollectHellosTimesOutWhenRenderersDontConnect uses a real listener
// that nobody dials: a spawned renderer that never connects back (bad
// RendererBin, exec failure, crash) must not wedge collectHellos forever.
func TestCollectHellosTimesOutWhenRenderersDontConnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	connCh := make(chan helloConn, 16)
	go acceptRenderers(l, connCh)

	start := time.Now()
	_, err = collectHellos(connCh, 1, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("collectHellos: want an error when no renderer connects, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("collectHellos blocked for %s, want it to return near the 100ms deadline", elapsed)
	}
}

// TestWatchResizeReconvergesOnChange drives watchResize deterministically (no
// time.Sleep): winSize reads from sizeCh so the test controls exactly when the
// watcher observes each size, and each tick sent on `tick` blocks until the
// watcher is back at its select — so sending the next tick is a barrier that
// proves the previous iteration (including any send) has fully completed.
func TestWatchResizeReconvergesOnChange(t *testing.T) {
	tick := make(chan time.Time)
	stop := make(chan struct{})
	sizeCh := make(chan [2]int)
	winSize := func() (int, int) { s := <-sizeCh; return s[0], s[1] }

	var sent []string
	send := func(s string) { sent = append(sent, s) }

	done := make(chan struct{})
	go func() { watchResize(winSize, 100, 30, send, stop, tick); close(done) }()

	// Tick 1 — unchanged (100x30): no send.
	tick <- time.Now()
	sizeCh <- [2]int{100, 30}

	// Tick 2 — changed (120x40): exactly one send. This tick's send blocks the
	// goroutine from re-selecting, so the next tick can't unblock until it lands.
	tick <- time.Now()
	sizeCh <- [2]int{120, 40}

	// Tick 3 — same as the new size (120x40): must NOT resend (tracks new size).
	tick <- time.Now()
	sizeCh <- [2]int{120, 40}

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchResize did not return after stop was closed")
	}

	want := []string{ConvergeCmd(120, 40)}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent = %v, want %v", sent, want)
	}
}
