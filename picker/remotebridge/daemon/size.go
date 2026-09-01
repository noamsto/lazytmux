package daemon

import (
	"fmt"
	"sync"
)

// ConvergeCmd returns the control-mode command that caps one remote window at
// WxH. The per-window form of refresh-client -C, not the whole-client one:
// tmux applies it as a clamp *after* window-size's own calculation (resize.c
// clients_calculate_size), so it holds even when a human client is attached to
// the same remote session and owns the window as w->latest. The whole-client
// form loses that race — under window-size latest every client but w->latest
// is skipped outright.
func ConvergeCmd(remoteID string, w, h int) string {
	return fmt.Sprintf("refresh-client -C %s:%dx%d", remoteID, w, h)
}

// clientSizeKey is the converger slot for the whole-client size below. Remote
// window ids are all @N, so this cannot collide with one.
const clientSizeKey = "client"

// ClientSizeCmd returns the control-mode command that gives the bridge's own
// control client a size.
//
// Without it the client has none: tmux defaults a control client to 80 columns
// and no rows, and under `window-size latest` a window CREATED on the remote is
// born at that size (measured on a live bridge: 80x23, converging to 250x63
// only 230ms later, once ConvergeCmd's per-window cap lands). Anything the
// remote starts in that gap reads the wrong terminal size.
//
// This does not replace the per-window caps — it cannot, for the reason
// ConvergeCmd documents. It only gives a window a sane size to be BORN at; the
// cap still owns the size it settles to.
func ClientSizeCmd(w, h int) string {
	return fmt.Sprintf("refresh-client -C %dx%d", w, h)
}

// converger records the size last asserted for each remote window so the
// resize poll only re-sends on change. Written from the window setup path and
// from the resize watcher goroutine, hence the mutex.
type converger struct {
	mu   sync.Mutex
	last map[string][2]int
}

func newConverger() *converger { return &converger{last: map[string][2]int{}} }

// need reports whether remoteID still has to be told about WxH, recording the
// size as asserted when it does. clientSizeKey tracks the whole-client size in
// the same map, so it is re-sent on a change and skipped otherwise.
func (c *converger) need(remoteID string, w, h int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last[remoteID] == [2]int{w, h} {
		return false
	}
	c.last[remoteID] = [2]int{w, h}
	return true
}

func (c *converger) forget(remoteID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.last, remoteID)
}
