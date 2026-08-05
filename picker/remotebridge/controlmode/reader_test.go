package controlmode

import (
	"strings"
	"testing"
)

func TestReaderInterleavesReplyAndOutput(t *testing.T) {
	in := strings.Join([]string{
		`%output %1 hi`,
		`%begin 100 7 0`,
		`captured line one`,
		`captured line two`,
		`%end 100 7 0`,
		`%output %1 bye`,
	}, "\n") + "\n"
	rd := NewReader(strings.NewReader(in))

	l, ok := rd.Next()
	if !ok || l.Kind != Output || string(l.Data) != "hi" {
		t.Fatalf("first should be output hi: %+v", l)
	}
	l, ok = rd.Next()
	if !ok || l.Kind != End || l.Args[0] != "100" || !strings.Contains(string(l.Data), "captured line one") {
		t.Fatalf("second should be the completed reply block: %+v", l)
	}
	l, ok = rd.Next()
	if !ok || l.Kind != Output || string(l.Data) != "bye" {
		t.Fatalf("third should be output bye: %+v", l)
	}
	if _, ok = rd.Next(); ok {
		t.Fatal("expected EOF")
	}
}

// TestReaderEmitsNotificationsInsideBlock is #276: tmux emits the notifications
// a command causes inside that command's own block. Folding them into the body
// made them read as command output — a %layout-change became the layout string
// display-message was asked for.
func TestReaderEmitsNotificationsInsideBlock(t *testing.T) {
	in := strings.Join([]string{
		`%begin 100 7 1`,
		`%window-add @12`,
		`the actual reply`,
		`%output %1 mid-block`,
		`%end 100 7 1`,
	}, "\n") + "\n"
	rd := NewReader(strings.NewReader(in))

	l, ok := rd.Next()
	if !ok || l.Kind != WindowAdd || l.Args[0] != "@12" {
		t.Fatalf("first should be the in-block %%window-add: %+v", l)
	}
	l, ok = rd.Next()
	if !ok || l.Kind != Output || string(l.Data) != "mid-block" {
		t.Fatalf("second should be the in-block %%output: %+v", l)
	}
	l, ok = rd.Next()
	if !ok || l.Kind != End || string(l.Data) != "the actual reply" {
		t.Fatalf("third should be the reply, body free of notifications: %+v", l)
	}
	if l.Flags != ClientCommandFlag {
		t.Errorf("reply Flags = %d, want %d", l.Flags, ClientCommandFlag)
	}
	if _, ok = rd.Next(); ok {
		t.Fatal("expected EOF")
	}
}

// TestReaderKeepsUnknownPercentBodyLines: only a known verb is taken for a
// notification, so captured pane content that happens to start with '%' — a zsh
// prompt, an echoed format — stays part of the reply.
func TestReaderKeepsUnknownPercentBodyLines(t *testing.T) {
	in := strings.Join([]string{
		`%begin 100 7 1`,
		`% 50% done`,
		`%end 100 7 1`,
	}, "\n") + "\n"

	l, ok := NewReader(strings.NewReader(in)).Next()
	if !ok || l.Kind != End || string(l.Data) != `% 50% done` {
		t.Fatalf("pane content starting with %% must stay body: %+v", l)
	}
}

// TestReaderHookBlockKeepsItsFlag: a block a remote hook's command produced is
// flagged 0, which is how a reply reader tells it from its own reply.
func TestReaderHookBlockKeepsItsFlag(t *testing.T) {
	l, ok := NewReader(strings.NewReader("%begin 100 8 0\n%end 100 8 0\n")).Next()
	if !ok || l.Kind != End {
		t.Fatalf("want the completed block: %+v", l)
	}
	if l.Flags != 0 {
		t.Errorf("hook block Flags = %d, want 0", l.Flags)
	}
}
