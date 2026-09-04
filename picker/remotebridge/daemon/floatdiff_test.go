package daemon

import (
	"reflect"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestPlanFloatOps(t *testing.T) {
	cellA := controlmode.PaneCell{ID: "%2", W: 30, H: 7, X: 9, Y: 3}
	cellAMoved := controlmode.PaneCell{ID: "%2", W: 40, H: 10, X: 0, Y: 0}
	cellB := controlmode.PaneCell{ID: "%5", W: 20, H: 5, X: 1, Y: 1}

	tests := []struct {
		name string
		have map[string]controlmode.PaneCell
		want []controlmode.PaneCell
		ops  floatOps
	}{
		{
			name: "no-op",
			have: map[string]controlmode.PaneCell{"%2": cellA},
			want: []controlmode.PaneCell{cellA},
			ops:  floatOps{},
		},
		{
			name: "add-only",
			have: map[string]controlmode.PaneCell{},
			want: []controlmode.PaneCell{cellA, cellB},
			ops:  floatOps{Add: []controlmode.PaneCell{cellA, cellB}},
		},
		{
			name: "remove-only",
			have: map[string]controlmode.PaneCell{"%2": cellA, "%5": cellB},
			want: nil,
			ops:  floatOps{Remove: []string{"%2", "%5"}},
		},
		{
			name: "move-only",
			have: map[string]controlmode.PaneCell{"%2": cellA},
			want: []controlmode.PaneCell{cellAMoved},
			ops:  floatOps{Move: []controlmode.PaneCell{cellAMoved}},
		},
		{
			name: "simultaneous add, remove and move",
			// %2 survives but moves, %5 is dropped, %9 is new.
			have: map[string]controlmode.PaneCell{"%2": cellA, "%5": cellB},
			want: []controlmode.PaneCell{cellAMoved, {ID: "%9", W: 15, H: 4, X: 2, Y: 2}},
			ops: floatOps{
				Remove: []string{"%5"},
				Add:    []controlmode.PaneCell{{ID: "%9", W: 15, H: 4, X: 2, Y: 2}},
				Move:   []controlmode.PaneCell{cellAMoved},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planFloatOps(tt.have, tt.want)
			if !reflect.DeepEqual(got, tt.ops) {
				t.Errorf("planFloatOps(%v, %v) = %+v, want %+v", tt.have, tt.want, got, tt.ops)
			}
		})
	}
}

// TestPlanFloatOpsRemoveIsSorted pins Remove's ordering against map iteration
// order, which Go deliberately randomizes: a single-case assertion could pass
// by luck, so this drives enough distinct removed ids that an unsorted result
// would eventually mismatch across runs.
func TestPlanFloatOpsRemoveIsSorted(t *testing.T) {
	have := map[string]controlmode.PaneCell{
		"%9": {ID: "%9"},
		"%2": {ID: "%2"},
		"%5": {ID: "%5"},
		"%1": {ID: "%1"},
		"%7": {ID: "%7"},
	}
	want := []string{"%1", "%2", "%5", "%7", "%9"}

	got := planFloatOps(have, nil).Remove
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Remove = %v, want %v", got, want)
	}
}
