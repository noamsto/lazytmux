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
	// The closed set of tools a bind may launch on the remote: a name reaches a
	// remote shell only by being a key here, so the socket peer cannot smuggle
	// one in.
	remoteTools  = map[string]bool{"prdash": true, "lazygit": true, "tmux-gh-dash": true, "yazi": true}
	remoteThemes = map[string]bool{"dark": true, "light": true}
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
		// The prompt prefills from @window_bridge_name, so the value handed back is
		// already in that option's escaped dialect — re-encoding it without decoding
		// first is a double-encode. The re-encode is still kept: the remote's own
		// rename-window format-expands its argument, so a user-typed 'a#(x)' must
		// not reach it bare.
		name := sanitizeWindowName(decodeWindowName(a[0]))
		if name == "" {
			return nil, fmt.Errorf("rename: empty name")
		}
		return []string{fmt.Sprintf("rename-window -t %s -- %s", win, tmuxQuote(name))}, nil
	}},
	// The carousel toggle opens its own split on the remote, so this verb only
	// launches it. TMUX_PANE is injected explicitly because run-shell does not
	// export it (the local bind this replaces does the same via #{pane_id}), and
	// AEYE_BRIDGED tells the viewer its frames cross a network, not a disk.
	// Backgrounded (-b) so a slow launch never blocks the remote command queue;
	// the split arrives as a %layout-change like any other structural event.
	//
	// A missing binary surfaces as a short remote split rather than a
	// display-message: the only client attached to this remote is the daemon's
	// control client, which has no status line for a message to land on, while a
	// split mirrors back into the window the human is looking at.
	"carousel": {layout: true, moves: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
		// show-options (not display-message -F "#{@…}"): run-shell expands #{}
		// before /bin/sh runs. Case arms omit an '' empty alternative — that
		// becomes a dense '\'' stack after double tmuxQuote and is not portable.
		script := carouselResolveScript(pane)
		// run-shell uses tmux's configured default shell (fish on the normal
		// host), while this validation is POSIX shell syntax. Quote each layer so
		// the remote pane id remains data all the way through both parsers.
		cmd := fmt.Sprintf("run-shell -b -t %s %s", pane, tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
		return []string{cmd}, nil
	}},
	// The tool binds (prefix p/g/G/y) open a float locally, but a float created
	// on the remote is pruned out of the tiled tree and never mirrored
	// (controlmode.Layout.Floats has no consumer), so the remote leg is a split,
	// as carousel's is. The cwd has to be the remote tmux's own expansion: the
	// mirror pane's cwd is the daemon's, not the worktree on screen.
	//
	// A bare command name, never the local ${tool}/bin/tool store path, which
	// exists on this host only. A remote missing the tool degrades to a
	// short-lived message pane, as carousel does.
	"tool": {args: 1, layout: true, moves: true, build: func(pane, _, _ string, a []string) ([]string, error) {
		if !remoteTools[a[0]] {
			return nil, fmt.Errorf("tool: unknown tool %q", a[0])
		}
		script := toolResolveScript(a[0])
		cmd := fmt.Sprintf("split-window -t %s -c '#{pane_current_path}' %s",
			pane, tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
		return []string{cmd}, nil
	}},
	// A local toggle re-themes only the local half of a mirror: the pane content
	// is bytes the remote's programs coloured from the remote's own theme state.
	// This asks the remote for the same theme, so content drawn after the toggle
	// matches the frame around it. Already-running full-screen apps keep their
	// palette until they redraw, exactly as they do on a local toggle.
	//
	// run-shell, not a split: nothing should appear on screen. A remote without
	// theme-toggle (any headless host — it ships from the desktop profile) is
	// silent for the same reason, so this is the one remote-executed verb with
	// no visible failure mode.
	"theme": {args: 1, build: func(pane, _, _ string, a []string) ([]string, error) {
		if !remoteThemes[a[0]] {
			return nil, fmt.Errorf("theme: unknown theme %q", a[0])
		}
		script := themeApplyScript(a[0])
		cmd := fmt.Sprintf("run-shell -b -t %s %s", pane, tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
		return []string{cmd}, nil
	}},
}

// themeApplyScript is the POSIX body run under exec /bin/sh -c. Like the two
// scripts below it must contain zero single-quote characters so double tmuxQuote
// only wraps. theme is a remoteThemes key, so it needs no quoting of its own.
func themeApplyScript(theme string) string {
	return fmt.Sprintf("command -v theme-toggle >/dev/null 2>&1 && exec theme-toggle apply %s", theme)
}

// toolResolveScript is the POSIX body run under exec /bin/sh -c: split-window
// runs its command through the remote's default shell (fish on the normal host),
// and this is POSIX. Like carouselResolveScript it must contain zero single-quote
// characters so double tmuxQuote only wraps. tool is a remoteTools key, so it is
// [a-z-] and needs no quoting of its own.
func toolResolveScript(tool string) string {
	return fmt.Sprintf(
		"command -v %s >/dev/null 2>&1 && exec %s; "+
			"echo lazytmux: %s is not on PATH on this host; sleep 5",
		tool, tool, tool)
}

// carouselResolveScript is the POSIX body run under exec /bin/sh -c. It must
// contain zero single-quote characters so double tmuxQuote only wraps.
func carouselResolveScript(pane string) string {
	return fmt.Sprintf(
		"src=$(tmux show-options -pqv -t %s @claude_img_src); "+
			"case \"$src\" in %%[0-9]*) case \"${src#%%}\" in *[!0-9]*) src=%s;; esac;; *) src=%s;; esac; "+
			"command -v tmux-claude-images >/dev/null 2>&1 && "+
			"exec env TMUX_PANE=\"$src\" AEYE_BRIDGED=1 tmux-claude-images; "+
			`tmux split-window -t "$src" -l 3 "echo lazytmux: tmux-claude-images is not on PATH on this host; sleep 5"`,
		pane, pane, pane,
	)
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
	// This probe deliberately resolves before pane lookup: a new ctl can tell a
	// live v2 daemon from a pre-carousel v1 daemon without needing a mirrored
	// pane. The latter rejects its v2 version before reaching this branch.
	if name == "ping" {
		if len(args) != 0 {
			return ctlRequest{}, fmt.Errorf("ping wants no arguments")
		}
		return ctlRequest{}, nil
	}

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
