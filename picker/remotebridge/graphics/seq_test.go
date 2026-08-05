package graphics

import "testing"

func TestSeqGet(t *testing.T) {
	cs := NewScanner().Feed([]byte(bareSeq))
	q := cs[0].Seq
	if got := q.Get("t"); got != "f" {
		t.Fatalf("t = %q, want f", got)
	}
	if got := q.Get("i"); got != "31" {
		t.Fatalf("i = %q, want 31", got)
	}
	if got := q.Get("a"); got != "T" {
		t.Fatalf("a = %q, want T", got)
	}
	if got := q.Get("zz"); got != "" {
		t.Fatalf("absent key = %q, want empty", got)
	}
}

func TestEncodeWrappedIsAlwaysExactlyOneWrapper(t *testing.T) {
	for _, in := range []string{bareSeq, wrappedSeq} {
		q := NewScanner().Feed([]byte(in))[0].Seq
		out := string(q.EncodeWrapped())
		if n := countSub(out, passStart); n != 1 {
			t.Fatalf("input %q: %d wrappers, want 1", in, n)
		}
		// Round-trips: unwrapping the output yields the canonical bare form.
		back := NewScanner().Feed([]byte(out))[0].Seq
		if string(back.Keys) != string(q.Keys) || string(back.Payload) != string(q.Payload) {
			t.Fatalf("round trip lost data: %q / %q", back.Keys, back.Payload)
		}
	}
}

// HasBody is what keeps "no payload" distinct from "empty payload", and only
// Encode acts on it. A keys-only sequence (a delete, say) must not gain a ';',
// and one with an empty body must not lose it.
func TestEncodePreservesPresenceOfTheSeparator(t *testing.T) {
	for _, in := range []string{"\x1b_Ga=d,d=A\x1b\\", "\x1b_Ga=d,d=A;\x1b\\"} {
		q := NewScanner().Feed([]byte(in))[0].Seq
		if got := string(q.Encode()); got != in {
			t.Fatalf("Encode = %q, want %q", got, in)
		}
	}
}

func countSub(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
