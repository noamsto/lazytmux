package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSplitPasteDrops(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantKept  string
		wantDrops int
	}{
		{"no ctrl-v", "ls\r", "ls\r", 0},
		{"bare ctrl-v", "\x16", "", 1},
		{"mid-chunk", "a\x16b", "ab", 1},
		{"two in one payload", "\x16\x16", "", 2},
		// Inside a bracketed paste the byte is content, not the gesture.
		{"inside bracketed paste", "\x1b[200~a\x16b\x1b[201~", "\x1b[200~a\x16b\x1b[201~", 0},
		{"before and after bracket", "\x16\x1b[200~\x16\x1b[201~\x16", "\x1b[200~\x16\x1b[201~", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, drops := splitPasteDrops([]byte(c.in))
			if string(kept) != c.wantKept || drops != c.wantDrops {
				t.Errorf("splitPasteDrops(%q) = %q, %d — want %q, %d",
					c.in, kept, drops, c.wantKept, c.wantDrops)
			}
		})
	}
}

func TestImageTarget(t *testing.T) {
	cases := []struct {
		name       string
		targets    []string
		wantTarget string
		wantExt    string
	}{
		{"no image", []string{"text/plain", "UTF8_STRING"}, "", ""},
		{"png", []string{"text/plain", "image/png"}, "image/png", "png"},
		{"jpeg maps to jpg", []string{"image/jpeg"}, "image/jpeg", "jpg"},
		{"png preferred over bmp", []string{"image/bmp", "image/png"}, "image/png", "png"},
		{"bmp only", []string{"image/bmp"}, "image/bmp", "bmp"},
		{"whitespace tolerated", []string{"image/png\r"}, "image/png", "png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, ext := imageTarget(c.targets)
			if target != c.wantTarget || ext != c.wantExt {
				t.Errorf("imageTarget(%v) = %q, %q — want %q, %q",
					c.targets, target, ext, c.wantTarget, c.wantExt)
			}
		})
	}
}

// pasteFixture wires a handler with fakes; every observable the async paste
// goroutine produces arrives on a channel, so tests synchronize with it
// instead of racing it.
type pasteFixture struct {
	h         *pasteHandler
	uploadErr error
	uploadOut string
	sent      chan string
	notified  chan string
}

func newPasteFixture() *pasteFixture {
	f := &pasteFixture{
		uploadOut: "/tmp/lazytmux-paste/img-abc123.png",
		sent:      make(chan string, 4),
		notified:  make(chan string, 1),
	}
	f.h = &pasteHandler{
		upload: func(_ context.Context, _ string, _ []byte) (string, error) {
			return f.uploadOut, f.uploadErr
		},
		readClipboard: func() ([]byte, string, bool, error) {
			return []byte("png-bytes"), "png", true, nil
		},
		procFor: func(string) string { return "claude" },
		notify:  func(msg string) { f.notified <- msg },
	}
	return f
}

func (f *pasteFixture) send(s string) { f.sent <- s }

// awaitOutcome blocks until the async paste produces its first observable
// (a send-keys command or a notify) and returns it with the channel it came
// from drained into the fixture for assertion.
func (f *pasteFixture) awaitOutcome(t *testing.T) (sent, notified string) {
	t.Helper()
	select {
	case s := <-f.sent:
		return s, ""
	case msg := <-f.notified:
		return "", msg
	case <-time.After(5 * time.Second):
		t.Fatal("paste produced no outcome")
		return "", ""
	}
}

func TestHandleForwardsWithoutCtrlV(t *testing.T) {
	f := newPasteFixture()
	in := []byte("ls -la\r")
	if got := f.h.handle("%1", in, f.send); string(got) != string(in) {
		t.Errorf("handle rewrote %q to %q", in, got)
	}
}

func TestHandleForwardsOnNonAgentPane(t *testing.T) {
	f := newPasteFixture()
	f.h.procFor = func(string) string { return "fish" }
	in := []byte("\x16")
	if got := f.h.handle("%1", in, f.send); string(got) != string(in) {
		t.Errorf("shell pane: handle dropped the byte: %q", got)
	}
}

func TestHandleForwardsWhenNoImage(t *testing.T) {
	f := newPasteFixture()
	f.h.readClipboard = func() ([]byte, string, bool, error) { return nil, "", false, nil }
	in := []byte("\x16")
	if got := f.h.handle("%1", in, f.send); string(got) != string(in) {
		t.Errorf("empty clipboard: handle dropped the byte: %q", got)
	}
}

func TestHandleSwallowsAndInjectsOnImage(t *testing.T) {
	f := newPasteFixture()
	got := f.h.handle("%1", []byte("x\x16"), f.send)
	if string(got) != "x" {
		t.Fatalf("handle kept the ctrl+v: %q", got)
	}
	cmd, notified := f.awaitOutcome(t)
	if notified != "" {
		t.Fatalf("unexpected notify: %q", notified)
	}
	// The injection is hex send-keys of the path plus a trailing space
	// (0x20), so what the user types next cannot merge into the path token.
	if !strings.HasPrefix(cmd, "send-keys -H -t %1 ") {
		t.Fatalf("injection %q is not a hex send-keys to the pane", cmd)
	}
	if !strings.HasSuffix(cmd, " 20") {
		t.Errorf("injection %q does not end with the trailing space byte", cmd)
	}
	select {
	case extra := <-f.sent:
		t.Errorf("more than one command sent; extra: %q", extra)
	default:
	}
}

func TestPasteFailuresNotifyAndSendNothing(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*pasteFixture)
		wantInMsg string
	}{
		{"bmp unsupported", func(f *pasteFixture) {
			f.h.readClipboard = func() ([]byte, string, bool, error) { return []byte("b"), "bmp", true, nil }
		}, "not pasteable"},
		{"oversize", func(f *pasteFixture) {
			f.h.readClipboard = func() ([]byte, string, bool, error) {
				return make([]byte, pasteMaxBytes+1), "png", true, nil
			}
		}, "too large"},
		{"upload error", func(f *pasteFixture) {
			f.uploadErr = errors.New("exit status 1")
		}, "upload to remote failed"},
		{"bad reply path", func(f *pasteFixture) {
			f.uploadOut = "/etc/passwd"
		}, "unexpected paste path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPasteFixture()
			c.mutate(f)
			if got := f.h.handle("%1", []byte("\x16"), f.send); len(got) != 0 {
				t.Fatalf("byte not swallowed: %q", got)
			}
			sent, notified := f.awaitOutcome(t)
			if sent != "" {
				t.Fatalf("send-keys ran on failure: %q", sent)
			}
			if !strings.Contains(notified, c.wantInMsg) {
				t.Errorf("notify %q lacks %q", notified, c.wantInMsg)
			}
		})
	}
}

func TestHandleClipboardReadErrorNotifies(t *testing.T) {
	f := newPasteFixture()
	f.h.readClipboard = func() ([]byte, string, bool, error) {
		return nil, "", false, errors.New("xclip died")
	}
	if got := f.h.handle("%1", []byte("\x16"), f.send); len(got) != 0 {
		t.Fatalf("byte not swallowed: %q", got)
	}
	select {
	case msg := <-f.notified:
		if !strings.Contains(msg, "clipboard image read failed") {
			t.Errorf("notify %q lacks the read-failure text", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("no notify on read error")
	}
}

func TestPasterNilWithoutUpload(t *testing.T) {
	var cfg Config
	if cfg.paster() != nil {
		t.Error("paster() non-nil without PasteUpload")
	}
}
