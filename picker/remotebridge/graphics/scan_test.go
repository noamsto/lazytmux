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

func TestScanLiteralBeforePartialIsEmittedImmediately(t *testing.T) {
	cs := NewScanner().Feed([]byte("visible\x1b_Gi=1,a=T;abc"))
	if chunkKinds(cs) != "L" || string(cs[0].Literal) != "visible" {
		t.Fatalf("kinds = %q, first = %q", chunkKinds(cs), cs[0].Literal)
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
