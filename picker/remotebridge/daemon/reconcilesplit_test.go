package daemon

import (
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// #447: the appended pane is created on the remote's own axis and already
// running the renderer. Before, every split was -h with a shell that
// respawn-pane then replaced, so a stacked remote split showed side-by-side —
// and a shell prompt — until select-layout landed.
func TestApplyPaneOpsSplitsOnTheRemoteAxis(t *testing.T) {
	var mu sync.Mutex
	var trace []string
	var split bool

	cfg := Config{
		SockPath:    "/run/sock",
		RendererBin: "/nix/store/x-renderer/bin/renderer",
		LocalTmux: func(argv ...string) error {
			mu.Lock()
			defer mu.Unlock()
			trace = append(trace, strings.Join(argv, " "))
			if argv[0] == "split-window" {
				split = true
			}
			return nil
		},
		LocalTmuxOut: func(...string) (string, error) {
			if !split {
				return "%l1 0\n", nil
			}
			return "%l1 0\n%l2 0\n", nil
		},
	}

	// The remote stacked %2 under %1: same column, so the mirror must use -v.
	L := controlmode.Layout{W: 80, H: 40, Panes: []controlmode.PaneCell{
		{ID: "%1", W: 80, H: 20, X: 0, Y: 0},
		{ID: "%2", W: 80, H: 19, X: 0, Y: 21},
	}}
	w := &mirrorWindow{
		remoteID: "@1", localWin: "@101",
		remotePanes: []string{"%1"}, localPanes: []string{"%l1"},
		conns: map[string]net.Conn{},
	}

	ops := paneOps{Append: []string{"%2"}}
	waiter := func(int) (map[string]net.Conn, error) { return map[string]net.Conn{}, nil }
	if err := applyPaneOps(cfg, w, ops, L, []string{"%1"}, []string{"%1", "%2"},
		func(string) {}, NewRouter(), waiter, nil); err != nil {
		t.Fatalf("applyPaneOps: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var splitCmd string
	for _, c := range trace {
		if strings.HasPrefix(c, "split-window") {
			splitCmd = c
		}
		if strings.HasPrefix(c, "respawn-pane") {
			t.Errorf("the split should carry the renderer, not respawn a shell: %q", c)
		}
	}
	if splitCmd == "" {
		t.Fatalf("no split-window in %v", trace)
	}
	if !strings.HasPrefix(splitCmd, "split-window -v ") {
		t.Errorf("split = %q, want the remote's -v axis", splitCmd)
	}
	for _, want := range []string{
		"LZTMUX_RENDER_PANE=%2",
		"LZTMUX_RENDER_SOCK=/run/sock",
		"/nix/store/x-renderer/bin/renderer",
	} {
		if !strings.Contains(splitCmd, want) {
			t.Errorf("split %q missing %q", splitCmd, want)
		}
	}
}
