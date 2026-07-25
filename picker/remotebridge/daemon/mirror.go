package daemon

import (
	"strconv"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// FitWindowCmd returns the tmux argv that pins target to L's exact geometry.
// Without it select-layout rescales the remote layout to whatever size the
// local client gives the window, and the mirror paints a screen of one size
// into panes of another. The remote can legitimately be smaller than the
// mirror — another client attached to it clamps it down — so the mirror
// window is what has to give. resize-window flips it to window-size manual,
// which is the intent: a bridge window's size is the remote's, and tmux pads
// the leftover client area.
func FitWindowCmd(target string, L controlmode.Layout) []string {
	return []string{"resize-window", "-t", target, "-x", strconv.Itoa(L.W), "-y", strconv.Itoa(L.H)}
}

// PlanWindow returns the tmux argv sequence to shape an existing 1-pane local
// window <target> into layout L: a fit to L's geometry, (N-1) split-window
// commands + one select-layout. Splits use -h; select-layout then fixes exact
// geometry (verified: assignment is positional in local pane-list order, so
// pane creation order == L.Panes order).
func PlanWindow(target string, L controlmode.Layout) [][]string {
	cmds := [][]string{FitWindowCmd(target, L)}
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
