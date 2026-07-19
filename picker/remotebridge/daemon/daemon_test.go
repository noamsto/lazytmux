package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// capBuf is a tiny io.Writer that captures what it's given, for asserting
// routed output.
type capBuf struct{ bytes.Buffer }

func newTestReader(stream string) *controlmode.Reader {
	return controlmode.NewReader(strings.NewReader(stream))
}

// A minimal check that the main loop routes %output to a registered pane and stops
// on %exit. Uses a canned reader and a router with a capture sink; the full
// end-to-end path is exercised by the bats integration test (Task 9).
func TestLoopRoutesAndExits(t *testing.T) {
	stream := strings.Join([]string{
		"%output %1 hello",
		"%exit",
	}, "\n") + "\n"
	reader := newTestReader(stream)
	router := NewRouter()
	var sink capBuf
	router.Register("%1", &sink)

	stop := runLoop(reader, router) // extracted inner loop from Run (steps 10-11)
	if !stop {
		t.Fatal("runLoop should return true on an exit line")
	}
	if sink.String() != "hello" {
		t.Errorf("routed %q, want hello", sink.String())
	}
}

func TestLoopStopsOnWindowClose(t *testing.T) {
	stream := strings.Join([]string{
		"%output %1 x",
		"%window-close @1",
	}, "\n") + "\n"
	reader := newTestReader(stream)
	router := NewRouter()

	stop := runLoop(reader, router)
	if !stop {
		t.Fatal("runLoop should return true on a window-close line")
	}
}

func TestLoopReturnsFalseOnEOF(t *testing.T) {
	reader := newTestReader("%output %1 x\n")
	router := NewRouter()

	stop := runLoop(reader, router)
	if stop {
		t.Fatal("runLoop should return false when the stream ends without an exit/window-close line")
	}
}
