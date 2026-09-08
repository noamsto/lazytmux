package main

import "testing"

func TestSockAndPaneArity(t *testing.T) {
	tests := []struct {
		name string
		args []string
		ok   bool
		sock string
		pane string
	}{
		{name: "none", args: []string{"renderer"}},
		{name: "one", args: []string{"renderer", "/run/sock"}},
		{name: "ok", args: []string{"renderer", "/run/sock", "%2"}, ok: true, sock: "/run/sock", pane: "%2"},
		{name: "extra", args: []string{"renderer", "/run/sock", "%2", "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock, pane, ok := sockAndPane(tt.args)
			if ok != tt.ok || sock != tt.sock || pane != tt.pane {
				t.Errorf("sockAndPane(%q) = %q, %q, %v; want %q, %q, %v",
					tt.args, sock, pane, ok, tt.sock, tt.pane, tt.ok)
			}
		})
	}
}
