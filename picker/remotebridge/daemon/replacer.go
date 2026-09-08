package daemon

import "sync/atomic"

// viewReplacer is the seam that raises a control-client replacement (#574).
//
// It is a package-level type rather than a few variables inside Run because
// the two loops that act on it — runConn's select and the attach loop — are
// closures over Run's locals, and Run has exactly one caller
// (cmd/daemon/main.go), so no Go test can drive them. The whole decision
// therefore lives on this type, constructible verbatim in a test the way
// ctl_test.go hand-builds a ctlState.

// raiseVerdict is what raise decided. The caller acts on all three.
type raiseVerdict int

const (
	// raiseNone — nothing to raise, so the gesture submits normally: the live
	// control client already advertises what the viewer carries, nobody is
	// attached to resolve from (R3), or this daemon cannot dial a second
	// connection at all.
	raiseNone raiseVerdict = iota
	// raiseStarted — this call raised the replacement.
	raiseStarted
	// raiseInFlight — an earlier call's replacement is still running, so this
	// one raises nothing. Told apart from raiseNone because the caller must
	// nack rather than submit; deliberately NOT told apart from raiseStarted
	// at the call site, which is what makes R8's "the same press-again text"
	// a property of the code rather than two strings kept in step by hand.
	raiseInFlight
)

// viewReplacer owns the resolve/compare/raise decision and the wake-up that
// carries it to the main loop.
type viewReplacer struct {
	view    *Viewing
	resolve func() (ViewIdentity, bool)
	// reconnect is Run's own flag, passed in rather than re-derived: with no
	// re-dialable transport and no recorded identity there is no second
	// connection to swap in, and the gesture runs on the client it has.
	reconnect bool
	// inFlight crosses goroutines by construction — a ctl handler sets it, the
	// main loop clears it — so it is atomic (R11).
	inFlight atomic.Bool
	ch       chan struct{}
}

// newViewReplacer builds the seam. The channel is depth 1 and created here,
// once per Run: see C.
func newViewReplacer(view *Viewing, resolve func() (ViewIdentity, bool), reconnect bool) *viewReplacer {
	return &viewReplacer{
		view:      view,
		resolve:   resolve,
		reconnect: reconnect,
		ch:        make(chan struct{}, 1),
	}
}

// C is the arm runConn selects on, so a raise wakes the loop instead of
// waiting out mainLoopTickInterval — every other ctl gesture needs no wake-up
// only because its command is sent, and this one deliberately withholds it
// (R6).
//
// Exposed as an accessor because the channel has to be created at session
// lifetime, outside runConn: a per-attach channel would leak one per
// reconnect, and the loop can only select on the handle it can see. Same
// reason loopTick is built once (daemon.go).
func (r *viewReplacer) C() <-chan struct{} { return r.ch }

// raise owns the WHOLE decision and reports what it did, plus the resolved
// termname for the caller's user-facing message (meaningless on raiseNone).
// No call site may re-implement the comparison: this is the only version a
// test drives, so a second copy is free to drift.
func (r *viewReplacer) raise() (raiseVerdict, string) {
	id, ok := r.resolve()
	if !ok {
		// R3: nobody looking is not evidence. Keep advertising whatever the
		// live client carries rather than re-dialling for an empty termname.
		return raiseNone, ""
	}
	if id.Term == r.view.Advertised() {
		// The same terminal, any number of session switches: the published
		// client already carries this termname, so a replacement could only
		// spend a dial and a repair() to change nothing (R8).
		return raiseNone, id.Term
	}
	// Recorded even when nothing is raised below: every dial's argv reads
	// Desired, so an involuntary reattach then carries the fresh termname too.
	r.view.SetDesired(id.Term)
	if !r.reconnect {
		return raiseNone, id.Term
	}
	if !r.inFlight.CompareAndSwap(false, true) {
		return raiseInFlight, id.Term
	}
	// The send always fits, and the reason is the ordering rather than the
	// depth: only runConn's receive can lead to done(), so inFlight is cleared
	// strictly after the pending wake-up was taken, and the next raise finds
	// the buffer empty. Non-blocking anyway — a ctl handler that could block
	// on main-loop progress is the deadlock R6 forbids, and a blocking send is
	// one refactor away from being exactly that.
	select {
	case r.ch <- struct{}{}:
	default:
	}
	return raiseStarted, id.Term
}

// done re-arms the seam. The attach loop calls it AFTER replaceConn returns,
// which is after that routine's Advertised write at its publish point:
// clearing it any earlier would let a press in that window read the stale
// Advertised and raise a second, redundant dial and repair().
func (r *viewReplacer) done() { r.inFlight.Store(false) }

// cancel discards a raise that a connection drop has overtaken.
//
// runConn selects over the pump and this seam's channel, so a raise landing in
// the same instant the control stream EOFs is a coin toss: when the pump wins,
// runConn returns connDrop having never read the wake-up. Both the queued
// signal and inFlight would then outlive the connection meant to serve them,
// and reattach has already dialled with Desired and recorded Advertised — so
// there is nothing left for the raise to do.
func (r *viewReplacer) cancel() {
	select {
	case <-r.ch:
	default:
	}
	r.inFlight.Store(false)
}
