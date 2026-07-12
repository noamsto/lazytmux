package main

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/agentdetect/debounce"
	"github.com/noamsto/lazytmux/picker/agentdetect/drainbuf"
	"github.com/noamsto/lazytmux/picker/agentdetect/manifest"
	"github.com/noamsto/lazytmux/picker/agentdetect/screen"
	"github.com/noamsto/lazytmux/picker/agentdetect/statefile"
)

const (
	debounceWindow = 80 * time.Millisecond
	stateDir       = "/tmp/claude-status/screen"
	// Per-pane backlog cap. The reader always drains stdin into this buffer so
	// tmux never buffers the pipe-pane backlog in-server; if the emulator can't
	// keep up, oldest bytes are dropped and the emulator is resynced. 1 MiB
	// holds many full-screen repaints, so normal bursts never truncate.
	maxBufferedBytes = 1 << 20
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	paneID := os.Args[1] // already sans '%'

	cols, rows, cmd := paneInfo(paneID)
	ms, err := manifest.Load()
	if err != nil {
		return
	}
	m, ok := manifest.ForCommand(ms, cmd)
	if !ok {
		return // pane isn't running a known agent; nothing to watch
	}

	scr := screen.New(cols, rows)
	deb := debounce.New(debounceWindow, nil)
	w := statefile.New(stateDir, paneID)

	buf := drainbuf.New(maxBufferedBytes)
	go readStdin(buf)

	ticker := time.NewTicker(debounceWindow / 2)
	defer ticker.Stop()

	for {
		select {
		case <-buf.Notify():
			data, truncated, closed := buf.Take()
			if truncated {
				// Dropped bytes broke VT continuity; resync from a blank
				// screen so stale rows can't linger. The next full repaint
				// (within a debounce window for an alt-screen TUI) restores it.
				scr = screen.New(cols, rows)
			}
			if len(data) > 0 {
				scr.Feed(data)
				deb.Mark(time.Now())
			}
			if closed {
				emit(scr, m, w) // final snapshot on EOF
				return
			}
		case <-ticker.C:
			if deb.Due(time.Now()) {
				emit(scr, m, w)
			}
		}
	}
}

func emit(scr screen.Screen, m manifest.Manifest, w *statefile.Writer) {
	state, _ := manifest.Match(m, scr.Text(), scr.Title(), scr.AltScreen())
	_, _ = w.Update(state, time.Now())
}

func readStdin(buf *drainbuf.Buffer) {
	r := bufio.NewReader(os.Stdin)
	b := make([]byte, 4096)
	for {
		n, err := r.Read(b)
		if n > 0 {
			buf.Append(b[:n]) // Append copies; reusing b is safe
		}
		if err != nil {
			buf.Close()
			return
		}
	}
}

func paneInfo(paneID string) (cols, rows int, cmd string) {
	cols, rows = 80, 24
	out, err := exec.Command("tmux", "display", "-p", "-t", "%"+paneID,
		"#{pane_width} #{pane_height} #{pane_current_command}").Output()
	if err != nil {
		return
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) >= 2 {
		if c, e := strconv.Atoi(f[0]); e == nil {
			cols = c
		}
		if r, e := strconv.Atoi(f[1]); e == nil {
			rows = r
		}
	}
	if len(f) >= 3 {
		cmd = f[2]
	}
	return
}
