package daemon

import "testing"

func TestConvergeCmd(t *testing.T) {
	// The per-window form (@id:WxH) — the whole-client form loses to any other
	// client attached to the remote session.
	if got := ConvergeCmd("@3", 210, 52); got != "refresh-client -C @3:210x52" {
		t.Errorf("ConvergeCmd = %q, want %q", got, "refresh-client -C @3:210x52")
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
