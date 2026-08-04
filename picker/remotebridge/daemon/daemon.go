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
	LocalArea      func() (int, int)          // content area the local mirror session's clients can show (injected)
	Reflow         func()                     // forces a status-bar reflow of the mirror session (injected; nil = off)
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
// client area. A human terminal resize is discrete and infrequent, so a 1s
// poll is responsive enough and cheap (one LocalArea query/sec).
const resizePollInterval = time.Second

// watchResize re-asserts every mirrored window's cap whenever the local client
// area changes. A local terminal/client resize emits no control-stream event,
// so the daemon must poll: on a change it re-pushes ConvergeCmd per mirrored
// window, which resizes the remote and makes it emit %layout-change per
// window, driving the existing reconcile + re-seed (and the re-fit of the
// local window to the remote's new size). send is the same mutex-guarded,
// no-op-when-closed sender the main loop uses; this only injects
// fire-and-forget commands (their %begin/%end acks are consumed harmlessly by
// the main loop's top-level reader.Next()).
func watchResize(area func() (int, int), reg *registry, cv *converger, send func(string), stop <-chan struct{}, tick <-chan time.Time) {
	for {
		select {
		case <-stop:
			return
		case <-tick:
			w, h := area()
			for _, remoteID := range reg.remoteIDs() {
				if cv.need(remoteID, w, h) {
					send(ConvergeCmd(remoteID, w, h))
				}
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
	// sendOK is called from this setup path, from every renderer's input pump
	// goroutine, and from ctl connections — mutex-guarded so command lines never
	// interleave on the wire (mirrors M1 main.go's sendMu). It reports whether
	// the line was written: a ctl request that loses the race with teardown must
	// not be acked as accepted, or the keybind reports success for a gesture that
	// never happened.
	sendOK := func(s string) bool {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return false
		}
		fmt.Fprintf(cmds, "%s\n", s)
		cmds.Flush()
		return true
	}
	// send is the fire-and-forget form the mirror paths use.
	send := func(s string) { sendOK(s) }

	// Drain the implicit attach reply (startup skip is sanctioned — B3).
	readReply(reader)

	// Enumerate every window of the bridged remote session. Read BOTH index
	// and id: --window is an *index*, the registry is keyed by *id* (@N).
	send(fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowListFormat))
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
	cst := newCtlState()
	go acceptConns(listener, connCh, func(argv []string) error {
		req, err := cst.parseCtl(argv, cfg.RemoteSession)
		if err != nil {
			return err
		}
		if !cst.submit(req, sendOK) {
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
	}

	reg := newRegistry(cfg.BaseIndex)
	cv := newConverger()
	// stopWatch stops the resize watcher (started just before the main loop).
	// Declared here so teardown can close it; teardown runs exactly once per
	// Run return path, so a plain close is safe.
	stopWatch := make(chan struct{})
	teardown := func() {
		close(stopWatch)
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
		if name := sanitizeWindowName(rw.name); name != "" {
			cfg.LocalTmux("set-option", "-w", "-t", localWin, "@window_bridge_name", name)
			cfg.LocalTmux("rename-window", "-t", localWin, name) // instant floor; reflow self-heals window_name
		}
		mw := reg.add(rw.id, localWin)
		if err := setupWindow(cfg, reader, send, router, connCh, cst, mw, cv, readReply); err != nil {
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
	cfg.reflow()

	// Re-converge the remote whenever the local client resizes. A local resize
	// emits no control-stream event, so poll; teardown closes stopWatch.
	ticker := time.NewTicker(resizePollInterval)
	go func() { defer ticker.Stop(); watchResize(cfg.LocalArea, reg, cv, send, stopWatch, ticker.C) }()

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
					reconcileLayout(cfg, mw, reader, send, router, connCh, cst)
				}
			}
		case controlmode.WindowRenamed, controlmode.SessionWindowChanged:
			if argv, ok := translateWindowNotification(l, reg); ok {
				cfg.LocalTmux(argv...)
				if l.Kind == controlmode.WindowRenamed {
					cfg.reflow()
				}
			}
		case controlmode.WindowAdd:
			if len(l.Args) > 0 {
				addWindow(cfg, reader, send, router, connCh, cst, reg, cv, l.Args[0])
			}
		case controlmode.WindowClose:
			if len(l.Args) > 0 {
				closeWindow(cfg, router, cst, reg, cv, l.Args[0])
				if reg.empty() {
					teardown()
					return nil
				}
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
				handleContinue(reader, router, send, l.Args[0])
			}
		case controlmode.Exit:
			teardown()
			return nil
		}

		// Drain the reconcile intents a ctl request registered. This runs after
		// the line above because reader.Next() blocks: the reply block for the
		// ctl request's own remote command is what wakes the loop back to here,
		// so a gesture is always followed by a drain without needing a timer.
		if wantWindows, layouts := cst.takeIntents(); wantWindows || len(layouts) > 0 {
			if wantWindows {
				reconcileWindows(cfg, reader, send, router, connCh, cst, reg, cv)
				if reg.empty() {
					teardown()
					return nil
				}
			}
			for _, remoteID := range layouts {
				// A layout intent for a window reconcileWindows just closed has
				// nothing to reconcile.
				if mw, ok := reg.byRemoteID(remoteID); ok {
					reconcileLayout(cfg, mw, reader, send, router, connCh, cst)
				}
			}
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
func setupWindow(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, cst *ctlState, mw *mirrorWindow, cv *converger, reply replyFn) error {
	// Cap the remote window at what the local clients can show before reading
	// its layout, so the layout that gets mirrored is the converged one.
	if w, h := cfg.LocalArea(); cv.need(mw.remoteID, w, h) {
		send(ConvergeCmd(mw.remoteID, w, h))
		reply(reader) // consume refresh-client's own (empty) reply
	}

	L, _, err := readLayout(reader, send, remoteWinTarget(cfg, mw.remoteID), reply)
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
	cst.setWindowPanes(mw.remoteID, mw.remotePanes)

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
func addWindow(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, cst *ctlState, reg *registry, cv *converger, remoteID string) {
	if _, already := reg.byRemoteID(remoteID); already {
		return
	}
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }

	send(fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowListFormat))
	lw, ok := reply(reader)
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

	localWin := reg.allocLocalWin(cfg.LocalSess)
	if err := cfg.LocalTmux("new-window", "-d", "-t", localWin); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: window-add %s: new-window %s: %v\n", remoteID, localWin, err)
		return
	}
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "@bridge_win", "1")
	cfg.LocalTmux("set-option", "-w", "-t", localWin, "pane-base-index", "0")
	if name := sanitizeWindowName(addedName); name != "" {
		cfg.LocalTmux("set-option", "-w", "-t", localWin, "@window_bridge_name", name)
		cfg.LocalTmux("rename-window", "-t", localWin, name) // instant floor; reflow self-heals
	}
	mw := reg.add(remoteID, localWin)
	if err := setupWindow(cfg, reader, send, router, connCh, cst, mw, cv, reply); err != nil {
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

// readLayout reads target's layout string and, in the same round-trip, the
// remote window's active pane id — #{pane_id} in window scope. Layout strings
// contain no spaces, so one space-separated reply carries both, and every
// reconcile gets the remote's focus from ground truth instead of a belief.
func readLayout(reader *controlmode.Reader, send func(string), target string, reply replyFn) (controlmode.Layout, string, error) {
	send(fmt.Sprintf("display-message -p -t %s -F '#{window_layout} #{pane_id}'", target))
	l, ok := reply(reader)
	if !ok {
		return controlmode.Layout{}, "", fmt.Errorf("daemon: control connection closed reading layout for %s", target)
	}
	if l.Kind == controlmode.Error {
		return controlmode.Layout{}, "", fmt.Errorf("daemon: display-message window_layout -t %s: %s", target, l.Data)
	}
	fields := strings.Fields(string(l.Data))
	if len(fields) == 0 {
		return controlmode.Layout{}, "", fmt.Errorf("daemon: empty layout reply for %s", target)
	}
	active := ""
	if len(fields) > 1 {
		active = fields[1]
	}
	L, err := controlmode.ParseLayout(fields[0])
	return L, active, err
}

// spawnRenderer respawns localWin's pane at position index (targeted by
// window.index, since PlanWindow's local panes are created in RemotePaneOrder
// position) with the renderer binary, wired to dial back with remotePane's id.
//
// It also stamps the pane's remote id into the @bridge_pane pane option: that
// is the carrier a local keybind reads to tell the daemon which remote pane a
// structural gesture applies to. Pane options survive respawn-pane -k and ride
// with the pane through select-layout and swap-pane (verified), so this is the
// only place it needs writing.
func spawnRenderer(cfg Config, localWin string, index int, remotePane string) error {
	target := fmt.Sprintf("%s.%d", localWin, index)
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
