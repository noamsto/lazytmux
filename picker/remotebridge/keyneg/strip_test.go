package keyneg

import (
	"strings"
	"testing"
)

func TestFeedStripsModifyOtherKeysSet(t *testing.T) {
	got := NewFilter().Feed([]byte("before\x1b[>4;2mafter"))
	if want := "beforeafter"; string(got) != want {
		t.Fatalf("Feed() = %q, want %q", got, want)
	}
}

func TestFeedStripsModifyOtherKeysReset(t *testing.T) {
	got := NewFilter().Feed([]byte("before\x1b[>4nafter"))
	if want := "beforeafter"; string(got) != want {
		t.Fatalf("Feed() = %q, want %q", got, want)
	}
}

func TestFeedPreservesUnrelatedPrivateCSI(t *testing.T) {
	// "CSI > 1;95;0 c" is a DA2 reply, not modifyOtherKeys — must survive.
	in := "\x1b[>1;95;0c"
	got := NewFilter().Feed([]byte(in))
	if string(got) != in {
		t.Fatalf("Feed() = %q, want unchanged %q", got, in)
	}
}

func TestFeedPreservesOrdinarySGR(t *testing.T) {
	in := "\x1b[31mHello\x1b[0m"
	got := NewFilter().Feed([]byte(in))
	if string(got) != in {
		t.Fatalf("Feed() = %q, want unchanged %q", got, in)
	}
}

func TestFeedSequenceSplitAcrossFeeds(t *testing.T) {
	f := NewFilter()
	seq := "\x1b[>4;2m"
	cut := len(seq) / 2
	got1 := f.Feed([]byte("before" + seq[:cut]))
	if want := "before"; string(got1) != want {
		t.Fatalf("first Feed() = %q, want %q", got1, want)
	}
	got2 := f.Feed([]byte(seq[cut:] + "after"))
	if want := "after"; string(got2) != want {
		t.Fatalf("second Feed() = %q, want %q", got2, want)
	}
}

func TestFeedPrefixSplitAcrossFeeds(t *testing.T) {
	f := NewFilter()
	got1 := f.Feed([]byte("before\x1b["))
	if want := "before"; string(got1) != want {
		t.Fatalf("first Feed() = %q, want %q", got1, want)
	}
	got2 := f.Feed([]byte(">4;2mafter"))
	if want := "after"; string(got2) != want {
		t.Fatalf("second Feed() = %q, want %q", got2, want)
	}
}

func TestFlushReleasesHeldPartial(t *testing.T) {
	f := NewFilter()
	f.Feed([]byte("\x1b[>4;"))
	got := f.Flush()
	if want := "\x1b[>4;"; string(got) != want {
		t.Fatalf("Flush() = %q, want %q", got, want)
	}
	if f.Flush() != nil {
		t.Fatal("second Flush() should return nil once drained")
	}
}

func TestFeedOverlongPendingReleasedAsLiteral(t *testing.T) {
	// Digits keep scanParams genuinely unterminated (a NUL byte would end
	// the params scan immediately, never reaching the maxPending check).
	in := "\x1b[>" + strings.Repeat("9", maxPending+1)
	got := NewFilter().Feed([]byte(in))
	if string(got) != in {
		t.Fatalf("Feed() = %q, want unchanged %q", got, in)
	}
}
