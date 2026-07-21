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
	"strconv"
	"strings"
	"syscall"

	"github.com/noamsto/lazytmux/picker/remotebridge/daemon"
)

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
	localTmux := flag.String("local-tmux", envDefault("LZTMUX_DAEMON_LOCAL_TMUX", "tmux"), "local tmux binary (may carry args, e.g. \"tmux -L sock\")")
	localSess := flag.String("local-sess", os.Getenv("LZTMUX_DAEMON_LOCAL_SESS"), `local session name (default "<host>-<session>")`)
	sock := flag.String("sock", os.Getenv("LZTMUX_DAEMON_SOCK"), "unix socket path for renderers")
	rendererBin := flag.String("renderer", os.Getenv("LZTMUX_DAEMON_RENDERER"), "absolute path to the renderer binary")
	baseIndex := flag.Int("base-index", envIntDefault("LZTMUX_DAEMON_BASE_INDEX", 1), "local tmux base-index for daemon-created windows")
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
			// ssh space-joins the post-host argv into one string run by the
			// remote login shell, so shell-quote the session name (may
			// contain spaces) to keep it a single target token.
			args := append([]string{"-T", "-e", "none", *host, "--", "env", "TMUX_TMPDIR=" + *tmpdir}, tmuxArgv...)
			args = append(args, "-C", "attach-session", "-t", shellQuote(*session))
			ctl = exec.Command(*sshCmd, args...)
		}
		localTmuxArgv = strings.Fields(*localTmux)
	}
	stdin, err := ctl.StdinPipe()
	if err != nil {
		fatal(err)
	}
	stdout, err := ctl.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	if err := ctl.Start(); err != nil {
		fatal(err)
	}

	// On SIGTERM/SIGINT, kill the control transport so daemon.Run's reader hits
	// EOF and its teardown runs (removes the socket + pidfile, kills the local
	// mirror session). Without this, a killed daemon leaves that state behind
	// and blocks the next launch.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		if ctl.Process != nil {
			ctl.Process.Kill()
		}
	}()

	runLocalTmux := func(args ...string) error {
		cmd := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...), args...)...)
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	winSize := func() (int, int) { return localWinSize(localTmuxArgv, *localSess) }

	cfg := daemon.Config{
		Ctl:            rwc{stdout, stdin},
		SockPath:       *sock,
		LocalSess:      *localSess,
		RemoteSession:  *session,
		RemoteWindow:   strconv.Itoa(*window),
		BaseIndex:      *baseIndex,
		PauseAfterSecs: *pauseAfter,
		RendererBin:    *rendererBin,
		LocalTmux:      runLocalTmux,
		WinSize:        winSize,
	}

	if err := daemon.Run(cfg); err != nil {
		fatal(err)
	}
}

// localWinSize queries the local session's active-window content dims, used to
// converge the remote windows' size to match (one control client, one size).
// Defaults to 80x24 if the session doesn't exist yet or the query fails.
func localWinSize(localTmuxArgv []string, localSess string) (int, int) {
	out, err := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...),
		"display-message", "-p", "-t", localSess, "-F", "#{window_width} #{window_height}")...).Output()
	if err != nil {
		return 80, 24
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 80, 24
	}
	w, errW := strconv.Atoi(fields[0])
	h, errH := strconv.Atoi(fields[1])
	if errW != nil || errH != nil {
		return 80, 24
	}
	return w, h
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
