package daemon

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// scriptedRTRouter is scriptedRT (sessionpin_test.go), except router is the
// caller's own rather than a fresh one built inside. scriptedRT can't be reused
// as-is here: a script that injects live %output needs it to route into the
// same router the code under test registered its sinks on, not into a router
// the test never observes.
func scriptedRTRouter(script string, router *Router) roundTrip {
	return newRoundTrip(newTestReader(script), router, &asyncQueue{}, testStream())
}

// TestReconcileSeedsPaneBeforeItsLaterOutputArrives pins the load-bearing
// invariant the whole batched-roundTrip design rests on (the design doc's
// "ordering hazard" section): a pane's FrameSeed must reach its sink before any
// %output that arrived on the wire after that pane's capture-pane reply.
//
// PaneSeeds calls onSeed(i, ...) as soon as pane i's two replies are parsed,
// strictly before reading pane i+1's replies off the stream. A batch that
// instead read every pane's replies first and wired sinks only afterward would
// let pane B's read route pane A's post-capture %output into A's sink ahead of
// A's own seed — repainting A with a screen that predates output the renderer
// already painted (the #233/#412/#417 defect class). Two panes make the
// distinction observable: with only one pane there is no "later pane's read"
// to smuggle the live output in behind.
func TestReconcileSeedsPaneBeforeItsLaterOutputArrives(t *testing.T) {
	localA, peerA := net.Pipe()
	defer localA.Close()
	defer peerA.Close()
	localB, peerB := net.Pipe()
	defer localB.Close()
	defer peerB.Close()
	// Pane B's frames are never asserted on; drain them so its sink's pump
	// goroutine never blocks writing to a pipe nobody reads.
	go io.Copy(io.Discard, peerB)

	router := NewRouter()
	router.Register("%0", newOutputSink(localA, nil))
	router.Register("%1", newOutputSink(localB, nil))

	w := &mirrorWindow{
		remoteID: "@1", localWin: "@101",
		remotePanes: []string{"%0", "%1"}, localPanes: []string{"%l0", "%l1"},
	}

	// A two-pane layout, unchanged across the reconcile pass: the point is the
	// re-seed loop's ordering, not any pane add/remove/swap.
	const layout = "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"
	script := strings.Join([]string{
		"%begin 1 1 1", layout + " %0 0", "%end 1 1 1", // readLayout
		"%begin 1 2 1", "0 0 0 0", "%end 1 2 1", // PaneSeed(%0): cursor
		"%begin 1 3 1", "SEED-A", "%end 1 3 1", // PaneSeed(%0): capture
		"%output %0 LIVE-AFTER-A-SEED",          // must land after FrameSeed(%0), never before
		"%begin 1 4 1", "0 0 0 0", "%end 1 4 1", // PaneSeed(%1): cursor
		"%begin 1 5 1", "SEED-B", "%end 1 5 1", // PaneSeed(%1): capture
		"%begin 1 6 1", layout + " %0 0", "%end 1 6 1", // trailing re-read: unchanged, stop
	}, "\n") + "\n"

	rt := scriptedRTRouter(script, router)

	cfg := Config{
		LocalTmux:    func(...string) error { return nil },
		LocalTmuxOut: func(...string) (string, error) { return "0\n", nil }, // localZoomed: not zoomed
	}
	go reconcileLayout(cfg, w, func(string) {}, router, noHellos, newCtlState(), newConverger(), rt)

	peerA.SetDeadline(time.Now().Add(5 * time.Second))

	// The resize lands first (pushed right after select-layout, ahead of any
	// re-seed) — same shape as TestStructuralReconcileReseedsTheSurvivor.
	f, err := wire.ReadFrame(peerA)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if f.Type != wire.FrameResize {
		t.Fatalf("first frame = %v, want a resize", f.Type)
	}

	f, err = wire.ReadFrame(peerA)
	if err != nil {
		t.Fatalf("read second frame: %v", err)
	}
	if f.Type != wire.FrameSeed || !strings.Contains(string(f.Payload), "SEED-A") {
		t.Fatalf("second frame = %v %q, want the FrameSeed carrying SEED-A", f.Type, f.Payload)
	}

	f, err = wire.ReadFrame(peerA)
	if err != nil {
		t.Fatalf("read third frame: %v", err)
	}
	if f.Type != wire.FrameOutput || !strings.Contains(string(f.Payload), "LIVE-AFTER-A-SEED") {
		t.Fatalf("third frame = %v %q, want the live output routed after the seed", f.Type, f.Payload)
	}
}
