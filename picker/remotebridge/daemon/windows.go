package daemon

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

// mirrorWindow is one remote window's local mirror: the remote window id it
// tracks, the local tmux window target it renders into, the remote pane ids in
// creation order, and the renderer conns keyed by remote pane id.
type mirrorWindow struct {
	remoteID    string
	localWin    string
	remotePanes []string
	conns       map[string]net.Conn
}

// registry maps remote window ids (@N) to their local mirror windows and hands
// out monotonically-increasing local window indices. LocalTmux can't capture a
// created window's index, so the daemon assigns indices rather than reading
// them back; the counter never decrements, so a closed window's index is never
// reused and a stale @N->index mapping can't collide.
//
// The main loop mutates it while the resize watcher reads the mirrored ids
// each tick, so every access takes mu.
type registry struct {
	mu       sync.Mutex
	byRemote map[string]*mirrorWindow
	nextIdx  int
}

func newRegistry(baseIdx int) *registry {
	return &registry{byRemote: map[string]*mirrorWindow{}, nextIdx: baseIdx}
}

func (r *registry) allocLocalWin(sess string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	win := fmt.Sprintf("%s:%d", sess, r.nextIdx)
	r.nextIdx++
	return win
}

func (r *registry) add(remoteID, localWin string) *mirrorWindow {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := &mirrorWindow{remoteID: remoteID, localWin: localWin, conns: map[string]net.Conn{}}
	r.byRemote[remoteID] = w
	return w
}

func (r *registry) byRemoteID(remoteID string) (*mirrorWindow, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.byRemote[remoteID]
	return w, ok
}

func (r *registry) remove(remoteID string) (*mirrorWindow, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.byRemote[remoteID]
	if ok {
		delete(r.byRemote, remoteID)
	}
	return w, ok
}

func (r *registry) empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byRemote) == 0
}

// remoteIDs snapshots the mirrored window ids for the resize watcher, which
// runs off the main loop's goroutine.
func (r *registry) remoteIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.byRemote))
	for id := range r.byRemote {
		ids = append(ids, id)
	}
	return ids
}

// all snapshots the mirror windows themselves, for teardown.
func (r *registry) all() []*mirrorWindow {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*mirrorWindow, 0, len(r.byRemote))
	for _, w := range r.byRemote {
		out = append(out, w)
	}
	return out
}

// remoteWindow pairs a remote window's index (#{window_index}) with its id
// (#{window_id}, @N). --window / Config.RemoteWindow is an *index*; the registry
// is keyed by *id* — different tmux namespaces, so both must be carried.
type remoteWindow struct {
	index string
	id    string
	name  string
}

// parseWindowList turns a `list-windows -F '#{window_index} #{window_id} #{window_name}'` reply
// body into the ordered remote windows, dropping blank/malformed rows.
func parseWindowList(body string) []remoteWindow {
	var wins []remoteWindow
	for _, row := range strings.Split(body, "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		idx, rest, ok := strings.Cut(row, " ")
		if !ok {
			continue
		}
		id, name, _ := strings.Cut(rest, " ") // name is optional; "" when absent
		wins = append(wins, remoteWindow{index: idx, id: id, name: name})
	}
	return wins
}

// localWinForRemoteIndex resolves the initially-selected window: it maps a
// remote window *index* (as carried by --window) to the remote window *id* via
// the enumerated windows, then to the local window via the registry. This keeps
// --window <idx> from being misread as window id "@<idx>".
func localWinForRemoteIndex(wins []remoteWindow, reg *registry, remoteIdx string) (string, bool) {
	for _, rw := range wins {
		if rw.index == remoteIdx {
			if mw, ok := reg.byRemoteID(rw.id); ok {
				return mw.localWin, true
			}
		}
	}
	return "", false
}

// sanitizeWindowName strips characters that would break the reflow FMT
// delimiter ('|') or a tmux command line (newlines/control chars) from a
// remote-derived window name before it is written to @window_bridge_name.
func sanitizeWindowName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '|' || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
