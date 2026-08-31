package daemon

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// sessionIDRe matches a tmux session id. The id is interpolated into a command
// sent over the control stream, so a value that is not one is not pinned with.
var sessionIDRe = regexp.MustCompile(`^\$[0-9]+$`)

// sessionPin keeps this control client on the session it mirrors.
//
// A control-mode client receives %output only for the session it is currently
// attached to. A mirror pane's keystrokes reach the *remote* shell, where $TMUX
// is set, so `sesh connect` — or any switch-client from a bridged shell — runs
// switch-client with no -c, and tmux resolves "current client" to this daemon:
// the only client the bridged session has. Every %output for our panes stops
// from that instant, while input still lands and the remote command completes,
// so the mirror reads as frozen rather than dead (#396).
type sessionPin struct {
	// id is the mirrored session's id ($N), read from the remote rather than
	// learned from the first %session-changed — stream order is not evidence of
	// which session we are supposed to be on. Empty disables pinning.
	id string
	// handOff opens the session we were switched to as a mirror of its own, so
	// the gesture that switched us does what the user meant instead of nothing.
	// nil disables it.
	handOff func(session string)
}

func newSessionPin(cfg Config, rt roundTrip) *sessionPin {
	p := &sessionPin{handOff: cfg.HandOff}
	l, ok := one(rt, fmt.Sprintf("display-message -p -t %s -F '#{session_id}'", tmuxQuote(cfg.RemoteSession)))
	if !ok || l.Kind == controlmode.Error {
		fmt.Fprintf(os.Stderr, "daemon: cannot read session id for %s; session pinning is off\n", cfg.RemoteSession)
		return p
	}
	id := strings.TrimSpace(string(l.Data))
	if !sessionIDRe.MatchString(id) {
		fmt.Fprintf(os.Stderr, "daemon: session id for %s is not an id (%q); session pinning is off\n", cfg.RemoteSession, id)
		return p
	}
	p.id = id
	return p
}

// apply reacts to one %session-changed. Our own id is the attach-time
// notification, or our switch back landing; any other is an excursion.
func (p *sessionPin) apply(l controlmode.Line, reg *registry, router *Router, rt roundTrip) {
	if p.id == "" || len(l.Args) == 0 || l.Args[0] == p.id {
		return
	}
	away := string(l.Data)
	// No -c: a command sent over this stream resolves "current client" to this
	// control client, which is exactly the one that was switched away.
	if r, ok := one(rt, fmt.Sprintf("switch-client -t '%s'", p.id)); !ok || r.Kind == controlmode.Error {
		fmt.Fprintf(os.Stderr, "daemon: switch back to %s failed; mirror stays frozen\n", p.id)
		return
	}
	p.reseed(reg, router, rt)
	if p.handOff != nil && away != "" {
		go p.handOff(away)
	}
}

// reseed repaints every mirrored pane. Output produced while the client was on
// another session is dropped by the server, not buffered, so the switch back
// alone leaves live panes showing a stale screen — the excursion swallows the
// very command that caused it and the prompt that followed.
func (p *sessionPin) reseed(reg *registry, router *Router, rt roundTrip) {
	wins := reg.all()
	n := 0
	for _, mw := range wins {
		n += len(mw.remotePanes)
	}
	ids := make([]string, 0, n)
	sinks := make([]*outputSink, 0, n)
	for _, mw := range wins {
		for _, id := range mw.remotePanes {
			if s := router.sink(id); s != nil {
				ids = append(ids, id)
				sinks = append(sinks, s)
			}
		}
	}
	PaneSeeds(rt, ids, func(i int, seed []byte, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reseed %s after session change: %v\n", ids[i], err)
			return
		}
		sinks[i].enqueue(wire.FrameSeed, seed)
	})
}
