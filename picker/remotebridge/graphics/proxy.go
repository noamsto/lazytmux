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

// Proxy filters one pane's output stream. It is owned by that pane's output
// sink and called only from the sink's pump goroutine, so it needs no locking —
// and it may block there, bounded by timeout: holding one pane's stream at a
// sequence boundary is what keeps a store ahead of the placements that
// reference it (spec D4).
type Proxy struct {
	sc      *Scanner
	loc     Localizer
	logf    func(format string, args ...any)
	timeout time.Duration
}

func New(loc Localizer, logf func(format string, args ...any)) *Proxy {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Proxy{sc: NewScanner(), loc: loc, logf: logf, timeout: fetchTimeout}
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
		out = append(out, q.EncodeWrapped()...)
	}
	return out
}

// Close flushes any held partial sequence so it isn't swallowed when the pane
// goes away.
func (p *Proxy) Close() []byte {
	var out []byte
	for _, c := range p.sc.Flush() {
		out = append(out, c.Literal...)
	}
	return out
}
