package main

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/agentdetect/debounce"
	"github.com/noamsto/lazytmux/picker/agentdetect/manifest"
	"github.com/noamsto/lazytmux/picker/agentdetect/screen"
	"github.com/noamsto/lazytmux/picker/agentdetect/statefile"
)

const (
	debounceWindow = 80 * time.Millisecond
	stateDir       = "/tmp/claude-status/screen"
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

	bytesCh := make(chan []byte, 64)
	go readStdin(bytesCh)

	ticker := time.NewTicker(debounceWindow / 2)
	defer ticker.Stop()

	for {
		select {
		case b, open := <-bytesCh:
			if !open {
				emit(scr, m, w) // final snapshot on EOF
				return
			}
			scr.Feed(b)
			deb.Mark(time.Now())
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

func readStdin(ch chan<- []byte) {
	r := bufio.NewReader(os.Stdin)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			cp := make([]byte, n)
			copy(cp, buf[:n])
			ch <- cp
		}
		if err != nil {
			close(ch)
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
