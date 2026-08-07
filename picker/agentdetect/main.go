package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
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
	// Longest an animating pane can go unsampled. Agents that repaint faster
	// than debounceWindow never fall quiet, so this is their only sampling
	// path (#238); it also bounds how long any pane can show a stale state.
	sampleCeiling = 500 * time.Millisecond
	stateDir      = "/tmp/claude-status/screen"
	// Per-pane backlog cap. The reader always drains stdin into this buffer so
	// tmux never buffers the pipe-pane backlog in-server; if the emulator can't
	// keep up, oldest bytes are dropped and the emulator is resynced. 1 MiB
	// holds many full-screen repaints, so normal bursts never truncate.
	maxBufferedBytes = 1 << 20
	// Leak-reaper interval (#239): coarse because it forks `tmux capture-pane`.
	// Anything faster would fork per tick across every live watcher for no
	// benefit — a dead pane isn't time-sensitive the way animation is; tens
	// of seconds is fine for a backstop that only exists to bound the worst
	// case.
	livenessInterval = 30 * time.Second
	watcherRegDir    = "/tmp/claude-status/watchers"
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

	myPID := os.Getpid()
	if !registerWatcher(watcherRegDir, paneID, myPID) {
		return
	}

	scr := seededScreen(paneID, cols, rows)
	deb := debounce.New(debounceWindow, sampleCeiling)
	w := statefile.New(stateDir, paneID)
	emit(scr, m, w) // report what is already on screen, before any new output

	buf := drainbuf.New(maxBufferedBytes)
	go readStdin(buf)

	ticker := time.NewTicker(debounceWindow / 2)
	defer ticker.Stop()
	liveness := time.NewTicker(livenessInterval)
	defer liveness.Stop()

	for {
		select {
		case <-buf.Notify():
			data, truncated, closed := buf.Take()
			if truncated {
				// Dropped bytes broke VT continuity; re-seed so stale rows
				// can't linger. Seeding rather than blanking matters for an
				// agent that only ever repaints a few cells — a blank screen
				// would never be filled back in.
				scr = seededScreen(paneID, cols, rows)
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
			// A local file read, no fork, so this rides the existing hot
			// ticker rather than waiting on the coarse liveness probe below.
			// Neither removes the registry file (by now it's the new
			// watcher's, not ours — deleting it would make the new watcher
			// self-evict on its own next check) nor emits (the new watcher
			// already reported current state at its own startup, and
			// statefile.Writer's temp file isn't pid-namespaced, so a stale
			// write here could race and clobber the new watcher's write).
			if !stillOwner(watcherRegDir, paneID, myPID) {
				return
			}
			if deb.Due(time.Now()) {
				emit(scr, m, w)
			}
		case <-liveness.C:
			// Backstop for the case EOF never arrives: dead pane, or the
			// tmux server gone outright.
			if !paneAlive(paneID) {
				emit(scr, m, w)
				return
			}
		}
	}
}

// seededScreen returns an emulator primed with the pane's current contents.
// pipe-pane only delivers bytes written after it is armed, so an emulator that
// starts blank shows whatever the agent happens to repaint next. Agents that
// redraw a whole frame converge within a frame or two; codex animates a few
// cells at a time, so the line we match on ("esc to interrupt") is never
// rewritten and a blank-start emulator never sees it at all (#238).
func seededScreen(paneID string, cols, rows int) screen.Screen {
	scr := screen.New(cols, rows)
	if out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-t", "%"+paneID).Output(); err == nil {
		scr.Feed(out)
	}
	return scr
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

// registerWatcher atomically claims paneID for pid, replacing whatever a
// previous watcher wrote. Returns false only if the filesystem itself is
// unusable (can't mkdir/write/rename) — in that case the caller can't
// guarantee it's the sole watcher for this pane, so it should not become a
// long-lived process that might duplicate one (#239).
func registerWatcher(dir, paneID string, pid int) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	final := filepath.Join(dir, paneID)
	tmp := final + "." + strconv.Itoa(pid) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return false
	}
	return os.Rename(tmp, final) == nil
}

// stillOwner reports whether pid is still the registered watcher for paneID.
// A re-arm overwrites the registry with the new watcher's pid; once that
// happens the old process reads a mismatch here and exits, which is what
// closes the re-arm leak (#239) without any tmux-side changes.
func stillOwner(dir, paneID string, pid int) bool {
	content, err := os.ReadFile(filepath.Join(dir, paneID))
	if err != nil {
		return false
	}
	return ownerMatches(string(content), pid)
}

// ownerMatches is the pure comparison stillOwner delegates to, split out so
// the decision is testable without touching the filesystem.
func ownerMatches(registered string, pid int) bool {
	return strings.TrimSpace(registered) == strconv.Itoa(pid)
}

// paneAlive reports whether the pane still exists, by asking tmux directly
// rather than trusting any cached state. Deliberately not `tmux display-message
// -t <pane>`: display-message's target lookup is declared CMD_FIND_CANFAIL in
// tmux's own source (cmd-display-message.c), so it tolerates a missing target
// and exits 0 even against a dead pane — verified empirically against tmux
// 3.7b, the exact binary this repo wraps. capture-pane's target flag is not
// CANFAIL (cmd-capture-pane.c), so it correctly errors "can't find pane" and
// exits nonzero on a dead one; it's also the pattern seededScreen already uses
// elsewhere in this file, so no new probing style is introduced.
func paneAlive(paneID string) bool {
	err := exec.Command("tmux", "capture-pane", "-p", "-t", "%"+paneID).Run()
	return aliveFromProbe(err)
}

// aliveFromProbe is the pure decision behind the leak-reaper backstop: a nil
// error means the pane answered, anything else — pane gone, session gone, or
// tmux server unreachable entirely — means exit. An unreachable tmux must
// never be read as "assume alive" (#239): a dead server can't tell us
// otherwise, so silence has to mean gone.
func aliveFromProbe(err error) bool { return err == nil }
