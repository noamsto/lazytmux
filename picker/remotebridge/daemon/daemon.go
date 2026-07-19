// Package daemon owns the M2.1 mirror: one control-mode connection to a
// remote tmux, converged to a local window's size, with one native local
// pane per remote pane and one renderer process per pane feeding/draining a
// unix socket. Run wires all of it together; see the orchestration sequence
// in docs/superpowers/plans/2026-07-17-remote-bridge-m2.1.md (Task 8).
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

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// Config is the injectable seam for Run: everything that talks to a real
// ssh/tmux/socket in production is a field here, so Task 9's bats test can
// point it at a second local tmux instead.
type Config struct {
	Ctl         io.ReadWriteCloser         // the ssh -CC stream (stdin+stdout duplex)
	SockPath    string                     // unix socket renderers dial
	LocalSess   string                     // "<host>-<sess>"
	LocalWin    string                     // "<host>-<sess>:1"
	RemoteWin   string                     // "<sess>:<win>" on the remote
	RendererBin string                     // absolute store path to cmd/renderer
	LocalTmux   func(args ...string) error // runs local tmux (injected; prod = exec)
	WinSize     func() (int, int)          // local window content size (injected)
}

// outputSinkBuf is the per-renderer output buffer depth. Overflow drops the
// frame rather than blocking the control-stream loop; M2.2 will mark the
// sink dirty and reseed it instead of silently dropping.
const outputSinkBuf = 4096

// helloConn pairs an accepted renderer connection with the remote pane id it
// announced via FrameHello.
type helloConn struct {
	paneID string
	conn   net.Conn
}

// Run drives the whole mirror for one remote window until %exit/%window-close
// or the control connection drops.
func Run(cfg Config) error {
	reader := controlmode.NewReader(cfg.Ctl)

	var sendMu sync.Mutex
	closed := false
	cmds := bufio.NewWriter(cfg.Ctl)
	// send is called from this setup path and from every renderer's input
	// pump goroutine (step 6/8) — mutex-guarded so command lines never
	// interleave on the wire (mirrors M1 main.go's sendMu).
	send := func(s string) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return
		}
		fmt.Fprintf(cmds, "%s\n", s)
		cmds.Flush()
	}

	// Step 2: drain the implicit attach reply.
	readReply(reader)

	// Step 3: converge remote size to the local window's content size.
	w, h := cfg.WinSize()
	send(ConvergeCmd(w, h))
	readReply(reader)

	// Step 4: validate the target window exists.
	remoteIDs, err := listPanes(reader, send, cfg.RemoteWin)
	if err != nil {
		return err
	}
	if len(remoteIDs) == 0 {
		return fmt.Errorf("daemon: remote window %s has no panes", cfg.RemoteWin)
	}

	// Step 5: fetch the layout to plan the local mirror.
	L, err := readLayout(reader, send, cfg.RemoteWin)
	if err != nil {
		return err
	}

	router := NewRouter()

	// Step 6: listen for renderer connections.
	os.Remove(cfg.SockPath)
	listener, err := net.Listen("unix", cfg.SockPath)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", cfg.SockPath, err)
	}
	connCh := make(chan helloConn, 16)
	go acceptRenderers(listener, connCh)

	// conns tracks every currently-wired renderer conn by remote pane id, so
	// teardown and layout-change reconciliation can close/replace them.
	conns := map[string]net.Conn{}

	teardown := func() {
		listener.Close()
		for _, c := range conns {
			c.Close()
		}
		sendMu.Lock()
		closed = true
		sendMu.Unlock()
		cfg.Ctl.Close()
		if cfg.LocalSess != "" {
			cfg.LocalTmux("kill-session", "-t", cfg.LocalSess)
		}
	}

	// Step 7: apply the mirror shape to the local window.
	for _, c := range PlanWindow(cfg.LocalWin, L) {
		if err := cfg.LocalTmux(c...); err != nil {
			teardown()
			return fmt.Errorf("daemon: apply mirror: %w", err)
		}
	}

	remotePanes := RemotePaneOrder(L)

	// Step 8: spawn one renderer per local pane, targeted by position — the
	// local window has no other source of pane identity available through
	// Config (LocalTmux runs commands but doesn't capture output), and
	// PlanWindow's splits are already verified to create panes in
	// RemotePaneOrder position (see mirror.go).
	for i, remotePane := range remotePanes {
		if err := spawnRenderer(cfg, i, remotePane); err != nil {
			teardown()
			return fmt.Errorf("daemon: spawn renderer for %s: %w", remotePane, err)
		}
	}

	// Collect exactly len(remotePanes) Hellos (any order) before seeding —
	// step 9 seeds sequentially over the single control stream, so all
	// renderers must be connected (and hence writable) first.
	byRemote, err := collectHellos(connCh, len(remotePanes))
	if err != nil {
		teardown()
		return err
	}
	for id, c := range byRemote {
		conns[id] = c
	}

	// Step 9: seed each pane, then wire it into the router. Ordering is the
	// load-bearing bit: the seed frame is written synchronously here, and
	// only afterwards do we start the output-pump goroutine that becomes the
	// conn's other writer — so the two writers are never concurrent and no
	// per-conn mutex is needed (see cross-task delta #2).
	for _, remotePane := range remotePanes {
		conn := conns[remotePane]
		if conn == nil {
			continue // didn't connect; already logged by collectHellos caller
		}
		if !seedRenderer(reader, send, router, conn, remotePane) {
			if len(remotePanes) == 1 {
				teardown()
				return fmt.Errorf("daemon: seed failed for sole pane %s", remotePane)
			}
			delete(conns, remotePane)
			continue
		}
		go pumpInput(conn, remotePane, send)
	}

	// Steps 10-11: main loop + teardown.
	for {
		l, ok := reader.Next()
		if !ok {
			break
		}
		if l.Kind == controlmode.LayoutChange {
			remotePanes = reconcileLayout(cfg, reader, send, router, connCh, conns, remotePanes)
			continue
		}
		if handleLine(l, router) {
			break
		}
	}
	teardown()
	return nil
}

// handleLine processes one control-mode line for the mirror loop: routes
// %output to its registered renderer and reports whether the session ended
// (%window-close/%exit). It's the shared core between runLoop (below, for
// unit testing) and Run's real loop (which layers %layout-change on top,
// since reconciling a layout change needs the full Config/pane-tracking
// state that a fake-stream test doesn't have).
func handleLine(l controlmode.Line, router *Router) (stop bool) {
	switch l.Kind {
	case controlmode.Output:
		router.Route(l.Pane, l.Data)
	case controlmode.WindowClose, controlmode.Exit:
		return true
	}
	return false
}

// runLoop is the routing/teardown core of Run's main loop, extracted so it's
// unit-testable without ssh/tmux. Returns true if the stream ended via
// %window-close/%exit, false if the control connection just dropped (EOF).
func runLoop(reader *controlmode.Reader, router *Router) bool {
	for {
		l, ok := reader.Next()
		if !ok {
			return false
		}
		if handleLine(l, router) {
			return true
		}
	}
}

// listPanes issues list-panes against target and returns the pane ids, only
// to validate the window exists (M2.1 derives actual pane identity from the
// layout string, not this list — see readLayout/RemotePaneOrder).
func listPanes(reader *controlmode.Reader, send func(string), target string) ([]string, error) {
	send(fmt.Sprintf("list-panes -t %s -F '#{pane_id}'", target))
	l, ok := readReply(reader)
	if !ok {
		return nil, fmt.Errorf("daemon: control connection closed listing panes for %s", target)
	}
	if l.Kind == controlmode.Error {
		return nil, fmt.Errorf("daemon: list-panes -t %s: %s", target, l.Data)
	}
	var ids []string
	for _, row := range strings.Split(string(l.Data), "\n") {
		row = strings.TrimSpace(row)
		if row != "" {
			ids = append(ids, row)
		}
	}
	return ids, nil
}

func readLayout(reader *controlmode.Reader, send func(string), target string) (controlmode.Layout, error) {
	send(fmt.Sprintf("display-message -p -t %s -F '#{window_layout}'", target))
	l, ok := readReply(reader)
	if !ok {
		return controlmode.Layout{}, fmt.Errorf("daemon: control connection closed reading layout for %s", target)
	}
	if l.Kind == controlmode.Error {
		return controlmode.Layout{}, fmt.Errorf("daemon: display-message window_layout -t %s: %s", target, l.Data)
	}
	return controlmode.ParseLayout(strings.TrimSpace(string(l.Data)))
}

// spawnRenderer respawns the local pane at position index (targeted by
// window.index, since PlanWindow's local panes are created in RemotePaneOrder
// position) with the renderer binary, wired to dial back with remotePane's id.
func spawnRenderer(cfg Config, index int, remotePane string) error {
	target := fmt.Sprintf("%s.%d", cfg.LocalWin, index)
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
// the remote pane id each announced.
func collectHellos(connCh <-chan helloConn, n int) (map[string]net.Conn, error) {
	out := map[string]net.Conn{}
	for i := 0; i < n; i++ {
		hc, ok := <-connCh
		if !ok {
			return nil, fmt.Errorf("daemon: renderer socket closed after %d/%d connections", i, n)
		}
		out[hc.paneID] = hc.conn
	}
	return out, nil
}

// seedRenderer produces the initial screen for remotePane, writes it to conn
// as a FrameSeed, and (only on success) registers conn's output sink with
// router. Returns false — logging to stderr rather than crashing — if the
// pane closed between listing and seeding (PaneSeed now errors on an empty
// capture-pane reply, not just a nil one — see seed.go): the caller decides
// whether that's fatal (sole pane) or just leaves that pane unwired.
func seedRenderer(reader *controlmode.Reader, send func(string), router *Router, conn net.Conn, remotePane string) bool {
	seed, err := PaneSeed(reader, send, remotePane)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: seed %s: %v (skipping renderer)\n", remotePane, err)
		conn.Close()
		return false
	}
	if err := wire.WriteFrame(conn, wire.FrameSeed, seed); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: write seed to %s: %v (skipping renderer)\n", remotePane, err)
		conn.Close()
		return false
	}
	sink := newOutputSink(conn)
	router.Register(remotePane, sink)
	return true
}

// outputSink buffers FrameOutput frames for one renderer so a slow reader
// can't block Router.Route, which runs on the single main control-stream
// loop. A full buffer drops the frame; M2.2 will mark the sink dirty and
// reseed it instead.
type outputSink struct {
	ch chan []byte
}

func newOutputSink(conn net.Conn) *outputSink {
	s := &outputSink{ch: make(chan []byte, outputSinkBuf)}
	go func() {
		for p := range s.ch {
			if err := wire.WriteFrame(conn, wire.FrameOutput, p); err != nil {
				return
			}
		}
	}()
	return s
}

func (s *outputSink) Write(p []byte) (int, error) {
	select {
	case s.ch <- append([]byte(nil), p...):
	default:
		// Buffer full: drop. TODO(M2.2): mark dirty and reseed from
		// capture-pane instead of silently losing this chunk.
	}
	return len(p), nil
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

// reconcileLayout re-reads the remote window's layout and, if the pane set
// changed, applies the two minimal M2.1 cases: a pure tail-append (a remote
// split added panes) or a pure tail-removal (remote pane(s) closed). Any
// other change (reordering, mid-list insert/remove) is a full diff engine —
// deferred to M2.2 — so it's logged and left as-is for this cycle. Returns
// the pane-order slice callers should track from here on.
func reconcileLayout(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, conns map[string]net.Conn, oldRemote []string) []string {
	L, err := readLayout(reader, send, cfg.RemoteWin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
		return oldRemote
	}
	newRemote := RemotePaneOrder(L)

	switch {
	case reflect.DeepEqual(newRemote, oldRemote):
		// Geometry-only change: pane set is identical.
	case isPrefixOf(oldRemote, newRemote):
		for i := len(oldRemote); i < len(newRemote); i++ {
			if err := cfg.LocalTmux("split-window", "-h", "-t", cfg.LocalWin); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change split: %v\n", err)
				return oldRemote
			}
			if err := spawnRenderer(cfg, i, newRemote[i]); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change spawn renderer: %v\n", err)
				return oldRemote
			}
		}
		added, err := collectHellos(connCh, len(newRemote)-len(oldRemote))
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
			return oldRemote
		}
		for id, c := range added {
			conns[id] = c
			if seedRenderer(reader, send, router, c, id) {
				go pumpInput(c, id, send)
			} else {
				delete(conns, id)
			}
		}
	case isPrefixOf(newRemote, oldRemote):
		for i := len(oldRemote) - 1; i >= len(newRemote); i-- {
			removed := oldRemote[i]
			router.Unregister(removed)
			if c := conns[removed]; c != nil {
				c.Close()
				delete(conns, removed)
			}
			if err := cfg.LocalTmux("kill-pane", "-t", fmt.Sprintf("%s.%d", cfg.LocalWin, i)); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change kill-pane: %v\n", err)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "daemon: layout-change: unsupported pane reshuffle %v -> %v, skipping reconcile\n", oldRemote, newRemote)
		return oldRemote
	}

	if err := cfg.LocalTmux("select-layout", "-t", cfg.LocalWin, L.Raw); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change select-layout: %v\n", err)
	}
	return newRemote
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
