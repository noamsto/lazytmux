package daemon

import (
	"reflect"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// TestFloatOuterFromCellProbeRoundTrip pins the measured tmux next-3.8 probe:
// `new-pane -x 60 -y 20 -X 10 -Y 5` yields cell `58x18,11,6`, so the inverse
// must recover the original flags.
func TestFloatOuterFromCellProbeRoundTrip(t *testing.T) {
	c := controlmode.PaneCell{ID: "%2", W: 58, H: 18, X: 11, Y: 6}
	w, h, x, y := outerFromCell(c, 190, 45)
	if w != 60 || h != 20 || x != 10 || y != 5 {
		t.Errorf("outerFromCell = (%d,%d,%d,%d), want (60,20,10,5)", w, h, x, y)
	}
}

func TestFloatOuterFromCellClampsNegativeOffset(t *testing.T) {
	c := controlmode.PaneCell{ID: "%0", W: 40, H: 20, X: 0, Y: 0}
	_, _, x, y := outerFromCell(c, 190, 45)
	if x != 0 || y != 0 {
		t.Errorf("outerFromCell offset = (%d,%d), want (0,0)", x, y)
	}
}

// TestFloatOuterFromCellClampsToWindow covers a `-B none` remote float: a
// zero-inset cell already spanning the whole window would otherwise size the
// outer box past the window's own edge.
func TestFloatOuterFromCellClampsToWindow(t *testing.T) {
	c := controlmode.PaneCell{ID: "%0", W: 190, H: 45, X: 0, Y: 0}
	w, h, x, y := outerFromCell(c, 190, 45)
	if w != 190 || h != 45 {
		t.Errorf("outerFromCell size = (%d,%d), want (190,45)", w, h)
	}
	if x != 0 || y != 0 {
		t.Errorf("outerFromCell offset = (%d,%d), want (0,0)", x, y)
	}
}

// A `-B none` remote float flush against the far edge: the cell's own X plus
// the border the local mirror grows would put the outer box's right edge one
// past the window, so the offset has to give way rather than each axis clamping
// on its own.
func TestFloatOuterFromCellKeepsTheBoxInsideTheWindow(t *testing.T) {
	c := controlmode.PaneCell{ID: "%0", W: 50, H: 20, X: 140, Y: 25}
	w, h, x, y := outerFromCell(c, 190, 45)
	if x+w > 190 || y+h > 45 {
		t.Errorf("outerFromCell = (%d,%d,%d,%d), want a box inside 190x45", w, h, x, y)
	}
	if w != 52 || h != 22 || x != 138 || y != 23 {
		t.Errorf("outerFromCell = (%d,%d,%d,%d), want (52,22,138,23)", w, h, x, y)
	}
}

func TestFloatCreateArgv(t *testing.T) {
	c := controlmode.PaneCell{ID: "%2", W: 58, H: 18, X: 11, Y: 6}
	got := floatCreateArgv("@7", c, 190, 45)
	want := []string{
		"new-pane", "-d", "-P", "-F", "#{pane_id}",
		"-t", "@7",
		"-B", "heavy", "-A",
		"-x", "60", "-y", "20",
		"-X", "10", "-Y", "5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("floatCreateArgv =\n%v\nwant\n%v", got, want)
	}
}

func TestFloatResizeArgv(t *testing.T) {
	c := controlmode.PaneCell{ID: "%2", W: 58, H: 18, X: 11, Y: 6}
	got := floatResizeArgv("%9", c, 190, 45)
	want := []string{"resize-pane", "-t", "%9", "-x", "60", "-y", "20"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("floatResizeArgv = %v, want %v", got, want)
	}
}

func TestFloatMoveArgv(t *testing.T) {
	c := controlmode.PaneCell{ID: "%2", W: 58, H: 18, X: 11, Y: 6}
	got := floatMoveArgv("%9", c, 190, 45)
	want := []string{"move-pane", "-t", "%9", "-X", "10", "-Y", "5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("floatMoveArgv = %v, want %v", got, want)
	}
}

func TestFloatGeomStamp(t *testing.T) {
	c := controlmode.PaneCell{ID: "%2", W: 58, H: 18, X: 11, Y: 6}
	got := floatGeomStamp(c, 190, 45)
	want := "60 20 10 5"
	if got != want {
		t.Errorf("floatGeomStamp = %q, want %q", got, want)
	}
}
