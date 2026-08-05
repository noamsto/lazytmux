package daemon

import (
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// Pins reconcileWindows' every-path-reflows invariant against the three ways
// its round-trip can bail.
func TestReconcileWindowsReflowsOnEarlyReturn(t *testing.T) {
	cases := []struct {
		name  string
		reply controlmode.Line
		ok    bool
	}{
		{"list-windows errors", controlmode.Line{Kind: controlmode.Error}, true},
		{"round-trip lost", controlmode.Line{}, false},
		{"empty window list", controlmode.Line{Kind: controlmode.End}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reflowed := 0
			cfg := Config{
				RemoteSession: "rem",
				LocalTmux:     func(...string) error { return nil },
				Reflow:        func() { reflowed++ },
			}
			reg := newRegistry(1)
			reg.add("@1", "h-s:1")
			rt := func(string) (controlmode.Line, bool) { return c.reply, c.ok }

			reconcileWindows(cfg, func(string) {}, NewRouter(),
				make(chan helloConn, 1), newCtlState(), reg, newConverger(), rt)

			if reflowed != 1 {
				t.Fatalf("reflow called %d times, want 1", reflowed)
			}
			if _, ok := reg.byRemoteID("@1"); !ok {
				t.Fatal("early return must leave the mirror registry alone")
			}
		})
	}
}
