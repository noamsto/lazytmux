package main

import "testing"

func TestTmuxPassthrough(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := tmuxPassthrough("\x1b_Ga=d\x1b\\"); got != "\x1b_Ga=d\x1b\\" {
		t.Errorf("no-tmux passthrough should be identity, got %q", got)
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	got := tmuxPassthrough("\x1b_Ga=d\x1b\\")
	want := "\x1bPtmux;\x1b\x1b_Ga=d\x1b\x1b\\\x1b\\"
	if got != want {
		t.Errorf("tmux passthrough = %q, want %q", got, want)
	}
}

func TestTransmitVirtual(t *testing.T) {
	t.Setenv("TMUX", "")
	// base64 of "/x.png" is "L3gucG5n"
	got := transmitVirtual(7, "/x.png", 20, 10)
	want := "\x1b_Gi=7,a=T,U=1,f=100,c=20,r=10,t=f;L3gucG5n\x1b\\"
	if got != want {
		t.Errorf("transmitVirtual = %q, want %q", got, want)
	}
}

func TestDeleteAll(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := deleteAll(); got != "\x1b_Ga=d,d=A\x1b\\" {
		t.Errorf("deleteAll = %q", got)
	}
}

func TestChooseGridBackend(t *testing.T) {
	cases := []struct {
		term string
		want gridBackend
	}{
		{"xterm-kitty", backendKitty},
		{"xterm-ghostty", backendKitty},
		{"xterm-kitty-something", backendKitty},
		{"foot", backendSymbols},
		{"xterm-256color", backendSymbols},
		{"", backendSymbols},
	}
	for _, c := range cases {
		if got := chooseGridBackend(c.term); got != c.want {
			t.Errorf("chooseGridBackend(%q) = %v, want %v", c.term, got, c.want)
		}
	}
}

func TestPlaceholderBlock(t *testing.T) {
	// id=1 -> fg 0;0;1; 2 cols x 1 row. Cell = U+10EEEE + diacritic[row] + diacritic[col].
	got := placeholderBlock(1, 2, 1)
	want := "\x1b[38;2;0;0;1m" +
		"\U0010EEEE̅̅" + // row 0, col 0
		"\U0010EEEE̅̍" + // row 0, col 1
		"\x1b[39m"
	if got != want {
		t.Errorf("placeholderBlock(1,2,1) =\n%q\nwant\n%q", got, want)
	}
}

func TestPlaceholderBlockTwoRows(t *testing.T) {
	got := placeholderBlock(1, 1, 2)
	want := "\x1b[38;2;0;0;1m\U0010EEEE̅̅\x1b[39m\n" +
		"\x1b[38;2;0;0;1m\U0010EEEE̍̅\x1b[39m"
	if got != want {
		t.Errorf("placeholderBlock(1,1,2) =\n%q\nwant\n%q", got, want)
	}
}

func TestParseManifest(t *testing.T) {
	data := []byte(`{"type":"image","path":"/a/one.png","source":"Read","ts":"t","mtime":1}

  {"type":"image","path":"/b/two.png","source":"Write","ts":"t","mtime":2}
not json
{"type":"image","path":"","source":"Read"}
{"type":"image","path":"/c/three.png","source":"Screenshot"}
`)
	got := parseManifest(data)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (blank/corrupt/empty-path skipped): %+v", len(got), got)
	}
	if got[0].Path != "/a/one.png" || got[0].Source != "Read" {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[2].Path != "/c/three.png" {
		t.Errorf("entry 2 = %+v", got[2])
	}
}
