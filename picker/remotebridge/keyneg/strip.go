// Package keyneg strips xterm modifyOtherKeys negotiation sequences from a
// mirrored pane's output stream (#338).
//
// A remote pane's own occupant writes these sequences — "CSI > 4 ; Pv m" to
// request extended key reporting, "CSI > 4 n" to release it — to tell ITS
// terminal how to encode future keystrokes for it: fish requests mode 1 at
// startup, agent CLIs like Claude Code request mode 2. render.Run writes a
// mirrored pane's FrameOutput straight to the local pane's pty, so a
// negotiation sequence the remote occupant wrote reaches the local tmux
// server's parser exactly as if the renderer — the mirror pane's actual
// occupant, which never negotiates anything for itself — had made the
// request. Mode 2 unconditionally extends every key, so a leaked mode-2
// request silently turns a local Ctrl+R keypress into a multi-byte CSI-u
// sequence that the renderer forwards byte-for-byte and the remote shell
// never recognizes as Ctrl+R.
//
// The mirror pane's occupant never requests extended keys for itself, so its
// key mode should never move off tmux's default; any negotiation sequence
// reaching it is bridge leakage, and dropping it has no rendering effect
// either way (it paints nothing).
package keyneg

import "bytes"

// maxPending bounds bytes held waiting for a sequence's final byte. A real
// modifyOtherKeys sequence is a handful of bytes ("\x1b[>4;2m" is 7); this
// much unterminated means it was never one, and the hold is released as
// literal.
const maxPending = 32

const prefix = "\x1b[>"

// Filter strips modifyOtherKeys negotiation sequences from a pane's output
// stream, holding an incomplete trailing sequence until the next Feed. Not
// goroutine-safe — one Filter per pane's output pump, like graphics.Scanner.
type Filter struct{ held []byte }

func NewFilter() *Filter { return &Filter{} }

// Feed returns p with any modifyOtherKeys negotiation sequence removed.
func (f *Filter) Feed(p []byte) []byte {
	buf := p
	if len(f.held) > 0 {
		buf = append(append([]byte(nil), f.held...), p...)
		f.held = nil
	}
	var out []byte
	for len(buf) > 0 {
		i := bytes.Index(buf, []byte(prefix))
		if i < 0 {
			tail := partialPrefixTail(buf)
			out = append(out, buf[:len(buf)-len(tail)]...)
			if len(tail) > 0 {
				f.held = append([]byte(nil), tail...)
			}
			return out
		}
		out = append(out, buf[:i]...)
		buf = buf[i:]

		paramLen, final, complete := scanParams(buf[len(prefix):])
		if !complete {
			if len(buf) > maxPending {
				out = append(out, buf...)
				return out
			}
			f.held = append([]byte(nil), buf...)
			return out
		}
		seqLen := len(prefix) + paramLen
		if final != 'm' && final != 'n' {
			// A different "CSI >" sequence (e.g. DA2 "c", XDA "q"): not
			// ours, forward untouched.
			out = append(out, buf[:seqLen]...)
		}
		buf = buf[seqLen:]
	}
	return out
}

// Flush emits any held partial sequence as a literal — called when a pane's
// sink closes, so held bytes are never silently swallowed.
func (f *Filter) Flush() []byte {
	held := f.held
	f.held = nil
	return held
}

// scanParams walks the parameter bytes (digits and ';') following the
// "\x1b[>" prefix and returns the length of the params-plus-final-byte span,
// the final byte itself, and whether a final byte was found.
func scanParams(p []byte) (length int, final byte, complete bool) {
	for i, b := range p {
		if b >= '0' && b <= '9' || b == ';' {
			continue
		}
		return i + 1, b, true
	}
	return 0, 0, false
}

// partialPrefixTail returns the longest suffix of p that could be the start
// of prefix, so a prefix split across Feed calls is still recognized.
func partialPrefixTail(p []byte) []byte {
	limit := min(len(p), len(prefix)-1)
	for n := limit; n > 0; n-- {
		if bytes.Equal(p[len(p)-n:], []byte(prefix[:n])) {
			return p[len(p)-n:]
		}
	}
	return nil
}
