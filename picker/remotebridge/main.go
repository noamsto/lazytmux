package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

var paneIDRe = regexp.MustCompile(`^%[0-9]+$`)

func main() {
	host := flag.String("host", "", "ssh host")
	session := flag.String("session", "", "remote session")
	window := flag.Int("window", 0, "remote window index")
	remoteTmux := flag.String("tmux", "tmux", "absolute remote tmux path")
	tmpdir := flag.String("tmpdir", "", "remote TMUX_TMPDIR")
	flag.Parse()

	ctl := exec.Command("ssh", "-T", "-e", "none", *host, "--",
		"env", "TMUX_TMPDIR="+*tmpdir, *remoteTmux, "-C", "attach-session", "-t", *session)
	stdin, err := ctl.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lztmux-remote-bridge: %v\r\n", err)
		os.Exit(1)
	}
	stdout, err := ctl.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lztmux-remote-bridge: %v\r\n", err)
		os.Exit(1)
	}
	cmds := bufio.NewWriter(stdin)
	// send is called from the main setup path plus the input and resize
	// goroutines once the loops start; serialize so command lines never
	// interleave on the wire, and so a send racing teardown's stdin.Close
	// sees "closed" instead of writing to a closed pipe.
	var sendMu sync.Mutex
	closed := false
	send := func(s string) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return
		}
		fmt.Fprintf(cmds, "%s\n", s)
		cmds.Flush()
	}

	if err := ctl.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "lztmux-remote-bridge: %v\r\n", err)
		os.Exit(1)
	}
	reader := controlmode.NewReader(stdout)

	// resolve active pane in target window
	send(fmt.Sprintf("list-panes -t %s:%d -F '#{pane_active} #{pane_id}'", tmuxQuote(*session), *window))
	pane := readActivePane(reader)
	if !paneIDRe.MatchString(pane) {
		fmt.Fprintf(os.Stderr, "lztmux-remote-bridge: no active pane for %s:%d\r\n", *session, *window)
		os.Exit(1)
	}

	// size + seed
	if w, h, err := render.Size(0); err == nil {
		send(fmt.Sprintf("refresh-client -C -x %d -y %d", w, h))
	}
	send(fmt.Sprintf("display-message -p -t %s -F '#{cursor_x} #{cursor_y} #{alternate_on} #{keypad_cursor_flag}'", pane))
	cx, cy, alt, appck := readCursor(reader)
	send(fmt.Sprintf("capture-pane -e -p -t %s", pane))
	captured := readCapture(reader)
	// Raw mode (below) clears OPOST, so the kernel stops mapping bare "\n"
	// to "\r\n"; without this the seeded snapshot staircases until the
	// first repaint. capture-pane output uses bare "\n" line separators.
	captured = bytes.ReplaceAll(captured, []byte("\n"), []byte("\r\n"))
	os.Stdout.Write(render.Seed(captured, cx, cy, alt, appck))

	restore, _ := render.MakeRaw(0)

	teardown := func() {
		if restore != nil {
			restore()
		}
		sendMu.Lock()
		closed = true
		stdin.Close()
		sendMu.Unlock()
	}

	// input goroutine
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for _, cmd := range controlmode.SendKeysArgs(pane, buf[:n], 500) {
					send(quoteArgs(cmd))
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// resize goroutine
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		var t *time.Timer
		for range winch {
			if t != nil {
				t.Stop()
			}
			t = time.AfterFunc(50*time.Millisecond, func() {
				if w, h, err := render.Size(0); err == nil {
					send(fmt.Sprintf("refresh-client -C -x %d -y %d", w, h))
				}
			})
		}
	}()

	// render loop (main goroutine)
	for {
		l, ok := reader.Next()
		if !ok {
			teardown()
			return
		}
		switch l.Kind {
		case controlmode.Output:
			if l.Pane == pane {
				os.Stdout.Write(l.Data)
			}
		case controlmode.WindowClose, controlmode.Exit:
			teardown()
			return
		}
	}
}

// readActivePane consumes reply-block Lines until the list-panes reply
// arrives, then picks the "1 %id" row (pane_active == 1).
func readActivePane(reader *controlmode.Reader) string {
	for {
		l, ok := reader.Next()
		if !ok || l.Kind == controlmode.End || l.Kind == controlmode.Error {
			for _, row := range strings.Split(string(l.Data), "\n") {
				fields := strings.Fields(row)
				if len(fields) == 2 && fields[0] == "1" {
					return fields[1]
				}
			}
			return ""
		}
	}
}

// readCursor consumes reply-block Lines until the display-message reply
// arrives and parses "cursor_x cursor_y alternate_on keypad_cursor_flag".
func readCursor(reader *controlmode.Reader) (cx, cy int, alt, appCursorKeys bool) {
	for {
		l, ok := reader.Next()
		if !ok || l.Kind == controlmode.End || l.Kind == controlmode.Error {
			fields := strings.Fields(string(l.Data))
			if len(fields) != 4 {
				return 0, 0, false, false
			}
			cx, _ = strconv.Atoi(fields[0])
			cy, _ = strconv.Atoi(fields[1])
			return cx, cy, fields[2] == "1", fields[3] == "1"
		}
	}
}

// readCapture consumes reply-block Lines until the capture-pane reply
// arrives and returns its body (pane content, already newline-joined by
// the Reader).
func readCapture(reader *controlmode.Reader) []byte {
	for {
		l, ok := reader.Next()
		if !ok || l.Kind == controlmode.End || l.Kind == controlmode.Error {
			return l.Data
		}
	}
}

// tmuxQuote single-quotes s for a tmux control-mode command line, escaping
// any embedded single quote the tmux-safe way. Needed for the session name
// in the initial list-panes target, since session names may contain spaces;
// every other command below targets the resolved pane id (%N), which never
// contains spaces and needs no quoting.
func tmuxQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteArgs joins a send-keys arg slice into one control-mode command line.
// Every token here is a pane id or hex byte pair — both safe, unquoted
// tokens — so a plain space-join is sufficient.
func quoteArgs(args []string) string {
	return strings.Join(args, " ")
}
