package main

import (
	"io"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// TestSeedFlowTTYAlignsRepliesWithCommands is the regression guard for the
// tty seed path: on a real tty refresh-client is sent and emits its own
// %begin/%end reply, so every reply-reading step must consume exactly its
// own command's reply. The scripted stream mirrors what tmux next-3.8 emits
// (attach preamble + %session-changed / %layout-change notifications
// interleaved with the command replies).
//
// Pre-fix (positional reads with no attach drain and no refresh-client
// consume, plus "refresh-client -C -x W -y H") this fails: readCursor eats
// the empty refresh-client reply and readCapture eats the cursor reply, so
// the real capture is dropped and the refresh-client syntax errors.
func TestSeedFlowTTYAlignsRepliesWithCommands(t *testing.T) {
	stream := strings.Join([]string{
		`%begin 100 1 0`, // implicit attach-session ack (empty body)
		`%end 100 1 0`,
		`%session-changed $0 src`,
		`%begin 100 2 1`, // list-panes reply
		`0 %5`,
		`1 %7`,
		`%end 100 2 1`,
		`%begin 100 3 1`, // refresh-client reply (empty)
		`%end 100 3 1`,
		`%layout-change @0 aaaa,80x24,0,0,0`,
		`%begin 100 4 1`, // display-message (cursor) reply
		`12 5 1 1`,
		`%end 100 4 1`,
		`%begin 100 5 1`, // capture-pane reply
		`SCREEN_LINE_ONE`,
		`SCREEN_LINE_TWO`,
		`%end 100 5 1`,
	}, "\n") + "\n"

	reader := controlmode.NewReader(strings.NewReader(stream))
	var sent []string
	send := func(s string) { sent = append(sent, s) }

	s, err := seedFlow(reader, send, "src", 0, true, 80, 24)
	if err != nil {
		t.Fatalf("seedFlow: %v", err)
	}
	if s.pane != "%7" {
		t.Errorf("pane = %q, want %%7", s.pane)
	}
	if s.cx != 12 || s.cy != 5 || !s.alt || !s.appck {
		t.Errorf("cursor = (%d,%d,alt=%v,appck=%v), want (12,5,true,true)", s.cx, s.cy, s.alt, s.appck)
	}
	got := string(s.captured)
	if !strings.Contains(got, "SCREEN_LINE_ONE") || !strings.Contains(got, "SCREEN_LINE_TWO") {
		t.Errorf("captured = %q, want the capture-pane body", got)
	}

	// C2: next-3.8 wants a single WxH arg.
	var refresh string
	for _, cmd := range sent {
		if strings.HasPrefix(cmd, "refresh-client") {
			refresh = cmd
		}
	}
	if refresh != "refresh-client -C 80x24" {
		t.Errorf("refresh-client = %q, want %q", refresh, "refresh-client -C 80x24")
	}
}

// TestSeedFlowNonTTYSkipsRefresh checks the non-tty path never sends
// refresh-client and still resolves the pane + capture.
func TestSeedFlowNonTTYSkipsRefresh(t *testing.T) {
	stream := strings.Join([]string{
		`%begin 100 1 0`,
		`%end 100 1 0`,
		`%session-changed $0 src`,
		`%begin 100 2 1`,
		`1 %3`,
		`%end 100 2 1`,
		`%begin 100 3 1`,
		`4 2 0 0`,
		`%end 100 3 1`,
		`%begin 100 4 1`,
		`ONLY_LINE`,
		`%end 100 4 1`,
	}, "\n") + "\n"

	reader := controlmode.NewReader(strings.NewReader(stream))
	var sent []string
	send := func(s string) { sent = append(sent, s) }

	s, err := seedFlow(reader, send, "src", 0, false, 0, 0)
	if err != nil {
		t.Fatalf("seedFlow: %v", err)
	}
	if s.pane != "%3" {
		t.Errorf("pane = %q, want %%3", s.pane)
	}
	if string(s.captured) != "ONLY_LINE" {
		t.Errorf("captured = %q, want ONLY_LINE", string(s.captured))
	}
	for _, cmd := range sent {
		if strings.HasPrefix(cmd, "refresh-client") {
			t.Errorf("non-tty path sent %q, expected no refresh-client", cmd)
		}
	}
}

// TestSeedFlowPipelinesDisplayMessageAndCapturePane asserts that both are
// written before either reply is read. The two tests above hold the whole
// script in a strings.Reader, so they'd pass whether display-message and
// capture-pane are pipelined or sent one at a time — this one uses a reader
// that withholds their replies until both commands have been recorded in
// sent, which only a pipelined seedFlow can satisfy.
//
// The gate is staged, not blanket: it serves the script freely through the
// list-panes reply and only then starts checking sent, because a bufio.Scanner
// latches done permanently on the first EOF (controlmode/parse.go:148-151) —
// gating from byte 0 would kill the scanner on readActivePane's very first
// read and fail a correct implementation too.
func TestSeedFlowPipelinesDisplayMessageAndCapturePane(t *testing.T) {
	pre := strings.Join([]string{
		`%begin 100 1 0`,
		`%end 100 1 0`,
		`%session-changed $0 src`,
		`%begin 100 2 1`, // list-panes reply
		`1 %3`,
		`%end 100 2 1`,
	}, "\n") + "\n"
	post := strings.Join([]string{
		`%begin 100 3 1`, // display-message (cursor) reply
		`4 2 0 0`,
		`%end 100 3 1`,
		`%begin 100 4 1`, // capture-pane reply
		`ONLY_LINE`,
		`%end 100 4 1`,
	}, "\n") + "\n"

	var sent []string
	send := func(s string) { sent = append(sent, s) }
	ready := func() bool {
		var haveDM, haveCap bool
		for _, s := range sent {
			haveDM = haveDM || strings.HasPrefix(s, "display-message")
			haveCap = haveCap || strings.HasPrefix(s, "capture-pane")
		}
		return haveDM && haveCap
	}
	gated := &gatedReader{data: []byte(pre + post), free: len(pre), ready: ready}
	reader := controlmode.NewReader(gated)

	s, err := seedFlow(reader, send, "src", 0, false, 0, 0)
	if err != nil {
		t.Fatalf("seedFlow: %v (serialized writes would starve the gated reader)", err)
	}
	if s.pane != "%3" {
		t.Errorf("pane = %q, want %%3", s.pane)
	}
	if s.cx != 4 || s.cy != 2 || s.alt || s.appck {
		t.Errorf("cursor = (%d,%d,alt=%v,appck=%v), want (4,2,false,false)", s.cx, s.cy, s.alt, s.appck)
	}
	if string(s.captured) != "ONLY_LINE" {
		t.Errorf("captured = %q, want ONLY_LINE", string(s.captured))
	}
}

// gatedReader serves data freely up to free, then withholds everything past it
// (returning EOF) until ready reports true — used to prove a caller writes
// every command of a pipelined pair before reading either reply.
type gatedReader struct {
	data  []byte
	pos   int
	free  int
	ready func() bool
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if g.pos >= len(g.data) {
		return 0, io.EOF
	}
	end := len(g.data)
	if g.pos < g.free {
		end = g.free
	} else if !g.ready() {
		return 0, io.EOF
	}
	n := copy(p, g.data[g.pos:end])
	g.pos += n
	return n, nil
}
