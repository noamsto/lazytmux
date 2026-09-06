package daemon

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// paneLister answers list-panes with a fixed localPaneListFormat body and
// records whether anything asked, so a case can assert the retry was skipped
// without a round-trip rather than merely that it did not reshape.
type paneLister struct {
	mu    sync.Mutex
	body  string
	asked []string
}

func (p *paneLister) out(argv ...string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, strings.Join(argv, " "))
	return p.body, nil
}

func (p *paneLister) config() Config {
	return Config{
		LocalSess:    "host-sess",
		LocalTmux:    func(...string) error { return nil },
		LocalTmuxOut: p.out,
	}
}

// A window whose shape failed and still holds the float that failed it must not
// burn a readLayout round-trip: while the float is open the reshape can only
// fail again (tmux has no float-tolerant select-layout — tmux/tmux#5577).
func TestRetryFailedShapesSkipsAWindowStillHoldingAFloat(t *testing.T) {
	p := &paneLister{body: "%l0 0\n%l9 1\n"}
	reg := newRegistry()
	w := reg.add("@1", "@101")
	w.shapeFailedFor = "some-layout"

	// emptyRemote's script answers exactly one round-trip; reaching it would
	// therefore also desync the ordinal count, not merely cost a call.
	retryFailedShapes(p.config(), func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())

	if w.shapeFailedFor != "some-layout" {
		t.Errorf("shapeFailedFor = %q, want it untouched: nothing has changed for the retry to succeed on", w.shapeFailedFor)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.asked) != 1 || !strings.Contains(p.asked[0], "list-panes") {
		t.Errorf("local calls = %v, want exactly the one list-panes float check", p.asked)
	}
}

// The case the retry exists for: the user closed the float, and no remote event
// will follow. reconcileLayout only runs on a %layout-change, a coalesced batch
// of them, or a reattach — so without this pass the mirror kept the stale shape
// until the remote happened to move that window again.
func TestRetryFailedShapesReconcilesOnceTheFloatIsGone(t *testing.T) {
	p := &paneLister{body: "%l0 0\n"}
	reg := newRegistry()
	w := reg.add("@1", "@101")
	w.shapeFailedFor = "some-layout"

	// The stream's writes are the evidence: reconcileLayout's first act is a
	// readLayout round-trip, so a display-message on the wire proves the retry
	// reached it rather than skipping the window.
	var wire bytes.Buffer
	rt := scriptedRTRouterW("%begin 1 1 1\n%end 1 1 1\n", NewRouter(), &wire)

	retryFailedShapes(p.config(), func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), rt)

	if got := wire.String(); !strings.Contains(got, "window_layout") {
		t.Errorf("stream wrote %q, want the readLayout display-message: the retry never reconciled", got)
	}
}

// A window that never failed is not probed at all: the steady state is every
// window in that condition, so the check has to cost nothing there.
func TestRetryFailedShapesIgnoresAWindowThatNeverFailed(t *testing.T) {
	p := &paneLister{body: "%l0 0\n"}
	reg := newRegistry()
	reg.add("@1", "@101")

	retryFailedShapes(p.config(), func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.asked) != 0 {
		t.Errorf("local calls = %v, want none for a window with no failed shape", p.asked)
	}
}
