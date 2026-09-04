// Command daemon is the production entrypoint: it opens an ssh -CC
// control-mode connection to a remote tmux, mirrors every window of the
// bridged session into its own local window, and runs until the remote
// session exits or the connection drops. See picker/remotebridge/daemon for
// the orchestration.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/daemon"
	"github.com/noamsto/lazytmux/picker/remotebridge/graphics"
)

// How long the control connection may go unanswered before ssh gives up on it
// and exits, which is what surfaces a dead remote to this process (#471).
//
// The product is the detection window: 15s x 4 = 60s of total silence. Probes
// are sent only while the connection is idle and cost a round-trip each, so the
// interval is cheap; the count is what keeps a transient blip from tearing down
// a live mirror, since a mirror torn down is every window of it gone.
const (
	serverAliveInterval = 15
	serverAliveCountMax = 4
)

// sshControlArgs builds the argv for the control-mode ssh. Extracted so the
// options below are assertable — every one of them is load-bearing and none of
// them is visible in a passing test otherwise.
func sshControlArgs(ctlSock, host, tmpdir, term, session string, tmuxArgv []string) []string {
	args := []string{"-T", "-e", "none",
		// ControlMaster on the control connection makes every image fetch a
		// multiplexed exec on this same TCP connection: no second handshake,
		// and image bytes never share the control stream with live output.
		// ControlPersist=no ties the master's lifetime to this process.
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + ctlSock, "-o", "ControlPersist=no",
		// Keepalives are how this process finds out the remote is gone. A host
		// that dies abruptly — a reboot, a yanked cable — sends no FIN and no
		// RST, so the TCP connection stays open and ssh sits on it
		// indefinitely. Without these the control stream never reaches EOF, so
		// the daemon's teardown is never reached and the mirror goes on
		// presenting stale screens while discarding every keystroke (#471).
		// Observed on a rebooted host: hours in that state.
		"-o", "ServerAliveInterval=" + strconv.Itoa(serverAliveInterval),
		"-o", "ServerAliveCountMax=" + strconv.Itoa(serverAliveCountMax),
		host, "--", "env", "TMUX_TMPDIR=" + tmpdir}
	// TERM decides the remote viewer's graphics backend: it reads
	// #{client_termname}, which is whatever this control client advertises. A
	// non-kitty local terminal therefore degrades the remote carousel to block
	// art on its own. -T means no pty, so ssh won't send TERM itself — it has
	// to ride in this env prefix like TMUX_TMPDIR does.
	if term != "" {
		args = append(args, "TERM="+shellQuote(term))
	}
	args = append(args, tmuxArgv...)
	// ssh space-joins the post-host argv into one string run by the remote
	// login shell, so shell-quote the session name (may contain spaces) to keep
	// it a single target token.
	return append(args, "-C", "attach-session", "-t", shellQuote(session))
}

// remoteStoreScript is the paste upload's remote half (#361): it lands the
// image bytes (stdin) in a fresh 0700 mktemp -d directory and prints the path
// of a plain-named file inside it. The find sweep is the cleanup answer — the
// file must outlive prompt editing (the agent reads the path at SUBMIT,
// possibly minutes later), so delete-on-inject is wrong and a per-paste
// janitor needs no daemon state.
//
// The directory is per-invocation (mktemp -d's random suffix) rather than a
// fixed shared path: a fixed path can be pre-created by another local account
// ahead of the victim's first paste (any owner, any mode, or a symlink), which
// escalates from image substitution to an arbitrary-file overwrite as the
// victim's own uid. mktemp -d's create is atomic and unpredictable, so there
// is nothing to pre-create, and its 0700 mode (by construction, not umask)
// means no other local account can write into it at all. It also sidesteps
// the unverified BSD/macOS mktemp question of a suffix trailing the X's,
// since the extension no longer rides in the template.
//
// It runs under sh -c exactly like graphics' remoteFetch: ssh space-joins the
// post-host argv for the remote LOGIN shell (fish on the normal host), which
// never parses the script because shellQuote makes it one argv element.
// $1 is the extension, validated against pasteExtRe before it is ever
// interpolated.
const remoteStoreScript = `umask 077
d=$(mktemp -d /tmp/lazytmux-paste-XXXXXXXX) || exit 1
find /tmp -maxdepth 1 -name "lazytmux-paste-*" -type d -mmin +60 -exec rm -rf {} + 2>/dev/null
f="$d/img.$1"
cat > "$f" || { rm -rf "$d"; exit 1; }
printf "%s" "$f"`

// pasteExtRe gates the extension before it reaches the remote mktemp
// template. bmp is absent on purpose: the agent's path-inlining regex
// excludes it, so shipping one would plant a path nothing reads.
var pasteExtRe = regexp.MustCompile(`^(png|jpe?g|gif|webp)$`)

// pasteUploadArgs builds the ssh argv for one image upload, riding the
// control connection's ControlMaster exactly like the graphics fetcher: no
// second handshake, and image bytes never share the control stream with live
// terminal output. Extracted so the argv is assertable.
func pasteUploadArgs(sshCmd, ctlSock, host, ext string) []string {
	return []string{sshCmd, "-S", ctlSock, "-T", host, "--",
		"sh", "-c", shellQuote(remoteStoreScript), "_", ext}
}

func main() {
	// Flags default to LZTMUX_BRIDGE_*/LZTMUX_DAEMON_* env vars, mirroring
	// M1's remotebridge/main.go: the launcher passes untrusted, remote-derived
	// values through tmux's environment rather than interpolating them into a
	// /bin/sh command string.
	host := flag.String("host", os.Getenv("LZTMUX_BRIDGE_HOST"), "ssh host")
	session := flag.String("session", os.Getenv("LZTMUX_BRIDGE_SESSION"), "remote session")
	window := flag.Int("window", envInt("LZTMUX_BRIDGE_WINDOW"), "initially-selected remote window index (all windows are mirrored)")
	remoteTmux := flag.String("tmux", envDefault("LZTMUX_BRIDGE_TMUX", "tmux"), "absolute remote tmux path")
	tmpdir := flag.String("tmpdir", os.Getenv("LZTMUX_BRIDGE_TMPDIR"), "remote TMUX_TMPDIR")
	sshCmd := flag.String("ssh", envDefault("LZTMUX_BRIDGE_SSH", "ssh"), "control transport command (empty = run tmux locally)")
	term := flag.String("term", os.Getenv("LZTMUX_BRIDGE_TERM"), "termname to advertise to the remote (steers the remote viewer's graphics backend)")
	cacheDir := flag.String("gfx-cache", envDefault("LZTMUX_BRIDGE_GFX_CACHE", filepath.Join(os.TempDir(), "lztmux-gfx")), "local cache dir for images fetched from the remote")
	gfxMax := flag.Int64("gfx-max-bytes", 8<<20, "largest single image fetched from the remote; bigger stores are dropped")
	localTmux := flag.String("local-tmux", envDefault("LZTMUX_DAEMON_LOCAL_TMUX", "tmux"), "local tmux binary (may carry args, e.g. \"tmux -L sock\")")
	localSess := flag.String("local-sess", os.Getenv("LZTMUX_DAEMON_LOCAL_SESS"), `local session name (default "<host>-<session>")`)
	sock := flag.String("sock", os.Getenv("LZTMUX_DAEMON_SOCK"), "unix socket path for renderers")
	rendererBin := flag.String("renderer", os.Getenv("LZTMUX_DAEMON_RENDERER"), "absolute path to the renderer binary")
	reflowBin := flag.String("reflow", os.Getenv("LZTMUX_DAEMON_REFLOW"), "absolute path to tmux-reflow-windows (empty = never force a reflow)")
	remoteOpenBin := flag.String("remote-open", os.Getenv("LZTMUX_DAEMON_REMOTE_OPEN"), "absolute path to lztmux-remote-open (empty = a remote switch-client is pinned back but never handed off)")
	pauseAfter := flag.Int("pause-after", envIntDefault("LZTMUX_DAEMON_PAUSE_AFTER", 1), "seconds of client-read stall before tmux pauses a pane's %output (0 disables); the daemon answers %pause with a %continue re-seed")
	// --test-local is Task 9's offline seam: instead of ssh, both "remote" and
	// "local" are separate local tmux servers on their own -L sockets, so the
	// bats integration test never touches the network. --session/--window
	// still name the "remote" target (a real session:window on --src-socket);
	// only the transport differs.
	testLocal := flag.Bool("test-local", false, "test only: mirror --session:--window from a local tmux -L --src-socket instead of ssh")
	srcSocket := flag.String("src-socket", "", "test-local: tmux -L socket name standing in for the remote server")
	dstSocket := flag.String("dst-socket", "", "test-local: tmux -L socket name standing in for the local server")
	flag.Parse()

	if *localSess == "" {
		*localSess = fmt.Sprintf("%s-%s", *host, *session)
	}
	if *sock == "" {
		*sock = fmt.Sprintf("%s/lztmux-daemon-%d.sock", os.TempDir(), os.Getpid())
	}

	// newCtlCmd is the transport as a *recipe* the daemon can re-run on every
	// reconnect, rather than one already-built command. Every branch gets one,
	// the offline --test-local seam included — that is the seam the reconnect
	// integration tests drive.
	var newCtlCmd func() *exec.Cmd
	var ctlSock string
	var localTmuxArgv []string
	if *testLocal {
		newCtlCmd = func() *exec.Cmd {
			return exec.Command("tmux", "-L", *srcSocket, "-C", "attach-session", "-t", *session)
		}
		localTmuxArgv = []string{"tmux", "-L", *dstSocket}
	} else {
		// remoteTmux/localTmux may carry args (e.g. "tmux -L sock" for
		// tests), so split into argv rather than passing as a single token.
		tmuxArgv := strings.Fields(*remoteTmux)
		if *sshCmd == "" {
			newCtlCmd = func() *exec.Cmd {
				return exec.Command(tmuxArgv[0], append(append([]string{}, tmuxArgv[1:]...),
					"-C", "attach-session", "-t", *session)...)
			}
		} else {
			// Derived from the pid, so it is stable across re-dials — the
			// NewGraphics closure below captures it, and a per-dial path would
			// leave the image fetcher pointing at a dead socket after the first
			// reconnect.
			ctlSock = fmt.Sprintf("%s/lztmux-bridge-%d.sock", os.TempDir(), os.Getpid())
			newCtlCmd = func() *exec.Cmd {
				return exec.Command(*sshCmd, sshControlArgs(ctlSock, *host, *tmpdir, *term, *session, tmuxArgv)...)
			}
		}
		localTmuxArgv = strings.Fields(*localTmux)
	}

	// ControlPersist=no ties the ControlMaster socket's lifetime to this
	// process, so nothing but this process will ever unlink it — ssh cleans up
	// its own ControlPath only when it exits normally or catches a signal, and
	// a SIGKILL (the fallback below) it cannot catch.
	// Called explicitly at every exit path rather than deferred: fatal() calls
	// os.Exit(1), which skips deferred functions.
	cleanup := func() {
		if ctlSock != "" {
			os.Remove(ctlSock)
		}
	}

	tr := &transport{}
	dial := func() (io.ReadWriteCloser, error) {
		// ControlPersist=no ties the master's lifetime to the ssh process, but a
		// process killed without catching a signal (the SIGKILL fallback below,
		// a crash) leaves its ControlPath socket behind — and ControlMaster=auto
		// meeting a socket it cannot connect to *disables multiplexing* for that
		// instance rather than replacing it, silently. The reconnect would look
		// healthy while every image fetch stopped being multiplexed. The path
		// itself must not move, so unlink it and dial.
		if ctlSock != "" {
			os.Remove(ctlSock)
		}
		c, err := newChild(newCtlCmd())
		if err != nil {
			return nil, err
		}
		if err := tr.start(c); err != nil {
			return nil, err
		}
		return c, nil
	}

	// On SIGTERM/SIGINT, ask the control transport to exit so daemon.Run's
	// reader hits EOF and its teardown runs (removes the socket + pidfile,
	// kills the local mirror session). See child.Close for how it is ended.
	//
	// stop is closed BEFORE the transport is touched, so the daemon reads the
	// EOF that follows as a detach rather than as a link failure to retry.
	// With reconnect, a transport this handler kills EOFs exactly like a
	// dropped one, so stop is the only thing that tells the daemon "the user
	// asked to detach" from "the link died" — and during a backoff sleep there
	// is no transport for the signal to reach at all.
	stop := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		close(stop)
		tr.stop()
	}()

	runLocalTmux := func(args ...string) error {
		cmd := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...), args...)...)
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	runLocalTmuxOut := func(args ...string) (string, error) {
		cmd := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...), args...)...)
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		return string(out), err
	}
	area := func() (int, int) { return localArea(localTmuxArgv, *localSess) }
	// -b so the reflow's own tmux round-trips stay off the command queue the
	// daemon is about to use again.
	reflow := func() {
		if *reflowBin == "" {
			return
		}
		runLocalTmux(reflowRunShellArgs(*reflowBin, *localSess)...)
	}
	panes := func() map[string]string { return localPaneMap(localTmuxArgv, *localSess) }
	// A remote switch-client that moved this client is pinned back; handOff then
	// opens the session it named as a mirror of its own, so `sesh connect` from a
	// bridged shell lands somewhere instead of no-opping. The launcher switches
	// the local client to the new mirror itself.
	var handOff func(string)
	if *remoteOpenBin != "" {
		handOff = func(sess string) {
			cmd := exec.Command(*remoteOpenBin, *host, sess)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: hand off %s:%s: %v\n", *host, sess, err)
			}
		}
	}

	// The paste upload rides the same ControlMaster as the graphics fetcher
	// and is disabled for the same reason when there is no ssh transport.
	// The closure captures ctlSock, which is derived from the pid and stable
	// across re-dials (see the dial closure's comment).
	var pasteUpload func(ctx context.Context, ext string, data []byte) (string, error)
	if ctlSock != "" {
		pasteUpload = func(ctx context.Context, ext string, data []byte) (string, error) {
			if !pasteExtRe.MatchString(ext) {
				return "", fmt.Errorf("paste: bad extension %q", ext)
			}
			args := pasteUploadArgs(*sshCmd, ctlSock, *host, ext)
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Stdin = bytes.NewReader(data)
			out, err := cmd.Output()
			if err != nil {
				return "", fmt.Errorf("paste store: %w", err)
			}
			return strings.TrimSpace(string(out)), nil
		}
	}

	cfg := daemon.Config{
		Dial:           dial,
		Shutdown:       stop,
		SockPath:       *sock,
		LocalSess:      *localSess,
		RemoteHost:     *host,
		RemoteSession:  *session,
		RemoteWindow:   strconv.Itoa(*window),
		PauseAfterSecs: *pauseAfter,
		RendererBin:    *rendererBin,
		LocalTmux:      runLocalTmux,
		LocalTmuxOut:   runLocalTmuxOut,
		LocalArea:      area,
		Reflow:         reflow,
		LocalPanes:     panes,
		HandOff:        handOff,
		PasteUpload:    pasteUpload,
		NewGraphics: func(string) *graphics.Proxy {
			if ctlSock == "" {
				return nil // --test-local / local-tmux transport: no remote filesystem
			}
			return graphics.New(
				graphics.NewSSHFetcher(*host, ctlSock, *cacheDir, *gfxMax),
				func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) },
			)
		},
	}

	err := daemon.Run(cfg)
	cleanup()
	if err != nil {
		fatal(err)
	}
}

// localArea reports the content area the local mirror session can display,
// used as the cap asserted on every mirrored remote window.
// Defaults to 80x24 if neither query yields a size.
func localArea(localTmuxArgv []string, localSess string) (int, int) {
	if w, h := clientArea(localTmuxArgv, localSess); w > 0 && h > 0 {
		return w, h
	}
	// No attached client: the launcher creates the mirror session before
	// switching a client to it, and it can be left detached afterwards. Fall
	// back to the session's own window dims, which is what tmux hands a client
	// when one does attach.
	if w, h := sessionWinSize(localTmuxArgv, localSess); w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

// localPaneMap reads the mirror session's panes back as remote pane id -> local
// pane id. The daemon stamps @bridge_pane when it spawns each renderer, so tmux
// itself holds the mapping the agent-status shipper needs to re-key the remote's
// state onto local pane ids.
func localPaneMap(localTmuxArgv []string, localSess string) map[string]string {
	out, err := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...),
		"list-panes", "-s", "-t", localSess, "-F", "#{@bridge_pane} #{pane_id}")...).Output()
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		m[fields[0]] = fields[1]
	}
	return m
}

// clientArea is the smallest attached client's content area: its size minus
// its status lines. It reads the clients rather than #{window_width} because
// the daemon pins each mirror window to its remote's size (window-size
// manual), so a mirror window's own dims no longer track the terminal it is
// shown in. Returns 0,0 when no client is attached.
func clientArea(localTmuxArgv []string, localSess string) (int, int) {
	out, err := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...),
		"list-clients", "-t", localSess, "-F", "#{client_width} #{client_height} #{status}")...).Output()
	if err != nil {
		return 0, 0
	}
	w, h := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		cw, errW := strconv.Atoi(fields[0])
		ch, errH := strconv.Atoi(fields[1])
		if errW != nil || errH != nil {
			continue
		}
		ch -= statusLines(fields[2])
		if cw < 1 || ch < 1 {
			continue
		}
		if w == 0 || cw < w {
			w = cw
		}
		if h == 0 || ch < h {
			h = ch
		}
	}
	return w, h
}

// sessionWinSize is the detached fallback: the local session's active-window
// content dims. Returns 0,0 if the session doesn't exist yet or the query
// fails.
func sessionWinSize(localTmuxArgv []string, localSess string) (int, int) {
	out, err := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...),
		"display-message", "-p", "-t", localSess, "-F", "#{window_width} #{window_height}")...).Output()
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0
	}
	w, errW := strconv.Atoi(fields[0])
	h, errH := strconv.Atoi(fields[1])
	if errW != nil || errH != nil {
		return 0, 0
	}
	return w, h
}

// statusLines converts tmux's `status` option value — off, on, or 2-5 — into
// the number of rows the status bar takes off a client's height.
func statusLines(v string) int {
	switch v {
	case "off":
		return 0
	case "on":
		return 1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 1
	}
	return n
}

// transport holds the control-mode child the daemon is currently talking to.
// The signal handler and the dialer race over it otherwise: the handler is
// started once and closes over whatever is current, while the dialer replaces
// the process on every reconnect.
type transport struct {
	mu       sync.Mutex
	ch       *child
	stopping bool
}

// start runs c and publishes it as the current transport, atomically against
// stop. A signal landing between the two would otherwise reach the transport
// this one replaces — already dead and reaped, so signalling it is a no-op —
// and the ssh child just started would outlive the daemon, holding the mirror's
// ControlMaster open. Once stop has run, a start that still wins the lock ends
// its own child rather than publishing a survivor.
func (t *transport) start(c *child) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := c.cmd.Start(); err != nil {
		// No pipes to release: Start closes the parent's ends of the ones it
		// opened when it fails. And c.Close is not the tool if that ever
		// changes — it schedules end(), which signals c.cmd.Process, nil until
		// Start succeeds.
		return err
	}
	t.ch = c
	if t.stopping {
		c.Close()
	}
	return nil
}

// stop ends the current transport and bars any later one from surviving. Only
// the signal handler calls it: stopping means "the user asked to detach", which
// a child ended because it was superseded or timed out is not — reading one as
// the other would stop the daemon on a reconnect.
func (t *transport) stop() {
	t.mu.Lock()
	t.stopping = true
	c := t.ch
	t.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// transportKillGrace is how long a control transport gets to exit on its own
// after SIGTERM before it is killed.
const transportKillGrace = 2 * time.Second

// child is one control-transport process as the io.ReadWriteCloser
// daemon.Config wants: read the control stream off its stdout, write commands
// to its stdin.
type child struct {
	cmd  *exec.Cmd
	out  io.ReadCloser
	in   io.WriteCloser
	once sync.Once
	// ended is closed once the process has been signalled and reaped.
	ended chan struct{}
}

func newChild(cmd *exec.Cmd) (*child, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	return &child{cmd: cmd, out: out, in: in, ended: make(chan struct{})}, nil
}

func (c *child) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *child) Write(p []byte) (int, error) { return c.in.Write(p) }

// Close ends this transport for good, and is the whole of what makes closing a
// connection mean anything. Closing stdin alone does not end one: ssh keeps
// reading until the REMOTE closes its side, so a far end that accepts the
// connection and then goes silent leaves the control reader parked forever —
// which is exactly the case the identity deadline exists for. Closing our own
// read end is what unparks the reader, and it does so whether or not the
// process cooperates.
//
// Never blocks: the grace window and the reap run on their own goroutine, so
// the deadline timer, a drop and teardown can each call this and get on with
// it. Idempotent, because the drop path and teardown both do.
func (c *child) Close() error {
	c.once.Do(func() {
		c.in.Close()
		c.out.Close()
		go c.end()
	})
	return nil
}

// end signals the process and reaps it. SIGTERM first: ssh can catch it and
// unlink its own ControlPath, which main's cleanup only guarantees as a
// backstop. Kill after the grace window covers a wedged ssh — on an already
// exited process it reports ErrProcessDone rather than reaching a pid the
// kernel has since recycled, since os.Process holds a handle, which is also why
// the ordinary EOF case costs nothing here. Wait is what keeps a daemon that
// reconnects from leaving a zombie per attempt; it cannot outlast the grace
// window, since the kill is already scheduled when it starts.
func (c *child) end() {
	defer close(c.ended)
	kill := time.AfterFunc(transportKillGrace, func() { c.cmd.Process.Kill() })
	defer kill.Stop()
	c.cmd.Process.Signal(syscall.SIGTERM)
	c.cmd.Wait()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "lztmux-remote-daemon: %v\n", err)
	os.Exit(1)
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}

func envIntDefault(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// shellQuote single-quotes s for a POSIX shell, escaping embedded single
// quotes. Used for the session name in the ssh remote-command argv.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// reflowRunShellArgs builds the run-shell argv that forces a reflow.
// run-shell format-expands its whole shell-command string before /bin/sh
// ever sees it, so a value embedded directly in that string is exposed to
// the tmux format layer — "#(...)" is a job introducer there, not just a
// POSIX shell metacharacter — and shellQuote's escaping (the shell layer)
// does nothing to stop that (#368). Passing reflowBin/localSess as trailing
// run-shell arguments and referencing them only via #{1}/#{2} keeps them out
// of the string tmux format-expands: argv values are substituted in
// literally, after format expansion has already run over the command
// template, so a "#(" embedded in either value can never be read back as a
// job introducer. shellQuote still runs first — argv closes the format-layer
// hole, it does not exempt the value from needing to be a single shell token.
func reflowRunShellArgs(reflowBin, localSess string) []string {
	return []string{"run-shell", "-b", "#{1} --force #{2}", shellQuote(reflowBin), shellQuote(localSess)}
}
