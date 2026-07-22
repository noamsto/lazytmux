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
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// Config is the injectable seam for Run: everything that talks to a real
// ssh/tmux/socket in production is a field here, so the bats test can point it
// at a second local tmux instead.
type Config struct {
	Ctl            io.ReadWriteCloser         // the ssh -CC stream (stdin+stdout duplex)
	SockPath       string                     // unix socket renderers dial
	LocalSess      string                     // "<host>-<sess>"
	RemoteSession  string                     // remote session name (may contain spaces)
	RemoteWindow   string                     // initially-selected remote window INDEX (not a mirror filter)
	BaseIndex      int                        // local base-index for daemon-created windows (default 1)
	PauseAfterSecs int                        // refresh-client -f pause-after=N (0 disables); backpressure insurance answered by a %continue re-seed
	RendererBin    string                     // absolute store path to cmd/renderer
	LocalTmux      func(args ...string) error // runs local tmux (injected; prod = exec)
	WinSize        func() (int, int)          // local window content size (injected)
}

// outputSinkBuf is the per-renderer output buffer depth. Overflow drops the
// frame rather than blocking the control-stream loop; the pane self-heals on
// its next %output, or on the fresh FrameSeed any %continue sends.
const outputSinkBuf = 4096

// helloTimeout bounds how long collectHellos waits for renderers to dial
// back. A spawned renderer that never connects (bad RendererBin, exec
// failure, crash before it dials) doesn't surface as a LocalTmux error —
// respawn-pane itself succeeds — so without a deadline the wait blocks Run
// forever (startup never proceeds; reconcile blocks the main loop, so the
// control stream stops draining).
const helloTimeout = 10 * time.Second

// helloConn pairs an accepted renderer connection with the remote pane id it
// announced via FrameHello.
type helloConn struct {
	paneID string
	conn   net.Conn
}

// resizePollInterval is how often the resize watcher re-checks the local
// client size. A human terminal resize is discrete and infrequent, so a 1s
// poll is responsive enough and cheap (one WinSize query/sec).
const resizePollInterval = time.Second

// watchResize re-converges the remote to the local client size whenever it
// changes. A local terminal/client resize emits no control-stream event, so
// the daemon must poll: on a change it re-pushes ConvergeCmd (one control
// client = one size = all remote windows), which resizes the remote and makes
// it emit %layout-change per window, driving the existing reconcile + re-seed.
// send is the same mutex-guarded, no-op-when-closed sender the main loop uses;
// this only injects a fire-and-forget command (its %begin/%end ack is consumed
// harmlessly by the main loop's top-level reader.Next()).
func watchResize(winSize func() (int, int), w, h int, send func(string), stop <-chan struct{}, tick <-chan time.Time) {
	for {
		select {
		case <-stop:
			return
		case <-tick:
			nw, nh := winSize()
			if nw != w || nh != h {
				w, h = nw, nh
				send(ConvergeCmd(nw, nh))
			}
		}
	}
}

// Run mirrors every window of the bridged remote session, each into its own
// local window, over the single -CC connection, until %exit or the control
// connection drops.
func Run(cfg Config) error {
	reader := controlmode.NewReader(cfg.Ctl)

	var sendMu sync.Mutex
	closed := false
	cmds := bufio.NewWriter(cfg.Ctl)
	// send is called from this setup path and from every renderer's input
	// pump goroutine — mutex-guarded so command lines never interleave on the
	// wire (mirrors M1 main.go's sendMu).
	send := func(s string) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return
		}
		fmt.Fprintf(cmds, "%s\n", s)
		cmds.Flush()
	}

	// Drain the implicit attach reply (startup skip is sanctioned — B3).
	readReply(reader)

	// Converge remote size to the local window's content size. One control
	// client means one size, so this converges ALL remote windows at once.
	w, h := cfg.WinSize()
	send(ConvergeCmd(w, h))
	readReply(reader)

	// Enumerate every window of the bridged remote session. Read BOTH index
	// and id: --window is an *index*, the registry is keyed by *id* (@N).
	send(fmt.Sprintf("list-windows -t %s -F '#{window_index} #{window_id} #{window_name}'", tmuxQuote(cfg.RemoteSession)))
	lw, ok := readReply(reader)
	if !ok || lw.Kind == controlmode.Error {
		return fmt.Errorf("daemon: list-windows for %s failed", cfg.RemoteSession)
	}
	remoteWins := parseWindowList(string(lw.Data))
	if len(remoteWins) == 0 {
		return fmt.Errorf("daemon: remote session %s has no windows", cfg.RemoteSession)
	}

	router := NewRouter()

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
	go acceptRenderers(listener, connCh)

	reg := newRegistry(cfg.BaseIndex)
	// stopWatch stops the resize watcher (started just before the main loop).
	// Declared here so teardown can close it; teardown runs exactly once per
	// Run return path, so a plain close is safe.
	stopWatch := make(chan struct{})
	teardown := func() {
		close(stopWatch)
		listener.Close()
		os.Remove(cfg.SockPath)
		os.Remove(pidFile)
		for _, mw := range reg.byRemote {
			// Unregister closes each pane's output sink, stopping its pump
			// goroutine (mirrors closeWindow); then drop the renderer conns.
			for _, id := range mw.remotePanes {
				router.Unregister(id)
			}
			for _, c := range mw.conns {
				c.Close()
			}
		}
		sendMu.Lock()
		closed = true
		sendMu.Unlock()
		cfg.Ctl.Close()
		if cfg.LocalSess != "" {
			cfg.LocalTmux("kill-session", "-t", cfg.LocalSess)
		}
	}

	// Mirror each remote window into its own local window. The first reuses
	// the launcher's initial window (base-index); the rest are created at an
	// explicit monotonically-increasing index.
	for i, rw := range remoteWins {
		localWin := reg.allocLocalWin(cfg.LocalSess)
		if i > 0 {
			if err := cfg.LocalTmux("new-window", "-d", "-t", localWin); err != nil {
				teardown()
				return fmt.Errorf("daemon: new-window %s: %w", localWin, err)
			}
		}
		cfg.LocalTmux("set-option", "-w", "-t", localWin, "@bridge_win", "1")
		// Panes are addressed 0-based (spawnRenderer/reconcileLayout use index
		// starting at 0); force window-level pane-base-index 0 so that holds
		// regardless of the host's global pane-base-index (real hosts set 1).
		cfg.LocalTmux("set-option", "-w", "-t", localWin, "pane-base-index", "0")
		mw := reg.add(rw.id, localWin)
		if err := setupWindow(cfg, reader, send, router, connCh, mw, readReply); err != nil {
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

	// Re-converge the remote whenever the local client resizes. A local resize
	// emits no control-stream event, so poll; teardown closes stopWatch.
	ticker := time.NewTicker(resizePollInterval)
	go func() { defer ticker.Stop(); watchResize(cfg.WinSize, w, h, send, stopWatch, ticker.C) }()

	// Main loop.
	pauseAfterSet := false
	for {
		// Enable pause-after only now that every window is set up and the loop
		// is draining the async stream — setup does blocking collectHellos/seed
		// round-trips without draining, so arming it earlier would let a pane
		// get %pause'd mid-setup with no %continue re-seed to answer it (a
		// deadlock offline bats can't catch).
		if !pauseAfterSet {
			pauseAfterSet = true
			if cfg.PauseAfterSecs > 0 {
				send(fmt.Sprintf("refresh-client -f pause-after=%d", cfg.PauseAfterSecs))
			}
		}
		l, ok := reader.Next()
		if !ok {
			break // control-stream EOF
		}
		switch l.Kind {
		case controlmode.Output:
			router.Route(l.Pane, l.Data)
		case controlmode.LayoutChange:
			if len(l.Args) > 0 {
				if mw, ok := reg.byRemoteID(l.Args[0]); ok {
					reconcileLayout(cfg, mw, reader, send, router, connCh)
				}
			}
		case controlmode.WindowRenamed, controlmode.SessionWindowChanged:
			if argv, ok := translateWindowNotification(l, reg); ok {
				cfg.LocalTmux(argv...)
			}
		case controlmode.WindowAdd:
			if len(l.Args) > 0 {
				addWindow(cfg, reader, send, router, connCh, reg, l.Args[0])
			}
		case controlmode.WindowClose:
			if len(l.Args) > 0 {
				closeWindow(cfg, router, reg, l.Args[0])
				if reg.empty() {
					teardown()
					return nil
				}
			}
		case controlmode.Pause:
			if len(l.Args) > 0 {
				handlePause(router, send, l.Args[0])
			}
		case controlmode.Continue:
			if len(l.Args) > 0 {
				handleContinue(reader, router, send, l.Args[0])
			}
		case controlmode.Exit:
			teardown()
			return nil
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
// reply is the caller's reply reader: startup enumeration passes the plain
// skip reader (readReply) since no window is streaming yet (sanctioned startup
// skip); a live %window-add passes the routing-aware reader (B3), since
// sibling windows are streaming while this window's pipeline runs. When the
// plain reader is used, live %output for an already-seeded window during this
// interval is dropped rather than routed; it self-heals on the pane's next
// %output once the main loop starts.
//
// For a 1-pane remote window this is exactly M1's behavior — no split, one
// renderer, matching dims — since PlanWindow emits zero splits for a 1-pane
// layout.
func setupWindow(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, mw *mirrorWindow, reply replyFn) error {
	L, err := readLayout(reader, send, remoteWinTarget(cfg, mw.remoteID), reply)
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

	// Spawn one renderer per local pane, targeted by position — the local
	// window has no other source of pane identity available through Config
	// (LocalTmux runs commands but doesn't capture output), and PlanWindow's
	// splits create panes in RemotePaneOrder position (see mirror.go).
	for i, remotePane := range mw.remotePanes {
		if err := spawnRenderer(cfg, mw.localWin, i, remotePane); err != nil {
			return fmt.Errorf("daemon: spawn renderer for %s: %w", remotePane, err)
		}
	}

	// Collect exactly len(remotePanes) Hellos (any order) before seeding —
	// seeding is sequential over the single control stream, so all renderers
	// must be connected (and hence writable) first.
	byRemote, err := collectHellos(connCh, len(mw.remotePanes), helloTimeout)
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
			continue // didn't connect; already logged by collectHellos caller
		}
		if !seedRenderer(reader, send, router, conn, remotePane, reply, L.Panes[i]) {
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

// addWindow B2-confirms a %window-add notification via a routing-aware
// list-windows re-read — sibling windows are streaming while this round-trip
// is in flight, so any %output seen must be routed rather than dropped (B3).
// If remoteID is now in the bridged session and not already mirrored, it runs
// the same plan/spawn/hello/seed pipeline as startup (routing-aware, since
// siblings are live). Otherwise (the window belongs elsewhere, or a duplicate
// notification for an already-registered window) it's a no-op.
func addWindow(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, reg *registry, remoteID string) {
	if _, already := reg.byRemoteID(remoteID); already {
		return
	}
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }

	send(fmt.Sprintf("list-windows -t %s -F '#{window_index} #{window_id} #{window_name}'", tmuxQuote(cfg.RemoteSession)))
	lw, ok := reply(reader)
	if !ok || lw.Kind == controlmode.Error {
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: list-windows failed\n", remoteID)
		return
	}
	inSession := false
	for _, rw := range parseWindowList(string(lw.Data)) {
		if rw.id == remoteID {
			inSession = true
			break
		}
	}
	if !inSession {
		return
	}

	localWin := reg.allocLocalWin(cfg.LocalSess)
	if err := cfg.LocalTmux("new-window", "-d", "-t", localWin); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: new-window %s: %v\n", remoteID, localWin, err)
		return
	}
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "@bridge_win", "1")
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "pane-base-index", "0")
	mw := reg.add(remoteID, localWin)
	if err := setupWindow(cfg, reader, send, router, connCh, mw, reply); err != nil {
		// Drop the half-created entry + local window so the already-registered
		// guard doesn't block a later %window-add retry for this id.
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: %v\n", remoteID, err)
		reg.remove(remoteID)
		cfg.LocalTmux("kill-window", "-t", localWin)
	}
}

// closeWindow tears down remoteID's local mirror: unregisters (and thereby
// closes) each pane's output sink, closes each renderer conn, and kills the
// local window. A notification for a window outside the registry is a no-op
// (B2) — kill-window must never run against a window this daemon doesn't own.
func closeWindow(cfg Config, router *Router, reg *registry, remoteID string) {
	mw, ok := reg.remove(remoteID)
	if !ok {
		return
	}
	for _, id := range mw.remotePanes {
		router.Unregister(id)
	}
	for _, c := range mw.conns {
		c.Close()
	}
	cfg.LocalTmux("kill-window", "-t", mw.localWin)
}

// readReplyRouting is the steady-state (post-startup) reply reader (B3): it
// returns the next command-reply block (End/Error) but routes any %output it
// encounters to router first, so a mid-stream round-trip for one pane never
// drops live %output for another. Startup seeding keeps readReply's plain
// skip-behavior (no live stream yet).
func readReplyRouting(reader *controlmode.Reader, router *Router) (controlmode.Line, bool) {
	for {
		l, ok := reader.Next()
		if !ok {
			return controlmode.Line{}, false
		}
		switch l.Kind {
		case controlmode.End, controlmode.Error:
			return l, true
		case controlmode.Output:
			router.Route(l.Pane, l.Data)
		}
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
func handleContinue(reader *controlmode.Reader, router *Router, send func(string), paneID string) {
	s := router.sink(paneID)
	if s == nil {
		return
	}
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }
	if seed, err := PaneSeed(reader, send, paneID, reply); err == nil {
		s.enqueue(wire.FrameSeed, seed)
	} else {
		fmt.Fprintf(os.Stderr, "daemon: %%continue reseed for %s: %v\n", paneID, err)
	}
	s.resume()
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

func readLayout(reader *controlmode.Reader, send func(string), target string, reply replyFn) (controlmode.Layout, error) {
	send(fmt.Sprintf("display-message -p -t %s -F '#{window_layout}'", target))
	l, ok := reply(reader)
	if !ok {
		return controlmode.Layout{}, fmt.Errorf("daemon: control connection closed reading layout for %s", target)
	}
	if l.Kind == controlmode.Error {
		return controlmode.Layout{}, fmt.Errorf("daemon: display-message window_layout -t %s: %s", target, l.Data)
	}
	return controlmode.ParseLayout(strings.TrimSpace(string(l.Data)))
}

// spawnRenderer respawns localWin's pane at position index (targeted by
// window.index, since PlanWindow's local panes are created in RemotePaneOrder
// position) with the renderer binary, wired to dial back with remotePane's id.
func spawnRenderer(cfg Config, localWin string, index int, remotePane string) error {
	target := fmt.Sprintf("%s.%d", localWin, index)
	return cfg.LocalTmux("respawn-pane", "-k",
		"-e", "LZTMUX_RENDER_SOCK="+cfg.SockPath,
		"-e", "LZTMUX_RENDER_PANE="+remotePane,
		"-t", target,
		cfg.RendererBin,
	)
}

// acceptRenderers accepts connections on l until it's closed, reads each
// one's FrameHello, and delivers (remote pane id, conn) pairs to out.
// Connections that don't Hello correctly are dropped.
func acceptRenderers(l net.Listener, out chan<- helloConn) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			f, err := wire.ReadFrame(conn)
			if err != nil || f.Type != wire.FrameHello {
				conn.Close()
				return
			}
			out <- helloConn{paneID: string(f.Payload), conn: conn}
		}()
	}
}

// collectHellos reads exactly n renderer connections off connCh, keyed by
// the remote pane id each announced. Bounded by timeout so a renderer that
// never dials back can't wedge the caller forever (see helloTimeout); on
// timeout any connections already collected are closed here (nothing else
// owns them yet) and an error is returned.
func collectHellos(connCh <-chan helloConn, n int, timeout time.Duration) (map[string]net.Conn, error) {
	out := map[string]net.Conn{}
	deadline := time.After(timeout)
	for i := 0; i < n; i++ {
		select {
		case hc, ok := <-connCh:
			if !ok {
				closeConns(out)
				return nil, fmt.Errorf("daemon: renderer socket closed after %d/%d connections", i, n)
			}
			out[hc.paneID] = hc.conn
		case <-deadline:
			closeConns(out)
			return nil, fmt.Errorf("daemon: timed out after %s waiting for renderers (%d/%d connected)", timeout, i, n)
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
// (frozen wire invariant). reply is the reader startup passes readReply (skip);
// steady-state reconcile passes a router-bound routing closure (B3). Returns
// false — logging to stderr rather than crashing — if the pane closed between
// listing and seeding: the caller decides whether that's fatal (sole pane) or
// just leaves that pane unwired.
func seedRenderer(reader *controlmode.Reader, send func(string), router *Router, conn net.Conn, remotePane string, reply replyFn, dims controlmode.PaneCell) bool {
	seed, err := PaneSeed(reader, send, remotePane, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: seed %s: %v (skipping renderer)\n", remotePane, err)
		conn.Close()
		return false
	}
	sink := newOutputSink(conn)
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
// never race. A full buffer or a paused pane drops the frame; state is
// recovered by the mandatory fresh FrameSeed that every %continue enqueues.
type outputSink struct {
	mu     sync.Mutex
	ch     chan sinkFrame
	closed bool
	paused bool
}

func newOutputSink(conn net.Conn) *outputSink {
	s := &outputSink{ch: make(chan sinkFrame, outputSinkBuf)}
	go func() {
		for f := range s.ch {
			if err := wire.WriteFrame(conn, f.typ, f.payload); err != nil {
				return
			}
		}
	}()
	return s
}

// Write is the router-facing io.Writer path: it enqueues a FrameOutput. While
// paused, output is dropped (tmux is discarding it remote-side anyway) and
// recovered by the fresh FrameSeed on the paired %continue. A full buffer drops
// the frame too; the pane self-heals on its next %output or the next re-seed.
func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return len(p), nil
	}
	if s.paused {
		return len(p), nil
	}
	select {
	case s.ch <- sinkFrame{typ: wire.FrameOutput, payload: append([]byte(nil), p...)}:
	default:
	}
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
	}
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
		for _, args := range controlmode.SendKeysArgs(remotePane, f.Payload, 500) {
			send(strings.Join(args, " "))
		}
	}
}

// maxReconcilePasses bounds reconcileLayout's trailing-reread loop (below).
const maxReconcilePasses = 5

// reconcileLayout re-reads window w's remote layout and, if the pane set
// changed, applies the two minimal M2.1 cases: a pure tail-append (a remote
// split added panes) or a pure tail-removal (remote pane(s) closed). Any
// other change (reordering, mid-list insert/remove) is a full diff engine —
// deferred — so it's logged and left as-is for this cycle. The updated
// pane-order is stored back on w.remotePanes.
//
// Round-trips here use the routing-aware reply reader (B3): sibling windows
// are streaming during a live reconcile, so any %output seen while awaiting a
// reply is routed rather than dropped. The remote window is targeted by its
// id (@N) directly, never by a bare index.
//
// Loops on a trailing re-read after applying: a second remote layout change
// landing back-to-back with the first can have its own %layout-change
// swallowed while this function is mid-flight (readReplyRouting still returns
// on the reply block, not on the async notification). Re-reading once more
// right after applying catches this: the round-trips above give the remote
// plenty of time to settle, so a still-different layout means something
// changed underneath us and needs its own pass.
func reconcileLayout(cfg Config, w *mirrorWindow, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn) {
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }
	target := remoteWinTarget(cfg, w.remoteID)

	remote := w.remotePanes
	L, err := readLayout(reader, send, target, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
		return
	}

	for pass := 0; pass < maxReconcilePasses; pass++ {
		newRemote := RemotePaneOrder(L)

		switch {
		case reflect.DeepEqual(newRemote, remote):
			// Geometry-only change (typically a client/terminal resize propagated to
			// the remote): same pane set, new dims. The painters hold no back-buffer to
			// reflow, so re-seed each pane's current screen for a clean repaint at the
			// new size; the FrameResize broadcast below records the dims.
			for _, id := range remote {
				s := router.sink(id)
				if s == nil {
					continue
				}
				if seed, err := PaneSeed(reader, send, id, reply); err == nil {
					s.enqueue(wire.FrameSeed, seed)
				}
			}
		case isPrefixOf(remote, newRemote):
			// Relies on split-window's new pane landing at pane_index==i (bare
			// split, no -b/target-pane games): spawnRenderer targets the local
			// pane by that position right after the split with no readback
			// confirming it.
			for i := len(remote); i < len(newRemote); i++ {
				if err := cfg.LocalTmux("split-window", "-h", "-t", w.localWin); err != nil {
					fmt.Fprintf(os.Stderr, "daemon: layout-change split: %v\n", err)
					w.remotePanes = remote
					return
				}
				if err := spawnRenderer(cfg, w.localWin, i, newRemote[i]); err != nil {
					fmt.Fprintf(os.Stderr, "daemon: layout-change spawn renderer: %v\n", err)
					w.remotePanes = remote
					return
				}
			}
			added, err := collectHellos(connCh, len(newRemote)-len(remote), helloTimeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
				w.remotePanes = remote
				return
			}
			for i := len(remote); i < len(newRemote); i++ {
				id := newRemote[i]
				c := added[id]
				if c == nil {
					continue
				}
				w.conns[id] = c
				if seedRenderer(reader, send, router, c, id, reply, L.Panes[i]) {
					go pumpInput(c, id, send)
				} else {
					delete(w.conns, id)
				}
			}
		case isPrefixOf(newRemote, remote):
			for i := len(remote) - 1; i >= len(newRemote); i-- {
				removed := remote[i]
				router.Unregister(removed)
				if c := w.conns[removed]; c != nil {
					c.Close()
					delete(w.conns, removed)
				}
				if err := cfg.LocalTmux("kill-pane", "-t", fmt.Sprintf("%s.%d", w.localWin, i)); err != nil {
					fmt.Fprintf(os.Stderr, "daemon: layout-change kill-pane: %v\n", err)
				}
			}
		default:
			fmt.Fprintf(os.Stderr, "daemon: layout-change: unsupported pane reshuffle %v -> %v, skipping reconcile\n", remote, newRemote)
			w.remotePanes = remote
			return
		}

		if err := cfg.LocalTmux("select-layout", "-t", w.localWin, L.Raw); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: layout-change select-layout: %v\n", err)
		}
		// select-layout reshapes every surviving pane, so push each its new
		// dims (layout is daemon-authoritative — renderers only record them).
		for i, id := range newRemote {
			if s := router.sink(id); s != nil {
				s.enqueue(wire.FrameResize, wire.EncodeResize(L.Panes[i].W, L.Panes[i].H))
			}
		}
		remote = newRemote

		fresh, err := readLayout(reader, send, target, reply)
		if err != nil || fresh.Raw == L.Raw {
			w.remotePanes = remote
			return
		}
		L = fresh
	}
	fmt.Fprintf(os.Stderr, "daemon: layout-change: didn't converge after %d passes, stopping at %v\n", maxReconcilePasses, remote)
	w.remotePanes = remote
}

// isPrefixOf reports whether a is a prefix of b (used to detect pure
// tail-append/tail-removal pane-set changes).
func isPrefixOf(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i, v := range a {
		if b[i] != v {
			return false
		}
	}
	return true
}
