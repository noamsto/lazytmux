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
// tiled pane ids, in tmux's own order, and its floating ones. A float occupies
// an ordinal slot like any other pane, so only the tiled list is positionally
// comparable to the remote's pane order.
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

// refreshLocalPanes re-reads w's tiled pane ids into w.localPanes. This is the
// mirror's only source of local pane identity: the panes are created by
// split-window, which reports nothing back through LocalTmux.
//
// Re-read after each structural change rather than tracked incrementally: a
// mirror window can acquire a pane this daemon never created (a local float),
// so tmux is the only authority on what it holds.
func refreshLocalPanes(cfg Config, w *mirrorWindow) error {
	out, err := cfg.LocalTmuxOut("list-panes", "-t", w.localWin, "-F", localPaneListFormat)
	if err != nil {
		return fmt.Errorf("list-panes %s: %w", w.localWin, err)
	}
	w.localPanes, _ = parseLocalPaneList(out)
	return nil
}

// localPaneAt returns the local pane rendering the i'th remote pane, reporting
// a miss rather than guessing one.
func localPaneAt(w *mirrorWindow, i int) (string, bool) {
	if i < 0 || i >= len(w.localPanes) {
		return "", false
	}
	return w.localPanes[i], true
}

// localZoomed reads the mirror window's own zoom state, reporting false with
// ok=false when it cannot be established (no read seam wired, or the window is
// gone). tmux exposes zoom only as a toggle, so the caller has to know the
// current state before it can set one — and must not guess: toggling on a
// wrong belief inverts the zoom instead of matching it.
func localZoomed(cfg Config, localWin string) (zoomed, ok bool) {
	if cfg.LocalTmuxOut == nil {
		return false, false
	}
	out, err := cfg.LocalTmuxOut("display-message", "-p", "-t", localWin, "-F", "#{window_zoomed_flag}")
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(out) == "1", true
}
