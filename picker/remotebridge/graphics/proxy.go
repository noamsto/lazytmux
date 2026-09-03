package graphics

import (
	"context"
	"time"
)

// fetchTimeout bounds how long one sequence may hold its pane's byte stream.
// A frozen pane is worse than a missing image (spec D4), so a timeout drops
// the store down the same path as any other unlocalisable one and the stream
// resumes.
const fetchTimeout = 2 * time.Second

// retainMaxIDs caps how many distinct kitty image ids one pane's proxy keeps
// for post-reseed replay. Each id holds only its newest localised store.
const retainMaxIDs = 8

// Proxy filters one pane's output stream. It is owned by that pane's output
// sink and called only from the sink's pump goroutine — Filter on every
// output batch, Replay immediately after each FrameSeed, Close on teardown —
// so retain needs no locking. That confinement outlives Close: the pump may
// still be flushing (Filter then Close) when Close returns, so a caller that
// needs to inspect retain state from outside the pump — a test, typically —
// must wait for the pump to actually exit (outputSink.Wait) rather than
// racing that flush. Filter may block there, bounded by timeout: holding one
// pane's stream at a sequence boundary is what keeps a store ahead of the
// placements that reference it (spec D4).
type Proxy struct {
	sc        *Scanner
	loc       Localizer
	logf      func(format string, args ...any)
	timeout   time.Duration
	retainCap int
	// retain holds the last localised wrapped store per image id for replay
	// after a mirror re-seed restores placeholders without the store APC.
	retain map[string][]byte
	order  []string // oldest-to-newest ids; drives Replay order and LRU eviction
}

func New(loc Localizer, logf func(format string, args ...any)) *Proxy {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Proxy{
		sc:        NewScanner(),
		loc:       loc,
		logf:      logf,
		timeout:   fetchTimeout,
		retainCap: retainMaxIDs,
		retain:    make(map[string][]byte),
	}
}

// Filter returns the bytes to forward to the renderer. An incomplete trailing
// sequence is held until the next call.
func (p *Proxy) Filter(data []byte) []byte {
	before := p.sc.Malformed
	chunks := Coalesce(p.sc.Feed(data))
	if n := p.sc.Malformed - before; n > 0 {
		// Never reaches the per-sequence log below, because a scanner drop
		// yields no chunk at all: this is the scanner refusing to forward a
		// kitty sequence it could not decode whole. No legitimate sender emits
		// one, so it is worth a line.
		p.logf("graphics: dropped %d undecodable kitty sequence(s)", n)
	}
	var out []byte
	for _, c := range chunks {
		if c.Seq == nil {
			out = append(out, c.Literal...)
			continue
		}
		// Cancelled per sequence rather than deferred: this loop can run many
		// sequences in one batch, and a deferred cancel would hold every one of
		// them until Filter returns.
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		q, drop, err := Rewrite(ctx, c.Seq, p.loc)
		cancel()
		if drop {
			if err != nil {
				p.logf("graphics: dropped i=%s: %v", c.Seq.Get("i"), err)
			} else {
				p.logf("graphics: dropped i=%s (t=%s cannot cross hosts)", c.Seq.Get("i"), c.Seq.Get("t"))
			}
			continue
		}
		if q.Get("a") == "d" {
			if id := q.Get("i"); id != "" {
				p.evict(id)
			} else {
				switch q.Get("d") {
				case "A", "a":
					// Bulk delete: clear every retained id so a later re-seed
					// cannot resurrect images the sender already killed.
					p.retain = make(map[string][]byte)
					p.order = nil
				}
			}
		}
		wrapped := q.EncodeWrapped()
		if isStore(q) {
			p.retainStore(q.Get("i"), wrapped)
		}
		out = append(out, wrapped...)
	}
	return out
}

// Replay returns the retained localised stores in oldest-to-newest id order,
// ready to append after a FrameSeed without another fetch or round-trip.
func (p *Proxy) Replay() []byte {
	var out []byte
	for _, id := range p.order {
		if b, ok := p.retain[id]; ok {
			out = append(out, b...)
		}
	}
	return out
}

func (p *Proxy) retainStore(id string, wrapped []byte) {
	if id == "" {
		return
	}
	if _, ok := p.retain[id]; ok {
		p.removeFromOrder(id)
	} else if len(p.order) >= p.retainCap {
		p.evict(p.order[0])
	}
	p.retain[id] = append([]byte(nil), wrapped...)
	p.order = append(p.order, id)
}

func (p *Proxy) evict(id string) {
	if id == "" {
		return
	}
	delete(p.retain, id)
	p.removeFromOrder(id)
}

func (p *Proxy) removeFromOrder(id string) {
	for i, v := range p.order {
		if v == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			return
		}
	}
}

// Close flushes any held partial sequence so it isn't swallowed when the pane
// goes away, and drops retained replay state with the pane.
func (p *Proxy) Close() []byte {
	var out []byte
	for _, c := range p.sc.Flush() {
		out = append(out, c.Literal...)
	}
	p.retain = make(map[string][]byte)
	p.order = nil
	return out
}
