package graphics

import (
	"bytes"
	"testing"
)

const (
	bareSeq    = "\x1b_Gi=31,a=T,U=1,f=100,t=f;L3RtcC94LnBuZw==\x1b\\"
	wrappedSeq = "\x1bPtmux;\x1b\x1b_Gi=31,a=T,U=1,f=100,t=f;L3RtcC94LnBuZw==\x1b\x1b\\\x1b\\"
)

func chunkKinds(cs []Chunk) string {
	var b bytes.Buffer
	for _, c := range cs {
		if c.Seq != nil {
			b.WriteByte('S')
		} else {
			b.WriteByte('L')
		}
	}
	return b.String()
}

func TestScanBareSequence(t *testing.T) {
	cs := NewScanner().Feed([]byte("before" + bareSeq + "after"))
	if got := chunkKinds(cs); got != "LSL" {
		t.Fatalf("kinds = %q, want LSL", got)
	}
	if got := string(cs[1].Seq.Keys); got != "i=31,a=T,U=1,f=100,t=f" {
		t.Fatalf("keys = %q", got)
	}
	if got := string(cs[1].Seq.Payload); got != "L3RtcC94LnBuZw==" {
		t.Fatalf("payload = %q", got)
	}
	if cs[1].Seq.Wrapped {
		t.Fatal("bare sequence reported as wrapped")
	}
}

func TestScanWrappedSequenceUndoublesEscapes(t *testing.T) {
	cs := NewScanner().Feed([]byte(wrappedSeq))
	if got := chunkKinds(cs); got != "S" {
		t.Fatalf("kinds = %q, want S", got)
	}
	if !cs[0].Seq.Wrapped {
		t.Fatal("wrapped sequence not flagged")
	}
	if got := string(cs[0].Seq.Keys); got != "i=31,a=T,U=1,f=100,t=f" {
		t.Fatalf("keys = %q", got)
	}
}

func TestScanSequenceSplitAcrossFeeds(t *testing.T) {
	s := NewScanner()
	cut := len(bareSeq) / 2
	if cs := s.Feed([]byte(bareSeq[:cut])); chunkKinds(cs) != "" {
		t.Fatalf("partial sequence emitted early: %q", chunkKinds(cs))
	}
	cs := s.Feed([]byte(bareSeq[cut:]))
	if chunkKinds(cs) != "S" {
		t.Fatalf("kinds = %q, want S", chunkKinds(cs))
	}
}

// A passthrough carrying something other than a graphics APC is complete, not
// partial: it must be forwarded at once, or every later byte of the pane queues
// behind it until the partial cap.
func TestScanCompleteNonGraphicsPassthroughForwardsVerbatim(t *testing.T) {
	const in = "\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\tail"
	s := NewScanner()
	cs := s.Feed([]byte(in))
	var got []byte
	for _, c := range cs {
		if c.Seq != nil {
			t.Fatal("OSC 52 passthrough decoded as a graphics sequence")
		}
		got = append(got, c.Literal...)
	}
	if string(got) != in {
		t.Fatalf("forwarded %q, want byte-identical %q", got, in)
	}
	if len(s.held) != 0 {
		t.Fatalf("held %q, want nothing held", s.held)
	}
}

// A wrapper holding more than its first sequence is forwarded whole. Decoding
// only the leading sequence would drop whatever trailed it, and a proxy must
// never lose bytes it was asked to relay.
func TestScanWrapperWithTrailingBytesForwardsWhole(t *testing.T) {
	const in = "\x1bPtmux;\x1b\x1b_Gi=1,a=T;abc\x1b\x1b\\extra\x1b\\"
	s := NewScanner()
	cs := s.Feed([]byte(in))
	var got []byte
	for _, c := range cs {
		if c.Seq != nil {
			t.Fatal("wrapper with trailing bytes decoded as a graphics sequence")
		}
		got = append(got, c.Literal...)
	}
	if string(got) != in {
		t.Fatalf("forwarded %q, want byte-identical %q", got, in)
	}
	if len(s.held) != 0 {
		t.Fatalf("held %q, want nothing held", s.held)
	}
}

func TestScanRecoversAfterNonGraphicsPassthrough(t *testing.T) {
	cs := NewScanner().Feed([]byte("\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\" + bareSeq))
	if got := chunkKinds(cs); got != "LS" {
		t.Fatalf("kinds = %q, want LS", got)
	}
	if got := string(cs[1].Seq.Keys); got != "i=31,a=T,U=1,f=100,t=f" {
		t.Fatalf("keys = %q", got)
	}
}

// The mirror of the case above: an unterminated passthrough is genuinely
// partial and must still be held, not forwarded.
func TestScanIncompletePassthroughIsHeld(t *testing.T) {
	s := NewScanner()
	if cs := s.Feed([]byte("\x1bPtmux;\x1b\x1b_Gi=1,a=T;abc")); chunkKinds(cs) != "" {
		t.Fatalf("incomplete passthrough emitted early: %q", chunkKinds(cs))
	}
	cs := s.Feed([]byte("\x1b\x1b\\\x1b\\"))
	if chunkKinds(cs) != "S" {
		t.Fatalf("kinds = %q, want S", chunkKinds(cs))
	}
}

func TestScanLiteralBeforePartialIsEmittedImmediately(t *testing.T) {
	cs := NewScanner().Feed([]byte("visible\x1b_Gi=1,a=T;abc"))
	if chunkKinds(cs) != "L" || string(cs[0].Literal) != "visible" {
		t.Fatalf("kinds = %q, first = %q", chunkKinds(cs), cs[0].Literal)
	}
}

// Flush is the whole mechanism behind "held bytes are never silently
// swallowed", so both directions are pinned: a held partial comes back out as a
// literal, and a Scanner holding nothing stays quiet. The second call also
// proves Flush clears what it emitted — re-emitting would duplicate those bytes
// into the pane.
func TestFlushEmitsHeldPartialThenNothing(t *testing.T) {
	const partial = "\x1b_Gi=1,a=T;abc"
	s := NewScanner()
	if cs := s.Feed([]byte(partial)); chunkKinds(cs) != "" {
		t.Fatalf("partial emitted early: %q", chunkKinds(cs))
	}
	cs := s.Flush()
	if chunkKinds(cs) != "L" || string(cs[0].Literal) != partial {
		t.Fatalf("kinds = %q literal = %q, want L / %q", chunkKinds(cs), cs[0].Literal, partial)
	}
	if got := s.Flush(); got != nil {
		t.Fatalf("second Flush = %v, want nil", got)
	}
	if got := NewScanner().Flush(); got != nil {
		t.Fatalf("fresh Flush = %v, want nil", got)
	}
}

func TestScanOversizedPartialFlushesAsLiteral(t *testing.T) {
	s := NewScanner()
	s.Feed([]byte("\x1b_Gi=1,a=T;"))
	cs := s.Feed(bytes.Repeat([]byte("A"), maxPartial+1))
	if chunkKinds(cs) != "L" {
		t.Fatalf("kinds = %q, want L (give up, forward verbatim)", chunkKinds(cs))
	}
}
