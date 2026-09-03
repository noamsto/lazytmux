package graphics

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProxyRewritesAndWrapsInOnePass(t *testing.T) {
	p := New(&fakeLocalizer{local: "/local/a.bin"}, nil)
	out := string(p.Filter([]byte("hello " + bareSeq + " bye")))
	if !strings.HasPrefix(out, "hello ") || !strings.HasSuffix(out, " bye") {
		t.Fatalf("literals lost: %q", out)
	}
	if countSub(out, passStart) != 1 {
		t.Fatalf("want exactly one wrapper: %q", out)
	}
	if !strings.Contains(out, "L2xvY2FsL2EuYmlu") { // base64("/local/a.bin")
		t.Fatalf("payload not localised: %q", out)
	}
}

func TestProxyDropsWhatItCannotLocalise(t *testing.T) {
	var logged int
	p := New(&fakeLocalizer{err: errFake}, func(string, ...any) { logged++ })
	out := string(p.Filter([]byte("x" + bareSeq + "y")))
	if out != "xy" {
		t.Fatalf("out = %q, want the literals only", out)
	}
	if logged == 0 {
		t.Fatal("a dropped sequence must be logged")
	}
}

func TestProxyCarriesPartialSequencesAcrossCalls(t *testing.T) {
	p := New(&fakeLocalizer{local: "/local/a.bin"}, nil)
	cut := len(bareSeq) / 2
	if got := string(p.Filter([]byte(bareSeq[:cut]))); got != "" {
		t.Fatalf("emitted a partial sequence: %q", got)
	}
	if got := string(p.Filter([]byte(bareSeq[cut:]))); countSub(got, passStart) != 1 {
		t.Fatalf("second half did not complete the sequence: %q", got)
	}
}

// Close must flush a held partial rather than lose it: the pane can exit with
// a sequence cut mid-stream, and the trailing bytes are still real output.
func TestProxyCloseFlushesAHeldPartial(t *testing.T) {
	p := New(&fakeLocalizer{local: "/local/a.bin"}, nil)
	cut := len(bareSeq) / 2
	if got := string(p.Filter([]byte("x" + bareSeq[:cut]))); got != "x" {
		t.Fatalf("literal before the partial was withheld: %q", got)
	}
	if got := string(p.Close()); got != bareSeq[:cut] {
		t.Fatalf("Close() = %q, want the held partial %q", got, bareSeq[:cut])
	}
}

// blockingLocalizer never returns on its own — it exercises the D4 timeout
// path directly by returning only once its context is cancelled.
type blockingLocalizer struct{}

func (blockingLocalizer) Localize(ctx context.Context, remote string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A fetch that never returns on its own must not freeze the pane forever
// (spec D4): Filter has to come back within its bound, with the store dropped
// down the same path as any other unlocalisable one and the surrounding
// literals still forwarded — the stream resumes, it doesn't just abort.
func TestFilterDropsAFetchThatOutrunsItsDeadline(t *testing.T) {
	var logged int
	p := New(blockingLocalizer{}, func(string, ...any) { logged++ })
	p.timeout = 20 * time.Millisecond

	done := make(chan []byte, 1)
	go func() { done <- p.Filter([]byte("x" + bareSeq + "y")) }()

	select {
	case got := <-done:
		if string(got) != "xy" {
			t.Fatalf("out = %q, want the literals only (store dropped)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Filter did not return within a bounded time")
	}
	if logged == 0 {
		t.Fatal("a timed-out fetch must be logged as a drop")
	}
}

// The localise-or-drop rule (D7) is a security boundary, not just an image
// policy: a t=f payload names a path, and the only paths the local terminal may
// be handed are ones the fetcher wrote. Each input below is a kitty store the
// scanner used to hand on verbatim, carrying the sender's own path straight
// through — so each asserts the payload does not appear in Filter's output, and
// that the localiser was never even consulted (a consulted-and-failed fetch is
// the ordinary drop path, already covered above).
func TestProxyNeverForwardsAnUnlocalisedPath(t *testing.T) {
	// base64("/etc/passwd"), the payload a sender would smuggle.
	const secret = "L2V0Yy9wYXNzd2Q="
	store := "\x1b_Gi=1,a=T,t=f;" + secret + "\x1b\\"

	// wrap doubles the inner ESCs, as a tmux passthrough does, and appends an
	// optional trailer inside the wrapper.
	wrap := func(inner, trailer string) string {
		var b strings.Builder
		b.WriteString("\x1bPtmux;")
		for i := 0; i < len(inner); i++ {
			if inner[i] == 0x1b {
				b.WriteByte(0x1b)
			}
			b.WriteByte(inner[i])
		}
		b.WriteString(trailer)
		b.WriteString("\x1b\\")
		return b.String()
	}

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"trailing byte inside the wrapper", wrap(store, "X")},
		{"inner APC left unterminated", wrap("\x1b_Gi=1,a=T,t=f;"+secret, "")},
		{"duplicate t= key, bare", "\x1b_Gi=1,a=T,t=d,t=f;" + secret + "\x1b\\"},
		{"duplicate t= key, wrapped", wrap("\x1b_Gi=1,a=T,t=d,t=f;"+secret+"\x1b\\", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := &fakeLocalizer{local: "/local/a.bin"}
			p := New(loc, nil)
			out := string(p.Filter([]byte(tc.in)))
			if strings.Contains(out, secret) {
				t.Fatalf("forwarded the sender's own path: %q", out)
			}
			if len(loc.asked) != 0 {
				t.Fatalf("localizer asked %v, want untouched — these drop before any fetch", loc.asked)
			}
		})
	}
}

// The forward-verbatim path must survive: a passthrough carrying an escape that
// is none of ours has no payload to localise, and swallowing it would break
// every non-graphics user of passthrough (OSC 52 and friends).
func TestProxyStillForwardsANonGraphicsPassthrough(t *testing.T) {
	const in = "\x1bPtmux;\x1b\x1b]52;c;aGk=\x1b\x1b\\\x1b\\"
	p := New(&fakeLocalizer{local: "/local/a.bin"}, nil)
	if got := string(p.Filter([]byte(in))); got != in {
		t.Fatalf("forwarded %q, want byte-identical %q", got, in)
	}
}
