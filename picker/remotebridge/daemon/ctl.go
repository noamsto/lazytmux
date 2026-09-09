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
//
// No ctl handler may block while holding mu either, which a third edge would
// make it do: the main loop takes this same mutex during a control-client
// replacement (repair() -> reconcileWindows -> forgetWindow, daemon.go), so a
// handler waiting on main-loop progress under mu deadlocks. That is why
// #574's resolve/compare/raise runs before submit and outside mu, and why the
// press that discovers a stale viewing identity is nacked rather than queued.
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
	// needsView carries the verb's flag of the same name through to the
	// handler.
	needsView bool
	// probePane is the remote pane whose verdict stamp the handler must wait
	// on, or "" for a verb that reports nothing back (carouselprobe.go).
	probePane string
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
	// needsView marks a verb whose effect on the remote depends on WHICH
	// terminal this bridge's control client advertises, so it must not run
	// under a client that advertises a terminal the user is no longer looking
	// through (#574). A field here rather than a verb-name compare in
	// daemon.go: the translation table is otherwise the only thing that knows
	// what a verb means, and the handler asks the parsed request a typed
	// question instead.
	needsView bool
	// probe marks a verb whose remote command reports its outcome by stamping
	// carouselVerdictOpt rather than by changing anything the mirror can see,
	// so the daemon has to read that stamp back to say anything about it
	// (#593). A field here for needsView's reason: the table stays the only
	// thing that knows what a verb means.
	probe bool
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
	remoteTools  = map[string]bool{"prdash": true, "lazygit": true, "yazi": true}
	remoteThemes = map[string]bool{"dark": true, "light": true}
	// remoteToolFloat gives each remoteTools entry its float shape, matching
	// config/tmux.conf.nix's floatShort/floatFull mkFloat shapes byte for byte
	// so a bridged tool opens at the same geometry the purely-local bind uses.
	// Not itself an authorization list — remoteTools stays that; a tool present
	// there but absent here (should never happen, kept in sync by hand) falls
	// back to remoteFloatFull rather than build a flagless new-pane.
	remoteToolFloat = map[string]string{
		"prdash":  remoteFloatShort,
		"yazi":    remoteFloatShort,
		"lazygit": remoteFloatFull,
	}
)

// remoteFloatShort and remoteFloatFull are config/tmux.conf.nix's
// floatShort/floatFull mkFloat shapes, always carrying -A: the local bind's
// version-guarded flagsNoA fallback exists only for a tmux old enough to lack
// new-pane -A, and every remote this daemon opens a ctl connection to is on
// this revision.
const (
	remoteFloatShort = "-x 90% -y 85% -X 5% -Y 8% -B heavy -A"
	remoteFloatFull  = "-x 90% -y 90% -X 5% -Y 5% -B heavy -A"
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
	// Zoom has to happen on the remote, because that is where the pane whose
	// size must change lives. A local resize-pane -Z does grow the renderer
	// pane, and it sticks — but the remote pane keeps its size, so the remote
	// program goes on rendering at the old one and the rows the pane gained are
	// dead space (measured: dst 150x39 against src 150x19), until some
	// unrelated remote layout change reconciles the zoom away. The
	// mirror learns the new flag from the layout reconcile, so a zoom made by
	// any other client on the remote follows too (verified): tmux emits
	// %layout-change for a zoom, even though #{window_layout} itself is
	// unchanged by one.
	"zoom": {layout: true, moves: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf("resize-pane -Z -t %s", pane)}, nil
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
	// Nothing the remote can say reaches the user directly: the only client
	// attached to it is this daemon's control client, which has no status line
	// for a display-message to land on. So neither a missing binary nor an
	// empty manifest opens anything there — the script stamps a verdict and
	// carouselProbe reads it back into a local message (#593).
	"carousel": {layout: true, moves: true, needsView: true, probe: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
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
	// The tool binds (prefix p/g/G/y) open a float locally, and this is the
	// remote leg: it now opens a float on the remote too, at the same shape
	// (remoteToolFloat) the local bind uses, so the mirror can reconcile it
	// into a local float the same way it reconciles any other remote float.
	// The cwd still has to be the remote tmux's own expansion: the mirror
	// pane's cwd is the daemon's, not the worktree on screen.
	//
	// A bare command name, never the local ${tool}/bin/tool store path, which
	// exists on this host only. A remote missing the tool degrades to a
	// short-lived message pane — unlike carousel, which reports its own
	// failures as a local status message (#593): this verb is asked to open a
	// float either way, so the message rides the float the press already
	// implies rather than needing a verdict read back for it.
	//
	// Never stamps @float_geom on the remote pane: that option is read by the
	// remote's own tmux-float-refit, which would then fight the mirror for
	// authority over this float's geometry on the remote's next resize.
	"tool": {args: 1, layout: true, moves: true, build: func(pane, _, _ string, a []string) ([]string, error) {
		if !remoteTools[a[0]] {
			return nil, fmt.Errorf("tool: unknown tool %q", a[0])
		}
		flags, ok := remoteToolFloat[a[0]]
		if !ok {
			flags = remoteFloatFull
		}
		script := toolResolveScript(a[0])
		cmd := fmt.Sprintf("new-pane -t %s -c '#{pane_current_path}' %s %s",
			pane, flags, tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
		return []string{cmd}, nil
	}},
	// A mirror's pane content is bytes the remote's programs coloured from the
	// remote's own theme state, so a local toggle cannot reach it: this asks the
	// remote for the same theme.
	//
	// run-shell, not a split: nothing should appear on screen. A remote without
	// theme-toggle (any headless host — it ships from the desktop profile) is
	// silent per call — Run()'s one-shot themeToggleAvailable probe (daemon.go)
	// is what reports the absence, once per bridge connect rather than once per
	// toggle, since this fire-and-forget verb never learns its own exit status
	// (#545).
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

// themeProbeCmd is the display-message line Run() sends once per bridge
// connect to tell "no theme-toggle here" apart from "applied" (#545).
//
// Deliberately not a run-shell, unlike every other capability probe in this
// file: run-shell without -b shows its output ON the target pane (a view-mode
// overlay), and that pane is the one this daemon is about to mirror — the
// overlay wedges it, so %pause/%continue never fires again and live output
// stops forever. display-message -p -t sess "#(...)" runs the same POSIX body
// as a tmux job and returns its stdout in the command's own reply without
// touching any pane. Tradeoff: a #() job populates asynchronously, so the
// first read of a fresh job string can come back empty before it has run once
// — themeToggleAvailable (themeprobe.go) retries for exactly that.
func themeProbeCmd(sess string) string {
	script := "command -v theme-toggle >/dev/null 2>&1 && echo yes || echo no"
	job := "#(/bin/sh -c " + tmuxQuote(script) + ")"
	return fmt.Sprintf("display-message -p -t %s %s", tmuxQuote(sess), tmuxQuote(job))
}

// toolResolveScript is the POSIX body run under exec /bin/sh -c: split-window
// runs its command through the remote's default shell (fish on the normal host),
// and this is POSIX. Like carouselResolveScript it must contain zero single-quote
// characters so double tmuxQuote only wraps. tool is a remoteTools key, so it is
// [a-z-] and needs no quoting of its own.
//
// The PATH restore is what makes the bare name resolvable at all. tmux hands a
// new pane its global environ, which carries every store path lazytmux's wrapper
// prepended — but split-window spawns through default-shell, and fish on NixOS
// rebuilds PATH from the login profile instead of inheriting it (measured on the
// remote: 67 entries in, 10 out). A tool that reaches the remote tmux only
// through the wrapper is then invisible; prdash and tmux-gh-dash are exactly
// that, while lazygit and yazi survive only by also being in the user profile.
//
// Prepended, not replaced, and guarded on a non-empty PATH= line: an unset
// variable prints nothing and exits 1, and blindly assigning that leaves the
// pane with no PATH at all — not even the sleep below would resolve, so the
// fallback would flash shut instead of showing its message.
//
// The trim is ${p#*=}, never ${p#PATH=}: split-window does NOT format-expand its
// shell-command (measured, next-3.8 — #P and #{pane_id} arrive byte-identical),
// but run-shell DOES, and there #P expands to the pane index, corrupting that
// spelling to ${p1ATH=}. #* passes through both. Keep this body #*-only so it
// stays correct if it ever moves near a run-shell verb, or if a tmux bump
// extends expansion to split-window.
func toolResolveScript(tool string) string {
	return fmt.Sprintf(
		"p=$(tmux show-environment -g PATH 2>/dev/null); "+
			"case $p in PATH=?*) PATH=${p#*=}:$PATH; export PATH;; esac; "+
			"command -v %s >/dev/null 2>&1 && exec %s; "+
			"echo lazytmux: %s is not on PATH on this host; sleep 5",
		tool, tool, tool)
}

// carouselResolveScript is the POSIX body run under exec /bin/sh -c. It must
// contain zero single-quote characters so double tmuxQuote only wraps.
//
// An empty/absent manifest is checked here rather than left to
// tmux-claude-images' own `[[ ! -s $MANIFEST ]]` branch: that branch's
// `tmux display-message` lands on the control-mode client's nonexistent
// status line and evaporates — so this uses `tmux-claude-images --resolve`
// (prints MODE\tKEY\tMANIFEST, launches nothing) to test the manifest itself.
//
// Every outcome is reported by stamping carouselVerdictOpt on the ctl pane and
// nothing else: the daemon reads it back and shows the local one-line message
// the purely-local bind shows (#593). It used to open a 90%x90% float here
// instead, which mirrors home as a bordered pane that steals focus for five
// seconds to say one sentence.
//
// The stamp lands on the ctl pane, never on $src: $src is the pane whose
// IMAGES these are (@claude_img_src can point elsewhere), while the ctl pane
// is the only id the daemon knows to read back. Cleared at entry so a
// previous press cannot be read as this one's answer; the daemon unsets it
// again on read, which covers a press whose verdict arrived after the probe
// had given up.
//
// manifest=${res####*$tab}, not ${res##*$tab}: run-shell format-expands its
// whole command string before /bin/sh ever sees it, and a run of `#`
// characters is that format language's own escape for a literal `#` — two
// collapse to one (measured, next-3.8: `##` arrives as `#`). Writing the
// POSIX "strip longest match" operator ## therefore requires doubling every
// `#` in it, i.e. four, so the shell that actually runs still sees `##`.
func carouselResolveScript(pane string) string {
	return fmt.Sprintf(
		"%s; "+
			"src=$(tmux show-options -pqv -t %s @claude_img_src); "+
			"case \"$src\" in %%[0-9]*) case \"${src#%%}\" in *[!0-9]*) src=%s;; esac;; *) src=%s;; esac; "+
			// @carousel_bin repoints on a config reload; PATH is frozen at
			// server start, so a bare name would keep resolving to the old
			// generation's script after a nix switch (#554). The -x guard
			// drops a stamp whose store path was garbage-collected.
			"bin=$(tmux show-options -gqv @carousel_bin); "+
			"if [ -n \"$bin\" ] && [ ! -x \"$bin\" ]; then bin=; fi; "+
			"if [ -z \"$bin\" ]; then bin=$(command -v tmux-claude-images 2>/dev/null); fi; "+
			"if [ -z \"$bin\" ]; then %s; exit 0; fi; "+
			"tab=$(printf \"\\t\"); "+
			"res=$(env TMUX_PANE=\"$src\" \"$bin\" --resolve 2>/dev/null); "+
			"manifest=${res####*$tab}; "+
			"if [ -n \"$manifest\" ] && [ -s \"$manifest\" ]; then "+
			// Stamped before the exec, so a press that launched is an answer
			// too: the probe stops on it rather than burning its whole retry
			// budget on the common case.
			"%s; "+
			"exec env TMUX_PANE=\"$src\" AEYE_BRIDGED=1 \"$bin\"; "+
			"fi; "+
			"%s",
		"tmux "+carouselClearCmd(pane),
		pane, pane, pane,
		"tmux "+carouselStampCmd(pane, carouselVerdictNoBin),
		"tmux "+carouselStampCmd(pane, carouselVerdictOK),
		"tmux "+carouselStampCmd(pane, carouselVerdictNoImages),
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
	req := ctlRequest{cmds: cmds, wantWindows: v.windows, needsView: v.needsView}
	if v.probe {
		req.probePane = pane
	}
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

// pressAgainErr is the nack a gesture gets while the bridge re-dials for a new
// viewing identity (#574). User-facing: the ctl client shows a request failure
// with display-message -d 5000, so this is a short status-line string, not a
// log line — and deliberately not submit's generic "bridge has no live
// connection to the remote", which reads as "your bridge is broken" when the
// truth is "your viewer identity is being updated" (R6).
func pressAgainErr(term string) error {
	return fmt.Errorf("re-dialling for %s — press again", term)
}

// handleCtl is the whole body of Run's acceptConns callback, package-level so
// a test can drive the ordering below without a live Run: the loops that act
// on a raise are closures over Run's locals, and Run has exactly one caller.
//
// The resolve/compare/raise runs before submit, and therefore outside
// ctlState.mu, for the deadlock reason ctlState's lock-order note gives. Both
// raise verdicts that nack come back through one call site, so the discovering
// press and a press landing mid-flight cannot drift apart (R8), and the nack
// is returned when the replacement is RAISED, not when it completes: a message
// arriving after a multi-second dial reads as a timeout rather than an
// instruction.
func handleCtl(cst *ctlState, rep *viewReplacer, probe *carouselProbe, argv []string, sess string, send func(string) bool) error {
	req, err := cst.parseCtl(argv, sess)
	if err != nil {
		return err
	}
	if req.needsView {
		if v, term := rep.raise(); v != raiseNone {
			return pressAgainErr(term)
		}
	}
	if !cst.submit(req, send) {
		return fmt.Errorf("bridge has no live connection to the remote")
	}
	// After the submit, never before: a press whose command was not written
	// has no verdict coming, and arming for it would spend the whole retry
	// budget reading an option nothing is going to stamp.
	if req.probePane != "" {
		probe.arm(req.probePane)
	}
	return nil
}
