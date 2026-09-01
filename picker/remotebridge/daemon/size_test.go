package daemon

import (
	"strings"
	"testing"
)

func TestConvergeCmd(t *testing.T) {
	// The per-window form (@id:WxH) — the whole-client form loses to any other
	// client attached to the remote session.
	if got := ConvergeCmd("@3", 210, 52); got != "refresh-client -C @3:210x52" {
		t.Errorf("ConvergeCmd = %q, want %q", got, "refresh-client -C @3:210x52")
	}
}

// Without a size of its own the control client is 80 columns and no rows, and
// under `window-size latest` a window created on the remote is born at that.
func TestClientSizeCmd(t *testing.T) {
	// The whole-client form: no @id, so it is the client's own size rather
	// than a per-window cap.
	if got := ClientSizeCmd(210, 52); got != "refresh-client -C 210x52" {
		t.Errorf("ClientSizeCmd = %q, want %q", got, "refresh-client -C 210x52")
	}
	if strings.Contains(ClientSizeCmd(210, 52), "@") {
		t.Error("the client size must not carry a window target")
	}
}

// The client size shares the converger's map, so it re-sends on a change and is
// skipped otherwise — and never collides with a window, whose ids are all @N.
func TestConvergerTracksClientSizeSeparately(t *testing.T) {
	cv := newConverger()

	if !cv.need(clientSizeKey, 100, 30) {
		t.Fatal("first client size should be needed")
	}
	if cv.need(clientSizeKey, 100, 30) {
		t.Error("re-asserting the same client size should be skipped")
	}
	// A window at the same size is tracked independently of the client.
	if !cv.need("@1", 100, 30) {
		t.Error("a window's first cap should be needed, not shadowed by the client")
	}
	if !cv.need(clientSizeKey, 120, 40) {
		t.Error("a changed client size should be needed")
	}
	if strings.HasPrefix(clientSizeKey, "@") {
		t.Errorf("clientSizeKey %q could collide with a remote window id", clientSizeKey)
	}
}

func TestConvergerNeedTracksPerWindow(t *testing.T) {
	cv := newConverger()

	if !cv.need("@1", 100, 30) {
		t.Fatal("first assertion for @1 should be needed")
	}
	if cv.need("@1", 100, 30) {
		t.Error("re-asserting the same size for @1 should be skipped")
	}
	// A sibling window has its own last-asserted size.
	if !cv.need("@2", 100, 30) {
		t.Error("first assertion for @2 should be needed")
	}
	if !cv.need("@1", 120, 40) {
		t.Error("a changed size for @1 should be needed")
	}

	// A closed window forgets, so a re-add re-asserts rather than assuming the
	// remote still holds the old cap.
	cv.forget("@1")
	if !cv.need("@1", 120, 40) {
		t.Error("after forget, @1 should re-assert")
	}
}
