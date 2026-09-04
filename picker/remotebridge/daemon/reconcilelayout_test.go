package daemon

import (
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// The two remote layouts every test below is built from. They share a Raw by
// construction: ParseLayout prunes the float leaf out of the tree, collapses
// nothing (the split keeps two children) and recomputes the checksum over the
// same body — so a float appearing changes L.Floats and leaves L.Raw
// byte-identical, which is exactly the blindness the convergence test and the
// "nothing to do" short-circuit have to cover.
const (
	tiledLayout      = "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"
	tiledFloatLayout = "9999,190x45,0,0{95x45,0,0,0,94x45,96,0,1,18x6,11,6,9}<18x6,11,6,9>"
	// The mirror window's own layout, read back by the geometry short-circuit:
	// the same cells under the local renderers' own pane ids, plus the local
	// float mirroring %9.
	localMatchingLayout = "0000,190x45,0,0{95x45,0,0,70,94x45,96,0,71,18x6,11,6,72}<18x6,11,6,72>"
	// One tiled pane where the remote has two — a length mismatch, which is a
	// short-circuit miss.
	localShortLayout = "0000,190x45,0,0,70"
	// What setupWindow reads back when the reset path rebuilds the window.
	onePaneFloatLayout = "9999,190x45,0,0{190x45,0,0,0,18x6,11,6,9}<18x6,11,6,9>"
	// The same tiled pair under two floats, for the case where one of them is
	// already mirrored: same Raw again, so only L.Floats separates it from
	// tiledFloatLayout.
	twoFloatLayout = "9999,190x45,0,0{95x45,0,0,0,94x45,96,0,1,18x6,11,6,9,20x8,30,10,8}<18x6,11,6,9,20x8,30,10,8>"
)

// float9 is the cell tiledFloatLayout reports for remote float %9, and
// wantFloatCreate the create it mirrors to — the cell's outer box, since
// new-pane's flags take the border inset the cell does not.
var (
	float9          = controlmode.PaneCell{ID: "%9", W: 18, H: 6, X: 11, Y: 6}
	float8          = controlmode.PaneCell{ID: "%8", W: 20, H: 8, X: 30, Y: 10}
	wantFloatCreate = []string{
		"new-pane", "-d", "-P", "-F", "#{pane_id}", "-t", "@101",
		"-B", "heavy", "-A", "-x", "20", "-y", "8", "-X", "10", "-Y", "5",
	}
)

// shapedMirror is the fixture for the float-only paths: a two-pane mirror
// already carrying the tiled shape, so only the float set can differ from what
// the remote reports.
func shapedMirror(t *testing.T) *mirrorWindow {
	t.Helper()
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.layout = mustLayout(t, tiledLayout).Raw
	return w
}

// drainedPipe is a wired renderer whose end of the socket is read and thrown
// away, for the panes a test only needs to exist.
func drainedPipe(t *testing.T) net.Conn {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() { conn.Close(); peer.Close() })
	go io.Copy(io.Discard, peer)
	return conn
}

// floatSeedScript is the reply pair one float's seed round-trip consumes,
// numbered from seq.
func floatSeedScript(seq int, screen string) string {
	return fmt.Sprintf("%%begin 1 %d 1\n0 0 0 0\n%%end 1 %d 1\n%%begin 1 %d 1\n%s\n%%end 1 %d 1\n",
		seq, seq, seq+1, screen, seq+1)
}

// layoutTmux fakes the local tmux seam for the shape path, recording every argv
// and answering each read by what it asks for rather than by call order — the
// code under test interleaves window_layout, window_zoomed_flag, window_id and
// list-panes reads, so a positional script would break on any reordering.
type layoutTmux struct {
	mu   sync.Mutex
	argv [][]string

	windowLayout    string
	windowLayoutErr error
	selectLayoutErr error
	// listPanes is consumed one entry per call, the last repeating: the Append
	// path re-reads the window before and after its split.
	listPanes  []string
	panesRead  int
	newPaneIDs []string
	created    int
	windowID   string
}

func (f *layoutTmux) run(argv ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argv = append(f.argv, argv)
	if argv[0] == "select-layout" {
		return f.selectLayoutErr
	}
	return nil
}

func (f *layoutTmux) out(argv ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argv = append(f.argv, argv)
	switch argv[0] {
	case "display-message":
		switch argv[len(argv)-1] {
		case "#{window_layout}":
			return f.windowLayout, f.windowLayoutErr
		case "#{window_id}":
			return f.windowID, nil
		}
		return "0\n", nil // #{window_zoomed_flag}: not zoomed
	case "list-panes":
		i := f.panesRead
		f.panesRead++
		if i >= len(f.listPanes) {
			if len(f.listPanes) == 0 {
				return "", nil
			}
			return f.listPanes[len(f.listPanes)-1], nil
		}
		return f.listPanes[i], nil
	case "new-pane":
		i := f.created
		f.created++
		if i >= len(f.newPaneIDs) {
			return "%lnew\n", nil
		}
		return f.newPaneIDs[i], nil
	}
	return "", nil
}

func (f *layoutTmux) config() Config {
	return Config{
		RendererBin:  "/nix/store/renderer",
		LocalTmux:    f.run,
		LocalTmuxOut: f.out,
		LocalArea:    func() (int, int) { return 190, 45 },
	}
}

// verbs returns every recorded argv issuing verb, so a count assertion names
// the command rather than a trace position.
func (f *layoutTmux) verbs(verb string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, a := range f.argv {
		if a[0] == verb {
			out = append(out, a)
		}
	}
	return out
}

// at returns the trace position of the first argv issuing verb against target,
// or -1, for the assertions that are about ordering.
func (f *layoutTmux) at(verb, target string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.argv {
		if a[0] != verb {
			continue
		}
		for _, word := range a {
			if word == target {
				return i
			}
		}
	}
	return -1
}

func mustLayout(t *testing.T, s string) controlmode.Layout {
	t.Helper()
	L, err := controlmode.ParseLayout(s)
	if err != nil {
		t.Fatalf("ParseLayout(%q): %v", s, err)
	}
	return L
}

// mirrorWithFloat is the shared fixture: a two-pane mirror already carrying one
// mirrored float, whose recorded shape is stale so applyLayout has work to do.
func mirrorWithFloat() *mirrorWindow {
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"
	return w
}

// The cheap path: the FitWindowCmd resize alone already reproduced the remote's
// cells (the remote's layout_resize is deterministic and path-independent), so
// the shape needs no select-layout — and no float has to die for one. Verified
// per pass against the mirror's own window_layout rather than assumed, so the
// compare is on cells only: the pane ids belong to two different hosts.
func TestApplyLayoutShortCircuitsWhenTheFitAlreadyMatched(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout}
	w := mirrorWithFloat()
	L := mustLayout(t, tiledFloatLayout)

	if !applyLayout(f.config(), w, L, NewRouter()) {
		t.Fatal("applyLayout ok = false, want true: the window already carries L's cells")
	}
	if got := f.verbs("select-layout"); got != nil {
		t.Errorf("issued %v, want no select-layout at all", got)
	}
	if got := f.verbs("kill-pane"); got != nil {
		t.Errorf("issued %v, want the float left alone", got)
	}
	if w.layout != L.Raw {
		t.Errorf("w.layout = %q, want %q so a later pass neither re-pays the read nor reads as failed", w.layout, L.Raw)
	}
	if w.localFloats["%9"] != "%l9" {
		t.Errorf("localFloats = %v, want the mirrored float still there", w.localFloats)
	}
	if w.floatsDropped {
		t.Error("floatsDropped = true on a hit; the token must stay unspent for a later real drop")
	}
}

// A pane count that disagrees is a miss, not a partial match: the pairing is
// positional, so there is no way to read a shorter local list as "the same
// cells". The drop then makes the window float-free for select-layout, which
// tmux otherwise refuses outright ("have 4 panes but need 3").
func TestApplyLayoutDropsFloatsWhenTheCellsDisagree(t *testing.T) {
	f := &layoutTmux{windowLayout: localShortLayout}
	w := mirrorWithFloat()
	L := mustLayout(t, tiledFloatLayout)

	if !applyLayout(f.config(), w, L, NewRouter()) {
		t.Fatal("applyLayout ok = false, want true: select-layout succeeded")
	}
	want := []string{"kill-pane", "-t", "%l9"}
	if got := f.verbs("kill-pane"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("kill-pane argv = %v, want exactly one %v", got, want)
	}
	if f.at("kill-pane", "%l9") > f.at("select-layout", L.Raw) {
		t.Error("select-layout ran before the drop; the window still held a float tmux would count")
	}
	if len(w.localFloats) != 0 || len(w.floatGeom) != 0 {
		t.Errorf("after the drop: localFloats=%v floatGeom=%v, want both empty so reconcileFloats re-adds",
			w.localFloats, w.floatGeom)
	}
	if !w.floatsDropped {
		t.Error("floatsDropped = false; a second applyLayout in the same pass would respawn the renderers again")
	}
}

// The short-circuit's read is an optimisation, so an unreadable window costs a
// respawn, not correctness — and must not take the daemon down on the way.
func TestApplyLayoutDropsFloatsWhenTheLocalReadFails(t *testing.T) {
	f := &layoutTmux{windowLayoutErr: errors.New("can't find window: @101")}
	w := mirrorWithFloat()
	L := mustLayout(t, tiledFloatLayout)

	if !applyLayout(f.config(), w, L, NewRouter()) {
		t.Fatal("applyLayout ok = false, want true: select-layout succeeded")
	}
	if got := f.verbs("kill-pane"); len(got) != 1 {
		t.Errorf("kill-pane argv = %v, want the drop to have run once", got)
	}
	if got := f.verbs("select-layout"); len(got) != 1 {
		t.Errorf("select-layout argv = %v, want the shape still applied", got)
	}
}

// A mirror window can hold a float this daemon never made — prefix + b/k/i are
// unguarded float binds — and the daemon must not reap a pane of the user's.
// Only ids in localFloats are killed, so select-layout still counts the foreign
// float and still fails; the mirror keeps its last-good screen. A documented
// degradation, not a handled case.
func TestApplyLayoutNeverKillsAFloatItDidNotCreate(t *testing.T) {
	f := &layoutTmux{
		windowLayout:    localShortLayout,
		selectLayoutErr: errors.New("have 4 panes but need 3"),
	}
	w := mirrorWithFloat()
	L := mustLayout(t, tiledFloatLayout)

	if applyLayout(f.config(), w, L, NewRouter()) {
		t.Fatal("applyLayout ok = true, want false so the caller suppresses the broadcast")
	}
	// %lforeign is the window's other float. It has no localFloats entry, which
	// is precisely why nothing here can name it.
	for _, a := range f.verbs("kill-pane") {
		for _, word := range a {
			if word == "%lforeign" {
				t.Fatalf("killed a float the daemon did not create: %v", a)
			}
		}
	}
	if got := f.verbs("kill-pane"); len(got) != 1 || got[0][len(got[0])-1] != "%l9" {
		t.Errorf("kill-pane argv = %v, want only the daemon's own %%l9", got)
	}
	if w.shapeFailedFor != L.Raw {
		t.Errorf("shapeFailedFor = %q, want %q so the next identical pass logs nothing", w.shapeFailedFor, L.Raw)
	}
	// A later pass that does land the shape clears the record, so a genuinely
	// new failure is reported rather than swallowed.
	f.selectLayoutErr = nil
	w.layout = "stale"
	if !applyLayout(f.config(), w, L, NewRouter()) {
		t.Fatal("applyLayout ok = false on the retry")
	}
	if w.shapeFailedFor != "" {
		t.Errorf("shapeFailedFor = %q after a successful shape, want cleared", w.shapeFailedFor)
	}
}

// The drop is per reconcileLayout call, not per applyLayout: a pass that does
// pane surgery calls applyLayout twice (once inside applyPaneOps, once after
// it), and each drop respawns every mirrored renderer.
func TestReconcileLayoutDropsFloatsOnceAcrossBothShapeAttempts(t *testing.T) {
	f := &layoutTmux{
		windowLayout: localShortLayout,
		// The window before the split, then after it — the Append path re-reads
		// both times. The float keeps its own ordinal slot in tmux's listing.
		listPanes:  []string{"%l0 0\n", "%l0 0\n%l1 0\n%l9 1\n"},
		newPaneIDs: []string{"%lre\n"},
		windowID:   "@101\n",
	}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0"}
	w.localPanes = []string{"%l0"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"
	// A stale token from an earlier call must not suppress this call's drop.
	w.floatsDropped = true

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // readLayout
		"%begin 1 2 1", tiledFloatLayout + " %0 0", "%end 1 2 1", // trailing re-read: converged
	}, "\n") + "\n")

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(), noHellos,
		newCtlState(), newConverger(), rt); retire {
		t.Fatal("reconcileLayout retire = true, want false")
	}

	var killedFloat int
	for _, a := range f.verbs("kill-pane") {
		if a[len(a)-1] == "%l9" {
			killedFloat++
		}
	}
	if killedFloat != 1 {
		t.Errorf("killed %%l9 %d times, want exactly 1 across both applyLayout calls", killedFloat)
	}
	if got := f.verbs("select-layout"); len(got) != 1 {
		t.Errorf("select-layout argv = %v, want one: the second call site finds the shape already applied", got)
	}
	// The post-loop reconcileFloats is what makes the drop survivable.
	if f.at("new-pane", "@101") < f.at("kill-pane", "%l9") {
		t.Errorf("the float was not re-added after the drop: %v", f.argv)
	}
}

// A shape that did not land leaves the local panes on tmux's own auto-geometry,
// so broadcasting the remote's dims and screen into them paints a mirror that
// reads as blank. Both halves are gated together.
func TestReconcileLayoutSuppressesTheBroadcastWhenTheShapeFails(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	router := NewRouter()
	router.Register("%0", newOutputSink(conn, nil))

	f := &layoutTmux{selectLayoutErr: errors.New("invalid layout"), windowID: "@101\n"}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.layout = "stale"

	var issued []string
	rt := recordingRT(strings.Join([]string{
		"%begin 1 1 1", tiledLayout + " %0 0", "%end 1 1 1", // readLayout
		"%begin 1 2 1", tiledLayout + " %0 0", "%end 1 2 1", // trailing re-read: converged
	}, "\n")+"\n", &issued)

	reconcileLayout(f.config(), w, func(string) {}, router, noHellos, newCtlState(), newConverger(), rt)

	for _, cmd := range issued {
		if strings.Contains(cmd, "capture-pane") {
			t.Fatalf("re-seeded after a failed shape: %q", cmd)
		}
	}
	// The FrameResize rides the same gate, so nothing at all should reach the
	// renderer.
	peer.SetDeadline(time.Now().Add(250 * time.Millisecond))
	if fr, err := wire.ReadFrame(peer); err == nil {
		t.Fatalf("sent %v %q to a pane that never took the shape", fr.Type, fr.Payload)
	}
	if w.layout == mustLayout(t, tiledLayout).Raw {
		t.Error("w.layout records a shape that failed; a later pass would then skip select-layout entirely")
	}
}

// L.Raw and the zoom flag are both float-blind, so a float that appeared
// between the apply and the trailing re-read would read as converged — and the
// post-loop reconcileFloats would then apply the float set from before it
// existed.
func TestReconcileLayoutRerunsWhenOnlyTheFloatSetMoved(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n", newPaneIDs: []string{"%lf\n"}}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.layout = "stale"

	var issued []string
	rt := recordingRT(strings.Join([]string{
		"%begin 1 1 1", tiledLayout + " %0 0", "%end 1 1 1", // readLayout: no float yet
		"%begin 1 2 1", tiledFloatLayout + " %0 0", "%end 1 2 1", // trailing: same Raw, one float
		"%begin 1 3 1", tiledFloatLayout + " %0 0", "%end 1 3 1", // second pass's trailing: converged
	}, "\n")+"\n", &issued)

	reconcileLayout(f.config(), w, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	var reads int
	for _, cmd := range issued {
		if strings.Contains(cmd, "window_layout") {
			reads++
		}
	}
	if reads != 3 {
		t.Errorf("%d layout reads, want 3: the float-only change has to force a second pass", reads)
	}
	if got := f.verbs("new-pane"); len(got) != 1 || !reflect.DeepEqual(got[0], wantFloatCreate) {
		t.Errorf("new-pane argv = %v, want exactly one %v — the float from the FRESH read", got, wantFloatCreate)
	}
}

// The remote replaced every pane, so resetWindow rebuilds the window from
// scratch — and that exit returns rather than breaking out of the pass loop:
// setupWindow has already redone the shape, the pane list AND the floats, so a
// second reconcileFloats after the loop would re-create a float the rebuild
// just finished settling. The rebuild's own float add is denied its hello here,
// so it ends with an empty floatGeom — the state a stray tail pass would act
// on, which is what makes the difference visible as a second new-pane.
func TestReconcileLayoutResetPathSkipsTheTail(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	go io.Copy(io.Discard, peer)

	f := &layoutTmux{
		windowLayout: localMatchingLayout,
		listPanes:    []string{"%l0 0\n"},
		newPaneIDs:   []string{"%lf\n"},
		windowID:     "@101\n",
	}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%7"} // no surviving pane -> planPaneOps.Reset
	w.localPanes = []string{"%l7"}
	w.layout = "stale"

	// The tiled pane hellos, the float does not: setupWindow's own
	// reconcileFloats then adds and immediately drops it.
	var helloCalls int
	waiter := func(int) (map[string]net.Conn, error) {
		helloCalls++
		if helloCalls == 1 {
			return map[string]net.Conn{"%0": conn}, nil
		}
		return nil, nil
	}

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // reconcileLayout's readLayout
		"%begin 1 2 1", "", "%end 1 2 1", // setupWindow's ConvergeCmd
		"%begin 1 3 1", onePaneFloatLayout + " %0 0", "%end 1 3 1", // setupWindow's readLayout
		"%begin 1 4 1", "0 0 0 0", "%end 1 4 1", // PaneSeed(%0): cursor
		"%begin 1 5 1", "RESEEDED", "%end 1 5 1", // PaneSeed(%0): capture
	}, "\n") + "\n")

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(), waiter,
		newCtlState(), newConverger(), rt); retire {
		t.Fatal("reconcileLayout retire = true; the local window is still there")
	}
	if got := f.verbs("new-pane"); len(got) != 1 {
		t.Errorf("new-pane argv = %v, want exactly one: the rebuild owns the window's floats, the pass must not add again", got)
	}
}

// A float opening on the remote changes L.Floats and leaves L.Raw and the zoom
// flag untouched, so the pre-loop "nothing to do" exit has to consult the float
// set too — otherwise the mirror never learns about the very floats this whole
// path exists to render.
func TestReconcileLayoutDoesNotSkipAFloatOnlyChange(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n", newPaneIDs: []string{"%lf\n"}}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.layout = mustLayout(t, tiledLayout).Raw // already shaped: only the float is new

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // readLayout
		"%begin 1 2 1", tiledFloatLayout + " %0 0", "%end 1 2 1", // trailing re-read: converged
	}, "\n") + "\n")

	reconcileLayout(f.config(), w, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if got := f.verbs("new-pane"); len(got) != 1 || !reflect.DeepEqual(got[0], wantFloatCreate) {
		t.Errorf("new-pane argv = %v, want exactly one %v", got, wantFloatCreate)
	}
}

// A float opening moves no tiled pane, so the tiled panes must see nothing at
// all: the shape they already carry is the shape L asks for, and pushing them
// the remote's dims and a fresh capture-pane apiece is a full repaint of every
// sibling — plus the round-trip traffic — for a change none of them took part
// in. Every float bind and every carousel toggle lands here.
func TestReconcileLayoutFloatOnlyChangeLeavesTheTiledPanesAlone(t *testing.T) {
	router := NewRouter()
	peers := map[string]net.Conn{}
	for _, id := range []string{"%0", "%1"} {
		conn, peer := net.Pipe()
		defer conn.Close()
		defer peer.Close()
		router.Register(id, newOutputSink(conn, nil))
		peers[id] = peer
	}

	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n", newPaneIDs: []string{"%lf\n"}}
	w := shapedMirror(t)

	var issued []string
	rt := recordingRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // readLayout: only the float is new
	}, "\n")+"\n"+floatSeedScript(2, "FLOAT-SEED"), &issued)

	reconcileLayout(f.config(), w, func(string) {}, router,
		hellos(map[string]net.Conn{"%9": drainedPipe(t)}), newCtlState(), newConverger(), rt)

	if got := f.verbs("new-pane"); len(got) != 1 {
		t.Fatalf("new-pane argv = %v, want the float mirrored exactly once", got)
	}
	for _, cmd := range issued {
		for _, tiled := range []string{"%0", "%1"} {
			if strings.Contains(cmd, "capture-pane") && strings.HasSuffix(cmd, tiled) {
				t.Errorf("re-seeded tiled pane %s for a float-only change: %q", tiled, cmd)
			}
		}
	}
	for id, peer := range peers {
		peer.SetDeadline(time.Now().Add(250 * time.Millisecond))
		if fr, err := wire.ReadFrame(peer); err == nil {
			t.Errorf("sent %v %q to tiled pane %s, which did not move", fr.Type, fr.Payload, id)
		}
	}
	if got := f.verbs("select-layout"); got != nil {
		t.Errorf("issued %v, want no reshape: the tiled shape already matches", got)
	}
}

// A float appearing is not structural — no tiled pane moved — so the pass
// loop's focus-follow never fires for one. But the verb that opened it (prefix
// + g) made it the remote's active pane, and without following, the user's
// keystrokes go on landing in a tiled renderer.
func TestReconcileLayoutFocusFollowsANewlyAddedFloat(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n", newPaneIDs: []string{"%lf\n"}}
	w := shapedMirror(t)

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %9 0", "%end 1 1 1", // the float is the remote's active pane
	}, "\n") + "\n" + floatSeedScript(2, "FLOAT-SEED"))

	reconcileLayout(f.config(), w, func(string) {}, NewRouter(),
		hellos(map[string]net.Conn{"%9": drainedPipe(t)}), newCtlState(), newConverger(), rt)

	want := []string{"select-pane", "-t", "%lf"}
	if got := f.verbs("select-pane"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("select-pane argv = %v, want exactly one %v", got, want)
	}
}

// The follow is gated on NEWLY added, never on "the active pane is a float":
// re-asserting focus on a float already mirrored would fight the user's own
// local focus on every unrelated reconcile — here, a second float opening
// somewhere else in the same window.
func TestReconcileLayoutDoesNotRefocusAnAlreadyMirroredFloat(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n", newPaneIDs: []string{"%lf8\n"}}
	w := shapedMirror(t)
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", twoFloatLayout + " %9 0", "%end 1 1 1", // %9 is still active, %8 is the new one
	}, "\n") + "\n" + floatSeedScript(2, "FLOAT-SEED"))

	reconcileLayout(f.config(), w, func(string) {}, NewRouter(),
		hellos(map[string]net.Conn{"%8": drainedPipe(t)}), newCtlState(), newConverger(), rt)

	if w.localFloats["%8"] != "%lf8" {
		t.Fatalf("localFloats = %v, want the second float mirrored", w.localFloats)
	}
	if got := f.verbs("select-pane"); got != nil {
		t.Errorf("issued %v, want local focus left where the user put it", got)
	}
}

// A layout string in a different float order is the same window: nothing
// promises tmux emits the float section in a stable order, and reading it as
// changed costs a whole extra reconcile pass ending in a spurious
// "didn't converge".
func TestFloatCellsEqualIgnoresOrder(t *testing.T) {
	a := []controlmode.PaneCell{float9, float8}
	b := []controlmode.PaneCell{float8, float9}
	if !floatCellsEqual(a, b) {
		t.Errorf("floatCellsEqual(%v, %v) = false, want true: same floats, same geometry", a, b)
	}
	moved := float8
	moved.X++
	if floatCellsEqual(a, []controlmode.PaneCell{moved, float9}) {
		t.Error("floatCellsEqual = true for a float that moved, want false")
	}
	if floatCellsEqual(a, []controlmode.PaneCell{float9, float9}) {
		t.Error("floatCellsEqual = true for a different float set, want false")
	}
}

// applyPaneOps' own applyLayout drops every mirrored float so a select-layout
// can land, and a failure after that returns straight out — past the post-loop
// reconcileFloats that would have put them back. Left there, the floats stay
// dead until some later reconcile of this window happens to succeed.
func TestReconcileLayoutReAddsFloatsAfterAFailedPaneOp(t *testing.T) {
	f := &layoutTmux{
		windowLayout: localShortLayout, // a cell miss, so the shape needs the drop
		listPanes:    []string{"%l0 0\n", "%l0 0\n%l1 0\n"},
		newPaneIDs:   []string{"%lre\n"},
		windowID:     "@101\n",
	}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0"}
	w.localPanes = []string{"%l0"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"

	// The appended pane's renderer never connects, which is what fails
	// applyPaneOps after the drop; the float's own wait then succeeds.
	calls := 0
	waiter := func(int) (map[string]net.Conn, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("renderer never connected")
		}
		return map[string]net.Conn{"%9": drainedPipe(t)}, nil
	}

	rt, _ := scriptedRT("%begin 1 1 1\n" + tiledFloatLayout + " %0 0\n%end 1 1 1\n")

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(), waiter,
		newCtlState(), newConverger(), rt); retire {
		t.Fatal("reconcileLayout retire = true; the local window is still there")
	}
	if w.localFloats["%9"] != "%lre" {
		t.Fatalf("localFloats = %v, want the dropped float re-added", w.localFloats)
	}
	if kill, add := f.at("kill-pane", "%l9"), f.at("new-pane", "@101"); kill < 0 || add < kill {
		t.Errorf("kill at %d, add at %d: want the re-add after the drop", kill, add)
	}
}

// resetLostWindow first: a mirror whose local window is gone is retired, and
// pointing a burst of new-panes at a window id that no longer resolves can only
// produce one failed create per float.
func TestReconcileLayoutDoesNotReAddFloatsIntoALostWindow(t *testing.T) {
	f := &layoutTmux{
		windowLayout: localShortLayout,
		listPanes:    []string{"%l0 0\n", "%l0 0\n%l1 0\n"},
		windowID:     "@999\n", // not w.localWin: the window is gone
	}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0"}
	w.localPanes = []string{"%l0"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"

	rt, _ := scriptedRT("%begin 1 1 1\n" + tiledFloatLayout + " %0 0\n%end 1 1 1\n")

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(),
		func(int) (map[string]net.Conn, error) { return nil, errors.New("renderer never connected") },
		newCtlState(), newConverger(), rt); !retire {
		t.Fatal("reconcileLayout retire = false, want true: the local window is gone")
	}
	if got := f.verbs("new-pane"); got != nil {
		t.Errorf("issued %v against a window that is gone, want nothing", got)
	}
}

// A rebuild strands the mirrored floats exactly as a select-layout drop does:
// dropMirroredPanes kills them unconditionally, and setupWindow's own
// reconcileFloats is the last thing it does, so a rebuild that fails anywhere
// earlier never puts them back. The local window survives that failure —
// dropMirroredPanes keeps pane 0 — so the mirror stays live and floatless.
func TestReconcileLayoutReAddsFloatsAfterAFailedReset(t *testing.T) {
	f := &layoutTmux{
		windowLayout: localMatchingLayout,
		newPaneIDs:   []string{"%lre\n"},
		windowID:     "@101\n", // still w.localWin: the window is alive
	}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%7"} // no surviving pane -> planPaneOps.Reset
	w.localPanes = []string{"%l7"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // reconcileLayout's readLayout
		"%begin 1 2 1", "", "%end 1 2 1", // setupWindow's ConvergeCmd
		"%begin 1 3 1", "%error 1 3 1", // setupWindow's readLayout fails: the rebuild never reaches its floats
	}, "\n") + "\n" + floatSeedScript(4, "FLOAT-SEED"))

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(),
		hellos(map[string]net.Conn{"%9": drainedPipe(t)}), newCtlState(), newConverger(), rt); retire {
		t.Fatal("reconcileLayout retire = true; the local window is still there")
	}
	if got := f.verbs("new-pane"); len(got) != 1 || !reflect.DeepEqual(got[0], wantFloatCreate) {
		t.Fatalf("new-pane argv = %v, want exactly one %v", got, wantFloatCreate)
	}
	if w.localFloats["%9"] != "%lre" {
		t.Errorf("localFloats = %v, want the dropped float re-added", w.localFloats)
	}
	if kill, add := f.at("kill-pane", "%l9"), f.at("new-pane", "@101"); kill < 0 || add < kill {
		t.Errorf("kill at %d, add at %d: want the re-add after the drop", kill, add)
	}
}

// Same failed rebuild, but the local window really is gone: resetLostWindow is
// consulted first, so the mirror retires instead of aiming one doomed new-pane
// per float at a window id that no longer resolves.
func TestReconcileLayoutDoesNotReAddFloatsAfterAResetIntoALostWindow(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@999\n"}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%7"}
	w.localPanes = []string{"%l7"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1",
		"%begin 1 2 1", "", "%end 1 2 1",
		"%begin 1 3 1", "%error 1 3 1",
	}, "\n") + "\n")

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(), noHellos,
		newCtlState(), newConverger(), rt); !retire {
		t.Fatal("reconcileLayout retire = false, want true: the local window is gone")
	}
	if got := f.verbs("new-pane"); got != nil {
		t.Errorf("issued %v against a window that is gone, want nothing", got)
	}
}

// The desync recovery reaches resetWindow by its own route, and strands the
// floats the same way when the rebuild fails. reconcileFloats touches no tiled
// pane mapping, so re-adding them is orthogonal to the broken mapping that
// forced the rebuild in the first place.
func TestReconcileLayoutReAddsFloatsAfterAFailedDesyncReset(t *testing.T) {
	f := &layoutTmux{
		windowLayout: localMatchingLayout,
		// Two tiled panes where the daemon believes one: the positional mapping
		// applyPaneOps rests on is broken, so it refuses to act on it.
		listPanes:  []string{"%l0 0\n%l1 0\n%l9 1\n"},
		newPaneIDs: []string{"%lre\n"},
		windowID:   "@101\n",
	}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0"} // the remote's second pane makes ops.Append -> structural
	w.localPanes = []string{"%l0"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"

	rt, _ := scriptedRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // reconcileLayout's readLayout
		"%begin 1 2 1", "", "%end 1 2 1", // setupWindow's ConvergeCmd
		"%begin 1 3 1", "%error 1 3 1", // setupWindow's readLayout fails
	}, "\n") + "\n" + floatSeedScript(4, "FLOAT-SEED"))

	if retire := reconcileLayout(f.config(), w, func(string) {}, NewRouter(),
		hellos(map[string]net.Conn{"%9": drainedPipe(t)}), newCtlState(), newConverger(), rt); retire {
		t.Fatal("reconcileLayout retire = true; the local window is still there")
	}
	if got := f.verbs("new-pane"); len(got) != 1 || !reflect.DeepEqual(got[0], wantFloatCreate) {
		t.Fatalf("new-pane argv = %v, want exactly one %v", got, wantFloatCreate)
	}
	if w.localFloats["%9"] != "%lre" {
		t.Errorf("localFloats = %v, want the dropped float re-added", w.localFloats)
	}
}
