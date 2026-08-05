package graphics

// Proxy filters one pane's output stream. It is owned by that pane's output
// sink and called only from the sink's pump goroutine, so it needs no locking —
// and it may block there: holding one pane's stream at a sequence boundary is
// what keeps a store ahead of the placements that reference it (spec D4).
type Proxy struct {
	sc   *Scanner
	loc  Localizer
	logf func(format string, args ...any)
}

func New(loc Localizer, logf func(format string, args ...any)) *Proxy {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Proxy{sc: NewScanner(), loc: loc, logf: logf}
}

// Filter returns the bytes to forward to the renderer. An incomplete trailing
// sequence is held until the next call.
func (p *Proxy) Filter(data []byte) []byte {
	chunks := Coalesce(p.sc.Feed(data))
	var out []byte
	for _, c := range chunks {
		if c.Seq == nil {
			out = append(out, c.Literal...)
			continue
		}
		q, drop, err := Rewrite(c.Seq, p.loc)
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
	return nil
}
