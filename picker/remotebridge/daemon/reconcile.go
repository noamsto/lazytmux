package daemon

import (
	"fmt"
	"os"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// maxReconcilePasses bounds reconcileLayout's trailing-reread loop (below).
const maxReconcilePasses = 5

// reconcileLayout re-reads window w's remote layout and applies the general
// pane diff (planPaneOps) so the local mirror renders the remote's panes in the
// remote's order, then re-fits geometry.
//
// Round-trips here use the routing-aware reply reader (B3): sibling windows are
// streaming during a live reconcile, so any %output seen while awaiting a reply
// is routed rather than dropped. The remote window is targeted by its id (@N)
// directly, never by a bare index.
//
// Loops on a trailing re-read after applying: a second remote layout change
// landing back-to-back with the first can have its own %layout-change swallowed
// while this function is mid-flight (readReplyRouting still returns on the reply
// block, not on the async notification). Re-reading once more right after
// applying catches this: the round-trips above give the remote plenty of time to
// settle, so a still-different layout means something changed underneath us and
// needs its own pass.
func reconcileLayout(cfg Config, w *mirrorWindow, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, cst *ctlState) {
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }
	target := remoteWinTarget(cfg, w.remoteID)

	L, remoteActive, err := readLayout(reader, send, target, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
		return
	}

	remote := w.remotePanes
	for pass := 0; pass < maxReconcilePasses; pass++ {
		newRemote := RemotePaneOrder(L)
		ops := planPaneOps(remote, newRemote)
		structural := len(ops.Remove) > 0 || len(ops.Append) > 0 || len(ops.Swaps) > 0

		switch {
		case ops.Reset:
			if err := resetWindow(cfg, w, reader, send, router, connCh, cst, reply); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change reset %s: %v\n", w.remoteID, err)
				w.remotePanes = remote
				return
			}
			// setupWindow re-read the layout and re-shaped the window itself.
			return
		case !structural:
			// Geometry-only change (typically a client/terminal resize propagated
			// to the remote): same pane set, new dims. The painters hold no
			// back-buffer to reflow, so the pane needs a fresh screen — but the
			// re-seed happens *after* the reshape below (#233), because a seed
			// sized for the new geometry painted into a pane that is still the old
			// size leaves the mirror blank.
		default:
			if err := applyPaneOps(cfg, w, ops, L, remote, newRemote, reader, send, router, connCh, reply); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
				w.remotePanes = remote
				return
			}
		}

		// Re-fit before select-layout: a geometry-only change is usually the
		// remote resizing under us (the other client resized, or our own cap
		// landed), and an unfitted window would rescale the layout to the local
		// client's size instead of taking the remote's.
		if err := cfg.LocalTmux(FitWindowCmd(w.localWin, L)...); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: layout-change resize-window: %v\n", err)
		}
		if err := cfg.LocalTmux("select-layout", "-t", w.localWin, L.Raw); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: layout-change select-layout: %v\n", err)
		}
		// select-layout reshapes every surviving pane, so push each its new
		// dims (layout is daemon-authoritative — renderers only record them).
		for i, id := range newRemote {
			if s := router.sink(id); s != nil {
				s.enqueue(wire.FrameResize, wire.EncodeResize(L.Panes[i].W, L.Panes[i].H))
			}
		}
		if !structural {
			for _, id := range newRemote {
				s := router.sink(id)
				if s == nil {
					continue
				}
				if seed, err := PaneSeed(reader, send, id, reply); err == nil {
					s.enqueue(wire.FrameSeed, seed)
				} else {
					fmt.Fprintf(os.Stderr, "daemon: layout-change reseed for %s: %v\n", id, err)
				}
			}
		}
		// Follow the remote's active pane, but only when the pane set or order
		// moved: a pure resize must not yank local focus. remoteActive comes
		// from the same round-trip as the layout, so it is ground truth rather
		// than a tracked belief.
		if structural {
			focusLocalPane(cfg, cst, w, newRemote, remoteActive)
		}
		remote = newRemote
		w.remotePanes = remote
		cst.setWindowPanes(w.remoteID, remote)

		fresh, freshActive, err := readLayout(reader, send, target, reply)
		if err != nil || fresh.Raw == L.Raw {
			return
		}
		L, remoteActive = fresh, freshActive
	}
	fmt.Fprintf(os.Stderr, "daemon: layout-change: didn't converge after %d passes, stopping at %v\n", maxReconcilePasses, remote)
	w.remotePanes = remote
}

// applyPaneOps performs the local pane surgery ops describes: kill the panes
// whose remote pane is gone, split new ones off the tail and wire a renderer to
// each, then swap the local panes into the remote's order.
func applyPaneOps(cfg Config, w *mirrorWindow, ops paneOps, L controlmode.Layout, remote, newRemote []string, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, reply replyFn) error {
	for _, i := range ops.Remove {
		removed := remote[i]
		router.Unregister(removed)
		if c := w.conns[removed]; c != nil {
			c.Close()
			delete(w.conns, removed)
		}
		if err := cfg.LocalTmux("kill-pane", "-t", fmt.Sprintf("%s.%d", w.localWin, i)); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reconcile kill-pane: %v\n", err)
		}
	}

	// Splits target the current last pane explicitly, which lands the new pane
	// at lastIdx+1 (verified) — the old code split the window and relied on the
	// new pane implicitly taking the loop index.
	base := len(remote) - len(ops.Remove)
	for k, id := range ops.Append {
		last := base + k - 1
		if err := cfg.LocalTmux("split-window", "-h", "-t", fmt.Sprintf("%s.%d", w.localWin, last)); err != nil {
			return fmt.Errorf("reconcile split-window: %w", err)
		}
		if err := spawnRenderer(cfg, w.localWin, base+k, id); err != nil {
			return fmt.Errorf("reconcile spawn renderer for %s: %w", id, err)
		}
	}

	if len(ops.Append) > 0 {
		// Seeding is sequential over the single control stream, so every new
		// renderer must be connected first (mirrors setupWindow).
		added, err := collectHellos(connCh, len(ops.Append), helloTimeout)
		if err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		for _, id := range ops.Append {
			c := added[id]
			if c == nil {
				continue
			}
			w.conns[id] = c
			// Dims come from the pane's cell in the REMOTE order, not from the
			// temporary local index it was appended at — the swaps below have
			// not run yet, so the two differ.
			if seedRenderer(reader, send, router, c, id, reply, L.Panes[indexOf(newRemote, id)]) {
				go pumpInput(c, id, send)
			} else {
				delete(w.conns, id)
			}
		}
	}

	// -d keeps the same pane active rather than the same index (verified: the
	// flag's effect on the active pane id is opposite between swap-pane's
	// one-target and two-target forms), so local focus rides with its pane.
	for _, s := range ops.Swaps {
		if err := cfg.LocalTmux("swap-pane", "-d",
			"-s", fmt.Sprintf("%s.%d", w.localWin, s[0]),
			"-t", fmt.Sprintf("%s.%d", w.localWin, s[1])); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reconcile swap-pane: %v\n", err)
		}
	}
	return nil
}

// resetWindow rebuilds w from scratch, for the case where no current pane
// survives (planPaneOps.Reset). Killing every pane instead would make tmux
// destroy the mirror window and leave a registry entry pointing at nothing.
func resetWindow(cfg Config, w *mirrorWindow, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, cst *ctlState, reply replyFn) error {
	for _, id := range w.remotePanes {
		router.Unregister(id)
		if c := w.conns[id]; c != nil {
			c.Close()
			delete(w.conns, id)
		}
	}
	// Leave pane 0 for setupWindow to re-shape and respawn; drop the rest.
	for i := len(w.remotePanes) - 1; i > 0; i-- {
		if err := cfg.LocalTmux("kill-pane", "-t", fmt.Sprintf("%s.%d", w.localWin, i)); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reset kill-pane: %v\n", err)
		}
	}
	w.remotePanes = nil
	return setupWindow(cfg, reader, send, router, connCh, cst, w, newConverger(), reply)
}

// focusLocalPane points local focus at whichever local pane renders the
// remote's active pane. A remote pane the mirror does not render (it closed
// between the layout read and now) leaves local focus alone.
func focusLocalPane(cfg Config, cst *ctlState, w *mirrorWindow, order []string, remoteActive string) {
	i := indexOf(order, remoteActive)
	if i < 0 {
		return
	}
	// Record before issuing: the local select-pane fires after-select-pane, whose
	// ctl report must be recognised as this move's echo rather than a new gesture.
	cst.noteLocalFocus(w.remoteID, remoteActive)
	if err := cfg.LocalTmux("select-pane", "-t", fmt.Sprintf("%s.%d", w.localWin, i)); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: reconcile select-pane: %v\n", err)
	}
}

func indexOf(ids []string, id string) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return -1
}
