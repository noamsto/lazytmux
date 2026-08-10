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
			if got.Raw != tt.in {
				t.Errorf("Raw = %q, want %q", got.Raw, tt.in)
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

// TestParseLayoutFloats uses strings captured live from the pinned
// tmux-next-3.8, not hand-written ones — tmux interleaves float panes into
// the tiled split tree AND repeats them in a trailing "<...>" section, and
// rejects that exact string when replayed through select-layout (verified
// separately). ParseLayout must recover the tiled-only tree (for Raw / Panes
// / RemotePaneOrder) and expose the floats separately.
func TestParseLayoutFloats(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantTiled  []string // Panes ids, in order
		wantFloats []PaneCell
		wantRaw    string // reconstructed select-layout-safe Raw
	}{
		{
			// One tiled pane (%1) + one float (%2). tmux wraps the sole
			// tiled pane in a framing split purely to carry the float
			// sibling; pruning must collapse that split back to a plain
			// single-pane cell, matching what 3.7b (no floats) emits.
			name:       "single tiled pane with one float",
			in:         "e2cc,80x24,0,0[80x24,0,0,1,30x7,9,3,2]<30x7,9,3,2>",
			wantTiled:  []string{"%1"},
			wantFloats: []PaneCell{{ID: "%2", W: 30, H: 7, X: 9, Y: 3}},
			wantRaw:    "b25e,80x24,0,0,1",
		},
		{
			// Two tiled panes (%0, %1, horizontal split) + one float (%2)
			// interleaved between them in the tree.
			name:       "two tiled panes with one float",
			in:         "aaee,80x24,0,0{40x24,0,0,0,30x7,9,3,2,39x24,41,0,1}<30x7,9,3,2>",
			wantTiled:  []string{"%0", "%1"},
			wantFloats: []PaneCell{{ID: "%2", W: 30, H: 7, X: 9, Y: 3}},
			wantRaw:    "8205,80x24,0,0{40x24,0,0,0,39x24,41,0,1}",
		},
		{
			// Two tiled panes + two floats: the trailing section holds
			// multiple cells comma-joined inside a single "<...>", not one
			// "<...>" per float.
			name:      "two tiled panes with two floats",
			in:        "a012,80x24,0,0{40x24,0,0,0,14x2,49,15,3,30x7,9,3,2,39x24,41,0,1}<14x2,49,15,3,30x7,9,3,2>",
			wantTiled: []string{"%0", "%1"},
			wantFloats: []PaneCell{
				{ID: "%3", W: 14, H: 2, X: 49, Y: 15},
				{ID: "%2", W: 30, H: 7, X: 9, Y: 3},
			},
			wantRaw: "8205,80x24,0,0{40x24,0,0,0,39x24,41,0,1}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLayout(tt.in)
			if err != nil {
				t.Fatalf("ParseLayout(%q) error: %v", tt.in, err)
			}
			var ids []string
			for _, p := range got.Panes {
				ids = append(ids, p.ID)
			}
			if len(ids) != len(tt.wantTiled) {
				t.Fatalf("tiled pane ids = %v, want %v", ids, tt.wantTiled)
			}
			for i := range ids {
				if ids[i] != tt.wantTiled[i] {
					t.Errorf("tiled pane[%d] id = %s, want %s", i, ids[i], tt.wantTiled[i])
				}
			}
			if len(got.Floats) != len(tt.wantFloats) {
				t.Fatalf("floats = %+v, want %+v", got.Floats, tt.wantFloats)
			}
			for i := range got.Floats {
				if got.Floats[i] != tt.wantFloats[i] {
					t.Errorf("float[%d] = %+v, want %+v", i, got.Floats[i], tt.wantFloats[i])
				}
			}
			if got.Raw != tt.wantRaw {
				t.Errorf("Raw = %q, want %q", got.Raw, tt.wantRaw)
			}
		})
	}
}

// TestParseLayoutNoFloats pins that a layout with no floating panes (the
// tmux 3.7b shape, and the common next-3.8 shape) is unaffected: Raw stays
// byte-identical to the input.
func TestParseLayoutNoFloats(t *testing.T) {
	in := "8205,80x24,0,0{40x24,0,0,0,39x24,41,0,1}"
	got, err := ParseLayout(in)
	if err != nil {
		t.Fatalf("ParseLayout(%q) error: %v", in, err)
	}
	if len(got.Floats) != 0 {
		t.Errorf("Floats = %+v, want none", got.Floats)
	}
	if got.Raw != in {
		t.Errorf("Raw = %q, want %q", got.Raw, in)
	}
}
