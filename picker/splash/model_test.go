package main

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestTimeoutQuits(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10, false)
	_, cmd := m.Update(timeoutMsg{})
	if !quits(cmd) {
		t.Error("timeoutMsg should produce tea.Quit")
	}
}

func TestFrameAdvances(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10, false)
	next, cmd := m.Update(frameMsg{})
	if next.(model).frame != 1 {
		t.Errorf("frame = %d, want 1", next.(model).frame)
	}
	if cmd == nil {
		t.Error("frameMsg should re-arm the frame tick")
	}
}

func TestRenderSubstitutesPrefix(t *testing.T) {
	m := newModel("dark", []tip{{Key: "prefix + s", Label: "Sessions"}}, "C-a", 10, false)
	m.width, m.height = 80, 24
	out := m.View().Content
	if !strings.Contains(out, "C-a + s") {
		t.Errorf("rendered tips should substitute prefix; got:\n%s", out)
	}
}

func TestStaticModeRendersSmallFrameImmediately(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10, true)
	if m.frame != introFrames {
		t.Errorf("static model frame = %d, want %d (introFrames)", m.frame, introFrames)
	}
	m.width, m.height = 200, 100 // large enough that the deck frame would normally fit
	frame, ok := m.selectedFrame()
	if !ok {
		t.Fatal("selectedFrame should always succeed in static mode")
	}
	if !reflect.DeepEqual(frame, m.small) {
		t.Error("static mode should always select the small frame, not the animated deck")
	}
}

func TestStaticModeInitSkipsFrameTick(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10, true)
	if cmd := m.Init(); cmd == nil {
		t.Error("static model with a timeout should still arm timeoutCmd")
	}

	m2 := newModel("dark", splashTips, "`", 0, true)
	if cmd := m2.Init(); cmd != nil {
		t.Error("static model with no timeout should return a nil Init cmd (no frame tick, no auto-dismiss)")
	}
}
