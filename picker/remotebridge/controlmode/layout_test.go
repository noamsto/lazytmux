package controlmode

import "testing"

func TestParseLayout(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantW   int
		wantH   int
		wantIDs []string
		wantP0  PaneCell
	}{
		{
			name:  "single pane",
			in:    "bd67,190x45,0,0,3",
			wantW: 190, wantH: 45,
			wantIDs: []string{"%3"},
			wantP0:  PaneCell{ID: "%3", W: 190, H: 45, X: 0, Y: 0},
		},
		{
			name:  "horizontal split (left-right)",
			in:    "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}",
			wantW: 190, wantH: 45,
			wantIDs: []string{"%0", "%1"},
			wantP0:  PaneCell{ID: "%0", W: 95, H: 45, X: 0, Y: 0},
		},
		{
			name:  "vertical split (top-bottom)",
			in:    "b5e9,190x45,0,0[190x22,0,0,0,190x22,0,23,1]",
			wantW: 190, wantH: 45,
			wantIDs: []string{"%0", "%1"},
			wantP0:  PaneCell{ID: "%0", W: 190, H: 22, X: 0, Y: 0},
		},
		{
			name:  "nested",
			in:    "a1b2,190x45,0,0{95x45,0,0,0,94x45,96,0[94x22,96,0,1,94x22,96,23,2]}",
			wantW: 190, wantH: 45,
			wantIDs: []string{"%0", "%1", "%2"},
			wantP0:  PaneCell{ID: "%0", W: 95, H: 45, X: 0, Y: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLayout(tt.in)
			if err != nil {
				t.Fatalf("ParseLayout(%q) error: %v", tt.in, err)
			}
			if got.W != tt.wantW || got.H != tt.wantH {
				t.Errorf("window dims = %dx%d, want %dx%d", got.W, got.H, tt.wantW, tt.wantH)
			}
			var ids []string
			for _, p := range got.Panes {
				ids = append(ids, p.ID)
			}
			if len(ids) != len(tt.wantIDs) {
				t.Fatalf("pane ids = %v, want %v", ids, tt.wantIDs)
			}
			for i := range ids {
				if ids[i] != tt.wantIDs[i] {
					t.Errorf("pane[%d] id = %s, want %s", i, ids[i], tt.wantIDs[i])
				}
			}
			if got.Panes[0] != tt.wantP0 {
				t.Errorf("pane[0] = %+v, want %+v", got.Panes[0], tt.wantP0)
			}
		})
	}
}

func TestParseLayoutError(t *testing.T) {
	for _, in := range []string{"", "nocomma", "bd67,notdims,0,0,3"} {
		if _, err := ParseLayout(in); err == nil {
			t.Errorf("ParseLayout(%q) expected error, got nil", in)
		}
	}
}
