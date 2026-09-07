package graphics

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"
)

// fetchTimeout bounds how long one output batch may hold its pane's byte
// stream. A frozen pane is worse than a missing image (spec D4), so a timeout
// drops the batch's unlocalised stores down the same path as any other
// unlocalisable one and the stream resumes. The budget is per BATCH, not per
// sequence: a carousel re-transmit stores the preview plus every filmstrip
// thumbnail in one batch, and a serialized per-sequence budget held the pane
// for N×timeout on a slow link (#556).
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
	// One deadline for the whole batch's fetches. D4's guarantee is unchanged
	// — a store still never trails the placements referencing it — but the
	// batch as a whole is what the timeout bounds, and the fetches run
	// concurrently when the localizer supports it.
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	outcomes := p.fetchBatch(ctx, chunks)
	var out []byte
	for _, c := range chunks {
		if c.Seq == nil {
			out = append(out, c.Literal...)
			continue
		}
		q, drop, err := rewrite(c.Seq, func(remote string) (string, error) {
			oc, ok := outcomes[remote]
			if !ok {
				// By construction unreachable: fetchBatch covers every
				// decodable t=f/t=t path in the batch, and rewrite only calls
				// this for those.
				return "", fmt.Errorf("graphics: no batch outcome for %s", remote)
			}
			return oc.local, oc.err
		})
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

// fetchOutcome is one path's result in a batch fetch.
type fetchOutcome struct {
	local string
	err   error
}

// fetchBatch localises every distinct t=f/t=t payload path in the batch in one
// go — concurrently when the localizer implements BatchLocalizer (the
// production SSHFetcher does), sequentially under the same shared deadline
// otherwise. Sequences whose payload is not base64 are skipped here; rewrite's
// own policy drops them below, which is also what keeps the outcome map
// complete for every path the rewrite loop can ask about.
func (p *Proxy) fetchBatch(ctx context.Context, chunks []Chunk) map[string]fetchOutcome {
	var paths []string
	seen := map[string]struct{}{}
	for _, c := range chunks {
		q := c.Seq
		if q == nil {
			continue
		}
		if t := q.Get("t"); t != "f" && t != "t" {
			continue
		}
		remote, err := base64.StdEncoding.DecodeString(string(q.Payload))
		if err != nil {
			continue
		}
		if _, dup := seen[string(remote)]; !dup {
			seen[string(remote)] = struct{}{}
			paths = append(paths, string(remote))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	outcomes := make(map[string]fetchOutcome, len(paths))
	if bl, ok := p.loc.(BatchLocalizer); ok {
		locals, errs := bl.LocalizeBatch(ctx, paths)
		for i, path := range paths {
			outcomes[path] = fetchOutcome{locals[i], errs[i]}
		}
		return outcomes
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			outcomes[path] = fetchOutcome{err: err}
			continue
		}
		local, err := p.loc.Localize(ctx, path)
		outcomes[path] = fetchOutcome{local, err}
	}
	return outcomes
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
