package daemon

import (
	"errors"
	"io"
	"testing"
)

// replacerFixture is the seam with a scripted resolver: the view cell
// advertises adv, and the returned string pointer is what the next resolve
// answers — empty for "nobody attached" (R3).
func replacerFixture(adv string, reconnect bool) (*viewReplacer, *Viewing, *string) {
	view := &Viewing{}
	view.Seed(adv)
	resolved := adv
	rep := newViewReplacer(view, func() (ViewIdentity, bool) {
		if resolved == "" {
			return ViewIdentity{}, false
		}
		return ViewIdentity{Term: resolved}, true
	}, reconnect)
	return rep, view, &resolved
}

// wakeUps stands in for runConn's receive: it drains the seam's channel and
// reports how many raises were pending. Non-blocking, so it also reads zero on
// a seam that raised nothing. Draining it before done() is the real order —
// the loop cannot reach done() without having taken the wake-up first — and
// the buffer holds one, so a test that re-arms without draining would be
// asserting against a state production never reaches.
func wakeUps(rep *viewReplacer) int {
	n := 0
	for {
		select {
		case <-rep.C():
			n++
		default:
			return n
		}
	}
}

// The whole point of the wake-up: runConn would otherwise not come back to the
// attach loop until mainLoopTickInterval, because this gesture sends no
// command of its own (R6).
func TestRaiseWakesTheLoopExactlyOnce(t *testing.T) {
	rep, view, resolved := replacerFixture("xterm-kitty", true)
	*resolved = "foot"

	v, term := rep.raise()
	if v != raiseStarted {
		t.Fatalf("verdict = %v, want raiseStarted (%v)", v, raiseStarted)
	}
	if term != "foot" {
		t.Errorf("term = %q, want foot", term)
	}
	if n := wakeUps(rep); n != 1 {
		t.Errorf("wake-ups = %d, want exactly 1", n)
	}
	// Desired is what the dial argv reads, so the raise must have moved it.
	if got := view.Desired(); got != "foot" {
		t.Errorf("Desired = %q, want foot", got)
	}
	// Only a publish site writes Advertised — never a resolve.
	if got := view.Advertised(); got != "xterm-kitty" {
		t.Errorf("Advertised = %q, want the still-published xterm-kitty", got)
	}
}

// The same terminal, any number of session switches: the published client
// already carries this termname, so a replacement could only spend a dial and
// a repair() to change nothing (R8).
func TestRaiseIgnoresAMatchingTermname(t *testing.T) {
	rep, view, _ := replacerFixture("foot", true)

	if v, _ := rep.raise(); v != raiseNone {
		t.Errorf("verdict = %v, want raiseNone (%v)", v, raiseNone)
	}
	if n := wakeUps(rep); n != 0 {
		t.Errorf("wake-ups = %d, want none", n)
	}
	if got := view.Desired(); got != "foot" {
		t.Errorf("Desired = %q, want foot untouched", got)
	}
}

// R3: an empty resolution is not evidence that nothing should be advertised,
// so it must not reach Desired either — a dial argv reading "" would advertise
// no terminal at all.
func TestRaiseIgnoresAnEmptyResolution(t *testing.T) {
	rep, view, resolved := replacerFixture("xterm-kitty", true)
	*resolved = ""

	if v, term := rep.raise(); v != raiseNone || term != "" {
		t.Errorf("raise = (%v, %q), want (raiseNone, \"\")", v, term)
	}
	if n := wakeUps(rep); n != 0 {
		t.Errorf("wake-ups = %d, want none", n)
	}
	if got := view.Desired(); got != "xterm-kitty" {
		t.Errorf("Desired = %q, want xterm-kitty untouched", got)
	}
}

// Without a re-dialable transport and a recorded identity there is no second
// connection to swap in — but Desired is still worth writing, since it is what
// the argv of any later dial reads.
func TestRaiseWithoutReconnectStillRecordsWhatWeWant(t *testing.T) {
	rep, view, resolved := replacerFixture("xterm-kitty", false)
	*resolved = "foot"

	if v, _ := rep.raise(); v != raiseNone {
		t.Errorf("verdict = %v, want raiseNone (%v)", v, raiseNone)
	}
	if n := wakeUps(rep); n != 0 {
		t.Errorf("wake-ups = %d, want none", n)
	}
	if got := view.Desired(); got != "foot" {
		t.Errorf("Desired = %q, want foot — the next dial's argv reads it", got)
	}
}

// One replacement at a time (R8). The second press must be distinguishable
// from "nothing to do", because its caller has to nack rather than submit.
func TestRaiseDropsASecondRaiseWhileOneIsInFlight(t *testing.T) {
	rep, _, resolved := replacerFixture("xterm-kitty", true)
	*resolved = "foot"

	if v, _ := rep.raise(); v != raiseStarted {
		t.Fatalf("first verdict = %v, want raiseStarted", v)
	}
	v, term := rep.raise()
	if v != raiseInFlight {
		t.Errorf("second verdict = %v, want raiseInFlight (%v)", v, raiseInFlight)
	}
	if term != "foot" {
		t.Errorf("term = %q, want foot — the message needs it on this path too", term)
	}
	if n := wakeUps(rep); n != 1 {
		t.Errorf("wake-ups = %d, want 1 — the second raise must add none", n)
	}
}

// done is what the attach loop calls once replaceConn has returned, so a later
// genuine change can raise again.
func TestDoneReArmsTheSeam(t *testing.T) {
	rep, view, resolved := replacerFixture("xterm-kitty", true)
	*resolved = "foot"

	rep.raise()
	if v, _ := rep.raise(); v != raiseInFlight {
		t.Fatalf("verdict = %v, want raiseInFlight before done", v)
	}
	if n := wakeUps(rep); n != 1 {
		t.Fatalf("wake-ups = %d, want 1 before done", n)
	}
	rep.done()
	// As a completed replacement would have left it.
	view.setAdvertised("foot")
	*resolved = "xterm-ghostty"
	if v, _ := rep.raise(); v != raiseStarted {
		t.Errorf("verdict after done = %v, want raiseStarted", v)
	}
	if n := wakeUps(rep); n != 1 {
		t.Errorf("wake-ups = %d, want the re-armed raise to wake the loop again", n)
	}
}

// SIGTERM works by dropping the transport, so a detach raised before this
// gesture reached the main loop must not be answered with a fresh transport
// for a daemon that is already shutting down — and nothing may be closed on
// the way to finding that out.
func TestReplaceConnRefusesOnceShutdownIsRaised(t *testing.T) {
	f := newReplaceFixture(t)
	stop := make(chan struct{})
	close(stop)
	cfg := f.cfg(func() (io.ReadWriteCloser, error) {
		t.Error("dialled a replacement after shutdown was raised")
		return nil, errors.New("must not dial")
	})
	cfg.Shutdown = stop

	c, outcome := replaceConn(cfg, f.router, f.hold, f.want, f.reg, func() bool {
		t.Error("repair ran after shutdown was raised")
		return true
	})
	if c != nil || outcome != notReplaced {
		t.Errorf("replaceConn = (%v, %v), want (nil, notReplaced)", c, outcome)
	}
	if f.hold.get() != f.old {
		t.Error("the published connection changed; a refused replacement must touch nothing")
	}
	if got := f.view.Advertised(); got != "xterm-kitty" {
		t.Errorf("Advertised = %q, want xterm-kitty unchanged", got)
	}
}

// A raise that loses runConn's select to a connection drop must not outlive
// that connection: the attach loop's connDrop branch cancels it. Without this
// the queued wake-up fires connReplace on the first pass over the connection
// reattach publishes — a replacement with nothing to change, since reattach
// dials with Desired() and records Advertised itself — and inFlight stays set,
// so every later press nacks "press again" while nothing is running.
func TestCancelDiscardsARaiseADropOvertook(t *testing.T) {
	rep, view, resolved := replacerFixture("xterm-kitty", true)
	*resolved = "foot"

	if v, _ := rep.raise(); v != raiseStarted {
		t.Fatalf("verdict = %v, want raiseStarted", v)
	}
	// len, not wakeUps: wakeUps DRAINS, so asserting the queued signal with it
	// would consume the very thing cancel is supposed to remove and leave the
	// check below vacuous — verified by mutation (cancel without its drain
	// still passed).
	if n := len(rep.C()); n != 1 {
		t.Fatalf("queued wake-ups = %d, want 1 before the drop", n)
	}

	rep.cancel()

	// Nothing left queued: the next pass over the reattached connection must
	// not be woken by a gesture the drop already overtook.
	if n := wakeUps(rep); n != 0 {
		t.Errorf("wake-ups after cancel = %d, want 0", n)
	}
	// And the seam is re-armed rather than stuck nacking: reattach re-dialled
	// with Desired and recorded it, so a viewer that has moved AGAIN raises.
	view.setAdvertised("foot")
	*resolved = "xterm-ghostty"
	if v, _ := rep.raise(); v != raiseStarted {
		t.Errorf("verdict after cancel = %v, want raiseStarted (inFlight left set)", v)
	}
}
