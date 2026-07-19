package daemon

import "github.com/noamsto/lazytmux/picker/remotebridge/controlmode"

// PlanWindow returns the tmux argv sequence to shape an existing 1-pane local
// window <target> into layout L: (N-1) split-window commands + one
// select-layout. Splits use -h; select-layout then fixes exact geometry
// (verified: assignment is positional in local pane-list order, so pane
// creation order == L.Panes order).
func PlanWindow(target string, L controlmode.Layout) [][]string {
	var cmds [][]string
	for i := 1; i < len(L.Panes); i++ {
		cmds = append(cmds, []string{"split-window", "-h", "-t", target})
	}
	cmds = append(cmds, []string{"select-layout", "-t", target, L.Raw})
	return cmds
}

// RemotePaneOrder returns the remote pane ids in the order local panes will
// be created (== L.Panes order), for wiring renderers to remote panes after
// apply.
func RemotePaneOrder(L controlmode.Layout) []string {
	ids := make([]string, len(L.Panes))
	for i, p := range L.Panes {
		ids[i] = p.ID
	}
	return ids
}
