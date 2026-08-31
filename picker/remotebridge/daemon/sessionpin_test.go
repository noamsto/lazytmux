package daemon

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// scriptedRT drives a roundTrip against a canned reply stream, capturing every
// command the pin sends. Blocks in script must be numbered from seq 1.
func scriptedRT(script string) (rt roundTrip, sent *bytes.Buffer) {
	sent = &bytes.Buffer{}
	reader := newTestReader(script)
	st := newStream(sent)
	router := NewRouter()
	async := &asyncQueue{}
	return func(cmd string) (controlmode.Line, bool) {
		seq, ok := st.stamp(cmd)
		if !ok {
			return controlmode.Line{}, false
		}
		return readReplyRouting(reader, router, async, st, seq)
	}, sent
}

func TestParseSessionChanged(t *testing.T) {
	l := controlmode.ParseLine("%session-changed $5 my proj")
	if l.Kind != controlmode.SessionChanged {
		t.Fatalf("kind = %v, want SessionChanged", l.Kind)
	}
	if len(l.Args) != 1 || l.Args[0] != "$5" {
		t.Errorf("args = %v, want [$5]", l.Args)
	}
	// A session name may hold spaces, so the name is the whole rest, not Fields.
	if string(l.Data) != "my proj" {
		t.Errorf("name = %q, want %q", l.Data, "my proj")
	}
}

// An id equal to ours is either the attach-time notification or our own switch
// back landing — neither is an excursion, and reacting to the latter would
// switch in a loop.
func TestSessionPinIgnoresOwnSession(t *testing.T) {
	rt, sent := scriptedRT("%exit\n")
	handedOff := false
	p := &sessionPin{id: "$0", handOff: func(string) { handedOff = true }}

	p.apply(controlmode.ParseLine("%session-changed $0 A"), newRegistry(), NewRouter(), rt)

	if sent.Len() != 0 {
		t.Errorf("sent %q, want nothing", sent.String())
	}
	if handedOff {
		t.Error("handed off on our own session")
	}
}

// A pin that could not resolve its session id must stay out of the way rather
// than switch the client to an empty target.
func TestSessionPinDisabledWithoutID(t *testing.T) {
	rt, sent := scriptedRT("%exit\n")
	p := &sessionPin{}

	p.apply(controlmode.ParseLine("%session-changed $5 other"), newRegistry(), NewRouter(), rt)

	if sent.Len() != 0 {
		t.Errorf("sent %q, want nothing", sent.String())
	}
}

// The whole fix in one pass: a foreign %session-changed switches the client
// back, repaints every mirrored pane (output during the excursion is dropped by
// the server, not buffered), and hands the session we were switched to off to a
// mirror of its own.
func TestSessionPinSwitchesBackReseedsAndHandsOff(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	router := NewRouter()
	router.Register("%1", newOutputSink(local, nil))
	reg := newRegistry()
	mw := reg.add("@1", "@101")
	mw.remotePanes = []string{"%1"}

	// Three replies: switch-client, then PaneSeed's cursor + capture.
	script := strings.Join([]string{
		"%begin 1 1 1",
		"%end 1 1 1",
		"%begin 1 2 1",
		"0 0 0 0",
		"%end 1 2 1",
		"%begin 1 3 1",
		"FRESH-CAPTURE",
		"%end 1 3 1",
	}, "\n") + "\n"
	rt, sent := scriptedRT(script)

	gotHandOff := make(chan string, 1)
	p := &sessionPin{id: "$0", handOff: func(s string) { gotHandOff <- s }}

	go p.apply(controlmode.ParseLine("%session-changed $5 other proj"), reg, router, rt)

	f, err := wire.ReadFrame(peer)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if f.Type != wire.FrameSeed {
		t.Fatalf("frame type = %v, want FrameSeed", f.Type)
	}
	if !bytes.Contains(f.Payload, []byte("FRESH-CAPTURE")) {
		t.Errorf("seed payload = %q, want the fresh capture", f.Payload)
	}
	if s := <-gotHandOff; s != "other proj" {
		t.Errorf("handed off %q, want %q", s, "other proj")
	}

	// No -c: over this stream, "current client" is the control client itself.
	if first := strings.SplitN(sent.String(), "\n", 2)[0]; first != "switch-client -t '$0'" {
		t.Errorf("first command = %q, want the switch back", first)
	}
}

func TestNewSessionPinRejectsNonID(t *testing.T) {
	// A reply that is not a session id (an error text, a truncated read) must
	// not be interpolated into switch-client.
	rt, _ := scriptedRT("%begin 1 1 1\nnot-an-id\n%end 1 1 1\n")
	if p := newSessionPin(Config{RemoteSession: "A"}, rt); p.id != "" {
		t.Errorf("id = %q, want pinning disabled", p.id)
	}
}

func TestNewSessionPinReadsID(t *testing.T) {
	rt, sent := scriptedRT("%begin 1 1 1\n$3\n%end 1 1 1\n")
	p := newSessionPin(Config{RemoteSession: "my proj"}, rt)
	if p.id != "$3" {
		t.Errorf("id = %q, want $3", p.id)
	}
	if !strings.Contains(sent.String(), "-t 'my proj'") {
		t.Errorf("sent %q, want the session name quoted as one token", sent.String())
	}
}
