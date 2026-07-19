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
	delete(r.sinks, paneID)
	r.mu.Unlock()
}

func (r *Router) Route(paneID string, data []byte) {
	r.mu.Lock()
	sink := r.sinks[paneID]
	r.mu.Unlock()
	if sink != nil {
		sink.Write(data) // best-effort; sink is non-blocking (see daemon.go)
	}
}
