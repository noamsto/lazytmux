package controlmode

import "testing"

func TestUnescape(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{`ls /`, []byte("ls /")},
		{`ls /\015\015\012`, []byte("ls /\r\r\n")},
		{`a\134b`, []byte(`a\b`)},                  // \134 == backslash
		{`\033[0m`, []byte("\x1b[0m")},             // ESC then literal
		{`\342\230\203`, []byte{0xe2, 0x98, 0x83}}, // a UTF-8 rune as raw bytes
	}
	for _, c := range cases {
		got := Unescape(c.in)
		if string(got) != string(c.want) {
			t.Errorf("Unescape(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseLine(t *testing.T) {
	l := ParseLine(`%output %3 ab\015`)
	if l.Kind != Output || l.Pane != "%3" || string(l.Data) != "ab\r" {
		t.Fatalf("output parse wrong: %+v", l)
	}
	if ParseLine(`%begin 1700000000 1 0`).Kind != Begin {
		t.Error("begin")
	}
	if ParseLine(`%end 1700000000 1 0`).Kind != End {
		t.Error("end")
	}
	if ParseLine(`%error 1700000000 1 0`).Kind != Error {
		t.Error("error")
	}
	if ParseLine(`%window-close @5`).Kind != WindowClose {
		t.Error("window-close")
	}
	if ParseLine(`%exit`).Kind != Exit {
		t.Error("exit")
	}
	if ParseLine(`%unlinked-window-add @9`).Kind != Other {
		t.Error("unlinked should be Other")
	}
}

func TestParseM22Notifications(t *testing.T) {
	if l := ParseLine(`%window-add @7`); l.Kind != WindowAdd || len(l.Args) != 1 || l.Args[0] != "@7" {
		t.Errorf("window-add: %+v", l)
	}
	l := ParseLine(`%window-renamed @7 my long name`)
	if l.Kind != WindowRenamed || l.Args[0] != "@7" || string(l.Data) != "my long name" {
		t.Errorf("window-renamed (name must stay whole): %+v", l)
	}
	if l := ParseLine(`%session-window-changed $2 @7`); l.Kind != SessionWindowChanged || l.Args[0] != "$2" || l.Args[1] != "@7" {
		t.Errorf("session-window-changed: %+v", l)
	}
	if l := ParseLine(`%window-pane-changed @7 %12`); l.Kind != WindowPaneChanged || l.Args[0] != "@7" || l.Args[1] != "%12" {
		t.Errorf("window-pane-changed: %+v", l)
	}
	if l := ParseLine(`%pause %12`); l.Kind != Pause || l.Args[0] != "%12" {
		t.Errorf("pause: %+v", l)
	}
	if l := ParseLine(`%continue %12`); l.Kind != Continue || l.Args[0] != "%12" {
		t.Errorf("continue: %+v", l)
	}
	if ParseLine(`%unlinked-window-add @9`).Kind != Other {
		t.Error("unlinked-window-add must stay Other")
	}
}

func TestParseExtendedOutput(t *testing.T) {
	// With flow control armed (refresh-client -f pause-after=N), tmux switches
	// the output notification to %extended-output %pane <age-ms> : <escaped>.
	// It must parse to the same Output line as %output (age dropped) or live
	// output is silently dropped (#183).
	l := ParseLine(`%extended-output %3 0 : ab\015`)
	if l.Kind != Output || l.Pane != "%3" || string(l.Data) != "ab\r" {
		t.Fatalf("extended-output parse wrong: %+v", l)
	}
	// Non-zero age, and data that itself contains " : " — split only on the
	// first separator so the payload stays whole.
	l2 := ParseLine(`%extended-output %0 42 : a : b`)
	if l2.Kind != Output || l2.Pane != "%0" || string(l2.Data) != "a : b" {
		t.Fatalf("extended-output age/colon wrong: %+v", l2)
	}
}
