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

// decodeSeq decodes the sequence at the head of b, returning it and the number
// of bytes consumed. ok is false when the terminator hasn't arrived yet.
func decodeSeq(b []byte) (*Seq, int, bool) {
	if bytes.HasPrefix(b, []byte(passStart)) {
		inner, n, ok := unwrapPassthrough(b)
		if !ok {
			return nil, 0, false
		}
		q, _, ok := decodeBare(inner)
		if !ok {
			return nil, 0, false
		}
		q.Wrapped = true
		return q, n, true
	}
	return decodeBare(b)
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
