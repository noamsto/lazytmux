package daemon

import (
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

type Router struct {
	mu    sync.Mutex
	sinks map[string]io.Writer
}

func NewRouter() *Router { return &Router{sinks: map[string]io.Writer{}} }

func (r *Router) Register(paneID string, sink io.Writer) {
	r.mu.Lock()
	r.sinks[paneID] = sink
	r.mu.Unlock()
}

func (r *Router) Unregister(paneID string) {
	r.mu.Lock()
	sink := r.sinks[paneID]
	delete(r.sinks, paneID)
	r.mu.Unlock()
	// Sinks that own a pump goroutine (outputSink) need a Close to stop it;
	// plain io.Writer fakes in tests don't implement it.
	if c, ok := sink.(interface{ Close() }); ok {
		c.Close()
	}
}

func (r *Router) Route(paneID string, data []byte) {
	r.mu.Lock()
	sink := r.sinks[paneID]
	r.mu.Unlock()
	if sink != nil {
		sink.Write(data) // best-effort; sink is non-blocking (see daemon.go)
	}
}

// sink returns paneID's registered *outputSink, or nil if none is registered
// or the registered writer is a test fake. A read accessor — it leaves
// Register/Unregister/Route unchanged — used by %pause/%continue to gate and
// re-seed the pane's serialized frame stream.
// dirtyPanes returns, sorted, the panes whose sink dropped frames and has since
// drained, clearing each one's count as it goes. A drop is recorded on the
// sink because it happens on the routing path, which must never block; this is
// how it reaches the main loop, the only place the re-seed round-trip may run.
func (r *Router) dirtyPanes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, w := range r.sinks {
		s, ok := w.(*outputSink)
		if !ok {
			continue
		}
		if n, dirty := s.takeDirty(); dirty {
			fmt.Fprintf(os.Stderr, "daemon: %s: re-seeding after %d dropped frame(s)\n", id, n)
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (r *Router) sink(paneID string) *outputSink {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, _ := r.sinks[paneID].(*outputSink)
	return s
}
