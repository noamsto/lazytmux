package daemon

import (
	"bytes"
	"testing"
)

func TestRouterRoutesByPane(t *testing.T) {
	r := NewRouter()
	var a, b bytes.Buffer
	r.Register("%1", &a)
	r.Register("%2", &b)

	r.Route("%1", []byte("one"))
	r.Route("%2", []byte("two"))
	r.Route("%1", []byte("-more"))
	r.Route("%9", []byte("dropped")) // unregistered: silently dropped

	if a.String() != "one-more" {
		t.Errorf("pane %%1 got %q, want %q", a.String(), "one-more")
	}
	if b.String() != "two" {
		t.Errorf("pane %%2 got %q, want %q", b.String(), "two")
	}

	r.Unregister("%1")
	r.Route("%1", []byte("after-unregister"))
	if a.String() != "one-more" {
		t.Errorf("pane %%1 received after unregister: %q", a.String())
	}
}

func TestRouterDemuxAcrossWindows(t *testing.T) {
	r := NewRouter()
	var a, b capBuf // capBuf from daemon_test.go (same package)
	r.Register("%1", &a) // window @1's pane
	r.Register("%9", &b) // window @2's pane
	r.Route("%1", []byte("A1"))
	r.Route("%9", []byte("B9"))
	r.Route("%99", []byte("DROP")) // no sink registered
	if a.String() != "A1" || b.String() != "B9" {
		t.Fatalf("misrouted: a=%q b=%q", a.String(), b.String())
	}
	// Unregister %1 (its window closed); further output for it is dropped.
	r.Unregister("%1")
	r.Route("%1", []byte("X"))
	if a.String() != "A1" {
		t.Errorf("output after unregister leaked: %q", a.String())
	}
}

func TestCloseWindowUnregistersOnlyItsPanes(t *testing.T) {
	reg := newRegistry(1)
	w1 := reg.add("@1", "h-s:1")
	w1.remotePanes = []string{"%1", "%2"}
	w2 := reg.add("@2", "h-s:2")
	w2.remotePanes = []string{"%9"}
	router := NewRouter()
	var s1, s2, s9 capBuf
	router.Register("%1", &s1)
	router.Register("%2", &s2)
	router.Register("%9", &s9)
	cfg := Config{LocalTmux: func(...string) error { return nil }}

	closeWindow(cfg, router, reg, newConverger(), "@1")

	router.Route("%1", []byte("x"))
	router.Route("%2", []byte("y"))
	router.Route("%9", []byte("z"))
	if s1.String() != "" || s2.String() != "" {
		t.Errorf("closed window's panes still routed: %q %q", s1.String(), s2.String())
	}
	if s9.String() != "z" {
		t.Errorf("sibling window's pane stopped routing: %q", s9.String())
	}
	if _, ok := reg.byRemoteID("@1"); ok {
		t.Error("@1 still in registry after close")
	}
}
