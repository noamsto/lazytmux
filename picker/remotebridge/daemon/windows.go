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
	index  string
	id     string
	active bool
	name   string
}

// windowListFormat is the one format every list-windows in the daemon uses.
// window_active sits BEFORE window_name because a name may contain spaces, so
// only the last field can be free-form.
const windowListFormat = "'#{window_index} #{window_id} #{window_active} #{window_name}'"

// parseWindowList turns a list-windows reply body (windowListFormat) into the
// ordered remote windows, dropping blank/malformed rows.
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
		id, rest, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		// name is optional; "" when absent
		active, name, _ := strings.Cut(rest, " ")
		wins = append(wins, remoteWindow{index: idx, id: id, active: active == "1", name: name})
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

// stripWindowName drops what must never reach a tmux command line — '|' (the
// reflow FMT delimiter), newlines and control chars — and then strips tmux
// #[...] style sequences. Both orderings here are load-bearing, and both guard
// the same failure: a '#'-run immediately followed by '[' is what tmux's
// format_expand passes through verbatim instead of collapsing pairwise
// (format.c:6671-6694), so the rename round trip drifts unboundedly.
//   - The drop runs FIRST because it can join a '#' to a '[' that a strip scan
//     never saw together: "a#|[x]b" would otherwise survive as "a##[x]b".
//   - The strip iterates to a fixed point because one pass can likewise join a
//     surviving '#' to a later '[': "##[a][" collapses to "#[".
//
// An unterminated "#[" (no ']' anywhere after it) drops to end-of-string; there
// is no way to escape it into something a later expansion reads as literal.
func stripWindowName(s string) string {
	var dropped strings.Builder
	for _, r := range s {
		if r == '|' || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			continue
		}
		dropped.WriteRune(r)
	}
	cur := dropped.String()
	for {
		var b strings.Builder
		for i := 0; i < len(cur); {
			if i+1 < len(cur) && cur[i] == '#' && cur[i+1] == '[' {
				j := strings.IndexByte(cur[i:], ']')
				if j < 0 {
					i = len(cur)
					continue
				}
				i += j + 1
				continue
			}
			b.WriteByte(cur[i])
			i++
		}
		if b.Len() == len(cur) {
			return cur
		}
		cur = b.String()
	}
}

// sanitizeWindowName cleans a remote-derived window name before it is written
// to @window_bridge_name: stripWindowName, then escape every surviving '#' as
// '##' so a format expansion of the option renders it literally.
func sanitizeWindowName(s string) string {
	var b strings.Builder
	for _, r := range stripWindowName(s) {
		if r == '#' {
			b.WriteString("##")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// decodeWindowName inverts sanitizeWindowName's escape. Its caller applies it to
// whatever the rename prompt returns, edited or not: the prompt is seeded from
// @window_bridge_name, so the whole field speaks that option's escaped dialect and
// nothing marks which parts the user retyped. A typed literal '##' therefore
// collapses to one '#'.
func decodeWindowName(s string) string {
	return strings.ReplaceAll(s, "##", "#")
}
