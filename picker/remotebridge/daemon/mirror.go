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

// SplitAxis returns the split direction that puts a new pane on the same axis
// the remote put it on, given the target layout L and the pane it is split
// from. The local pane is created with this instead of a hardcoded -h, so the
// intermediate frame — the one on screen until select-layout lands — already
// has the right shape (#447).
//
// Falls back to -h, which is what it always was: select-layout fixes the exact
// geometry either way, so a cell this cannot classify costs a wrong frame
// rather than a wrong layout.
func SplitAxis(L controlmode.Layout, order []string, srcID, newID string) string {
	src, srcOK := paneCell(L, order, srcID)
	dst, dstOK := paneCell(L, order, newID)
	if !srcOK || !dstOK {
		return "-h"
	}
	// Stacked: the two cells share a column, so the split is horizontal-of-axis
	// in tmux's naming — -v.
	if src.X == dst.X && src.W == dst.W {
		return "-v"
	}
	return "-h"
}

// paneCell resolves a remote pane id to its cell in L. order is the pane order
// L was read with, so the two are index-parallel.
func paneCell(L controlmode.Layout, order []string, id string) (controlmode.PaneCell, bool) {
	i := indexOf(order, id)
	if i < 0 || i >= len(L.Panes) {
		return controlmode.PaneCell{}, false
	}
	return L.Panes[i], true
}
