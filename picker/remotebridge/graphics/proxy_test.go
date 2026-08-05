package graphics

import (
	"strings"
	"testing"
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
