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
