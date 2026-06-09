package main

import (
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
	m := newModel("dark", splashTips, "`", 10)
	_, cmd := m.Update(timeoutMsg{})
	if !quits(cmd) {
		t.Error("timeoutMsg should produce tea.Quit")
	}
}

func TestFrameAdvances(t *testing.T) {
	m := newModel("dark", splashTips, "`", 10)
	next, cmd := m.Update(frameMsg{})
	if next.(model).frame != 1 {
		t.Errorf("frame = %d, want 1", next.(model).frame)
	}
	if cmd == nil {
		t.Error("frameMsg should re-arm the frame tick")
	}
}

func TestRenderSubstitutesPrefix(t *testing.T) {
	m := newModel("dark", []tip{{Key: "prefix + s", Label: "Sessions"}}, "C-a", 10)
	m.width, m.height = 80, 24
	out := m.View().Content
	if !contains(out, "C-a + s") {
		t.Errorf("rendered tips should substitute prefix; got:\n%s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
