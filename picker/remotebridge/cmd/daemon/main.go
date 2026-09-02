// Command daemon is the production entrypoint: it opens an ssh -CC
// control-mode connection to a remote tmux, mirrors every window of the
// bridged session into its own local window, and runs until the remote
// session exits or the connection drops. See picker/remotebridge/daemon for
// the orchestration.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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

	var ctl *exec.Cmd
	var ctlSock string
	var localTmuxArgv []string
	if *testLocal {
		ctl = exec.Command("tmux", "-L", *srcSocket, "-C", "attach-session", "-t", *session)
		localTmuxArgv = []string{"tmux", "-L", *dstSocket}
	} else {
		// remoteTmux/localTmux may carry args (e.g. "tmux -L sock" for
		// tests), so split into argv rather than passing as a single token.
		tmuxArgv := strings.Fields(*remoteTmux)
		if *sshCmd == "" {
			ctl = exec.Command(tmuxArgv[0], append(append([]string{}, tmuxArgv[1:]...),
				"-C", "attach-session", "-t", *session)...)
		} else {
			ctlSock = fmt.Sprintf("%s/lztmux-bridge-%d.sock", os.TempDir(), os.Getpid())
			ctl = exec.Command(*sshCmd, sshControlArgs(ctlSock, *host, *tmpdir, *term, *session, tmuxArgv)...)
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

	stdin, err := ctl.StdinPipe()
	if err != nil {
		cleanup()
		fatal(err)
	}
	stdout, err := ctl.StdoutPipe()
	if err != nil {
		cleanup()
		fatal(err)
	}
	if err := ctl.Start(); err != nil {
		cleanup()
		fatal(err)
	}

	// On SIGTERM/SIGINT, ask the control transport to exit so daemon.Run's
	// reader hits EOF and its teardown runs (removes the socket + pidfile,
	// kills the local mirror session). SIGTERM first: ssh can catch it and
	// unlink its own ControlPath itself, which cleanup() above only guarantees
	// as a backstop. A bounded fallback to SIGKILL covers a wedged ssh —
	// Process.Kill() on an already-exited process is a harmless no-op error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		if ctl.Process == nil {
			return
		}
		ctl.Process.Signal(syscall.SIGTERM)
		time.Sleep(2 * time.Second)
		ctl.Process.Kill()
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

	cfg := daemon.Config{
		Ctl:            rwc{stdout, stdin},
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

	err = daemon.Run(cfg)
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

// rwc adapts an ssh/tmux subprocess's separate stdout/stdin pipes into the
// single io.ReadWriteCloser daemon.Config wants: Read from the control
// stream's stdout, Write+Close its stdin.
type rwc struct {
	io.Reader
	io.WriteCloser
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
