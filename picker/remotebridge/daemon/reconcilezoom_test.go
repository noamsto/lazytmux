package daemon

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// TestReconcileGivesZoomedPaneTheWindowDims is #511's other half: a zoomed pane
// is told the layout root rather than the cell #{window_layout} still reports
// for it (see reconcile.go's comment there). Asserted on the FrameResize a
// renderer actually receives, so the fixture's root and cell must disagree.
func TestReconcileGivesZoomedPaneTheWindowDims(t *testing.T) {
	localA, peerA := net.Pipe()
	defer localA.Close()
	defer peerA.Close()
	localB, peerB := net.Pipe()
	defer localB.Close()
	defer peerB.Close()

	router := NewRouter()
	router.Register("%0", newOutputSink(localA, nil))
	router.Register("%1", newOutputSink(localB, nil))

	w := &mirrorWindow{
		remoteID: "@1", localWin: "@101",
		remotePanes: []string{"%0", "%1"}, localPanes: []string{"%l0", "%l1"},
	}

	// The layout root is 190x45 while pane 0's own cell is 95x45 — a fixture
	// where the two disagree, so a resize frame that carried the pane's own
	// cell instead of the root couldn't pass unnoticed as coincidence.
	const layout = "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"
	script := strings.Join([]string{
		"%begin 1 1 1", layout + " %0 1", "%end 1 1 1", // readLayout: remote zoomed, pane 0 active
		"%begin 1 2 1", "0 0 0 0", "%end 1 2 1", // PaneSeed(%0): cursor
		"%begin 1 3 1", "SEED-0", "%end 1 3 1", // PaneSeed(%0): capture
		"%begin 1 4 1", "0 0 0 0", "%end 1 4 1", // PaneSeed(%1): cursor
		"%begin 1 5 1", "SEED-1", "%end 1 5 1", // PaneSeed(%1): capture
		"%begin 1 6 1", layout + " %0 1", "%end 1 6 1", // trailing re-read: unchanged, stop
	}, "\n") + "\n"

	rt := scriptedRTRouter(script, router)

	cfg := Config{
		LocalTmux:    func(...string) error { return nil },
		LocalTmuxOut: func(...string) (string, error) { return "0\n", nil }, // local not zoomed -> toggle fires
	}

	// Run to completion first, then read: enqueue is a non-blocking select over
	// a 4096-deep channel, so nothing here waits on a reader. Only the sinks'
	// own pumps park in wire.WriteFrame, which is what the reads below release.
	reconcileLayout(cfg, w, func(string) {}, router, noHellos, newCtlState(), newConverger(), rt)

	// Without a deadline, a regression here hangs to the package timeout
	// instead of failing.
	peerA.SetDeadline(time.Now().Add(5 * time.Second))
	peerB.SetDeadline(time.Now().Add(5 * time.Second))

	fA, err := wire.ReadFrame(peerA)
	if err != nil {
		t.Fatalf("read pane 0's first frame: %v", err)
	}
	if fA.Type != wire.FrameResize {
		t.Fatalf("pane 0's first frame = %v, want a resize", fA.Type)
	}
	wA, hA, err := wire.DecodeResize(fA.Payload)
	if err != nil {
		t.Fatalf("decode pane 0's resize payload: %v", err)
	}
	if wA != 190 || hA != 45 {
		t.Errorf("pane 0 (zoomed, active) dims = %dx%d, want 190x45 (the layout root)", wA, hA)
	}

	fB, err := wire.ReadFrame(peerB)
	if err != nil {
		t.Fatalf("read pane 1's first frame: %v", err)
	}
	if fB.Type != wire.FrameResize {
		t.Fatalf("pane 1's first frame = %v, want a resize", fB.Type)
	}
	wB, hB, err := wire.DecodeResize(fB.Payload)
	if err != nil {
		t.Fatalf("decode pane 1's resize payload: %v", err)
	}
	if wB != 94 || hB != 45 {
		t.Errorf("pane 1 (unzoomed) dims = %dx%d, want 94x45 (its own cell)", wB, hB)
	}
}

// TestReconcileKeepsPaneCellDimsOnUnknownZoomState pins the first failure leg
// of #511's gating fix: when localZoomed can't be established (ok=false), the
// toggle block never fires — there is nothing to compare against — and the
// dims loop must key on localIsZoomed (still false, its last-known value), not
// on the remote's zoomed flag. A pass must never skip its reseed over this: it
// falls through to the same FrameResize + reseed as any other pass.
func TestReconcileKeepsPaneCellDimsOnUnknownZoomState(t *testing.T) {
	localA, peerA := net.Pipe()
	defer localA.Close()
	defer peerA.Close()
	localB, peerB := net.Pipe()
	defer localB.Close()
	defer peerB.Close()

	router := NewRouter()
	router.Register("%0", newOutputSink(localA, nil))
	router.Register("%1", newOutputSink(localB, nil))

	w := &mirrorWindow{
		remoteID: "@1", localWin: "@101",
		remotePanes: []string{"%0", "%1"}, localPanes: []string{"%l0", "%l1"},
	}

	// Same fixture as TestReconcileGivesZoomedPaneTheWindowDims: root 190x45,
	// pane 0's own cell 95x45 — root and cell disagree, so the assertion can't
	// pass by coincidence.
	const layout = "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"
	script := strings.Join([]string{
		"%begin 1 1 1", layout + " %0 1", "%end 1 1 1", // readLayout: remote zoomed, pane 0 active
		"%begin 1 2 1", "0 0 0 0", "%end 1 2 1", // PaneSeed(%0): cursor
		"%begin 1 3 1", "SEED-0", "%end 1 3 1", // PaneSeed(%0): capture
		"%begin 1 4 1", "0 0 0 0", "%end 1 4 1", // PaneSeed(%1): cursor
		"%begin 1 5 1", "SEED-1", "%end 1 5 1", // PaneSeed(%1): capture
		"%begin 1 6 1", layout + " %0 1", "%end 1 6 1", // trailing re-read: unchanged, stop
	}, "\n") + "\n"

	rt := scriptedRTRouter(script, router)

	cfg := Config{
		LocalTmux: func(...string) error { return nil },
		// localZoomed's read seam errors -> ok=false, no toggle attempted.
		LocalTmuxOut: func(...string) (string, error) { return "", errors.New("display-message: no such window") },
	}

	reconcileLayout(cfg, w, func(string) {}, router, noHellos, newCtlState(), newConverger(), rt)

	peerA.SetDeadline(time.Now().Add(5 * time.Second))
	peerB.SetDeadline(time.Now().Add(5 * time.Second))

	fA, err := wire.ReadFrame(peerA)
	if err != nil {
		t.Fatalf("read pane 0's first frame: %v", err)
	}
	if fA.Type != wire.FrameResize {
		t.Fatalf("pane 0's first frame = %v, want a resize", fA.Type)
	}
	wA, hA, err := wire.DecodeResize(fA.Payload)
	if err != nil {
		t.Fatalf("decode pane 0's resize payload: %v", err)
	}
	if wA != 95 || hA != 45 {
		t.Errorf("pane 0 (unknown local zoom state, active) dims = %dx%d, want 95x45 (its own cell, not the root)", wA, hA)
	}

	fB, err := wire.ReadFrame(peerB)
	if err != nil {
		t.Fatalf("read pane 1's first frame: %v", err)
	}
	wB, hB, err := wire.DecodeResize(fB.Payload)
	if err != nil {
		t.Fatalf("decode pane 1's resize payload: %v", err)
	}
	if wB != 94 || hB != 45 {
		t.Errorf("pane 1 dims = %dx%d, want 94x45 (its own cell)", wB, hB)
	}
}

// TestReconcileKeepsPaneCellDimsOnZoomToggleFailure pins the second failure
// leg: local zoom state IS known (unzoomed) and disagrees with the remote, so
// the toggle is attempted, but the resize-pane call itself errors. localIsZoomed
// must stay false — the daemon never actually imposed the zoom — so the active
// pane still gets its own cell, not the root. LocalTmux fails only for
// resize-pane so the rest of the pass (resize-window, select-layout) is
// unaffected.
func TestReconcileKeepsPaneCellDimsOnZoomToggleFailure(t *testing.T) {
	localA, peerA := net.Pipe()
	defer localA.Close()
	defer peerA.Close()
	localB, peerB := net.Pipe()
	defer localB.Close()
	defer peerB.Close()

	router := NewRouter()
	router.Register("%0", newOutputSink(localA, nil))
	router.Register("%1", newOutputSink(localB, nil))

	w := &mirrorWindow{
		remoteID: "@1", localWin: "@101",
		remotePanes: []string{"%0", "%1"}, localPanes: []string{"%l0", "%l1"},
	}

	const layout = "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"
	script := strings.Join([]string{
		"%begin 1 1 1", layout + " %0 1", "%end 1 1 1", // readLayout: remote zoomed, pane 0 active
		"%begin 1 2 1", "0 0 0 0", "%end 1 2 1", // PaneSeed(%0): cursor
		"%begin 1 3 1", "SEED-0", "%end 1 3 1", // PaneSeed(%0): capture
		"%begin 1 4 1", "0 0 0 0", "%end 1 4 1", // PaneSeed(%1): cursor
		"%begin 1 5 1", "SEED-1", "%end 1 5 1", // PaneSeed(%1): capture
		"%begin 1 6 1", layout + " %0 1", "%end 1 6 1", // trailing re-read: unchanged, stop
	}, "\n") + "\n"

	rt := scriptedRTRouter(script, router)

	cfg := Config{
		LocalTmux: func(args ...string) error {
			if len(args) > 0 && args[0] == "resize-pane" {
				return errors.New("resize-pane: pane not found")
			}
			return nil
		},
		LocalTmuxOut: func(...string) (string, error) { return "0\n", nil }, // local not zoomed -> toggle attempted
	}

	reconcileLayout(cfg, w, func(string) {}, router, noHellos, newCtlState(), newConverger(), rt)

	peerA.SetDeadline(time.Now().Add(5 * time.Second))
	peerB.SetDeadline(time.Now().Add(5 * time.Second))

	fA, err := wire.ReadFrame(peerA)
	if err != nil {
		t.Fatalf("read pane 0's first frame: %v", err)
	}
	if fA.Type != wire.FrameResize {
		t.Fatalf("pane 0's first frame = %v, want a resize", fA.Type)
	}
	wA, hA, err := wire.DecodeResize(fA.Payload)
	if err != nil {
		t.Fatalf("decode pane 0's resize payload: %v", err)
	}
	if wA != 95 || hA != 45 {
		t.Errorf("pane 0 (zoom toggle failed, active) dims = %dx%d, want 95x45 (its own cell, not the root)", wA, hA)
	}

	fB, err := wire.ReadFrame(peerB)
	if err != nil {
		t.Fatalf("read pane 1's first frame: %v", err)
	}
	wB, hB, err := wire.DecodeResize(fB.Payload)
	if err != nil {
		t.Fatalf("decode pane 1's resize payload: %v", err)
	}
	if wB != 94 || hB != 45 {
		t.Errorf("pane 1 dims = %dx%d, want 94x45 (its own cell)", wB, hB)
	}
}
