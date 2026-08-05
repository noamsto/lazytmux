package graphics

import "bytes"

// Seq is one decoded kitty graphics APC: the control keys before the ';' and
// the payload after it. Wrapped records whether it arrived inside a tmux
// passthrough, for diagnostics only — output is always wrapped exactly once,
// because the renderer pane always sits inside the local tmux.
type Seq struct {
	Keys    []byte
	Payload []byte
	HasBody bool // a ';' was present (an empty payload is distinct from none)
	Wrapped bool
}

// decodeSeq decodes the sequence at the head of b:
//
//	seq != nil, n > 0  — a graphics sequence; consume n
//	seq == nil, n > 0  — a COMPLETE sequence that isn't ours (a passthrough
//	                     carrying something else, e.g. OSC 52); forward b[:n]
//	                     verbatim and move past it
//	seq == nil, n == 0 — incomplete; hold for more bytes
//
// The middle case is why this returns a length rather than an ok bool. "Not
// complete yet" and "complete, but not mine" both mean "no sequence here", but
// conflating them stalls the pane: a clipboard escape would hold every later
// byte behind it until the partial cap or Flush.
func decodeSeq(b []byte) (*Seq, int) {
	if bytes.HasPrefix(b, []byte(passStart)) {
		inner, n, ok := unwrapPassthrough(b)
		if !ok {
			return nil, 0
		}
		q, m, ok := decodeBare(inner)
		// The sequence must fill the wrapper exactly. A wrapper carrying anything
		// after its first sequence is treated as not-ours and forwarded whole:
		// decoding only the first would silently drop the rest, and a proxy must
		// never lose bytes it was asked to relay. The cost is that such a store
		// goes unlocalised (blank image, per D7) — a case aeye cannot produce,
		// since tmuxPassthrough wraps exactly one sequence.
		if !ok || m != len(inner) {
			return nil, n
		}
		q.Wrapped = true
		return q, n
	}
	// Feed only calls this at an indexSeqStart hit, so a head that isn't a
	// passthrough is an apcStart: decodeBare can only fail for want of the ST.
	q, n, ok := decodeBare(b)
	if !ok {
		return nil, 0
	}
	return q, n
}

func decodeBare(b []byte) (*Seq, int, bool) {
	if !bytes.HasPrefix(b, []byte(apcStart)) {
		return nil, 0, false
	}
	end := indexOf(b[len(apcStart):], st)
	if end < 0 {
		return nil, 0, false
	}
	body := b[len(apcStart) : len(apcStart)+end]
	q := &Seq{}
	if i := bytes.IndexByte(body, ';'); i >= 0 {
		q.Keys = append([]byte(nil), body[:i]...)
		q.Payload = append([]byte(nil), body[i+1:]...)
		q.HasBody = true
	} else {
		q.Keys = append([]byte(nil), body...)
	}
	return q, len(apcStart) + end + len(st), true
}

// unwrapPassthrough un-doubles the ESCs of a \ePtmux;… wrapper and returns the
// inner sequence plus the bytes consumed. Scanning for the first ST would cut at
// the INNER terminator (\e\e\\ contains \e\\ at its second byte), so the ESCs are
// un-doubled as we walk instead.
func unwrapPassthrough(b []byte) ([]byte, int, bool) {
	i := len(passStart)
	var inner []byte
	for i < len(b) {
		if b[i] == 0x1b {
			if i+1 >= len(b) {
				return nil, 0, false
			}
			if b[i+1] == 0x1b {
				inner = append(inner, 0x1b)
				i += 2
				continue
			}
			if b[i+1] == '\\' {
				return inner, i + 2, true
			}
		}
		inner = append(inner, b[i])
		i++
	}
	return nil, 0, false
}
