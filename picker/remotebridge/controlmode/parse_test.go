package controlmode

import "testing"

func TestUnescape(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{`ls /`, []byte("ls /")},
		{`ls /\015\015\012`, []byte("ls /\r\r\n")},
		{`a\134b`, []byte(`a\b`)},          // \134 == backslash
		{`\033[0m`, []byte("\x1b[0m")},     // ESC then literal
		{`\342\230\203`, []byte{0xe2, 0x98, 0x83}}, // a UTF-8 rune as raw bytes
	}
	for _, c := range cases {
		got := Unescape(c.in)
		if string(got) != string(c.want) {
			t.Errorf("Unescape(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
