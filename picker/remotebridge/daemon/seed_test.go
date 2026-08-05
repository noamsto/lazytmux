package daemon

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// testRoundTrip wires PaneSeed the way Run does — one stream numbering the
// commands, one reader answering them — with the wire discarded and the command
// lines recorded for assertions.
func testRoundTrip(stream string, sent *[]string) roundTrip {
	reader := controlmode.NewReader(strings.NewReader(stream))
	st := newStream(io.Discard)
	router, async := NewRouter(), &asyncQueue{}
	return func(cmd string) (controlmode.Line, bool) {
		*sent = append(*sent, cmd)
		seq, ok := st.stamp(cmd)
		if !ok {
			return controlmode.Line{}, false
		}
		return readReplyRouting(reader, router, async, st, seq)
	}
}

func TestPaneSeed(t *testing.T) {
	// Scripted server replies: display-message (cursor/mode) then capture-pane.
	// Each command emits a %begin/…/%end block in issue order.
	stream := strings.Join([]string{
		"%begin 1 1 1", "5 2 0 0", "%end 1 1 1", // display-message: cx cy alt appck
		"%begin 2 2 1", "line-one", "line-two", "%end 2 2 1", // capture-pane
	}, "\n") + "\n"

	var sent []string
	got, err := PaneSeed(testRoundTrip(stream, &sent), "%3")
	if err != nil {
		t.Fatalf("PaneSeed: %v", err)
	}
	// Commands must target %3.
	if len(sent) != 2 || !strings.Contains(sent[0], "-t %3") || !strings.Contains(sent[1], "-t %3") {
		t.Fatalf("sent = %v, want display-message + capture-pane targeting %%3", sent)
	}
	// Seed must contain the captured content and a cursor CUP for (5,2) => \x1b[3;6H.
	if !bytes.Contains(got, []byte("line-one")) || !bytes.Contains(got, []byte("\x1b[3;6H")) {
		t.Errorf("seed missing content or cursor CUP: %q", got)
	}
}

func TestPaneSeedErrorReply(t *testing.T) {
	// capture-pane's reply is a %error block (e.g. the pane closed between
	// list-panes and this capture-pane) — PaneSeed must reject it.
	stream := strings.Join([]string{
		"%begin 1 1 1", "5 2 0 0", "%end 1 1 1", // display-message: cx cy alt appck
		"%begin 2 2 1", "%error 2 2 1", // capture-pane: error, no body
	}, "\n") + "\n"

	var sent []string
	got, err := PaneSeed(testRoundTrip(stream, &sent), "%3")
	if err == nil {
		t.Fatalf("PaneSeed: want error on %%error reply, got seed %q", got)
	}
}

func TestPaneSeedEmptyCaptureIsValid(t *testing.T) {
	// capture-pane's reply is a normal %end block with an EMPTY body — a
	// genuinely blank pane, which must NOT be treated as an error (fatal
	// for a sole pane if it were).
	stream := strings.Join([]string{
		"%begin 1 1 1", "5 2 0 0", "%end 1 1 1", // display-message: cx cy alt appck
		"%begin 2 2 1", "%end 2 2 1", // capture-pane: success, empty body
	}, "\n") + "\n"

	var sent []string
	got, err := PaneSeed(testRoundTrip(stream, &sent), "%3")
	if err != nil {
		t.Fatalf("PaneSeed: want nil error for a blank pane, got %v", err)
	}
	if got == nil {
		t.Errorf("PaneSeed: want a non-nil seed for a blank pane, got nil")
	}
}

// TestPaneSeedSurvivesHookBlocks: a remote hook's own %begin..%end (flagged 0)
// landing between our two commands must not be mistaken for either reply.
func TestPaneSeedSurvivesHookBlocks(t *testing.T) {
	stream := strings.Join([]string{
		"%begin 1 1 1", "5 2 0 0", "%end 1 1 1", // display-message
		"%begin 1 2 0", "%end 1 2 0", // a hook's command, not ours
		"%begin 2 3 1", "line-one", "%end 2 3 1", // capture-pane
	}, "\n") + "\n"

	var sent []string
	got, err := PaneSeed(testRoundTrip(stream, &sent), "%3")
	if err != nil {
		t.Fatalf("PaneSeed: %v", err)
	}
	if !bytes.Contains(got, []byte("line-one")) {
		t.Errorf("seed %q lost the capture to a hook block", got)
	}
}
