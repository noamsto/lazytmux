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
