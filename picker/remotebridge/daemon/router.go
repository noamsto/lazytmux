package daemon

import (
	"io"
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
