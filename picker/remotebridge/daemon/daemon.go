// Package daemon owns the M2.2 mirror: one control-mode connection to a
// remote tmux, converged to a local window's size, with one native local
// window per remote window (one local pane per remote pane) and one renderer
// process per pane feeding/draining a unix socket. Run wires all of it
// together; see the orchestration sequence in
// docs/superpowers/plans/2026-07-20-remote-bridge-m2.2.md (Task 3).
package daemon

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/graphics"
	"github.com/noamsto/lazytmux/picker/remotebridge/keyneg"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// Config is the injectable seam for Run: everything that talks to a real
// ssh/tmux/socket in production is a field here, so the bats test can point it
// at a second local tmux instead.
type Config struct {
	Ctl            io.ReadWriteCloser         // tmux -C control-mode stream over plain ssh (stdin+stdout duplex)
	SockPath       string                     // unix socket renderers dial
	LocalSess      string                     // "<host>-<sess>"
	RemoteHost     string                     // ssh host being mirrored (picker's Host column)
	RemoteSession  string                     // remote session name (may contain spaces)
	RemoteWindow   string                     // initially-selected remote window INDEX (not a mirror filter)
	PauseAfterSecs int                        // refresh-client -f pause-after=N (0 disables); backpressure insurance answered by a %continue re-seed
	RendererBin    string                     // absolute store path to cmd/renderer
	LocalTmux      func(args ...string) error // runs local tmux (injected; prod = exec)
	// LocalTmuxOut runs local tmux and captures stdout (injected; prod = exec).
	LocalTmuxOut func(args ...string) (string, error)
	LocalArea    func() (int, int)        // content area the local mirror session's clients can show (injected)
	Reflow       func()                   // forces a status-bar reflow of the mirror session (injected; nil = off)
	LocalPanes   func() map[string]string // remote pane id -> local pane id, read back from @bridge_pane (injected)
	// NewGraphics builds the per-pane kitty-graphics proxy that localises image
	// payloads crossing the bridge. nil disables proxying entirely (tests, and
	// any transport where there is no remote filesystem to fetch from).
	NewGraphics func(paneID string) *graphics.Proxy
	// HandOff opens a remote session this bridge was switched to as a mirror of
	// its own (injected; prod = lztmux-remote-open, nil = off). See sessionPin.
	HandOff func(remoteSession string)
}

func (c Config) graphicsFor(paneID string) *graphics.Proxy {
	if c.NewGraphics == nil {
		return nil
	}
	return c.NewGraphics(paneID)
}

// reflow re-derives the mirror session's window labels. The after-new-window
// hook's own reflow races the @bridge_win / @window_bridge_name stamps that
// follow the create, so it can label a mirror window from the launcher's cwd
// (#196); and a later rename changes no window count, so reflow's
// count:width cache would skip it. Every path that stamps a name ends here.
func (c Config) reflow() {
	if c.Reflow != nil {
		c.Reflow()
	}
}

// createMirrorWindow appends a window to the mirror session and returns its
// tmux window ID.
//
// The ID, not the index, is what every later command targets. renumber-windows
// is on, so closing one mirror window renumbers the rest — an index captured at
// creation would silently start addressing its neighbour (#411). Appending at
// {end} rather than at an index of our own choosing is what leaves the
// re-indexing to tmux, so a mirror never grows the gaps a local session can't.
func createMirrorWindow(cfg Config) (string, error) {
	out, err := cfg.LocalTmuxOut("new-window", "-d", "-P", "-F", "#{window_id}",
		"-a", "-t", cfg.LocalSess+":{end}")
	if err != nil {
		return "", fmt.Errorf("daemon: new-window in %s: %w", cfg.LocalSess, err)
	}
	return parseWindowID(out)
}

// firstMirrorWindow is the ID of the window the launcher created the mirror
// session with, which the first remote window reuses rather than adding a
// second one beside it.
func firstMirrorWindow(cfg Config) (string, error) {
	out, err := cfg.LocalTmuxOut("list-windows", "-t", cfg.LocalSess, "-F", "#{window_id}")
	if err != nil {
		return "", fmt.Errorf("daemon: list-windows %s: %w", cfg.LocalSess, err)
	}
	first, _, _ := strings.Cut(out, "\n")
	return parseWindowID(first)
}

// parseWindowID validates a window id read back from tmux. A reply that isn't
// one is an error rather than something to interpolate into a target: tmux
// would read a bare "7" as window INDEX 7, which is exactly the addressing this
// change exists to remove.
func parseWindowID(s string) (string, error) {
	id := strings.TrimSpace(s)
	if !strings.HasPrefix(id, "@") || len(id) == 1 {
		return "", fmt.Errorf("daemon: %q is not a tmux window id", id)
	}
	return id, nil
}

// stampMirrorWindow marks localWin as this daemon's mirror of a remote window
// and gives it that window's name.
//
// automatic-rename goes off because the daemon owns the name: tmux only
// re-derives one when the active pane produces output, and an idle renderer
// never does — so a name derived once, during setup, would freeze on the
// launcher's cwd for the life of the window.
//
// Panes are addressed 0-based (spawnRenderer/reconcileLayout use index starting
// at 0); the pane-base-index override keeps that true regardless of the host's
// global (real hosts set 1).
func stampMirrorWindow(cfg Config, localWin, remoteName string) {
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "@bridge_win", "1")
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "pane-base-index", "0")
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "automatic-rename", "off")
	applyMirrorName(cfg, localWin, remoteName)
}

// applyMirrorName writes the remote window's name to both places a mirror
// window carries it: @window_bridge_name (what reflow labels from) and the
// window name itself. Both, every time — with automatic-rename off nothing else
// re-derives the name, so a path that wrote only the option would leave the
// window name frozen at whatever the previous write left behind.
func applyMirrorName(cfg Config, localWin, remoteName string) {
	name := sanitizeWindowName(remoteName)
	if name == "" {
		return
	}
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "@window_bridge_name", name)
	cfg.LocalTmux("rename-window", "-t", localWin, name)
}

// outputSinkBuf is the per-renderer output buffer depth. Overflow drops the
// frame rather than blocking the control-stream loop; the pane self-heals on
// its next %output, or on the fresh FrameSeed any %continue sends.
const outputSinkBuf = 4096

// helloTimeout bounds how long waitHellos blocks for renderers to dial
// back. A spawned renderer that never connects (bad RendererBin, exec
// failure, crash before it dials) doesn't surface as a LocalTmux error —
// respawn-pane itself succeeds — so without a deadline the wait blocks its
// caller forever: startup never proceeds, and reconcile never returns the main
// loop to dispatching.
const helloTimeout = 10 * time.Second

// helloConn pairs an accepted renderer connection with the remote pane id it
// announced via FrameHello.
type helloConn struct {
	paneID string
	conn   net.Conn
}

// helloWaiter collects the n renderer connections a caller just spawned. Every
// mirror path takes one of these rather than the connection channel itself: the
// wait has to keep the control stream moving (see waitHellos), and the pump it
// drains to do that is Run's alone.
type helloWaiter func(n int) (map[string]net.Conn, error)

// resizePollInterval is how often the resize watcher re-checks the nudge
// file's mtime (an os.Stat, not a fork). It only forks LocalArea's
// display-message/list-clients calls when that mtime has advanced (#433).
const resizePollInterval = time.Second

// resizeNudgeSuffix names the per-bridge file a session-scoped client-resized
// hook touches (see registerResizeHook). Its mtime is the event watchResize
// polls for instead of forking a query every tick.
const resizeNudgeSuffix = ".resize"

// resizeFallbackInterval bounds staleness on top of the mtime nudge: a
// filesystem with coarse mtime resolution can make a real touch
// indistinguishable from one already observed (two resizes landing in the
// same rounded second read back as the same mtime), which would otherwise
// leave the mirror capped at a stale size forever if no further resize ever
// lands in a distinguishable bucket. Every tick where the interval has
// elapsed since the last check forces one regardless of the nudge, same as
// the unconditional poll this replaces — just far less often.
const resizeFallbackInterval = 30 * time.Second

// watchResize re-asserts every mirrored window's cap whenever the local client
// area changes. A local terminal/client resize emits no control-stream event,
// so the daemon polls — but cheaply: nudged reports the resize-hook file's
// mtime via a plain os.Stat, and LocalArea's fork-per-call query only runs
// once that mtime has advanced past the last one observed, collapsing the
// steady-state cost from one fork/sec to one stat/sec (plus one fork every
// resizeFallbackInterval as a safety net — see its doc). A tick whose stat
// misses a touch is not lost: mtime persists on disk, so the next tick's stat
// still sees it and converges — one poll cycle later than the hook itself.
//
// On a change it re-pushes ConvergeCmd per mirrored window, which resizes the
// remote and makes it emit %layout-change per window, driving the existing
// reconcile + re-seed (and the re-fit of the local window to the remote's new
// size). send is the same mutex-guarded, no-op-when-closed sender the main
// loop uses; this only injects fire-and-forget commands (their %begin/%end
// acks are consumed harmlessly by the main loop's own nextLine read).
func watchResize(area func() (int, int), nudged func() (time.Time, bool), reg *registry, cv *converger, send func(string), stop <-chan struct{}, tick <-chan time.Time) {
	var lastNudge time.Time
	lastCheck := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-tick:
			due := false
			if mtime, ok := nudged(); ok && mtime.After(lastNudge) {
				lastNudge = mtime
				due = true
			}
			if !due && now.Sub(lastCheck) >= resizeFallbackInterval {
				due = true
			}
			if !due {
				continue
			}
			lastCheck = now
			w, h := area()
			for _, remoteID := range reg.remoteIDs() {
				if cv.need(remoteID, w, h) {
					send(ConvergeCmd(remoteID, w, h))
				}
			}
		}
	}
}

// resizeHookEvents are the two events that can grow the mirror session's
// window: client-resized fires for an attached client's terminal resize,
// window-resized for any window resize including a programmatic one against a
// detached session (window-size is "latest", so the mirror stays detached
// between launcher switches — #433's own reproduction resizes it that way).
var resizeHookEvents = [...]string{"client-resized", "window-resized"}

// registerResizeHook wires session-scoped hooks that touch nudgePath — no
// fork on the daemon's side, just a stat once a tick sees the touch.
// Session-scoped (not the global config's hooks) so the lifecycle stays owned
// by the bridge: registered here, removed in unregisterResizeHook.
func registerResizeHook(cfg Config, nudgePath string) {
	if cfg.LocalSess == "" {
		return
	}
	touch := "touch -- " + tmuxQuote(nudgePath)
	hook := fmt.Sprintf("run-shell -b %s", tmuxQuote(touch))
	for _, event := range resizeHookEvents {
		cfg.LocalTmux("set-hook", "-t", cfg.LocalSess, event, hook)
	}
}

// unregisterResizeHook removes the hooks registerResizeHook set, so a dead
// bridge's session (or one reused for a later daemon) carries none of its
// hooks forward.
func unregisterResizeHook(cfg Config) {
	if cfg.LocalSess == "" {
		return
	}
	for _, event := range resizeHookEvents {
		cfg.LocalTmux("set-hook", "-u", "-t", cfg.LocalSess, event)
	}
}

// stream owns the command side of the control connection. It serializes writes
// — the setup path, every renderer's input pump and every ctl connection share
// one wire — and numbers the commands, which is what lets a round-trip find its
// own reply.
//
// tmux guards each command a control client sends with a %begin..%end carrying
// ClientCommandFlag, in the order the commands were run, so the Nth such block
// answers the Nth command. Counting is the only way to line them up: most
// commands here are fire-and-forget (a keystroke's send-keys, a ctl gesture, a
// converge) and leave a reply block behind that nobody waits for, and a hook on
// the remote adds blocks flagged 0 on top of those (#276).
type stream struct {
	mu     sync.Mutex
	w      *bufio.Writer
	closed bool
	sent   uint64 // commands written
	seen   uint64 // client-flagged reply blocks consumed
}

func newStream(w io.Writer) *stream { return &stream{w: bufio.NewWriter(w)} }

// stamp writes cmd and returns its ordinal. ok is false once the daemon is
// tearing down: a ctl request that loses that race must not be acked as
// accepted, or the keybind reports success for a gesture that never happened.
func (s *stream) stamp(cmd string) (seq uint64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, false
	}
	fmt.Fprintf(s.w, "%s\n", cmd)
	s.w.Flush()
	s.sent++
	return s.sent, true
}

// send is stamp for callers that don't read the reply.
func (s *stream) send(cmd string) bool {
	_, ok := s.stamp(cmd)
	return ok
}

// claim consumes one client-flagged reply block and returns the ordinal of the
// command it answers.
func (s *stream) claim() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen++
	return s.seen
}

func (s *stream) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Run mirrors every window of the bridged remote session, each into its own
// local window, over the single -CC connection, until %exit or the control
// connection drops.
func Run(cfg Config) error {
	pump := startCtlPump(controlmode.NewReader(cfg.Ctl))
	st := newStream(cfg.Ctl)
	// send is the fire-and-forget form the mirror paths use; the ctl path takes
	// st.send directly, since it has to report whether the line was written.
	send := func(s string) { st.send(s) }

	router := NewRouter()
	async := &asyncQueue{}
	// rt is the only way a mirror path asks the remote a question: it sends the
	// command and reads that command's own reply block.
	rt := func(cmd string) (controlmode.Line, bool) {
		seq, ok := st.stamp(cmd)
		if !ok {
			return controlmode.Line{}, false
		}
		return readReplyRouting(pump, router, async, st, seq)
	}

	// The implicit attach reply needs no draining: it is flagged 0, so the reply
	// reader skips it like any other block we did not ask for.
	//
	// Enumerate every window of the bridged remote session. Read BOTH index
	// and id: --window is an *index*, the registry is keyed by *id* (@N).
	lw, ok := rt(fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowListFormat))
	if !ok || lw.Kind == controlmode.Error {
		return fmt.Errorf("daemon: list-windows for %s failed", cfg.RemoteSession)
	}
	remoteWins := parseWindowList(string(lw.Data))
	if len(remoteWins) == 0 {
		return fmt.Errorf("daemon: remote session %s has no windows", cfg.RemoteSession)
	}
	pin := newSessionPin(cfg, rt)

	os.Remove(cfg.SockPath)
	listener, err := net.Listen("unix", cfg.SockPath)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", cfg.SockPath, err)
	}
	// The socket forwards keystrokes to the remote pane and streams its output,
	// so restrict it to the owning user.
	if err := os.Chmod(cfg.SockPath, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: chmod %s: %v\n", cfg.SockPath, err)
	}
	// Pidfile beside the socket: the launcher reads it to detect an already-live
	// bridge for this host:session (reuse instead of stacking a rival daemon)
	// and to tell a stale socket from one a running daemon still owns. Removed in
	// teardown so a clean exit leaves neither file behind.
	pidFile := cfg.SockPath + ".pid"
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: write pidfile %s: %v\n", pidFile, err)
	}
	connCh := make(chan helloConn, 64)
	// The one waiter every mirror path gets. connCh goes no further than this
	// closure: draining the stream while waiting needs the pump, and only Run
	// has it.
	waitHellosFn := func(n int) (map[string]net.Conn, error) {
		return waitHellos(pump.lines, router, async, st, connCh, n, helloTimeout)
	}
	cst := newCtlState()
	go acceptConns(listener, connCh, func(argv []string) error {
		req, err := cst.parseCtl(argv, cfg.RemoteSession)
		if err != nil {
			return err
		}
		if !cst.submit(req, st.send) {
			return fmt.Errorf("bridge is shutting down")
		}
		return nil
	})

	// @bridge_sock is the carrier a keybind reads to reach this daemon. Stamped
	// on the session before any mirror window exists, so no gate can fire against
	// a bridge window whose session has no socket yet — and stamped by the daemon
	// rather than the launcher so the offline --test-local harness gets it too.
	if cfg.LocalSess != "" {
		cfg.LocalTmux("set-option", "-t", cfg.LocalSess, "@bridge_sock", cfg.SockPath)
		// @bridge_host is what the session picker's Host column reads; the local
		// session name can't be split back into host+session (either may hold a "-").
		cfg.LocalTmux("set-option", "-t", cfg.LocalSess, "@bridge_host", cfg.RemoteHost)
	}

	reg := newRegistry()
	cv := newConverger()
	// nudgePath is the file registerResizeHook's client-resized hook touches;
	// removed here so a stale touch from a prior daemon on this same socket
	// path can't be mistaken for a resize before the hook ever fires again.
	nudgePath := cfg.SockPath + resizeNudgeSuffix
	os.Remove(nudgePath)
	// stopWatch stops the resize watcher (started just before the main loop).
	// Declared here so teardown can close it; teardown runs exactly once per
	// Run return path, so a plain close is safe.
	stopWatch := make(chan struct{})
	// Assigned once the mirror is up; teardown must drop the status files it
	// wrote, so it is declared ahead of the closure that captures it.
	var agents *agentShipper
	teardown := func() {
		close(stopWatch)
		unregisterResizeHook(cfg)
		os.Remove(nudgePath)
		if agents != nil {
			agents.clear()
		}
		listener.Close()
		os.Remove(cfg.SockPath)
		os.Remove(pidFile)
		for _, mw := range reg.all() {
			// Unregister closes each pane's output sink, stopping its pump
			// goroutine (mirrors closeWindow); then drop the renderer conns.
			for _, id := range mw.remotePanes {
				router.Unregister(id)
			}
			for _, c := range mw.conns {
				c.Close()
			}
		}
		st.close()
		cfg.Ctl.Close()
		if cfg.LocalSess != "" {
			cfg.LocalTmux("kill-session", "-t", cfg.LocalSess)
		}
	}

	// Mirror each remote window into its own local window. The first reuses the
	// launcher's initial window; the rest are appended.
	for i, rw := range remoteWins {
		var (
			localWin string
			err      error
		)
		if i == 0 {
			localWin, err = firstMirrorWindow(cfg)
		} else {
			localWin, err = createMirrorWindow(cfg)
		}
		if err != nil {
			teardown()
			return err
		}
		stampMirrorWindow(cfg, localWin, rw.name)
		mw := reg.add(rw.id, localWin)
		if err := setupWindow(cfg, send, router, waitHellosFn, cst, mw, cv, rt); err != nil {
			teardown()
			return err
		}
	}

	// Select the initially-requested window. RemoteWindow is a window INDEX
	// (not an id), so resolve index -> id -> local window via the enumerated
	// list; never treat it as id "@<idx>".
	if initWin, ok := localWinForRemoteIndex(remoteWins, reg, cfg.RemoteWindow); ok {
		cfg.LocalTmux("select-window", "-t", initWin)
	}

	// Re-read the remote once setup is done. Names were captured by the single
	// enumeration above, but setup spans a spawn/hello/seed round-trip per
	// window and reads its replies with the plain skip reader — so every
	// %window-renamed the remote emits in that interval is discarded (B3). A
	// remote whose windows rename as their shells settle (or, on a real
	// lazytmux host, on every automatic-rename tick) would otherwise keep the
	// name it happened to have at attach for the life of the mirror. Reconcile
	// re-asserts each name from ground truth, and ends in a reflow.
	reconcileWindows(cfg, send, router, waitHellosFn, cst, reg, cv, rt)
	if reg.empty() {
		teardown()
		return nil
	}

	// Re-converge the remote whenever the local client resizes. A local resize
	// emits no control-stream event, so poll (cheaply — see watchResize);
	// teardown closes stopWatch and removes the hook this registers. A resize
	// landing between the last per-window setup convergence above and this
	// registration is caught by resizeFallbackInterval rather than lost.
	registerResizeHook(cfg, nudgePath)
	nudged := func() (time.Time, bool) {
		fi, err := os.Stat(nudgePath)
		if err != nil {
			return time.Time{}, false
		}
		return fi.ModTime(), true
	}
	ticker := time.NewTicker(resizePollInterval)
	go func() {
		defer ticker.Stop()
		watchResize(cfg.LocalArea, nudged, reg, cv, send, stopWatch, ticker.C)
	}()

	// Ship the remote's agent state into the local claude-status tree.
	agents = newAgentShipper(cfg.LocalSess, remoteClockSkew(rt))

	// dispatch handles one notification, whether it came straight off the stream
	// or a reply reader queued it while awaiting a reply. It reports whether the
	// bridge is finished. Only the main loop calls it: several branches run their
	// own round-trips, which must not nest inside another.
	dispatch := func(l controlmode.Line) (done bool) {
		switch l.Kind {
		case controlmode.Output:
			router.Route(l.Pane, l.Data)
		case controlmode.LayoutChange:
			if len(l.Args) > 0 {
				if mw, ok := reg.byRemoteID(l.Args[0]); ok {
					reconcileLayout(cfg, mw, send, router, waitHellosFn, cst, rt)
				}
			}
		case controlmode.WindowRenamed:
			if len(l.Args) > 0 {
				if mw, ok := reg.byRemoteID(l.Args[0]); ok {
					applyMirrorName(cfg, mw.localWin, string(l.Data))
					cfg.reflow()
				}
			}
		case controlmode.SessionChanged:
			pin.apply(l, reg, router, rt)
		case controlmode.SessionWindowChanged:
			if argv, ok := translateWindowNotification(l, reg); ok {
				cfg.LocalTmux(argv...)
			}
		case controlmode.WindowAdd:
			if len(l.Args) > 0 {
				addWindow(cfg, send, router, waitHellosFn, cst, reg, cv, rt, l.Args[0])
			}
		case controlmode.WindowClose:
			if len(l.Args) > 0 {
				closeWindow(cfg, router, cst, reg, cv, l.Args[0])
				return reg.empty()
			}
		case controlmode.WindowPaneChanged:
			// The remote's active pane moved. The echo guards decide whether this
			// is our own select-pane coming back or a genuine external change that
			// local focus must follow (focus.go).
			if len(l.Args) > 1 {
				if mw, ok := reg.byRemoteID(l.Args[0]); ok {
					if pane, follow := cst.applyRemoteFocus(mw.remoteID, l.Args[1]); follow {
						focusLocalPane(cfg, cst, mw, mw.remotePanes, pane)
					}
				}
			}
		case controlmode.Pause:
			if len(l.Args) > 0 {
				handlePause(router, send, l.Args[0])
			}
		case controlmode.Continue:
			if len(l.Args) > 0 {
				handleContinue(router, rt, l.Args[0])
			}
		case controlmode.Exit:
			return true
		}
		return false
	}

	// settle runs the queued notifications and the reconcile intents a ctl
	// request registered, until neither has anything left: each dispatch and each
	// reconcile does round-trips of its own, which can queue more of both.
	settle := func() (done bool) {
		for {
			queued := async.take()
			wantWindows, layouts := cst.takeIntents()
			if len(queued) == 0 && !wantWindows && len(layouts) == 0 {
				return false
			}
			for _, q := range coalesceLayoutChanges(queued) {
				if dispatch(q) {
					return true
				}
			}
			if wantWindows {
				reconcileWindows(cfg, send, router, waitHellosFn, cst, reg, cv, rt)
				if reg.empty() {
					return true
				}
			}
			for _, remoteID := range layouts {
				// A layout intent for a window reconcileWindows just closed has
				// nothing to reconcile.
				if mw, ok := reg.byRemoteID(remoteID); ok {
					reconcileLayout(cfg, mw, send, router, waitHellosFn, cst, rt)
				}
			}
		}
	}

	// Main loop.
	pauseAfterSet := false
	for {
		// Settling before the blocking read is what makes a ctl gesture land
		// without a timer: nextLine wakes on any line, and by the time it
		// returns the intent is already registered, so the next pass through here
		// drains it. It also picks up whatever window setup queued.
		if settle() {
			break
		}
		// Same wake-up: an agent that changes state redraws its pane first, so
		// the output that ended a turn has already brought us here.
		agents.poll(cfg, rt)
		reseedDropped(router, rt)
		// Enable pause-after only now that every window is set up. Setup does
		// drain the stream (its round-trips route, and so does the hello wait),
		// but only dispatch runs handlePause — so a %pause arriving mid-setup is
		// merely queued, and its pane would sit paused with no %continue re-seed
		// until setup finished (a deadlock offline bats can't catch).
		if !pauseAfterSet {
			pauseAfterSet = true
			if cfg.PauseAfterSecs > 0 {
				send(fmt.Sprintf("refresh-client -f pause-after=%d", cfg.PauseAfterSecs))
			}
		}
		l, _, ok := nextLine(pump, st)
		if !ok {
			break // control-stream EOF
		}
		if dispatch(l) {
			break
		}
	}
	teardown()
	return nil
}

// setupWindow runs the per-window plan/spawn/hello/seed pipeline for mw: it
// reads the remote window's layout, shapes mw.localWin to match, spawns one
// renderer per pane, waits for their Hellos, then seeds each and wires it into
// the router. It records the remote pane ids and their conns on mw.
//
// For a 1-pane remote window this is exactly M1's behavior — no split, one
// renderer, matching dims — since PlanWindow emits zero splits for a 1-pane
// layout.
func setupWindow(cfg Config, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, mw *mirrorWindow, cv *converger, rt roundTrip) error {
	// Cap the remote window at what the local clients can show before reading
	// its layout, so the layout that gets mirrored is the converged one.
	if w, h := cfg.LocalArea(); cv.need(mw.remoteID, w, h) {
		rt(ConvergeCmd(mw.remoteID, w, h))
	}

	L, _, _, err := readLayout(rt, remoteWinTarget(cfg, mw.remoteID))
	if err != nil {
		return err
	}

	// Apply the mirror shape to the local window.
	for _, c := range PlanWindow(mw.localWin, L) {
		if err := cfg.LocalTmux(c...); err != nil {
			return fmt.Errorf("daemon: apply mirror for %s: %w", mw.remoteID, err)
		}
	}

	mw.remotePanes = RemotePaneOrder(L)
	mw.layout = L.Raw // PlanWindow's last command is this select-layout
	cst.setWindowPanes(mw.remoteID, mw.remotePanes)

	// PlanWindow's splits create panes in RemotePaneOrder position (see
	// mirror.go), so the tiled list lines up index-for-index with remotePanes.
	if err := refreshLocalPanes(cfg, mw); err != nil {
		return fmt.Errorf("daemon: mirror panes for %s: %w", mw.remoteID, err)
	}
	if len(mw.localPanes) != len(mw.remotePanes) {
		return fmt.Errorf("daemon: mirror for %s: %d local panes for %d remote",
			mw.remoteID, len(mw.localPanes), len(mw.remotePanes))
	}
	for i, remotePane := range mw.remotePanes {
		if err := spawnRenderer(cfg, mw.localPanes[i], remotePane); err != nil {
			return fmt.Errorf("daemon: spawn renderer for %s: %w", remotePane, err)
		}
	}

	// Collect exactly len(remotePanes) Hellos (any order) before seeding —
	// seeding is sequential over the single control stream, so all renderers
	// must be connected (and hence writable) first.
	byRemote, err := waitHellos(len(mw.remotePanes))
	if err != nil {
		return err
	}
	for id, c := range byRemote {
		mw.conns[id] = c
	}

	// Seed each pane and wire it into the router. seedRenderer registers the
	// sink first, then enqueues the seed (FIFO keeps it ahead of any routed
	// output), then starts the input pump.
	for i, remotePane := range mw.remotePanes {
		conn := mw.conns[remotePane]
		if conn == nil {
			continue // didn't connect; the hello wait reported the shortfall
		}
		if !seedRenderer(rt, router, conn, remotePane, L.Panes[i], cfg.graphicsFor(remotePane)) {
			if len(mw.remotePanes) == 1 {
				return fmt.Errorf("daemon: seed failed for sole pane %s", remotePane)
			}
			delete(mw.conns, remotePane)
			continue
		}
		go pumpInput(conn, remotePane, send)
	}
	return nil
}

// addWindow B2-confirms a %window-add notification with a list-windows re-read.
// If remoteID is now in the bridged session and not already mirrored, it runs
// the same plan/spawn/hello/seed pipeline as startup. Otherwise (the window
// belongs elsewhere, or a duplicate notification for an already-registered
// window) it's a no-op.
func addWindow(cfg Config, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, reg *registry, cv *converger, rt roundTrip, remoteID string) {
	if _, already := reg.byRemoteID(remoteID); already {
		return
	}

	lw, ok := rt(fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowListFormat))
	if !ok || lw.Kind == controlmode.Error {
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: list-windows failed\n", remoteID)
		return
	}
	inSession := false
	var addedName string
	for _, rw := range parseWindowList(string(lw.Data)) {
		if rw.id == remoteID {
			inSession = true
			addedName = rw.name
			break
		}
	}
	if !inSession {
		return
	}

	localWin, err := createMirrorWindow(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: %v\n", remoteID, err)
		return
	}
	stampMirrorWindow(cfg, localWin, addedName)
	mw := reg.add(remoteID, localWin)
	if err := setupWindow(cfg, send, router, waitHellos, cst, mw, cv, rt); err != nil {
		// Drop the half-created entry + local window so the already-registered
		// guard doesn't block a later %window-add retry for this id.
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: %v\n", remoteID, err)
		reg.remove(remoteID)
		cv.forget(remoteID)
		cfg.LocalTmux("kill-window", "-t", localWin)
		return
	}
	cfg.reflow()
}

// closeWindow tears down remoteID's local mirror: unregisters (and thereby
// closes) each pane's output sink, closes each renderer conn, and kills the
// local window. A notification for a window outside the registry is a no-op
// (B2) — kill-window must never run against a window this daemon doesn't own.
func closeWindow(cfg Config, router *Router, cst *ctlState, reg *registry, cv *converger, remoteID string) {
	mw, ok := reg.remove(remoteID)
	if !ok {
		return
	}
	cv.forget(remoteID)
	cst.forgetWindow(remoteID)
	for _, id := range mw.remotePanes {
		router.Unregister(id)
	}
	for _, c := range mw.conns {
		c.Close()
	}
	cfg.LocalTmux("kill-window", "-t", mw.localWin)
}

// asyncQueue holds the notifications a reply reader met while awaiting a reply.
// Only the main loop dispatches them: %window-add and friends run their own
// round-trips, which must not execute reentrantly from inside one. It needs no
// mutex — the main loop's goroutine is the only one that touches it, directly or
// through the reply readers it calls.
type asyncQueue struct{ lines []controlmode.Line }

func (q *asyncQueue) push(l controlmode.Line) { q.lines = append(q.lines, l) }

func (q *asyncQueue) take() []controlmode.Line {
	lines := q.lines
	q.lines = nil
	return lines
}

// coalesceLayoutChanges collapses a burst of %layout-change notifications for
// the same window into just the last one. reconcileLayout always re-reads the
// remote's current layout fresh, so only the last notification for a given
// window in an already-buffered batch can still matter — a resize drag can
// otherwise queue many of these while a single reconcileLayout call's own
// round-trips are in flight, and dispatching each one individually would pay
// for N-1 redundant readLayout round-trips before the dedup in reconcileLayout
// even gets a chance to discard them. Every other notification kind, and the
// relative order of what survives, is left untouched.
func coalesceLayoutChanges(lines []controlmode.Line) []controlmode.Line {
	last := map[string]int{}
	for i, l := range lines {
		if l.Kind == controlmode.LayoutChange && len(l.Args) > 0 {
			last[l.Args[0]] = i
		}
	}
	out := make([]controlmode.Line, 0, len(lines))
	for i, l := range lines {
		if l.Kind == controlmode.LayoutChange && len(l.Args) > 0 && last[l.Args[0]] != i {
			continue
		}
		out = append(out, l)
	}
	return out
}

// lineReader is the control stream as its consumers see it: one blocking read
// that ends with the stream. Both *controlmode.Reader and *ctlPump satisfy it.
type lineReader interface {
	Next() (controlmode.Line, bool)
}

// ctlPumpBuf is the depth of the pump's line channel. It is slack for every
// stretch where the consuming goroutine is busy rather than reading — LocalTmux
// execs, window shaping, per-pane seeding, the hello wait — which is what keeps
// the remote's output moving out of the socket and so keeps a pane below tmux's
// pause-after age. Deliberately looser than the one-line-at-a-time backpressure
// a synchronous reader gave: the slack IS the fix. Once it is full the pump
// blocks on the send and the remote feels the stall as it always did.
//
// The bound is a line count, so what it costs is 256 × one control-mode line —
// an %output chunk, or a reply block's joined body. Both are small in practice;
// the scanner's 4MB ceiling sizes a pathological reply body, not a routine one.
const ctlPumpBuf = 256

// ctlPump is the daemon's one caller of controlmode.Reader.Next(), for the life
// of the process. Reading on a goroutine of its own is what lets a consumer
// select over the stream alongside other events (see waitHellos) — Next()
// blocks, so a select cannot include it directly.
//
// The goroutine ends when the stream does, and otherwise dies with the process:
// teardown's cfg.Ctl.Close() closes only the ssh stdin, so a pump parked on a
// send to a channel nobody is draining is not woken by it. Harmless — Run
// returning means the process is on its way out.
type ctlPump struct {
	lines chan controlmode.Line
}

func startCtlPump(rd *controlmode.Reader) *ctlPump {
	p := &ctlPump{lines: make(chan controlmode.Line, ctlPumpBuf)}
	go func() {
		defer close(p.lines)
		for {
			l, ok := rd.Next()
			if !ok {
				return
			}
			p.lines <- l
		}
	}()
	return p
}

func (p *ctlPump) Next() (controlmode.Line, bool) {
	l, ok := <-p.lines
	return l, ok
}

// claimSeq gives a reply block the ordinal of the command it answers, and 0 to
// everything else. Every line a consumer takes off the stream must pass through
// here so the count stays exact: most of our commands are fire-and-forget and
// their reply blocks are consumed by the main loop, so counting only inside
// round-trips would let seen fall behind sent and no round-trip would ever
// recognise its reply again.
func claimSeq(l controlmode.Line, st *stream) uint64 {
	// A block flagged 0 answers a command we never sent, so it takes no ordinal.
	if (l.Kind == controlmode.End || l.Kind == controlmode.Error) && l.Flags == controlmode.ClientCommandFlag {
		return st.claim()
	}
	return 0
}

// handleAsideLine disposes of a line nobody is waiting for. %output is routed as
// it goes past, so a wait for one pane never drops live output for another (B3);
// every other notification is queued for the main loop rather than dropped,
// since a swallowed %pause leaves its pane paused on the remote with no
// %continue ever answered. A reply block is dropped whatever its ordinal: it
// answers a command whose caller has already moved on.
func handleAsideLine(l controlmode.Line, router *Router, async *asyncQueue) {
	switch l.Kind {
	case controlmode.End, controlmode.Error:
	case controlmode.Output:
		router.Route(l.Pane, l.Data)
	case controlmode.Other:
	default:
		async.push(l)
	}
}

// nextLine reads one line and claims its ordinal. For a reply block one of our
// own commands produced it returns that command's ordinal; seq is 0 for
// everything else.
func nextLine(reader lineReader, st *stream) (l controlmode.Line, seq uint64, ok bool) {
	l, ok = reader.Next()
	if !ok {
		return controlmode.Line{}, 0, false
	}
	return l, claimSeq(l, st), true
}

// readReplyRouting returns the reply block to command number want, passing every
// other line to handleAsideLine.
func readReplyRouting(reader lineReader, router *Router, async *asyncQueue, st *stream, want uint64) (controlmode.Line, bool) {
	for {
		l, seq, ok := nextLine(reader, st)
		if !ok {
			return controlmode.Line{}, false
		}
		if (l.Kind == controlmode.End || l.Kind == controlmode.Error) && seq == want {
			return l, true
		}
		handleAsideLine(l, router, async)
	}
}

// handlePause answers a %pause %N: mark the pane's sink paused (Write drops
// output while paused) and ask tmux to unblock it with a paired %continue,
// which the main loop turns into a full-repaint re-seed.
func handlePause(router *Router, send func(string), paneID string) {
	if s := router.sink(paneID); s != nil {
		s.pause()
		send(fmt.Sprintf("refresh-client -A '%s:continue'", paneID))
	}
}

// handleContinue answers a %continue %N: capture a fresh screen (routing-aware,
// so sibling panes keep streaming during the round-trip — B3) and enqueue it as
// a FrameSeed BEFORE resuming, so the full repaint lands ahead of any resumed
// output and closes the %pause gap.
func handleContinue(router *Router, rt roundTrip, paneID string) {
	s := router.sink(paneID)
	if s == nil {
		return
	}
	if seed, err := PaneSeed(rt, paneID); err == nil {
		s.enqueue(wire.FrameSeed, seed)
	} else {
		fmt.Fprintf(os.Stderr, "daemon: %%continue reseed for %s: %v\n", paneID, err)
	}
	s.resume()
}

// reseedDropped repaints every pane that lost frames to a full buffer.
//
// The drop itself is deliberate: blocking the control-stream loop on one
// stalled renderer would stall every other pane with it. But terminal output is
// positional, so a frame lost mid-repaint leaves those cells wrong until
// something happens to overwrite them — for an agent pane that has just
// finished a turn, that can be a very long time, and what the human sees is
// debris that never clears (#412). capture-pane is ground truth, so the re-seed
// takes the debris with it.
//
// Called from the main loop, which is the only place a round-trip may run, and
// reached without a timer for the same reason everything else here is: the
// output that caused the drop has already woken the loop.
func reseedDropped(router *Router, rt roundTrip) {
	for _, paneID := range router.dirtyPanes() {
		s := router.sink(paneID)
		if s == nil {
			continue
		}
		seed, err := PaneSeed(rt, paneID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: re-seed after drop for %s: %v\n", paneID, err)
			continue
		}
		s.enqueue(wire.FrameSeed, seed)
	}
}

// remoteWinTarget builds the tmux target for a remote window by its id (@N),
// quoting the session name so a name with spaces (e.g. "my proj") stays one
// token. The id is used verbatim — never TrimPrefix'd to a bare N, which tmux
// would read as window INDEX N (a different window).
func remoteWinTarget(cfg Config, remoteID string) string {
	return fmt.Sprintf("%s:%s", tmuxQuote(cfg.RemoteSession), remoteID)
}

// tmuxQuote single-quotes s for a tmux control-mode command line, escaping
// any embedded single quote the tmux-safe way.
func tmuxQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// readLayout reads target's layout string and, in the same round-trip, the
// remote window's active pane id — #{pane_id} in window scope — and whether it
// is zoomed. Layout strings contain no spaces, so one space-separated reply
// carries all three, and every reconcile gets the remote's focus and zoom from
// ground truth instead of a belief.
//
// #{window_layout} is deliberately the UNZOOMED geometry. #{window_visible_layout}
// would report a zoomed window as single-pane, and reconcile would read the
// hidden panes as closed and kill their renderers on every zoom toggle; the
// flag rides alongside instead, and zoom is applied locally as zoom (#413).
func readLayout(rt roundTrip, target string) (l0 controlmode.Layout, active string, zoomed bool, err error) {
	l, ok := rt(fmt.Sprintf("display-message -p -t %s -F '#{window_layout} #{pane_id} #{window_zoomed_flag}'", target))
	if !ok {
		return controlmode.Layout{}, "", false, fmt.Errorf("daemon: control connection closed reading layout for %s", target)
	}
	if l.Kind == controlmode.Error {
		return controlmode.Layout{}, "", false, fmt.Errorf("daemon: display-message window_layout -t %s: %s", target, l.Data)
	}
	fields := strings.Fields(string(l.Data))
	if len(fields) == 0 {
		return controlmode.Layout{}, "", false, fmt.Errorf("daemon: empty layout reply for %s", target)
	}
	if len(fields) > 1 {
		active = fields[1]
	}
	zoomed = len(fields) > 2 && fields[2] == "1"
	L, err := controlmode.ParseLayout(fields[0])
	return L, active, zoomed, err
}

// spawnRenderer respawns the local pane target (a %N pane id) with the
// renderer binary, wired to dial back with remotePane's id.
//
// It also stamps the pane's remote id into the @bridge_pane pane option: that
// is the carrier a local keybind reads to tell the daemon which remote pane a
// structural gesture applies to. Pane options survive respawn-pane -k and ride
// with the pane through select-layout and swap-pane (verified), so this is the
// only place it needs writing.
func spawnRenderer(cfg Config, target, remotePane string) error {
	if err := cfg.LocalTmux("respawn-pane", "-k",
		"-e", "LZTMUX_RENDER_SOCK="+cfg.SockPath,
		"-e", "LZTMUX_RENDER_PANE="+remotePane,
		"-t", target,
		cfg.RendererBin,
	); err != nil {
		return err
	}
	cfg.LocalTmux("set-option", "-p", "-t", target, "@bridge_pane", remotePane)
	return nil
}

// acceptConns accepts connections on l until it's closed and dispatches each on
// its FIRST frame: a FrameHello is a renderer, delivered to out and kept open for
// the life of its pane; a FrameCtl is a one-shot structural request from a local
// keybind, answered with a FrameCtlAck and closed. Anything else is dropped.
//
// onCtl reports an error to send back in the ack; an empty ack means accepted.
func acceptConns(l net.Listener, out chan<- helloConn, onCtl func(argv []string) error) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			f, err := wire.ReadFrame(conn)
			if err != nil {
				conn.Close()
				return
			}
			switch f.Type {
			case wire.FrameHello:
				out <- helloConn{paneID: string(f.Payload), conn: conn}
			case wire.FrameCtl:
				defer conn.Close()
				msg := ""
				if err := onCtl(wire.DecodeArgv(f.Payload)); err != nil {
					msg = err.Error()
				}
				wire.WriteFrame(conn, wire.FrameCtlAck, []byte(msg))
			default:
				conn.Close()
			}
		}()
	}
}

// waitHellos reads exactly n renderer connections off connCh, keyed by the
// remote pane id each announced, while keeping the control stream draining:
// every mirror path that waits here runs on the goroutine that owns the stream,
// so a wait that only watched connCh would let the remote's output back up for
// its whole duration and tmux would %pause every busy pane behind it (#434).
//
// Bounded by timeout so a renderer that never dials back can't wedge the caller
// forever (see helloTimeout); on timeout, or once the stream ends, any
// connections already collected are closed here (nothing else owns them yet)
// and an error is returned.
//
// got counts connections received rather than len(out), since two hellos naming
// the same pane collapse to one entry.
func waitHellos(lines <-chan controlmode.Line, router *Router, async *asyncQueue, st *stream, connCh <-chan helloConn, n int, timeout time.Duration) (map[string]net.Conn, error) {
	out := map[string]net.Conn{}
	deadline := time.After(timeout)
	for got := 0; got < n; {
		select {
		case hc, ok := <-connCh:
			if !ok {
				closeConns(out)
				return nil, fmt.Errorf("daemon: renderer socket closed after %d/%d connections", got, n)
			}
			out[hc.paneID] = hc.conn
			got++
		case l, ok := <-lines:
			if !ok {
				closeConns(out)
				return nil, fmt.Errorf("daemon: control stream ended after %d/%d connections", got, n)
			}
			claimSeq(l, st)
			handleAsideLine(l, router, async)
		case <-deadline:
			closeConns(out)
			return nil, fmt.Errorf("daemon: timed out after %s waiting for renderers (%d/%d connected)", timeout, got, n)
		}
	}
	return out, nil
}

func closeConns(conns map[string]net.Conn) {
	for _, c := range conns {
		c.Close()
	}
}

// seedRenderer produces the initial screen for remotePane and (only on
// success) registers conn's output sink with router, then enqueues the
// FrameSeed followed by a FrameResize (dims from the pane's layout cell)
// through that sink. Register-then-enqueue keeps the seed the sink's first
// frame (FIFO), so it precedes any routed output — no frame bypasses the sink
// (frozen wire invariant). Returns false — logging to stderr rather than
// crashing — if the pane closed between listing and seeding: the caller decides
// whether that's fatal (sole pane) or just leaves that pane unwired.
func seedRenderer(rt roundTrip, router *Router, conn net.Conn, remotePane string, dims controlmode.PaneCell, gfx *graphics.Proxy) bool {
	seed, err := PaneSeed(rt, remotePane)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: seed %s: %v (skipping renderer)\n", remotePane, err)
		conn.Close()
		return false
	}
	sink := newOutputSink(conn, gfx)
	router.Register(remotePane, sink)
	sink.enqueue(wire.FrameSeed, seed)
	sink.enqueue(wire.FrameResize, wire.EncodeResize(dims.W, dims.H))
	return true
}

// sinkFrame is a typed daemon->renderer frame queued on an outputSink. Once
// pause-after flow control lets a mid-stream re-seed happen, the seed is a
// second writer of the conn alongside the output pump, so every frame type
// (seed, output, resize) serializes through the one pump goroutine (frozen
// wire invariant: no frame bypasses the sink).
type sinkFrame struct {
	typ     wire.FrameType
	payload []byte
}

// outputSink serializes all daemon->renderer frames for one pane through a
// single pump goroutine so a slow reader can't block Router.Route (which runs
// on the single main control-stream loop) and the seed/resize/output writers
// never race. A full buffer or a paused pane drops the frame; a paused pane's
// state is recovered by the mandatory fresh FrameSeed that every %continue
// enqueues, an overflow's by reseedDropped (dropped, below).
type outputSink struct {
	mu     sync.Mutex
	ch     chan sinkFrame
	closed bool
	paused bool
	// dropped counts frames lost to a full buffer since the last re-seed.
	// Nonzero means this pane's screen no longer follows from what it was
	// sent, and only a re-seed can put that right — terminal output is
	// positional, so the bytes that would have repaired those cells are the
	// ones that went missing.
	dropped int
}

// newOutputSink constructs the sink and starts its pump immediately; see
// start's doc for what the pump does and why it's a separate method.
func newOutputSink(conn net.Conn, gfx *graphics.Proxy) *outputSink {
	s := &outputSink{ch: make(chan sinkFrame, outputSinkBuf)}
	s.start(conn, gfx)
	return s
}

// start launches the pump goroutine, which batch-drains queued FrameOutput
// frames before handing them to gfx: coalescing can only drop a store a later
// one supersedes if it can see that later store, and with the proxy on this
// goroutine, fetches are serial per pane, so there's never a second store
// arriving mid-fetch — the only way it sees a burst is by draining what's
// already queued behind the frame it woke on. gfx == nil skips filtering
// entirely (tests, and any transport with no remote filesystem to localise
// from).
//
// start is split from newOutputSink so a test can construct the sink,
// enqueue frames directly onto s.ch, and only then call start —
// guaranteeing the pump's first receive sees the whole burst instead of
// racing its startup against the writer.
func (s *outputSink) start(conn net.Conn, gfx *graphics.Proxy) {
	go func() {
		// kn strips modifyOtherKeys negotiation sequences a remote pane's
		// occupant wrote for itself before they reach the local mirror
		// pane's pty, where local tmux would otherwise treat them as a
		// request from the renderer and re-encode future keystrokes —
		// including Ctrl+R — accordingly (#338). Unconditional: unlike gfx,
		// this runs regardless of whether graphics localisation is wired in.
		kn := keyneg.NewFilter()
		var pending *sinkFrame
		for {
			var f sinkFrame
			if pending != nil {
				f, pending = *pending, nil
			} else {
				v, ok := <-s.ch
				if !ok {
					// The pane is gone: flush whatever the proxies were
					// still holding (a partial sequence cut mid-stream)
					// rather than silently dropping it. kn only ever holds
					// the newest unprocessed tail, so its leftover is
					// chronologically after anything gfx is already
					// holding — route it through gfx.Filter first, same as
					// the steady-state order, so the tail comes out in the
					// original byte order.
					tail := kn.Flush()
					if gfx != nil {
						tail = append(gfx.Filter(tail), gfx.Close()...)
					}
					if len(tail) > 0 {
						wire.WriteFrame(conn, wire.FrameOutput, tail)
					}
					return
				}
				f = v
			}
			if f.typ == wire.FrameOutput {
				// drainOutput can append more queued FrameOutput frames'
				// raw payload onto buf, and any of those can carry a
				// negotiation sequence too, so kn.Feed runs on the fully
				// drained batch.
				buf := append([]byte(nil), f.payload...)
				buf, pending = drainOutput(s.ch, buf)
				buf = kn.Feed(buf)
				if gfx != nil {
					buf = gfx.Filter(buf)
				}
				f.payload = buf
				if len(f.payload) == 0 {
					continue
				}
			}
			if err := wire.WriteFrame(conn, f.typ, f.payload); err != nil {
				return
			}
		}
	}()
}

// drainOutput appends every FrameOutput already queued on ch to buf, so the
// proxy sees a whole burst at once and can drop stores a later frame
// supersedes. It stops at the first non-output frame and hands it back to be
// written next: reordering a seed or resize past output would break the
// frozen wire invariant (sinkFrame's doc above).
func drainOutput(ch chan sinkFrame, buf []byte) ([]byte, *sinkFrame) {
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return buf, nil
			}
			if v.typ != wire.FrameOutput {
				return buf, &v
			}
			buf = append(buf, v.payload...)
		default:
			return buf, nil
		}
	}
}

// writeOwned enqueues p as a FrameOutput without copying. Callers must
// guarantee p is freshly allocated and never retained or mutated after this
// call returns. Non-blocking, like Write: a full buffer or a paused/closed
// sink drops the frame.
func (s *outputSink) writeOwned(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.paused {
		return
	}
	select {
	case s.ch <- sinkFrame{typ: wire.FrameOutput, payload: p}:
	default:
		s.dropped++
	}
}

// Write is the router-facing io.Writer path: it enqueues a FrameOutput. While
// paused, output is dropped (tmux is discarding it remote-side anyway) and
// recovered by the fresh FrameSeed on the paired %continue. A full buffer drops
// the frame too; the pane self-heals on its next %output or the next re-seed.
func (s *outputSink) Write(p []byte) (int, error) {
	s.writeOwned(append([]byte(nil), p...))
	return len(p), nil
}

// enqueue serializes a non-output frame (seed, resize) through the same pump so
// it never races the output writer. It must NOT block: a stalled (not dead)
// renderer with a full buffer would otherwise wedge the control-stream loop, so
// it uses the same bounded non-blocking select + drop as Write.
func (s *outputSink) enqueue(typ wire.FrameType, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- sinkFrame{typ: typ, payload: append([]byte(nil), payload...)}:
	default:
		s.dropped++
	}
}

// takeDirty reports how many frames this sink dropped, and clears the count, but
// only once the sink has drained: while a pane is still congested a re-seed
// would be dropped in its turn, and it is a whole extra screen on a queue that
// is already behind. A paused pane is left alone too — its %continue owes it a
// seed already.
func (s *outputSink) takeDirty() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped == 0 || s.closed || s.paused || len(s.ch) > 0 {
		return 0, false
	}
	n := s.dropped
	s.dropped = 0
	return n, true
}

func (s *outputSink) pause()  { s.mu.Lock(); s.paused = true; s.mu.Unlock() }
func (s *outputSink) resume() { s.mu.Lock(); s.paused = false; s.mu.Unlock() }

// Close stops the sink's pump goroutine so it doesn't leak once its pane is
// torn down (reconcile-removal, teardown); the channel is otherwise never
// closed and an idle sink would linger until process exit. Safe to call more
// than once, and safe to race with a concurrent Write.
func (s *outputSink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// pumpInput forwards conn's FrameInput frames to the remote pane as
// send-keys commands, until conn closes.
func pumpInput(conn net.Conn, remotePane string, send func(string)) {
	for {
		f, err := wire.ReadFrame(conn)
		if err != nil {
			return
		}
		if f.Type != wire.FrameInput {
			continue
		}
		for _, args := range controlmode.SendKeysArgs(remotePane, f.Payload, controlmode.InputChunkBytes) {
			send(strings.Join(args, " "))
		}
	}
}
