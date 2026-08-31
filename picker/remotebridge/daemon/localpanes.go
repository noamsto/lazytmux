package daemon

import (
	"fmt"
	"strings"
)

// localPaneListFormat asks tmux for each pane's identity and whether it floats.
// Space-delimited: a pane id and a flag are both bare ASCII, so neither field
// can carry the separator.
const localPaneListFormat = "#{pane_id} #{?pane_floating_flag,1,0}"

// parseLocalPaneList splits a localPaneListFormat listing into the window's
// tiled pane ids, in tmux's own order, and its floating ones.
//
// The split is the point: a float occupies an ordinal slot like any other pane
// (probed), so "the Nth pane of the window" and "the Nth mirrored pane" are
// different panes as soon as one float exists. Only the tiled list is
// positionally comparable to the remote's pane order.
func parseLocalPaneList(out string) (tiled, floats []string) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, flag, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(id, "%") {
			continue
		}
		if flag == "1" {
			floats = append(floats, id)
			continue
		}
		tiled = append(tiled, id)
	}
	return tiled, floats
}

// localPaneIDs reads localWin's panes back from tmux, split into tiled and
// floating. This is the mirror's only source of local pane identity: the panes
// are created by split-window, which reports nothing back through LocalTmux.
func localPaneIDs(cfg Config, localWin string) (tiled, floats []string, err error) {
	out, err := cfg.LocalTmuxOut("list-panes", "-t", localWin, "-F", localPaneListFormat)
	if err != nil {
		return nil, nil, fmt.Errorf("list-panes %s: %w", localWin, err)
	}
	tiled, floats = parseLocalPaneList(out)
	return tiled, floats, nil
}

// refreshLocalPanes re-reads w's local pane ids into w.localPanes, which every
// pane-addressing command targets instead of a window.index ordinal.
//
// Called after each structural change rather than tracked incrementally: tmux
// is the authority on what the window holds, and a mirror window can acquire a
// pane this daemon never created (a local float).
func refreshLocalPanes(cfg Config, w *mirrorWindow) error {
	tiled, _, err := localPaneIDs(cfg, w.localWin)
	if err != nil {
		return err
	}
	w.localPanes = tiled
	return nil
}

// localPaneAt returns the local pane rendering the i'th remote pane. A miss is
// reported rather than guessed: falling back to a window.index ordinal is what
// makes a float-bearing window address the wrong pane.
func localPaneAt(w *mirrorWindow, i int) (string, bool) {
	if i < 0 || i >= len(w.localPanes) {
		return "", false
	}
	return w.localPanes[i], true
}
