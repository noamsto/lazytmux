package daemon

import (
	"reflect"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestPlanWindow(t *testing.T) {
	L, _ := controlmode.ParseLayout("4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}")
	got := PlanWindow("host-sess:1", L)
	want := [][]string{
		{"resize-window", "-t", "host-sess:1", "-x", "190", "-y", "45"},
		{"split-window", "-h", "-t", "host-sess:1"},
		{"select-layout", "-t", "host-sess:1", "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlanWindow =\n%v\nwant\n%v", got, want)
	}
}

func TestPlanWindowSinglePane(t *testing.T) {
	L, _ := controlmode.ParseLayout("bd67,190x45,0,0,3")
	got := PlanWindow("host-sess:1", L)
	// One pane: no splits; still fit + pin the layout for size determinism.
	want := [][]string{
		{"resize-window", "-t", "host-sess:1", "-x", "190", "-y", "45"},
		{"select-layout", "-t", "host-sess:1", "bd67,190x45,0,0,3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single-pane plan = %v, want %v", got, want)
	}
}

func TestRemotePaneOrder(t *testing.T) {
	L, _ := controlmode.ParseLayout("4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}")
	if got := RemotePaneOrder(L); !reflect.DeepEqual(got, []string{"%0", "%1"}) {
		t.Errorf("RemotePaneOrder = %v, want [%%0 %%1]", got)
	}
}

// TestRemotePaneOrderExcludesFloats pins that a remote floating pane (tmux
// next-3.8's trailing "<...>" layout section) never reaches RemotePaneOrder
// — a float there would make planPaneOps treat it as a newly-appended tiled
// pane and try to split a phantom local pane for it.
func TestRemotePaneOrderExcludesFloats(t *testing.T) {
	L, err := controlmode.ParseLayout("aaee,80x24,0,0{40x24,0,0,0,30x7,9,3,2,39x24,41,0,1}<30x7,9,3,2>")
	if err != nil {
		t.Fatalf("ParseLayout: %v", err)
	}
	if got := RemotePaneOrder(L); !reflect.DeepEqual(got, []string{"%0", "%1"}) {
		t.Errorf("RemotePaneOrder = %v, want [%%0 %%1] (float %%2 excluded)", got)
	}

	// A mirror that already renders %0,%1 must see no ops at all once the
	// remote opens a float — not a phantom Append for %2.
	ops := planPaneOps([]string{"%0", "%1"}, RemotePaneOrder(L))
	want := paneOps{}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("planPaneOps with remote float = %+v, want no-op %+v", ops, want)
	}
}
