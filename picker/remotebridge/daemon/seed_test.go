package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestPaneSeed(t *testing.T) {
	// Scripted server replies: display-message (cursor/mode) then capture-pane.
	// Each command emits a %begin/…/%end block in issue order.
	stream := strings.Join([]string{
		"%begin 1 1 1", "5 2 0 0", "%end 1 1 1", // display-message: cx cy alt appck
		"%begin 2 2 1", "line-one", "line-two", "%end 2 2 1", // capture-pane
	}, "\n") + "\n"

	reader := controlmode.NewReader(strings.NewReader(stream))
	var sent []string
	send := func(s string) { sent = append(sent, s) }

	got, err := PaneSeed(reader, send, "%3")
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

	reader := controlmode.NewReader(strings.NewReader(stream))
	send := func(string) {}

	got, err := PaneSeed(reader, send, "%3")
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

	reader := controlmode.NewReader(strings.NewReader(stream))
	send := func(string) {}

	got, err := PaneSeed(reader, send, "%3")
	if err != nil {
		t.Fatalf("PaneSeed: want nil error for a blank pane, got %v", err)
	}
	if got == nil {
		t.Errorf("PaneSeed: want a non-nil seed for a blank pane, got nil")
	}
}
