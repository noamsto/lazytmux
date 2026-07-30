// Command ctl is the local half of the bridge's structural-input path. A tmux
// keybind inside a @bridge_win window runs it instead of acting on the mirror;
// it hands the verb and the pane's remote id to the bridge daemon over the
// daemon's unix socket, and the daemon runs the equivalent command on the remote.
//
// Usage: ctl --sock <path> <verb> <remote-pane-id> [args...]
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// Deadlines exist so a dead or wedged daemon can never hold tmux's command
// queue. A stale socket with no listener fails instantly (ECONNREFUSED); a
// daemon that accepts but never answers fails at the overall deadline. The
// daemon acks as soon as it has queued the request — it absorbs all ssh latency
// behind its own fire-and-forget send — so this never waits on the network.
const (
	dialTimeout    = 250 * time.Millisecond
	overallTimeout = 2 * time.Second
)

func main() {
	sock := flag.String("sock", os.Getenv("LZTMUX_DAEMON_SOCK"), "bridge daemon unix socket")
	flag.Parse()

	if *sock == "" {
		fail("no --sock (is this a bridge window?)")
	}
	if flag.NArg() < 2 {
		fail("usage: ctl --sock <path> <verb> <remote-pane-id> [args...]")
	}

	args := flag.Args()
	// The focus hook runs backgrounded, so its requests can reach the daemon out
	// of order. Stamp a sequence here rather than in the keybind — tmux formats
	// have no monotonic counter — so the daemon can drop a stale one.
	if args[0] == "focus" && len(args) == 2 {
		args = append(args, strconv.FormatInt(time.Now().UnixNano(), 10))
	}

	if err := run(*sock, args); err != nil {
		fail(err.Error())
	}
}

func run(sock string, args []string) error {
	conn, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return fmt.Errorf("bridge daemon unreachable: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(overallTimeout)); err != nil {
		return err
	}

	argv := append([]string{wire.CtlProtocolVersion}, args...)
	if err := wire.WriteFrame(conn, wire.FrameCtl, wire.EncodeArgv(argv)); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	f, err := wire.ReadFrame(conn)
	if err != nil {
		// An older daemon does not know FrameCtl: it reads the first frame,
		// sees it is not a Hello, and closes. That arrives here as EOF, so say
		// what it means rather than reporting a bare read error.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return errors.New("this bridge daemon does not speak the ctl protocol — reopen the bridge")
		}
		return fmt.Errorf("no ack: %w", err)
	}
	if f.Type != wire.FrameCtlAck {
		return fmt.Errorf("unexpected reply frame %d", f.Type)
	}
	if len(f.Payload) > 0 {
		return errors.New(string(f.Payload))
	}
	return nil
}

func fail(msg string) {
	fmt.Fprintf(os.Stderr, "lztmux-remote-bridge-ctl: %s\n", msg)
	os.Exit(1)
}
