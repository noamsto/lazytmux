package daemon

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// A local structural keybind inside a @bridge_win window does not act on the
// local mirror — it asks the daemon to run the equivalent command on the remote,
// and the mirror then repaints from the remote's own state. That request arrives
// as a FrameCtl on the same unix socket the renderers already use.
//
// The daemon cannot run the command and then wait for the remote's notification:
// tmux emits a notification caused by a control client's own command INSIDE that
// command's %begin/%end block (cmd-queue.c derives the block's flags from
// CMDQ_STATE_CONTROL), and controlmode.Reader folds a block's body into the
// reply's Data — so our own %layout-change never surfaces as a Line. Instead each
// request registers a reconcile intent that the main loop drains; the reply block
// for our own command is itself the line that wakes the loop back to the drain
// point, so no timer is needed.

// ctlState is everything ctl connections share with the main loop. It has its own
// mutex because mirrorWindow's fields are written by the main loop unlocked
// (registry.mu guards only the map), so a ctl goroutine must never read them —
// the main loop mirrors the pane->window mapping in here instead.
//
// Lock order is ctlState.mu -> sendMu, never the reverse: a ctl handler holds mu
// while it sends so the send and the intent registration are one critical
// section, and send never takes mu.
type ctlState struct {
	mu sync.Mutex

	// paneToWin maps a remote pane id to the remote window id holding it — how a
	// request carrying only @bridge_pane resolves to a window.
	paneToWin map[string]string
	// focus is the per-window echo-suppression state (focus.go).
	focus map[string]*focusState

	// Intents coalesce, so a burst of gestures in one window is one reconcile.
	wantWindows bool
	wantLayout  map[string]bool
}

func newCtlState() *ctlState {
	return &ctlState{
		paneToWin:  map[string]string{},
		focus:      map[string]*focusState{},
		wantLayout: map[string]bool{},
	}
}

// setWindowPanes records a window's remote pane order. The main loop calls it
// wherever it changes mirrorWindow.remotePanes, so the ctl side always has a
// snapshot it may read without touching mirrorWindow.
func (c *ctlState) setWindowPanes(remoteWin string, panes []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, w := range c.paneToWin {
		if w == remoteWin {
			delete(c.paneToWin, p)
		}
	}
	for _, p := range panes {
		c.paneToWin[p] = remoteWin
	}
	if _, ok := c.focus[remoteWin]; !ok {
		c.focus[remoteWin] = &focusState{}
	}
}

// forgetWindow drops a closed window's pane mapping, focus state and pending
// intent, so a stale commanded entry cannot outlive its window.
func (c *ctlState) forgetWindow(remoteWin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, w := range c.paneToWin {
		if w == remoteWin {
			delete(c.paneToWin, p)
		}
	}
	delete(c.focus, remoteWin)
	delete(c.wantLayout, remoteWin)
}

// takeIntents removes and returns the pending reconcile work.
func (c *ctlState) takeIntents() (windows bool, layouts []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	windows = c.wantWindows
	c.wantWindows = false
	for w := range c.wantLayout {
		layouts = append(layouts, w)
		delete(c.wantLayout, w)
	}
	return windows, layouts
}

// ctlRequest is a decoded, validated FrameCtl: the remote commands to run and
// the reconcile they imply. Producing one mutates nothing and sends nothing.
type ctlRequest struct {
	cmds        []string // control-mode command lines, in order
	wantWindows bool
	wantLayout  string // remote window id, or "" for none
	// invalidate names the window whose remote-active-pane belief this verb
	// makes unknowable (the new pane does not exist at command time).
	invalidate string
	// A focus request is filtered by the echo guards rather than run outright.
	focusPane string
	focusSeq  int64
}

// verb is one entry of the fixed translation table. The daemon builds every
// remote command from this table and never forwards raw command text, even
// though the socket is 0600 same-user.
type verb struct {
	args    int // trailing arguments after the pane id
	windows bool
	layout  bool
	// moves is true when the verb implicitly changes the remote's current pane.
	moves bool
	build func(pane, win, sess string, args []string) ([]string, error)
}

// Whitelisted direction flags, so a request cannot smuggle an arbitrary option
// into a remote command line.
var (
	resizeDirs = map[string]string{"U": "-U", "D": "-D", "L": "-L", "R": "-R"}
	swapDirs   = map[string]string{"U": "-U", "D": "-D"}
)

var verbs = map[string]verb{
	// Splits carry the pane's cwd, matching the local bindings they replace.
	"split-h": {layout: true, moves: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf("split-window -h -t %s -c '#{pane_current_path}'", pane)}, nil
	}},
	"split-v": {layout: true, moves: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf("split-window -v -t %s -c '#{pane_current_path}'", pane)}, nil
	}},
	// Killing a pane can empty its window, so it needs the window reconcile too.
	"kill-pane": {layout: true, windows: true, moves: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf("kill-pane -t %s", pane)}, nil
	}},
	"resize": {args: 2, layout: true, build: func(pane, _, _ string, a []string) ([]string, error) {
		dir, ok := resizeDirs[a[0]]
		if !ok {
			return nil, fmt.Errorf("resize: bad direction %q", a[0])
		}
		n, err := strconv.Atoi(a[1])
		if err != nil || n < 1 || n > 999 {
			return nil, fmt.Errorf("resize: bad amount %q", a[1])
		}
		return []string{fmt.Sprintf("resize-pane -t %s %s %d", pane, dir, n)}, nil
	}},
	// No -d: without it the active pane rides with the swap, so the remote keeps
	// the same pane focused (verified). The local reconcile swaps WITH -d, and
	// that pairing is what keeps both sides focused on the same remote pane.
	"swap": {args: 1, layout: true, build: func(pane, _, _ string, a []string) ([]string, error) {
		dir, ok := swapDirs[a[0]]
		if !ok {
			return nil, fmt.Errorf("swap: bad direction %q", a[0])
		}
		return []string{fmt.Sprintf("swap-pane -t %s %s", pane, dir)}, nil
	}},
	// -t '<sess>:' is a session target with the index unspecified: tmux picks the
	// lowest free index and makes the new window active (verified), matching the
	// local new-window this replaces. Never a bare session name in a
	// target-window slot (CLAUDE.md, "Session Targeting Gotcha").
	"new-window": {windows: true, moves: true, build: func(_, _, sess string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf("new-window -t %s: -c '#{pane_current_path}'", tmuxQuote(sess))}, nil
	}},
	"kill-window": {windows: true, moves: true, build: func(_, win, _ string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf("kill-window -t %s", win)}, nil
	}},
	// The new name is re-read from the remote by the window reconcile rather than
	// written locally first: a remote rename can fail, and writing
	// @window_bridge_name optimistically is the local-first mutation the mirror
	// invariant forbids.
	"rename": {args: 1, windows: true, build: func(_, win, _ string, a []string) ([]string, error) {
		name := sanitizeWindowName(a[0])
		if name == "" {
			return nil, fmt.Errorf("rename: empty name")
		}
		return []string{fmt.Sprintf("rename-window -t %s -- %s", win, tmuxQuote(name))}, nil
	}},
}

// parseCtl validates a FrameCtl argv and resolves it against the current
// pane->window mapping. argv is [version, verb, pane, args...]; the pane rides
// on every verb because @bridge_pane is the only id a keybind has, and the
// window is derived from it.
func (c *ctlState) parseCtl(argv []string, sess string) (ctlRequest, error) {
	if len(argv) < 3 {
		return ctlRequest{}, fmt.Errorf("want at least version, verb and pane, got %d fields", len(argv))
	}
	if argv[0] != wire.CtlProtocolVersion {
		return ctlRequest{}, fmt.Errorf("ctl protocol version %q, this daemon speaks %q — reopen the bridge", argv[0], wire.CtlProtocolVersion)
	}
	name, pane, args := argv[1], argv[2], argv[3:]

	c.mu.Lock()
	win, known := c.paneToWin[pane]
	c.mu.Unlock()
	if !known {
		return ctlRequest{}, fmt.Errorf("%s: pane %s is not mirrored by this bridge", name, pane)
	}

	if name == "focus" {
		if len(args) != 1 {
			return ctlRequest{}, fmt.Errorf("focus wants one sequence argument")
		}
		seq, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return ctlRequest{}, fmt.Errorf("focus: bad sequence %q", args[0])
		}
		return ctlRequest{focusPane: pane, focusSeq: seq, invalidate: win}, nil
	}

	v, ok := verbs[name]
	if !ok {
		return ctlRequest{}, fmt.Errorf("unknown verb %q", name)
	}
	if len(args) != v.args {
		return ctlRequest{}, fmt.Errorf("%s wants %d argument(s), got %d", name, v.args, len(args))
	}
	cmds, err := v.build(pane, win, sess, args)
	if err != nil {
		return ctlRequest{}, err
	}
	req := ctlRequest{cmds: cmds, wantWindows: v.windows}
	if v.layout {
		req.wantLayout = win
	}
	if v.moves {
		req.invalidate = win
	}
	return req, nil
}

// submit runs a parsed request: it registers the reconcile intent and writes the
// remote command inside one critical section, so any drain that happens after the
// command was sent necessarily happens after the intent was registered.
//
// It reports whether the command was actually written. send is a silent no-op
// once the daemon is tearing down, and a request that loses that race must not be
// acked as accepted — the keybind would report success for a gesture that never
// happened.
func (c *ctlState) submit(req ctlRequest, send func(string) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if req.focusPane != "" {
		cmd, ok := c.planFocusLocked(req.invalidate, req.focusPane, req.focusSeq)
		if !ok {
			return true // suppressed by the echo guards; nothing to send is success
		}
		return send(cmd)
	}

	if req.wantWindows {
		c.wantWindows = true
	}
	if req.wantLayout != "" {
		c.wantLayout[req.wantLayout] = true
	}
	// A verb that implicitly moves the remote's current pane leaves the belief
	// unknowable at command time (the new pane does not exist yet), so invalidate
	// rather than guess: the empty sentinel can never equal a real pane id, so it
	// never suppresses a later focus command. The reconcile the verb schedules
	// re-learns the truth from readLayout.
	if req.invalidate != "" {
		if f := c.focus[req.invalidate]; f != nil {
			f.remoteActivePane = ""
		}
	}
	for _, cmd := range req.cmds {
		if !send(cmd) {
			return false
		}
	}
	return true
}
