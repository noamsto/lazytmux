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

// dropReason says why decodeSeq consumed bytes without yielding a sequence.
// Two very different things used to share one bool, which is the conflation
// that let a partly-decoded store be forwarded as though it were none of ours.
type dropReason int

const (
	dropNone      dropReason = iota // not a drop
	dropSixel                       // sixel over the bridge is a non-goal
	dropMalformed                   // ours, but we could not decode it whole
)

// decodeSeq decodes the sequence at the head of b:
//
//	seq != nil, n > 0, dropNone      — a graphics sequence; consume n
//	seq == nil, n > 0, dropNone      — a COMPLETE sequence that isn't ours (a
//	                                   passthrough carrying something else, e.g.
//	                                   OSC 52); forward b[:n] verbatim
//	seq == nil, n > 0, dropSixel     — a complete sixel DCS (bare or wrapped);
//	                                   consume n and emit nothing
//	seq == nil, n > 0, dropMalformed — a kitty APC we could not decode whole;
//	                                   consume n and emit nothing (see the
//	                                   wrapped branch for why forwarding is
//	                                   not an option)
//	seq == nil, n == 0               — incomplete; hold for more bytes
//
// The forward-verbatim case is why this returns a length rather than an ok
// bool. "Not complete yet" and "complete, but not mine" both mean "no sequence
// here", but conflating them stalls the pane: a clipboard escape would hold
// every later byte behind it until the partial cap or Flush.
func decodeSeq(b []byte) (*Seq, int, dropReason) {
	if bytes.HasPrefix(b, []byte(passStart)) {
		inner, n, ok := unwrapPassthrough(b)
		if !ok {
			return nil, 0, dropNone
		}
		// Sixel through the bridge is an explicit non-goal: drop a
		// passthrough whose undoubled payload is a bare sixel DCS rather
		// than forwarding it (which would paint a SIXEL IMAGE placeholder
		// or garble mid-sequence text into the mirrored pane).
		if isConfirmedSixelHead(inner) {
			return nil, n, dropSixel
		}
		q, m, ok := decodeBare(inner)
		// Forwarding a wrapper verbatim is only safe when the inner escape is
		// none of ours. If it IS a kitty APC and we could not decode it whole,
		// forwarding hands the terminal a sequence whose t=f payload never
		// passed the localiser — a path on the REMOTE filesystem, or one the
		// sender chose to name on this one. D7 governs: an unlocalisable store
		// is dropped, never forwarded, because a missing image renders blank and
		// self-heals where a wrong one renders wrong. So the two failures split.
		switch {
		case !ok && !bytes.HasPrefix(inner, []byte(apcStart)):
			// Genuinely not ours (OSC 52, a title, …): forward the wrapper
			// whole, which is what its sender meant and what the renderer's
			// local tmux needs to reach the outer terminal.
			return nil, n, dropNone
		case !ok:
			// Ours, but unterminated inside the wrapper. Forwarding it would
			// leave the terminal holding an open APC that a LATER wrapper could
			// terminate, assembling a store out of pieces neither of which we
			// ever localised.
			return nil, n, dropMalformed
		case m != len(inner):
			// Ours, plus trailing bytes. Splitting the wrapper would mean
			// re-wrapping the remainder to keep its passthrough semantics; not
			// worth it for a shape no legitimate sender emits (tmuxPassthrough
			// wraps exactly one sequence), and dropping is the safe direction.
			return nil, n, dropMalformed
		case hasDuplicateKey(q.Keys):
			return nil, n, dropMalformed
		}
		q.Wrapped = true
		return q, n, dropNone
	}
	if isSixelPrefix(b) {
		n, complete := consumeBareSixel(b)
		if !complete {
			return nil, 0, dropNone
		}
		return nil, n, dropSixel
	}
	// Feed only calls this at an indexSeqStart hit, so a head that isn't a
	// passthrough or sixel is an apcStart: decodeBare can only fail for want
	// of the ST.
	q, n, ok := decodeBare(b)
	if !ok {
		return nil, 0, dropNone
	}
	if hasDuplicateKey(q.Keys) {
		return nil, n, dropMalformed
	}
	return q, n, dropNone
}

// hasDuplicateKey reports whether keys names the same key twice.
//
// Get returns the FIRST match, so a sequence carrying t=d,t=f reads to us as
// inline data while a terminal resolving last-wins reads it as a file
// transmission — and the payload, a path we never localised, reaches it
// verbatim. Rather than guess which way any given terminal resolves it, reject
// the sequence: no legitimate sender emits a duplicate control key, and this
// removes the disagreement instead of trying to match it.
func hasDuplicateKey(keys []byte) bool {
	seen := make(map[string]bool)
	for _, kv := range bytes.Split(keys, []byte{','}) {
		i := bytes.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		k := string(kv[:i])
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}

func decodeBare(b []byte) (*Seq, int, bool) {
	if !bytes.HasPrefix(b, []byte(apcStart)) {
		return nil, 0, false
	}
	end := bytes.Index(b[len(apcStart):], []byte(st))
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

// isConfirmedSixelHead reports whether b begins with a sixel DCS introducer
// `\eP[0-9;]*q`. Used after unwrap to decide drop-vs-forward on a complete
// passthrough, and does not treat an incomplete `\eP[0-9;]*` prefix as sixel.
func isConfirmedSixelHead(b []byte) bool {
	if !bytes.HasPrefix(b, []byte(dcsStart)) {
		return false
	}
	j := len(dcsStart)
	for j < len(b) && isSixelParam(b[j]) {
		j++
	}
	return j < len(b) && b[j] == 'q'
}

// isSixelPrefix reports whether b begins with a confirmed sixel introducer or
// an incomplete `\eP[0-9;]*` that has not yet been ruled out — the scanner must
// hold the latter rather than emit it as literal.
func isSixelPrefix(b []byte) bool {
	if !bytes.HasPrefix(b, []byte(dcsStart)) {
		return false
	}
	// `\ePtmux;` shares the `\eP` prefix; passthrough is handled first in
	// decodeSeq, but keep the prefix helper honest for held-byte checks.
	if bytes.HasPrefix(b, []byte(passStart)) {
		return false
	}
	j := len(dcsStart)
	for j < len(b) && isSixelParam(b[j]) {
		j++
	}
	if j >= len(b) {
		return true
	}
	return b[j] == 'q'
}

// consumeBareSixel returns the length of a complete bare sixel DCS at the head
// of b. complete is false when the ST has not arrived yet (caller holds).
func consumeBareSixel(b []byte) (n int, complete bool) {
	if !isConfirmedSixelHead(b) {
		// Incomplete header (`\eP` / `\eP0;1`) — still a sixel prefix, hold.
		return 0, false
	}
	j := len(dcsStart)
	for j < len(b) && isSixelParam(b[j]) {
		j++
	}
	// j points at 'q'.
	end := bytes.Index(b[j+1:], []byte(st))
	if end < 0 {
		return 0, false
	}
	return j + 1 + end + len(st), true
}

// isPartialSixel reports whether held/overflow bytes are a sixel in progress
// (bare, or a passthrough whose undoubled payload so far is sixel-headed).
// Those must be dropped on Flush / maxPartial rather than forwarded.
func isPartialSixel(b []byte) bool {
	if isSixelPrefix(b) {
		return true
	}
	if !bytes.HasPrefix(b, []byte(passStart)) {
		return false
	}
	return isSixelPrefix(peekPassthroughInner(b))
}

// peekPassthroughInner undoubles ESC pairs inside a `\ePtmux;…` wrapper, like
// unwrapPassthrough, but returns whatever has been seen so far when the outer
// ST is missing — enough to classify a partial wrapped sixel for drop-on-cap.
func peekPassthroughInner(b []byte) []byte {
	if !bytes.HasPrefix(b, []byte(passStart)) {
		return nil
	}
	i := len(passStart)
	var inner []byte
	for i < len(b) {
		if b[i] == 0x1b {
			if i+1 >= len(b) {
				return inner
			}
			if b[i+1] == 0x1b {
				inner = append(inner, 0x1b)
				i += 2
				continue
			}
			if b[i+1] == '\\' {
				return inner
			}
		}
		inner = append(inner, b[i])
		i++
	}
	return inner
}

// Get returns the value of a comma-separated control key ("t", "i", "a", …),
// or "" when absent.
func (q *Seq) Get(key string) string {
	for _, kv := range bytes.Split(q.Keys, []byte{','}) {
		i := bytes.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		if string(kv[:i]) == key {
			return string(kv[i+1:])
		}
	}
	return ""
}

// Encode renders the canonical bare form.
func (q *Seq) Encode() []byte {
	out := append([]byte(apcStart), q.Keys...)
	if q.HasBody {
		out = append(out, ';')
		out = append(out, q.Payload...)
	}
	return append(out, st...)
}

// EncodeWrapped renders the bare form inside exactly one tmux passthrough,
// whatever the input form was: the renderer pane always sits inside the local
// tmux, which needs one wrapper to unwrap to the outer terminal.
func (q *Seq) EncodeWrapped() []byte {
	inner := q.Encode()
	out := make([]byte, 0, len(passStart)+2*len(inner)+len(st))
	out = append(out, passStart...)
	for _, c := range inner {
		if c == 0x1b {
			out = append(out, 0x1b)
		}
		out = append(out, c)
	}
	return append(out, st...)
}

// isStore reports whether q transmits image data under an id — the sequences
// coalescing may supersede.
func isStore(q *Seq) bool {
	switch q.Get("a") {
	case "T", "t":
		return q.Get("i") != ""
	}
	return false
}

// isDelete reports whether q deletes an image by id.
func isDelete(q *Seq) bool { return q.Get("a") == "d" && q.Get("i") != "" }
