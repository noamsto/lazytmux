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

// A wrapper holding a kitty sequence plus trailing bytes is DROPPED, not
// forwarded whole.
//
// This assertion is the inverse of what it used to be. Forwarding preserved
// every byte the sender asked us to relay, which reads like the conservative
// choice — but among those bytes is a t=f payload that never passed the
// localiser, so the terminal is handed a path chosen by the far end. D7 already
// settles the direction for a store we cannot localise: drop it, because a
// missing image renders blank and self-heals where a wrong one renders wrong.
//
// The cost is that a legitimate multi-sequence wrapper loses its trailer too.
// Nothing produces one — tmuxPassthrough wraps exactly one sequence — and
// paying that uniformly beats teaching the scanner which payloads are dangerous.
func TestScanWrapperWithTrailingBytesIsDropped(t *testing.T) {
	const in = "\x1bPtmux;\x1b\x1b_Gi=1,a=T;abc\x1b\x1b\\extra\x1b\\"
	s := NewScanner()
	cs := s.Feed([]byte(in))
	if len(cs) != 0 {
		t.Fatalf("emitted %d chunk(s), want the wrapper dropped: %+v", len(cs), cs)
	}
	if s.Malformed != 1 {
		t.Fatalf("Malformed = %d, want 1 — a drop nobody can count is a silent drop", s.Malformed)
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
	// Giving up must not wedge the scanner: nothing stays held going forward,
	// and the very next Feed decodes an ordinary sequence like nothing happened.
	if len(s.held) != 0 {
		t.Fatalf("held %q after overflow, want nothing held", s.held)
	}
	if cs := s.Feed([]byte(bareSeq)); chunkKinds(cs) != "S" {
		t.Fatalf("kinds = %q, want S — scanner did not recover after the overflow", chunkKinds(cs))
	}
}

// TestScanPartialIntroducerAcrossFeeds proves indexSeqStart holds a buffer
// that ends partway through the apcStart or passStart introducer, the way its
// sibling indexSixelStart already holds a partial sixel prefix. A cut that
// lands inside the introducer (with no preceding literal, to isolate this
// from the ordinary split-sequence case already covered above) must emit
// nothing on the first Feed and reassemble into exactly one Seq chunk once
// the rest arrives — matching an unsplit baseline Feed of the same bytes.
func TestScanPartialIntroducerAcrossFeeds(t *testing.T) {
	tests := []struct {
		name    string
		full    string
		cut     int
		wrapped bool
	}{
		{name: "apc cut=1", full: bareSeq, cut: 1},
		{name: "apc cut=2", full: bareSeq, cut: 2},
		{name: "passthrough cut=1", full: tmuxPassthrough(bareSeq), cut: 1, wrapped: true},
		{name: "passthrough cut=2", full: tmuxPassthrough(bareSeq), cut: 2, wrapped: true},
		{name: "passthrough cut=3", full: tmuxPassthrough(bareSeq), cut: 3, wrapped: true},
		{name: "passthrough cut=4", full: tmuxPassthrough(bareSeq), cut: 4, wrapped: true},
		{name: "passthrough cut=5", full: tmuxPassthrough(bareSeq), cut: 5, wrapped: true},
		{name: "passthrough cut=6", full: tmuxPassthrough(bareSeq), cut: 6, wrapped: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseline := NewScanner().Feed([]byte(tt.full))
			if chunkKinds(baseline) != "S" {
				t.Fatalf("baseline kinds = %q, want S", chunkKinds(baseline))
			}

			s := NewScanner()
			buf := []byte(tt.full)
			if cs := s.Feed(buf[:tt.cut]); chunkKinds(cs) != "" {
				t.Fatalf("partial introducer emitted early: %q", chunkKinds(cs))
			}
			cs := s.Feed(buf[tt.cut:])
			if chunkKinds(cs) != "S" {
				t.Fatalf("kinds = %q, want S (introducer split at %d not reassembled)", chunkKinds(cs), tt.cut)
			}
			if cs[0].Seq.Wrapped != tt.wrapped {
				t.Fatalf("Wrapped = %v, want %v", cs[0].Seq.Wrapped, tt.wrapped)
			}
			if string(cs[0].Seq.Keys) != string(baseline[0].Seq.Keys) {
				t.Fatalf("Keys = %q, want %q", cs[0].Seq.Keys, baseline[0].Seq.Keys)
			}
			if string(cs[0].Seq.Payload) != string(baseline[0].Seq.Payload) {
				t.Fatalf("Payload = %q, want %q", cs[0].Seq.Payload, baseline[0].Seq.Payload)
			}
		})
	}
}

// TestScanLiteralNotHeldOnDivergence guards against the introducer-hold fix
// over-holding: bytes speculatively held as a possible apcStart/passStart
// introducer, but which turn out not to continue into one, must eventually
// surface as Literal rather than being silently dropped or held forever.
func TestScanLiteralNotHeldOnDivergence(t *testing.T) {
	const in = "\x1b[31mX"
	s := NewScanner()
	// "\x1b[" matches no prefix of apcStart ("\x1b_G") or passStart
	// ("\x1bPtmux;") past the first byte, so nothing should need holding here
	// even before the fix — this is the control case for the property.
	first := s.Feed([]byte(in[:2]))
	rest := s.Feed([]byte(in[2:]))
	got := concatLiterals(first) + concatLiterals(rest)
	if got != in {
		t.Fatalf("literals = %q, want byte-identical %q (held bytes lost or stuck)", got, in)
	}
	for _, c := range append(first, rest...) {
		if c.Seq != nil {
			t.Fatalf("unrelated CSI decoded as a graphics Seq: %+v", c)
		}
	}
}

// TestScanPartialIntroducerFlush pins Flush() behavior for every partial
// introducer sub-case in TestScanPartialIntroducerAcrossFeeds, held via Feed
// but never completed. In every case but one, Flush must return the held
// bytes byte-identical as a single Literal chunk — the same "never silently
// swallowed" contract TestFlushEmitsHeldPartialThenNothing pins for an
// ordinary partial APC. The one exception is passthrough cut=2, whose held
// bytes are exactly "\x1bP": isPartialSixel treats that as a possible partial
// sixel DCS (indistinguishable at 2 bytes from the start of a bare sixel
// introducer), and Flush's existing, deliberate sixel policy drops it. That
// is not something this fix changes or should change — it is pinned here
// explicitly so a future change to indexFixedStart doesn't accidentally
// alter it.
func TestScanPartialIntroducerFlush(t *testing.T) {
	tests := []struct {
		name string
		full string
		cut  int
	}{
		{name: "apc cut=1", full: bareSeq, cut: 1},
		{name: "apc cut=2", full: bareSeq, cut: 2},
		{name: "passthrough cut=1", full: tmuxPassthrough(bareSeq), cut: 1},
		{name: "passthrough cut=2", full: tmuxPassthrough(bareSeq), cut: 2},
		{name: "passthrough cut=3", full: tmuxPassthrough(bareSeq), cut: 3},
		{name: "passthrough cut=4", full: tmuxPassthrough(bareSeq), cut: 4},
		{name: "passthrough cut=5", full: tmuxPassthrough(bareSeq), cut: 5},
		{name: "passthrough cut=6", full: tmuxPassthrough(bareSeq), cut: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScanner()
			held := []byte(tt.full)[:tt.cut]
			if cs := s.Feed(append([]byte(nil), held...)); chunkKinds(cs) != "" {
				t.Fatalf("partial introducer emitted early: %q", chunkKinds(cs))
			}

			cs := s.Flush()
			if tt.name == "passthrough cut=2" {
				// held == "\x1bP" exactly: isPartialSixel's sixel-ambiguity
				// policy drops it, same as an ordinary partial sixel.
				if cs != nil {
					t.Fatalf("Flush = %+v, want nil (sixel-ambiguous \\x1bP is dropped)", cs)
				}
				return
			}
			if chunkKinds(cs) != "L" {
				t.Fatalf("kinds = %q, want L", chunkKinds(cs))
			}
			if string(cs[0].Literal) != string(held) {
				t.Fatalf("literal = %q, want byte-identical %q", cs[0].Literal, held)
			}
		})
	}
}

// tmuxPassthrough wraps inner the way EncodeWrapped / tmux's passthrough does:
// every ESC doubled, then a final ST.
func tmuxPassthrough(inner string) string {
	var b bytes.Buffer
	b.WriteString(passStart)
	for i := 0; i < len(inner); i++ {
		if inner[i] == 0x1b {
			b.WriteByte(0x1b)
		}
		b.WriteByte(inner[i])
	}
	b.WriteString(st)
	return b.String()
}

func concatLiterals(cs []Chunk) string {
	var b bytes.Buffer
	for _, c := range cs {
		if c.Seq != nil {
			continue
		}
		b.Write(c.Literal)
	}
	return b.String()
}

func TestScanDropsSixel(t *testing.T) {
	const (
		bareSixel    = "\x1bPq#0;2;100;0;0@@@@@@\x1b\\"
		chafaSixel   = "\x1bP0;1;0q\"1;1;40;40#0;2;100;0;0@@@@@@\x1b\\"
		decrqss      = "\x1bP$q\"q\x1b\\"
		osc52pass    = "\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\"
		sixelPayload = "SIXELPAYLOAD"
	)

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "bare sixel mid-literal",
			run: func(t *testing.T) {
				cs := NewScanner().Feed([]byte("before" + bareSixel + "after"))
				if got := concatLiterals(cs); got != "beforeafter" {
					t.Fatalf("literals = %q, want beforeafter", got)
				}
				if chunkKinds(cs) != "LL" {
					t.Fatalf("kinds = %q, want LL (no Seq, no sixel bytes)", chunkKinds(cs))
				}
			},
		},
		{
			name: "parametrized bare sixel (chafa)",
			run: func(t *testing.T) {
				cs := NewScanner().Feed([]byte("x" + chafaSixel + "y"))
				if got := concatLiterals(cs); got != "xy" {
					t.Fatalf("literals = %q, want xy", got)
				}
			},
		},
		{
			name: "passthrough-wrapped sixel",
			run: func(t *testing.T) {
				in := "pre" + tmuxPassthrough(bareSixel) + "post"
				cs := NewScanner().Feed([]byte(in))
				if got := concatLiterals(cs); got != "prepost" {
					t.Fatalf("literals = %q, want prepost", got)
				}
			},
		},
		{
			name: "sixel split across feeds",
			run: func(t *testing.T) {
				s := NewScanner()
				// Mid-header: `\eP0;1` held, then `0q…ST` completes and drops.
				if cs := s.Feed([]byte("a\x1bP0;1")); concatLiterals(cs) != "a" || len(s.held) == 0 {
					t.Fatalf("mid-header: literals=%q held=%q", concatLiterals(cs), s.held)
				}
				if cs := s.Feed([]byte("0q" + sixelPayload + st)); concatLiterals(cs) != "" || len(s.held) != 0 {
					t.Fatalf("after header complete: literals=%q held=%q, want drop", concatLiterals(cs), s.held)
				}
				// Mid-payload: header+partial body held, rest completes and drops.
				partial := "\x1bPq" + sixelPayload[:4]
				if cs := s.Feed([]byte("b" + partial)); concatLiterals(cs) != "b" || len(s.held) == 0 {
					t.Fatalf("mid-payload: literals=%q held=%q", concatLiterals(cs), s.held)
				}
				if cs := s.Feed([]byte(sixelPayload[4:] + st + "c")); concatLiterals(cs) != "c" {
					t.Fatalf("after payload complete: literals=%q, want c", concatLiterals(cs))
				}
			},
		},
		{
			name: "truncated sixel over maxPartial drops payload",
			run: func(t *testing.T) {
				s := NewScanner()
				s.Feed([]byte("\x1bPq"))
				cs := s.Feed(bytes.Repeat([]byte("Z"), maxPartial+1))
				got := concatLiterals(cs)
				if bytes.Contains([]byte(got), []byte("Z")) {
					t.Fatalf("overflow forwarded sixel payload: %q", got)
				}
				if len(s.held) != 0 {
					t.Fatalf("held %q after sixel overflow, want nothing", s.held)
				}
				if cs := s.Feed([]byte("ok" + bareSeq)); concatLiterals(cs) != "ok" || chunkKinds(cs) != "LS" {
					t.Fatalf("kinds=%q literals=%q, want LS / ok — scanner did not recover", chunkKinds(cs), concatLiterals(cs))
				}
			},
		},
		{
			name: "Flush drops held partial sixel",
			run: func(t *testing.T) {
				s := NewScanner()
				if cs := s.Feed([]byte("\x1bP0;1;0q" + sixelPayload)); chunkKinds(cs) != "" {
					t.Fatalf("partial sixel emitted early: %q", chunkKinds(cs))
				}
				if cs := s.Flush(); cs != nil {
					t.Fatalf("Flush = %v, want nil (drop partial sixel)", cs)
				}
				if got := s.Flush(); got != nil {
					t.Fatalf("second Flush = %v, want nil", got)
				}
			},
		},
		{
			name: "non-sixel DCS (DECRQSS) forwarded verbatim",
			run: func(t *testing.T) {
				in := "a" + decrqss + "b"
				cs := NewScanner().Feed([]byte(in))
				if got := concatLiterals(cs); got != in {
					t.Fatalf("forwarded %q, want byte-identical %q", got, in)
				}
			},
		},
		{
			name: "non-sixel passthrough (OSC 52) forwarded verbatim",
			run: func(t *testing.T) {
				cs := NewScanner().Feed([]byte(osc52pass + "tail"))
				if got := concatLiterals(cs); got != osc52pass+"tail" {
					t.Fatalf("forwarded %q, want OSC 52 + tail", got)
				}
				for _, c := range cs {
					if c.Seq != nil {
						t.Fatal("OSC 52 decoded as graphics Seq")
					}
				}
			},
		},
		{
			name: "kitty APC bare and wrapped still decoded",
			run: func(t *testing.T) {
				cs := NewScanner().Feed([]byte(bareSeq + wrappedSeq))
				if chunkKinds(cs) != "SS" {
					t.Fatalf("kinds = %q, want SS", chunkKinds(cs))
				}
				if cs[0].Seq.Wrapped || !cs[1].Seq.Wrapped {
					t.Fatalf("wrapped flags = %v/%v, want false/true", cs[0].Seq.Wrapped, cs[1].Seq.Wrapped)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
