// Package graphics localises kitty graphics sequences crossing the remote
// bridge. A store sent by a program on the remote host references a file by
// path (t=f), which the LOCAL terminal cannot read; this package rewrites that
// payload to a local copy. The placement half needs nothing: kitty unicode
// placeholders are ordinary grid text and already cross the bridge.
package graphics

// maxPartial bounds the bytes held waiting for a sequence terminator. A frame
// dropped by the sink's bounded buffer can truncate a sequence mid-flight, and
// without a cap the scanner would swallow the pane's stream forever waiting for
// an ST that was already discarded. On overflow the held bytes are forwarded
// verbatim: a garbled escape beats a dead pane.
const maxPartial = 64 << 10

const (
	apcStart  = "\x1b_G"
	passStart = "\x1bPtmux;"
	st        = "\x1b\\"
)

// Chunk is one piece of a pane's byte stream: either a literal run to forward
// untouched, or one decoded graphics sequence.
type Chunk struct {
	Literal []byte
	Seq     *Seq
}

// Scanner splits a pane's byte stream into Chunks across successive Feed calls,
// holding an incomplete trailing sequence until the rest arrives.
type Scanner struct{ held []byte }

func NewScanner() *Scanner { return &Scanner{} }

// Feed consumes p and returns every chunk that completed. Bytes belonging to a
// sequence whose terminator hasn't arrived are retained for the next call.
func (s *Scanner) Feed(p []byte) []Chunk {
	buf := p
	if len(s.held) > 0 {
		buf = append(s.held, p...)
		s.held = nil
	}
	var out []Chunk
	for len(buf) > 0 {
		i := indexSeqStart(buf)
		if i < 0 {
			out = appendLiteral(out, buf)
			return out
		}
		if i > 0 {
			out = appendLiteral(out, buf[:i])
			buf = buf[i:]
		}
		seq, n := decodeSeq(buf)
		if n == 0 {
			// Incomplete: hold it, unless it has outgrown the cap.
			if len(buf) > maxPartial {
				return appendLiteral(out, buf)
			}
			s.held = append([]byte(nil), buf...)
			return out
		}
		if seq == nil {
			// Complete, but not ours — forward it and carry on scanning.
			out = appendLiteral(out, buf[:n])
		} else {
			out = append(out, Chunk{Seq: seq})
		}
		buf = buf[n:]
	}
	return out
}

// Flush emits any held partial sequence as a literal. Called when a pane's sink
// closes, so held bytes are never silently swallowed.
func (s *Scanner) Flush() []Chunk {
	if len(s.held) == 0 {
		return nil
	}
	out := []Chunk{{Literal: s.held}}
	s.held = nil
	return out
}

func appendLiteral(out []Chunk, b []byte) []Chunk {
	if len(b) == 0 {
		return out
	}
	return append(out, Chunk{Literal: append([]byte(nil), b...)})
}

// indexSeqStart returns the offset of the next graphics sequence start (bare or
// passthrough-wrapped), or -1.
func indexSeqStart(b []byte) int {
	best := -1
	for _, pat := range []string{apcStart, passStart} {
		if i := indexOf(b, pat); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

func indexOf(b []byte, pat string) int {
	for i := 0; i+len(pat) <= len(b); i++ {
		if string(b[i:i+len(pat)]) == pat {
			return i
		}
	}
	return -1
}
