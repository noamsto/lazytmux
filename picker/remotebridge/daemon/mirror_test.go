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
	// One pane: no splits; still pin the layout for size determinism.
	want := [][]string{
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
