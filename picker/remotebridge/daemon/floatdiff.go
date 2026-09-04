package daemon

import (
	"sort"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// floatOps is the local float-pane surgery that turns a mirror window's
// currently-applied floats into the remote's. Modeled on paneOps
// (panediff.go), but a float has no tiled order to preserve — each is
// addressed by its own remote pane id rather than a position — so there is no
// Swaps phase, only Remove/Add/Move.
type floatOps struct {
	// Remove lists remote float pane ids whose local mirror should be killed:
	// present in have, absent from want.
	Remove []string
	// Add lists cells to create a new local float for: present in want,
	// absent from have.
	Add []controlmode.PaneCell
	// Move lists cells for float ids present in both have and want whose
	// geometry differs from the one recorded in have — the float survives,
	// only its position/size needs reasserting.
	Move []controlmode.PaneCell
}

// planFloatOps diffs the float geometry a mirror window last applied locally
// (have, keyed by remote pane id) against what the remote now reports (want).
// Pure: no I/O, no tmux calls.
//
// Emitted order is always Remove, then Add, then Move. Remove is built from a
// map, which iterates in no fixed order, so it is sorted; Add and Move follow
// want's own order, which is already deterministic, so neither is resorted.
func planFloatOps(have map[string]controlmode.PaneCell, want []controlmode.PaneCell) floatOps {
	wantByID := make(map[string]controlmode.PaneCell, len(want))
	for _, cell := range want {
		wantByID[cell.ID] = cell
	}

	var ops floatOps
	for id := range have {
		if _, ok := wantByID[id]; !ok {
			ops.Remove = append(ops.Remove, id)
		}
	}
	sort.Strings(ops.Remove)

	for _, cell := range want {
		prev, ok := have[cell.ID]
		if !ok {
			ops.Add = append(ops.Add, cell)
			continue
		}
		if prev != cell {
			ops.Move = append(ops.Move, cell)
		}
	}
	return ops
}
