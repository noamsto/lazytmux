package daemon

import (
	"bytes"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/graphics"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// capBuf is a tiny io.Writer that captures what it's given, for asserting
// routed output.
type capBuf struct{ bytes.Buffer }

func newTestReader(s string) *controlmode.Reader {
	return controlmode.NewReader(strings.NewReader(s))
}

// testStream is a stream whose command side goes nowhere, for driving a reply
// reader against a scripted response stream.
func testStream() *stream { return newStream(io.Discard) }

// TestLoopRoutesAndExits, TestLoopStopsOnWindowClose, and
// TestLoopReturnsFalseOnEOF (M2.1) drove the extracted runLoop/handleLine
// helpers, which are gone: Task 4 deletes them as dead code (Run's real main
// loop already had its own inline switch, never called them) and flips the
// stop-semantics they encoded — %window-close no longer ends the daemon, only
// %exit/EOF/an emptied registry do (see TestTranslateWindowNotification for the
// B2-filtered translation, and tests/remote-m2-integration.bats for live
// add/close/rename coverage).

// TestReadReplyRoutingRoutesSiblingOutput: while awaiting one command's
// %begin..%end reply, %output for another pane must be routed, not dropped —
// whether it arrives between blocks or inside the guard.
func TestReadReplyRoutingRoutesSiblingOutput(t *testing.T) {
	stream := strings.Join([]string{
		"%output %2 live-B",
		"%begin 1 1 1",
		"cursor-and-capture-reply",
		"%end 1 1 1",
	}, "\n") + "\n"
	reader := newTestReader(stream)
	router := NewRouter()
	var sink capBuf
	router.Register("%2", &sink)

	l, ok := readReplyRouting(reader, router, &asyncQueue{}, testStream(), 1)
	if !ok || l.Kind != controlmode.End {
		t.Fatalf("readReplyRouting returned %+v ok=%v, want End", l, ok)
	}
	if sink.String() != "live-B" {
		t.Errorf("sibling pane-B output %q was dropped, want %q", sink.String(), "live-B")
	}
}

// TestReadReplyRoutingMatchesItsOwnCommand is #276, in the exact shape `prefix
// c` produces: our list-windows is command 2, but three blocks reach it first —
// the reply to the fire-and-forget `new-window` a ctl gesture sent (command 1),
// and one flagged 0 per after-new-window hook the remote ran in our queue.
// Returning any of them hands back an empty body instead of the window list, and
// leaves every later round-trip a command behind.
func TestReadReplyRoutingMatchesItsOwnCommand(t *testing.T) {
	s := strings.Join([]string{
		"%begin 1 1 1", // the ctl gesture's own new-window reply
		"%end 1 1 1",
		"%begin 1 2 0", // after-new-window[0] on the remote
		"%end 1 2 0",
		"%begin 1 3 0", // after-new-window[10]
		"%end 1 3 0",
		"%begin 1 4 1", // ours
		"@5",
		"@6",
		"%end 1 4 1",
	}, "\n") + "\n"

	l, ok := readReplyRouting(newTestReader(s), NewRouter(), &asyncQueue{}, testStream(), 2)
	if !ok || l.Kind != controlmode.End {
		t.Fatalf("readReplyRouting returned %+v ok=%v, want End", l, ok)
	}
	if got := string(l.Data); got != "@5\n@6" {
		t.Errorf("reply body = %q, want the list-windows output %q", got, "@5\n@6")
	}
}

// TestReadReplyRoutingQueuesNotifications: a notification met while awaiting a
// reply must be handed to the main loop, not dropped. A dropped %pause left its
// pane paused on the remote with no %continue ever sent.
func TestReadReplyRoutingQueuesNotifications(t *testing.T) {
	s := strings.Join([]string{
		"%pause %1",
		"%window-add @7",
		"%begin 1 1 1",
		"body",
		"%end 1 1 1",
	}, "\n") + "\n"

	async := &asyncQueue{}
	if _, ok := readReplyRouting(newTestReader(s), NewRouter(), async, testStream(), 1); !ok {
		t.Fatal("readReplyRouting: want the reply block")
	}
	queued := async.take()
	if len(queued) != 2 || queued[0].Kind != controlmode.Pause || queued[1].Kind != controlmode.WindowAdd {
		t.Fatalf("queued = %+v, want %%pause then %%window-add", queued)
	}
	if rest := async.take(); len(rest) != 0 {
		t.Errorf("take must empty the queue, still holds %+v", rest)
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

	cv := newConverger()
	cv.need("@1", 100, 30)
	closeWindow(cfg, router, newCtlState(), reg, cv, "@1")

	if !cv.need("@1", 100, 30) {
		t.Fatal("closeWindow must forget the closed window's asserted size")
	}

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

	closeWindow(cfg, router, newCtlState(), reg, newConverger(), "@9")

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
	router.Register("%1", newOutputSink(oneLocal, nil))
	var two capBuf
	router.Register("%2", &two)

	// PaneSeed issues two commands (cursor display-message + capture-pane), so
	// the %continue round-trip carries two reply blocks; the sibling %output
	// lands mid-round-trip, exercising the routing-aware reply reader.
	s := strings.Join([]string{
		"%pause %1",
		"%output %1 dropped-while-paused", // paused: must never reach %1's conn
		"%continue %1",
		"%output %2 sibling", // routed by readReplyRouting during the round-trip
		"%begin 1 1 1",
		"0 0 0 0",
		"%end 1 1 1",
		"%begin 1 2 1",
		"FRESH-CAPTURE",
		"%end 1 2 1",
		"%output %1 after-continue", // must arrive AFTER the seed frame
		"%exit",
	}, "\n") + "\n"

	reader := newTestReader(s)
	st := newStream(io.Discard)
	async := &asyncQueue{}
	rt := func(cmd string) (controlmode.Line, bool) {
		seq, ok := st.stamp(cmd)
		if !ok {
			return controlmode.Line{}, false
		}
		return readReplyRouting(reader, router, async, st, seq)
	}
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
			handleContinue(router, rt, l.Args[0])
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
	go acceptConns(l, connCh, func([]string) error { return nil })

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
// time.Sleep): area reads from sizeCh so the test controls exactly when the
// watcher observes each size, and each tick sent on `tick` blocks until the
// watcher is back at its select — so sending the next tick is a barrier that
// proves the previous iteration (including any send) has fully completed.
func TestWatchResizeReconvergesOnChange(t *testing.T) {
	tick := make(chan time.Time)
	stop := make(chan struct{})
	sizeCh := make(chan [2]int)
	area := func() (int, int) { s := <-sizeCh; return s[0], s[1] }

	reg := newRegistry(1)
	reg.add("@1", "host-sess:1")
	reg.add("@2", "host-sess:2")
	cv := newConverger()
	// The setup path already asserted the startup size for both windows.
	cv.need("@1", 100, 30)
	cv.need("@2", 100, 30)

	var sent []string
	send := func(s string) { sent = append(sent, s) }

	done := make(chan struct{})
	go func() { watchResize(area, reg, cv, send, stop, tick); close(done) }()

	// Tick 1 — unchanged (100x30): no send.
	tick <- time.Now()
	sizeCh <- [2]int{100, 30}

	// Tick 2 — changed (120x40): one send per mirrored window. This tick's
	// sends block the goroutine from re-selecting, so the next tick can't
	// unblock until they land.
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

	// remoteIDs snapshots a map, so the per-window sends land in either order.
	sort.Strings(sent)
	want := []string{ConvergeCmd("@1", 120, 40), ConvergeCmd("@2", 120, 40)}
	sort.Strings(want)
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent = %v, want %v", sent, want)
	}
}

func TestOutputSinkFiltersAndCoalescesThroughTheProxy(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	p := graphics.New(&stubLocalizer{local: "/local/a.bin"}, nil)
	s := newOutputSink(remote, p)
	defer s.Close()

	// Two stores for the same id, queued before the pump can drain them.
	seq := func(payload string) []byte {
		return []byte("\x1b_Gi=7,a=T,U=1,f=100,t=f;" + payload + "\x1b\\")
	}
	s.Write(seq("L3RtcC9hLnBuZw==")) // /tmp/a.png
	s.Write(seq("L3RtcC9iLnBuZw==")) // /tmp/b.png

	got := readAllFrames(t, local, 500*time.Millisecond)
	if n := strings.Count(got, "\x1bPtmux;"); n != 1 {
		t.Fatalf("forwarded %d stores, want 1 (the newest); got %q", n, got)
	}
	if !strings.Contains(got, "L2xvY2FsL2EuYmlu") {
		t.Fatalf("payload not localised: %q", got)
	}
}

type stubLocalizer struct{ local string }

func (s *stubLocalizer) Localize(string) (string, error) { return s.local, nil }

// readAllFrames reads frames off conn until it goes quiet for the deadline and
// returns their concatenated payloads.
func readAllFrames(t *testing.T, conn net.Conn, quiet time.Duration) string {
	t.Helper()
	var out []byte
	for {
		if err := conn.SetReadDeadline(time.Now().Add(quiet)); err != nil {
			t.Fatal(err)
		}
		f, err := wire.ReadFrame(conn)
		if err != nil {
			return string(out)
		}
		out = append(out, f.Payload...)
	}
}
