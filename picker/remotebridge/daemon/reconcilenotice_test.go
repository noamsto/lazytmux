package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// noticeUnchangedLayout is a one-pane window, matching reconcilededup_test.go's
// fixture: small enough that "unchanged" and "changed" are unambiguous, and
// single-pane so a pane-set change (adding one) is the simplest possible gate
// 1 trigger.
const noticeUnchangedLayout = "bd67,190x45,0,0,3"

// noticeTwoPaneLayout is a %layout-change reporting the SAME window split into
// two panes — a pane set reconcileLayoutFrom's gate 1 must reject as stale
// once the read reports the remote already back to noticeUnchangedLayout.
const noticeTwoPaneLayout = "beef,190x45,0,0{95x45,0,0,3,95x45,95,0,4}"

// noticeGeometryLayout is the same two panes as reconcilelayout_test.go's
// tiledLayout, resized — same pane set and order, different cell widths, no
// floats — a geometry-only reshape for the gate this commit doesn't add yet.
const noticeGeometryLayout = "cccc,190x45,0,0{100x45,0,0,0,90x45,100,0,1}"

// noticeLine builds a %layout-change line and runs it through
// controlmode.ParseLine, so every test exercises the real field split rather
// than constructing Args by hand. flags == "" produces the 3-field form: tmux
// omits an empty window_printable_flags field rather than emit it blank.
func noticeLine(win, layout, visible, flags string) controlmode.Line {
	text := "%layout-change " + win + " " + layout + " " + visible
	if flags != "" {
		text += " " + flags
	}
	return controlmode.ParseLine(text)
}

// noticeFake is the Config seam for reconcileLayoutFrom tests. LocalTmux
// records every argv it's called with; LocalTmuxOut answers the
// #{window_zoomed_flag} probe with local (err, if set, instead), counting
// every call. When sent is non-nil, a call reached before anything is on the
// wire fails the test outright: a fork must never precede the read a pure
// gate has already decided to take.
type noticeFake struct {
	t     *testing.T
	sent  interface{ Len() int }
	local string
	err   error

	localTmux []string
	zoomAsks  int
}

func (f *noticeFake) config() Config {
	return Config{
		LocalTmux: func(args ...string) error {
			f.localTmux = append(f.localTmux, strings.Join(args, " "))
			return nil
		},
		LocalTmuxOut: func(args ...string) (string, error) {
			if f.sent != nil && f.sent.Len() == 0 {
				f.t.Fatal("LocalTmuxOut (zoom check) called before any read reached the wire")
			}
			f.zoomAsks++
			if f.err != nil {
				return "", f.err
			}
			for _, a := range args {
				if a == "#{window_zoomed_flag}" {
					return f.local, nil
				}
			}
			return "", nil
		},
	}
}

// TestNoticeNoOpWritesNothing is gate 3's whole point: a notification that
// changes nothing — a duplicate, an echo of the daemon's own verb, or the
// trailing line of a push/pop-zoom bracket once its predecessor has already
// landed — costs zero remote round-trips, not merely the one cheap read the
// pre-notification dedup paid.
func TestNoticeNoOpWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		flags string
		local string
	}{
		{"unzoomed", "*", "0\n"},
		{"zoomed", "*Z", "1\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &mirrorWindow{
				remoteID:    "@1",
				localWin:    "@101",
				remotePanes: []string{"%3"},
				localPanes:  []string{"%l3"},
				layout:      noticeUnchangedLayout,
			}
			rt, sent := scriptedRT("")
			// No sent guard here: gate 3's own fork IS what runs before any
			// read on this path — that's the no-op being tested, not a bug.
			fake := &noticeFake{local: c.local}
			l := noticeLine("@1", noticeUnchangedLayout, noticeUnchangedLayout, c.flags)

			if got := reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt); got {
				t.Errorf("retire = true, want false")
			}
			if sent.Len() != 0 {
				t.Errorf("sent %q, want nothing: gate 3's no-op must cost zero remote round-trips", sent.String())
			}
			if len(fake.localTmux) != 0 {
				t.Errorf("LocalTmux calls = %v, want none", fake.localTmux)
			}
			if fake.zoomAsks != 1 {
				t.Errorf("LocalTmuxOut calls = %d, want exactly one (the zoom check)", fake.zoomAsks)
			}

			// Negative control: today's read-first entry cannot keep the
			// stream silent even on the identical fixture, which is what
			// makes the assertion above bite rather than pass vacuously.
			t.Run("negative control", func(t *testing.T) {
				w := &mirrorWindow{
					remoteID:    "@1",
					localWin:    "@101",
					remotePanes: []string{"%3"},
					localPanes:  []string{"%l3"},
					layout:      noticeUnchangedLayout,
				}
				rt, sent := scriptedRT("")
				fake := &noticeFake{local: c.local}
				reconcileLayout(fake.config(), w, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)
				if sent.Len() == 0 {
					t.Error("sent nothing: want the read-first entry to write a display-message even here")
				}
			})
		})
	}
}

// TestNoticeZoomOnReads is #413 arriving as a notification: the flag comes on
// against an unchanged layout while the mirror is still unzoomed, so gate 3
// must read rather than trust the line, and the zoom is then applied from the
// read's own snapshot.
func TestNoticeZoomOnReads(t *testing.T) {
	w := &mirrorWindow{
		remoteID:    "@1",
		localWin:    "@101",
		remotePanes: []string{"%3"},
		localPanes:  []string{"%l3"},
		layout:      noticeUnchangedLayout,
	}
	script := strings.Join([]string{
		"%begin 1 1 1", noticeUnchangedLayout + " %3 1", "%end 1 1 1", // readLayout
		"%begin 1 2 1", noticeUnchangedLayout + " %3 1", "%end 1 2 1", // trailing re-read: converged
	}, "\n") + "\n"
	rt, sent := scriptedRT(script)
	// No sent guard: gate 3's own fork legitimately runs before the read here
	// too — it's what decides the notification can't be trusted and a real
	// zoom mismatch is what sends it to the read in the first place.
	fake := &noticeFake{local: "0\n"}
	l := noticeLine("@1", noticeUnchangedLayout, noticeUnchangedLayout, "*Z")

	reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if !strings.Contains(sent.String(), "window_zoomed_flag") {
		t.Errorf("sent %q, want a readLayout display-message (gate 3: flag mismatch)", sent.String())
	}
	found := false
	for _, c := range fake.localTmux {
		if strings.Contains(c, "resize-pane -Z") {
			found = true
		}
	}
	if !found {
		t.Errorf("LocalTmux calls = %v, want resize-pane -Z applied from the read", fake.localTmux)
	}
}

// TestNoticeUnzoomTransientReads is the push/pop-zoom bracket's transient
// middle line: the flag arrives off against an unchanged layout while the
// mirror is still zoomed, which gate 3 cannot tell apart from a real unzoom
// without reading. Here the read finds the remote still zoomed (the bracket's
// own trailing line hasn't landed yet), so nothing must flap.
func TestNoticeUnzoomTransientReads(t *testing.T) {
	w := &mirrorWindow{
		remoteID:    "@1",
		localWin:    "@101",
		remotePanes: []string{"%3"},
		localPanes:  []string{"%l3"},
		layout:      noticeUnchangedLayout,
	}
	script := strings.Join([]string{
		"%begin 1 1 1", noticeUnchangedLayout + " %3 1", "%end 1 1 1", // readLayout: still zoomed
		"%begin 1 2 1", noticeUnchangedLayout + " %3 1", "%end 1 2 1", // trailing re-read: converged
	}, "\n") + "\n"
	rt, sent := scriptedRT(script)
	// No sent guard: gate 3's own fork is the one that finds the mismatch and
	// sends this to the read.
	fake := &noticeFake{local: "1\n"}
	l := noticeLine("@1", noticeUnchangedLayout, noticeUnchangedLayout, "*")

	reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if !strings.Contains(sent.String(), "window_zoomed_flag") {
		t.Errorf("sent %q, want a readLayout display-message (gate 3: flag mismatch)", sent.String())
	}
	for _, c := range fake.localTmux {
		if strings.Contains(c, "resize-pane -Z") {
			t.Errorf("LocalTmux calls = %v, want no resize-pane -Z: the read found no real unzoom", fake.localTmux)
		}
		if strings.Contains(c, "select-layout") {
			t.Errorf("LocalTmux calls = %v, want no select-layout: the tiled shape never moved", fake.localTmux)
		}
	}
}

// TestNoticeUnknownLocalZoomReads is gate 3's !known case: with no way to
// establish the mirror's own zoom state, the notification cannot be trusted
// either way.
func TestNoticeUnknownLocalZoomReads(t *testing.T) {
	w := &mirrorWindow{
		remoteID:    "@1",
		localWin:    "@101",
		remotePanes: []string{"%3"},
		localPanes:  []string{"%l3"},
		layout:      noticeUnchangedLayout,
	}
	// Empty on purpose: readLayout's display-message reaches the wire before
	// it fails on EOF, and the assertion is only that it was issued — there is
	// nothing here to reply to it.
	rt, sent := scriptedRT("")
	fake := &noticeFake{err: errors.New("boom")}
	l := noticeLine("@1", noticeUnchangedLayout, noticeUnchangedLayout, "*")

	reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if !strings.Contains(sent.String(), "window_zoomed_flag") {
		t.Errorf("sent %q, want a readLayout display-message (local zoom state unknown)", sent.String())
	}
}

// TestNoticePaneSetChangeReads is gate 1: the notification's pane set has
// moved past what the mirror last saw, so it needs the active pane for
// focus-follow and must not be trusted at face value — the read here finds
// the remote already back to one pane, and nothing structural is derived from
// the stale notification.
func TestNoticePaneSetChangeReads(t *testing.T) {
	w := &mirrorWindow{
		remoteID:    "@1",
		localWin:    "@101",
		remotePanes: []string{"%3"},
		localPanes:  []string{"%l3"},
		layout:      noticeUnchangedLayout,
	}
	script := strings.Join([]string{
		"%begin 1 1 1", noticeUnchangedLayout + " %3 0", "%end 1 1 1", // readLayout: remote already back
	}, "\n") + "\n"
	rt, sent := scriptedRT(script)
	fake := &noticeFake{t: t, sent: sent, local: "0\n"}
	l := noticeLine("@1", noticeTwoPaneLayout, noticeTwoPaneLayout, "*")

	reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if !strings.Contains(sent.String(), "window_zoomed_flag") {
		t.Errorf("sent %q, want a readLayout display-message (gate 1: pane set differs)", sent.String())
	}
	for _, c := range fake.localTmux {
		if strings.Contains(c, "split-window") {
			t.Errorf("LocalTmux calls = %v, want no split-window: the read found the remote already back", fake.localTmux)
		}
	}
}

// TestNoticeFloatChangeReads is gate 2: tiledFloatLayout shares tiledLayout's
// Raw by construction (see reconcilelayout_test.go), so this is the one case
// where the float gate has to fire before gate 3 would otherwise have called
// it a no-op.
func TestNoticeFloatChangeReads(t *testing.T) {
	w := shapedMirror(t)
	script := strings.Join([]string{
		"%begin 1 1 1", tiledLayout + " %0 0", "%end 1 1 1", // readLayout: floats unchanged
	}, "\n") + "\n"
	rt, sent := scriptedRT(script)
	fake := &noticeFake{t: t, sent: sent, local: "0\n"}
	l := noticeLine("@1", tiledFloatLayout, tiledFloatLayout, "*")

	reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if !strings.Contains(sent.String(), "window_zoomed_flag") {
		t.Errorf("sent %q, want a readLayout display-message (gate 2: floats differ)", sent.String())
	}
}

// TestNoticeGeometryChangeReadsUntilGate6 pins commit A's placeholder: with no
// gate past 3, ANY layout change — however narrow — still falls through to a
// full read. A later gate that applies a geometry-only reshape straight from
// the notification replaces this test rather than extending it.
func TestNoticeGeometryChangeReadsUntilGate6(t *testing.T) {
	w := shapedMirror(t)
	rt, sent := scriptedRT("")
	fake := &noticeFake{local: "0\n"}
	l := noticeLine("@1", noticeGeometryLayout, noticeGeometryLayout, "*")

	reconcileLayoutFrom(fake.config(), w, l, func(string) {}, NewRouter(), noHellos, newCtlState(), newConverger(), rt)

	if !strings.Contains(sent.String(), "window_zoomed_flag") {
		t.Errorf("sent %q, want a readLayout display-message: a geometry-only reshape still reads until gate 6 lands", sent.String())
	}
}
