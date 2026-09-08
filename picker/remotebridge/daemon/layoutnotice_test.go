package daemon

import (
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestParseLayoutNotice(t *testing.T) {
	const (
		layout  = "c195,80x24,0,0[80x12,0,0,0,80x11,0,13,1]"
		visible = "b25e,80x24,0,0,1"
	)

	tests := []struct {
		name       string
		raw        string
		wantOK     bool
		wantLayout string
		wantZoomed bool
	}{
		{
			name:       "4 fields, zoomed",
			raw:        "%layout-change @0 " + layout + " " + visible + " *Z",
			wantOK:     true,
			wantLayout: layout,
			wantZoomed: true,
		},
		{
			name:       "4 fields, not zoomed",
			raw:        "%layout-change @0 " + layout + " " + visible + " *",
			wantOK:     true,
			wantLayout: layout,
			wantZoomed: false,
		},
		{
			// A 3.1-spelling of the activity flag (doubled '#' from
			// window_printable_flags' escape=1 form). The pinned tree can't
			// confirm this shape was ever emitted; the test only pins that
			// the alphabet accepts it alongside Z.
			name:       "4 fields, doubled activity flag plus zoomed",
			raw:        "%layout-change @0 " + layout + " " + visible + " ##Z",
			wantOK:     true,
			wantLayout: layout,
			wantZoomed: true,
		},
		{
			// A trailing space with nothing after it: window_printable_flags
			// reported no flags, so tmux never emits an empty 4th field and
			// bytes.Fields drops it, leaving the 3-field form.
			name:       "3 fields, flags empty",
			raw:        "%layout-change @0 " + layout + " " + visible + " ",
			wantOK:     true,
			wantLayout: layout,
			wantZoomed: false,
		},
		{
			name:   "2 fields",
			raw:    "%layout-change @0 " + layout,
			wantOK: false,
		},
		{
			// A shifted 3-field line: field 2 arrived empty and everything
			// moved left, so what would have been the flags field lands in
			// Args[2] where a layout is expected.
			name:   "shifted 3 fields, third field is flags",
			raw:    "%layout-change @0 " + visible + " *Z",
			wantOK: false,
		},
		{
			name:   "4 fields, flags outside the alphabet",
			raw:    "%layout-change @0 " + layout + " " + visible + " bogus",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := controlmode.ParseLine(tc.raw)
			got, ok := parseLayoutNotice(l)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (notice %+v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if got.layout != tc.wantLayout {
				t.Errorf("layout = %q, want %q", got.layout, tc.wantLayout)
			}
			if got.zoomed != tc.wantZoomed {
				t.Errorf("zoomed = %v, want %v", got.zoomed, tc.wantZoomed)
			}
		})
	}
}
