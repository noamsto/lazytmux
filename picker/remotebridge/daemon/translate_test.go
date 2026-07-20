package daemon

import (
	"reflect"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestTranslateWindowNotification(t *testing.T) {
	reg := newRegistry(1)
	reg.add("@1", "h-s:1")
	reg.add("@2", "h-s:2")

	cases := []struct {
		name string
		line controlmode.Line
		argv []string
		ok   bool
	}{
		{"rename in registry", controlmode.Line{Kind: controlmode.WindowRenamed, Args: []string{"@2"}, Data: []byte("my name")},
			[]string{"rename-window", "-t", "h-s:2", "my name"}, true},
		{"active-changed in registry", controlmode.Line{Kind: controlmode.SessionWindowChanged, Args: []string{"$1", "@1"}},
			[]string{"select-window", "-t", "h-s:1"}, true},
		{"rename out of registry (B2)", controlmode.Line{Kind: controlmode.WindowRenamed, Args: []string{"@9"}, Data: []byte("x")},
			nil, false},
		{"active-changed out of registry (B2)", controlmode.Line{Kind: controlmode.SessionWindowChanged, Args: []string{"$1", "@9"}},
			nil, false},
		{"pane-changed is a no-op (M2.2)", controlmode.Line{Kind: controlmode.WindowPaneChanged, Args: []string{"@1", "%3"}},
			nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, ok := translateWindowNotification(c.line, reg)
			if ok != c.ok || !reflect.DeepEqual(argv, c.argv) {
				t.Errorf("got (%v,%v), want (%v,%v)", argv, ok, c.argv, c.ok)
			}
		})
	}
}
