package daemon

import (
	"fmt"
	"net"
	"strings"
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
type registry struct {
	byRemote map[string]*mirrorWindow
	nextIdx  int
}

func newRegistry(baseIdx int) *registry {
	return &registry{byRemote: map[string]*mirrorWindow{}, nextIdx: baseIdx}
}

func (r *registry) allocLocalWin(sess string) string {
	win := fmt.Sprintf("%s:%d", sess, r.nextIdx)
	r.nextIdx++
	return win
}

func (r *registry) add(remoteID, localWin string) *mirrorWindow {
	w := &mirrorWindow{remoteID: remoteID, localWin: localWin, conns: map[string]net.Conn{}}
	r.byRemote[remoteID] = w
	return w
}

func (r *registry) byRemoteID(remoteID string) (*mirrorWindow, bool) {
	w, ok := r.byRemote[remoteID]
	return w, ok
}

func (r *registry) remove(remoteID string) (*mirrorWindow, bool) {
	w, ok := r.byRemote[remoteID]
	if ok {
		delete(r.byRemote, remoteID)
	}
	return w, ok
}

func (r *registry) empty() bool { return len(r.byRemote) == 0 }

// remoteWindow pairs a remote window's index (#{window_index}) with its id
// (#{window_id}, @N). --window / Config.RemoteWindow is an *index*; the registry
// is keyed by *id* — different tmux namespaces, so both must be carried.
type remoteWindow struct {
	index string
	id    string
}

// parseWindowList turns a `list-windows -F '#{window_index} #{window_id}'` reply
// body into the ordered remote windows, dropping blank/malformed rows.
func parseWindowList(body string) []remoteWindow {
	var wins []remoteWindow
	for _, row := range strings.Split(body, "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		idx, id, ok := strings.Cut(row, " ")
		if !ok {
			continue
		}
		wins = append(wins, remoteWindow{index: idx, id: id})
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
