package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// ctlConn is everything scoped to one control connection: the transport itself
// and the pump, stream, async queue and round-tripper built over it. A new
// stream restarts its ordinals at 0 and tmux restarts the command/reply
// correspondence at 1 on a fresh attach, so these are only ever replaced as
// one unit — a half-swapped set desyncs every round-trip (#482).
type ctlConn struct {
	rwc   io.ReadWriteCloser
	pump  *ctlPump
	st    *stream
	async *asyncQueue
	rt    roundTrip
}

// newCtlConn builds a connection whose output goes nowhere. readReplyRouting
// routes %output into registered sinks as it walks past reply blocks, so a far
// end that has not yet proved it is the server whose pane ids the registry
// holds must round-trip against a Router with nothing in it: Route finds no
// sink and drops. Remote pane ids are small and sequential, so an unverified
// far end could otherwise paint into panes the user believes are their shells.
// bind opens the connection onto the real router once, after the identity read.
func newCtlConn(rwc io.ReadWriteCloser) *ctlConn {
	c := &ctlConn{
		rwc:   rwc,
		pump:  startCtlPump(controlmode.NewReader(rwc)),
		st:    newStream(rwc),
		async: &asyncQueue{},
	}
	c.rt = newRoundTrip(c.pump, NewRouter(), c.async, c.st)
	return c
}

// bind re-points this connection's round-tripper at the mirror's real router,
// over the SAME pump, stream and async queue — a second ctlConn would restart
// the stream's ordinals and tmux's command/reply correspondence mid-connection.
// Output dropped before this needs no repair of its own: every caller reseeds
// each pane straight afterwards. Notifications the verification round-trip
// queued are on this connection's own async queue, so they reach the main loop
// on the bind path and are discarded with the connection when there is none.
//
// Called on the main-loop goroutine before the connection is published, so rt —
// read by every later round-trip through connHolder — is never written while
// another goroutine can reach it.
func (c *ctlConn) bind(router *Router) {
	c.rt = newRoundTrip(c.pump, router, c.async, c.st)
}

// close ends this connection. The stream goes first, so every later send fails
// closed deterministically rather than waiting for some write to hit EPIPE and
// latch closed inside stampAll's flush; then the transport, whose Close must
// unpark a reader blocked on it — see cmd/daemon's child.Close, since closing
// only the write half leaves a silent far end holding the pump forever.
func (c *ctlConn) close() {
	c.st.close()
	c.rwc.Close()
}

// connHolder is the one indirection between the daemon's long-lived goroutines
// and the connection of the moment. Each renderer's input pump, the resize
// watcher and the ctl accept loop are started once and outlive the connection
// they were started on, so they reach it through here rather than capturing it.
//
// An empty slot is a normal state, not a bug: it is what the holder is between
// a drop and the next successful dial, and what it stays as once the retry
// budget is exhausted.
type connHolder struct {
	mu sync.Mutex
	c  *ctlConn
}

func (h *connHolder) set(c *ctlConn) {
	h.mu.Lock()
	h.c = c
	h.mu.Unlock()
}

func (h *connHolder) get() *ctlConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.c
}

// close ends the current connection and empties the slot. Idempotent, because
// both the drop path and teardown call it, and because after an exhausted retry
// budget there is nothing left in the slot to close.
func (h *connHolder) close() {
	h.mu.Lock()
	c := h.c
	h.c = nil
	h.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// send writes cmd on whichever connection is current and reports whether it was
// written. With no connection it fails closed — the same answer stampAll gives
// on a closed stream, which every caller already handles (a ctl request is
// nacked, a keystroke is dropped). It must never block: a renderer's input pump
// waiting out a reconnect would wedge the pane it serves.
func (h *connHolder) send(cmd string) bool {
	c := h.get()
	if c == nil {
		return false
	}
	return c.st.send(cmd)
}

// roundTrip is the stable roundTrip the mirror paths hold. With no connection
// it yields a drained batch, which is exactly what a caller sees when the
// stream dies mid-batch.
func (h *connHolder) roundTrip(cmds ...string) replies {
	c := h.get()
	if c == nil {
		return func() (controlmode.Line, bool) { return controlmode.Line{}, false }
	}
	return c.rt(cmds...)
}

// dialConn opens the next control connection, unverified. Dial is the
// re-dialable form and, when set, the only source of connections; Ctl is the
// single already-opened one a caller that cannot make another supplies (M1, the
// scripted Go tests), and Run consumes it exactly once.
func dialConn(cfg Config) (*ctlConn, error) {
	if cfg.Dial == nil {
		if cfg.Ctl == nil {
			return nil, fmt.Errorf("daemon: Config needs one of Ctl or Dial")
		}
		return newCtlConn(cfg.Ctl), nil
	}
	rwc, err := cfg.Dial()
	if err != nil {
		return nil, err
	}
	return newCtlConn(rwc), nil
}

// armIdentityDeadline closes c unless the returned disarm is called within d.
//
// one(rt, …) has no deadline of its own, so an endpoint that accepts the
// connection and then never answers parks the identity read forever: the retry
// budget is only consulted between attempts, so it never advances, and a detach
// never lands either. See defaultIdentityTimeout for the case ssh's own
// keepalives cannot catch. Closing the connection is the whole mechanism — the
// pump then hits EOF and readIdentity returns its existing "connection closed
// before reply" retry shape — so this adds no second reply reader, which would
// take an ordinal of its own and desync the stream.
//
// disarm reports whether THIS watchdog has closed the connection — not whether
// it is live, which nothing here tracks. It is false once the deadline has
// closed it, including when the reply landed in the same instant, so a caller
// can never publish a connection the watchdog has shut. Both sides meet under
// one mutex, so the close happens at most once and never after disarm returned
// true. A connection closed by something else in the meantime — a SIGTERM
// reaching the transport during the first identity read — still disarms true;
// the round-trip that follows then fails on its own, which is the answer the
// caller acts on either way.
func armIdentityDeadline(c *ctlConn, d time.Duration) (disarm func() (live bool)) {
	var (
		mu     sync.Mutex
		fired  bool
		beaten bool
	)
	t := time.AfterFunc(d, func() {
		mu.Lock()
		defer mu.Unlock()
		if beaten {
			return
		}
		fired = true
		c.close()
	})
	return func() bool {
		t.Stop()
		mu.Lock()
		defer mu.Unlock()
		beaten = true
		return !fired
	}
}

// reattach re-dials after a drop and returns the connection the mirror is live
// on again, bound to router and published in hold, or nil once the daemon
// should tear down. want is the identity recorded at the first attach; repair
// brings the mirror back to remote ground truth and reports whether it still
// stands.
//
// Package-level rather than a closure over Run's locals so the endings it has
// to tell apart — a drop that retries, a different server that tears down, a
// detach raised mid-dial — are reachable from a test without a live mirror.
func reattach(cfg Config, router *Router, hold *connHolder, want remoteIdentity, repair func() bool) *ctlConn {
	// Every send fails closed from this instant, rather than from whenever a
	// write happens to hit EPIPE.
	hold.close()
	// Stamped before the first dial, so the badge appears within one status
	// tick of the drop rather than after the backoff.
	setBridgeState(cfg, bridgeStateDisconnected)
	bo := cfg.retrySchedule()
	start := bo.Now()
	for attempt := 1; ; attempt++ {
		// SIGTERM works by dropping the transport, so only the stop signal
		// tells a detach from a link failure (see Config.Shutdown). Consulted
		// before any retry is scheduled.
		if stopped(cfg.Shutdown) {
			return nil
		}
		d, ok := bo.Next(attempt, start)
		if !ok {
			fmt.Fprintf(os.Stderr, "daemon: giving up on %s after %d reconnect attempt(s)\n", cfg.RemoteHost, attempt-1)
			return nil
		}
		if Wait(d, cfg.Shutdown) {
			return nil
		}
		next, err := dialConn(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: re-dial %s: %v\n", cfg.RemoteHost, err)
			continue
		}
		// A detach raised while the dial was in flight must not be overtaken by
		// it: the check at the top of the iteration predates the dial, and
		// without this one Run re-enters runConn on a connection the user has
		// already asked to go away. lztmux-remote-detach then waits out its 2s,
		// kills the mirror session itself, and leaves this daemon holding a live
		// transport child.
		if stopped(cfg.Shutdown) {
			next.close()
			return nil
		}
		// The identity read comes before anything touches the mirror: this
		// connection is still unbound (see newCtlConn), so a far end that is not
		// the server whose pane ids the registry holds cannot paint into panes
		// the user believes are their shells, and an unpublished connection
		// cannot carry a renderer's keystroke there either.
		disarm := armIdentityDeadline(next, cfg.identityTimeout())
		id, err := readIdentity(next.rt, cfg.RemoteSession)
		live := disarm()
		if err != nil {
			if live {
				next.close()
			}
			var ire *identityReadErr
			if errors.As(err, &ire) && ire.Retry() {
				// The new connection died before answering — another drop,
				// not a verdict on the remote. A deadline that expired lands
				// here too, by closing the connection out from under the read.
				fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "daemon: %v; tearing the mirror down\n", err)
			return nil
		}
		if !live {
			// The reply parsed, but only after the deadline had already closed
			// the connection under it. Same shape as any other drop.
			fmt.Fprintf(os.Stderr, "daemon: identity read for %s answered after its deadline\n", cfg.RemoteSession)
			continue
		}
		if !want.matches(id) {
			fmt.Fprintf(os.Stderr, "daemon: %s now hosts %s on a different tmux server (was pid %d %s, now pid %d %s); tearing the mirror down\n",
				cfg.RemoteHost, cfg.RemoteSession, want.pid, want.sessionID, id.pid, id.sessionID)
			next.close()
			return nil
		}
		next.bind(router)
		hold.set(next)
		if !repair() {
			return nil
		}
		// Cleared once the panes show live content again, not on the bare
		// re-attach: a stale screen the user knows is stale is a paused
		// mirror, one they don't is a lie.
		clearBridgeState(cfg)
		return next
	}
}

// connVerdict is how one connection's main loop ended. Only connDrop is a
// transport failure worth another dial; connEnd covers the remote deliberately
// ending this control client (%exit) and a mirror left with no windows, either
// of which would reconnect into a session there is nothing left to mirror in.
type connVerdict int

const (
	connEnd connVerdict = iota
	connDrop
)

// bridgeStateDisconnected is the @bridge_state value picker/statusline renders
// a red marker for on a mirror window; absent means connected. It names the
// state of the user's mirror, not the daemon's retry activity (#482).
const bridgeStateDisconnected = "disconnected"

func setBridgeState(cfg Config, v string) {
	if cfg.LocalSess == "" {
		return
	}
	cfg.LocalTmux("set-option", "-t", cfg.LocalSess, "@bridge_state", v)
}

func clearBridgeState(cfg Config) {
	if cfg.LocalSess == "" {
		return
	}
	cfg.LocalTmux("set-option", "-u", "-t", cfg.LocalSess, "@bridge_state")
}

// stopped reports whether the user has asked the daemon to shut down. A nil
// channel is never ready, so a Config without one never reads as stopped.
func stopped(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
