// Command daemon is the M2.1 production entrypoint: it opens an ssh -CC
// control-mode connection to a remote tmux, mirrors one of its windows into a
// local multi-pane window, and runs until the remote window closes or the
// connection drops. See picker/remotebridge/daemon for the orchestration.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/noamsto/lazytmux/picker/remotebridge/daemon"
)

func main() {
	// Flags default to LZTMUX_BRIDGE_*/LZTMUX_DAEMON_* env vars, mirroring
	// M1's remotebridge/main.go: the launcher passes untrusted, remote-derived
	// values through tmux's environment rather than interpolating them into a
	// /bin/sh command string.
	host := flag.String("host", os.Getenv("LZTMUX_BRIDGE_HOST"), "ssh host")
	session := flag.String("session", os.Getenv("LZTMUX_BRIDGE_SESSION"), "remote session")
	window := flag.Int("window", envInt("LZTMUX_BRIDGE_WINDOW"), "remote window index")
	remoteTmux := flag.String("tmux", envDefault("LZTMUX_BRIDGE_TMUX", "tmux"), "absolute remote tmux path")
	tmpdir := flag.String("tmpdir", os.Getenv("LZTMUX_BRIDGE_TMPDIR"), "remote TMUX_TMPDIR")
	sshCmd := flag.String("ssh", envDefault("LZTMUX_BRIDGE_SSH", "ssh"), "control transport command (empty = run tmux locally)")
	localTmux := flag.String("local-tmux", envDefault("LZTMUX_DAEMON_LOCAL_TMUX", "tmux"), "local tmux binary (may carry args, e.g. \"tmux -L sock\")")
	localSess := flag.String("local-sess", os.Getenv("LZTMUX_DAEMON_LOCAL_SESS"), `local session name (default "<host>-<session>")`)
	sock := flag.String("sock", os.Getenv("LZTMUX_DAEMON_SOCK"), "unix socket path for renderers")
	rendererBin := flag.String("renderer", os.Getenv("LZTMUX_DAEMON_RENDERER"), "absolute path to the renderer binary")
	flag.Parse()

	if *localSess == "" {
		*localSess = fmt.Sprintf("%s-%s", *host, *session)
	}
	localWin := *localSess + ":1"
	if *sock == "" {
		*sock = fmt.Sprintf("%s/lztmux-daemon-%d.sock", os.TempDir(), os.Getpid())
	}

	// remoteTmux/localTmux may carry args (e.g. "tmux -L sock" for tests), so
	// split into argv rather than passing as a single token.
	tmuxArgv := strings.Fields(*remoteTmux)
	var ctl *exec.Cmd
	if *sshCmd == "" {
		ctl = exec.Command(tmuxArgv[0], append(append([]string{}, tmuxArgv[1:]...),
			"-C", "attach-session", "-t", *session)...)
	} else {
		// ssh space-joins the post-host argv into one string run by the
		// remote login shell, so shell-quote the session name (may contain
		// spaces) to keep it a single target token.
		args := append([]string{"-T", "-e", "none", *host, "--", "env", "TMUX_TMPDIR=" + *tmpdir}, tmuxArgv...)
		args = append(args, "-C", "attach-session", "-t", shellQuote(*session))
		ctl = exec.Command(*sshCmd, args...)
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

	localTmuxArgv := strings.Fields(*localTmux)
	runLocalTmux := func(args ...string) error {
		cmd := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...), args...)...)
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	winSize := func() (int, int) { return localWinSize(localTmuxArgv, localWin) }

	cfg := daemon.Config{
		Ctl:         rwc{stdout, stdin},
		SockPath:    *sock,
		LocalSess:   *localSess,
		LocalWin:    localWin,
		RemoteWin:   fmt.Sprintf("%s:%d", *session, *window),
		RendererBin: *rendererBin,
		LocalTmux:   runLocalTmux,
		WinSize:     winSize,
	}

	if err := daemon.Run(cfg); err != nil {
		fatal(err)
	}
}

// localWinSize queries the local mirror window's content dims, used to
// converge the remote window's size to match. Defaults to 80x24 if the
// window doesn't exist yet or the query fails.
func localWinSize(localTmuxArgv []string, localWin string) (int, int) {
	out, err := exec.Command(localTmuxArgv[0], append(append([]string{}, localTmuxArgv[1:]...),
		"display-message", "-p", "-t", localWin, "-F", "#{window_width} #{window_height}")...).Output()
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

// shellQuote single-quotes s for a POSIX shell, escaping embedded single
// quotes. Used for the session name in the ssh remote-command argv.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
