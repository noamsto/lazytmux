package screen

import "testing"

func TestFeedRendersText(t *testing.T) {
	s := New(80, 24)
	s.Feed([]byte("hello \x1b[31mworld\x1b[0m"))
	if got := s.Text(); !contains(got, "hello world") {
		t.Fatalf("Text() = %q, want it to contain %q", got, "hello world")
	}
}

func TestCapturesOSCTitle(t *testing.T) {
	s := New(80, 24)
	s.Feed([]byte("\x1b]2;⠐ working\x07")) // OSC 2 set-title, braille glyph
	if got := s.Title(); got != "⠐ working" {
		t.Fatalf("Title() = %q, want %q", got, "⠐ working")
	}
}

func TestAltScreenDetected(t *testing.T) {
	s := New(80, 24)
	s.Feed([]byte("\x1b[?1049h")) // enter alt screen
	if !s.AltScreen() {
		t.Fatal("AltScreen() = false, want true after ?1049h")
	}
}

func contains(h, n string) bool { return len(h) >= len(n) && (indexOf(h, n) >= 0) }
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
