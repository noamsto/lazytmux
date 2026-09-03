package daemon

import (
	"errors"
	"fmt"
	"os"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// maxReconcilePasses bounds reconcileLayout's trailing-reread loop (below).
const maxReconcilePasses = 5

// errLocalPanesDesynced reports that the mirror window no longer holds one
// local pane per remote pane, so applyPaneOps' positional mapping — local pane
// i renders remote pane i — cannot be trusted. Recovered from by rebuilding the
// window, never by acting on the broken mapping.
var errLocalPanesDesynced = errors.New("local panes desynced from the remote order")

// reconcileLayout re-reads window w's remote layout and applies the general
// pane diff (planPaneOps) so the local mirror renders the remote's panes in the
// remote's order, then re-fits geometry.
//
// The remote window is targeted by its id (@N) directly, never by a bare index.
//
// Loops on a trailing re-read after applying: a second remote layout change
// landing back-to-back with the first is queued for the main loop rather than
// acted on here, so this pass would otherwise finish against a layout that is
// already stale. Re-reading once more right after applying catches it: the
// round-trips above give the remote plenty of time to settle, so a still-
// different layout means something changed underneath us and needs its own pass.
func reconcileLayout(cfg Config, w *mirrorWindow, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, cv *converger, rt roundTrip) {
	target := remoteWinTarget(cfg, w.remoteID)

	L, remoteActive, zoomed, err := readLayout(rt, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
		return
	}

	// Nothing to do: same panes, same geometry, same zoom state. A resize
	// burst or a spurious/duplicate %layout-change then pays only this one
	// cheap readLayout round-trip instead of the FitWindowCmd + per-pane
	// resize/reseed below. L.Raw alone isn't enough — #{window_layout} is
	// deliberately the unzoomed geometry (see readLayout's doc comment), so a
	// zoom-only change must still fall through.
	if L.Raw == w.layout {
		if local, ok := localZoomed(cfg, w.localWin); ok && local == zoomed {
			return
		}
	}

	remote := w.remotePanes
	for pass := 0; pass < maxReconcilePasses; pass++ {
		newRemote := RemotePaneOrder(L)
		ops := planPaneOps(remote, newRemote)
		structural := len(ops.Remove) > 0 || len(ops.Append) > 0 || len(ops.Swaps) > 0

		switch {
		case ops.Reset:
			if err := resetWindow(cfg, w, send, router, waitHellos, cst, cv, rt); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change reset %s: %v\n", w.remoteID, err)
				w.remotePanes = remote
				return
			}
			// setupWindow re-read the layout and re-shaped the window itself.
			return
		case structural:
			err := applyPaneOps(cfg, w, ops, L, remote, newRemote, send, router, waitHellos, rt)
			if errors.Is(err, errLocalPanesDesynced) {
				fmt.Fprintf(os.Stderr, "daemon: layout-change %s: %v; rebuilding\n", w.remoteID, err)
				if err := resetWindow(cfg, w, send, router, waitHellos, cst, cv, rt); err != nil {
					fmt.Fprintf(os.Stderr, "daemon: layout-change reset %s: %v\n", w.remoteID, err)
					w.remotePanes = remote
				}
				// setupWindow re-read the layout and re-shaped the window itself.
				return
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change: %v\n", err)
				w.remotePanes = remote
				return
			}
		}

		applyLayout(cfg, w, L)
		// select-layout reshapes every surviving pane, so push each its new
		// dims (layout is daemon-authoritative — renderers only record them).
		for i, id := range newRemote {
			if s := router.sink(id); s != nil {
				s.enqueue(wire.FrameResize, wire.EncodeResize(L.Panes[i].W, L.Panes[i].H))
			}
		}
		// Every pane gets a fresh screen once the reshape has landed. The
		// painters hold no back-buffer to reflow, so a pane whose dims moved has
		// to be repainted from the remote — and that is as true of a pane that
		// survived a close or a split as of one whose window merely resized
		// (#417). A newly appended pane is re-seeded too: applyPaneOps enqueued
		// its first seed before the reshape, into a pane that was still the old
		// size.
		//
		// After the reshape, never before (#233): a seed sized for the new
		// geometry painted into a pane still at the old size leaves the mirror
		// blank.
		reseedIDs := make([]string, 0, len(newRemote))
		sinks := make([]*outputSink, 0, len(newRemote))
		for _, id := range newRemote {
			if s := router.sink(id); s != nil {
				reseedIDs = append(reseedIDs, id)
				sinks = append(sinks, s)
			}
		}
		PaneSeeds(rt, reseedIDs, func(i int, seed []byte, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "daemon: layout-change reseed for %s: %v\n", reseedIDs[i], err)
				return
			}
			sinks[i].enqueue(wire.FrameSeed, seed)
		})
		// tmux exposes zoom only as a toggle, and nothing here is guaranteed to
		// have cleared a local one first: applyLayout skips select-layout when the
		// tiled layout string is unchanged, which is exactly what a zoom-only
		// reconcile is. So read both sides and toggle only on a mismatch —
		// applying the remote's flag outright would turn an unzoom into a zoom.
		// The zoomed pane is by definition the active one.
		if local, ok := localZoomed(cfg, w.localWin); ok && local != zoomed {
			if target, ok := localPaneAt(w, indexOf(newRemote, remoteActive)); ok {
				if err := cfg.LocalTmux("resize-pane", "-Z", "-t", target); err != nil {
					fmt.Fprintf(os.Stderr, "daemon: layout-change zoom: %v\n", err)
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

		fresh, freshActive, freshZoom, err := readLayout(rt, target)
		if err != nil || (fresh.Raw == L.Raw && freshZoom == zoomed) {
			return
		}
		L, remoteActive, zoomed = fresh, freshActive, freshZoom
	}
	fmt.Fprintf(os.Stderr, "daemon: layout-change: didn't converge after %d passes, stopping at %v\n", maxReconcilePasses, remote)
	w.remotePanes = remote
}

// applyLayout fits the local mirror window to the remote's geometry and then
// shapes it into L. The fit comes first: an unfitted window would make
// select-layout rescale the remote's layout to the local client's size instead
// of taking the remote's.
//
// The shape is skipped when the window already carries L: tmux counts floating
// panes against the layout's cell count and rejects the whole string when they
// disagree ("have 4 panes but need 3"), so a select-layout the mirror does not
// need is one that can only fail. applyPaneOps clears w.layout, so surgery that
// reshapes the window always shapes it back.
func applyLayout(cfg Config, w *mirrorWindow, L controlmode.Layout) {
	if err := cfg.LocalTmux(FitWindowCmd(w.localWin, L)...); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change resize-window: %v\n", err)
	}
	if L.Raw == w.layout {
		return
	}
	if err := cfg.LocalTmux("select-layout", "-t", w.localWin, L.Raw); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: layout-change select-layout: %v\n", err)
		return
	}
	w.layout = L.Raw
}

// applyPaneOps performs the local pane surgery ops describes: kill the panes
// whose remote pane is gone, split new ones off the tail and wire a renderer to
// each, then swap the local panes into the remote's order.
func applyPaneOps(cfg Config, w *mirrorWindow, ops paneOps, L controlmode.Layout, remote, newRemote []string, send func(string), router *Router, waitHellos helloWaiter, rt roundTrip) error {
	// tmux is the only authority on what the window holds (see
	// refreshLocalPanes), and a pane can leave by a route this daemon never
	// saw — a renderer that died, a local kill, the window picker's ^x. Re-read
	// before trusting the positional mapping every op below depends on.
	//
	// A count that no longer matches means the mapping is broken outright, not
	// merely stale: local pane i stops rendering remote[i], so a kill or a swap
	// lands on a live neighbour, and the panes that should have gone survive
	// into a select-layout that then refuses them ("have 2 panes but need 1").
	// Rebuild instead — the caller's resetWindow is the existing escape hatch
	// for exactly this, and a desync is rare enough to afford it.
	if err := refreshLocalPanes(cfg, w); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	if len(w.localPanes) != len(remote) {
		return fmt.Errorf("%w: %d local panes for %d remote", errLocalPanesDesynced, len(w.localPanes), len(remote))
	}
	// Every op below reshapes the window, so what it carries is no longer the
	// layout last applied to it.
	w.layout = ""
	// Each kill renumbers the window's remaining panes, so the targets are pane
	// ids: they survive the kills ahead of them, an ordinal does not.
	for _, i := range ops.Remove {
		removed := remote[i]
		router.Unregister(removed)
		if c := w.conns[removed]; c != nil {
			c.Close()
			delete(w.conns, removed)
		}
		local, ok := localPaneAt(w, i)
		if !ok {
			return fmt.Errorf("reconcile: no local pane for %s", removed)
		}
		// Killing by id, so a failure here means the pane was already gone —
		// which the entry re-read above has already accounted for.
		if err := cfg.LocalTmux("kill-pane", "-t", local); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reconcile kill-pane: %v\n", err)
		}
	}
	if len(ops.Remove) > 0 {
		if err := refreshLocalPanes(cfg, w); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
	}

	// Splits target the current last pane explicitly, which lands the new pane
	// at lastIdx+1 (verified) — the old code split the window and relied on the
	// new pane implicitly taking the loop index. The re-read after each split
	// is what names the pane just created.
	//
	// The pane is created on the axis the remote used and already running the
	// renderer, rather than -h with a shell that respawn-pane then replaces
	// (#447). Both were visible: a stacked remote split showed side-by-side
	// until select-layout landed, and the shell painted a prompt into the pane
	// first. srcID is the pane being split from — the last surviving remote
	// pane for the first append, then each previous append in turn.
	srcID := ""
	for i := len(remote) - 1; i >= 0; i-- {
		if indexOf(newRemote, remote[i]) >= 0 {
			srcID = remote[i]
			break
		}
	}
	for _, id := range ops.Append {
		last, ok := localPaneAt(w, len(w.localPanes)-1)
		if !ok {
			return fmt.Errorf("reconcile: window %s has no pane to split", w.localWin)
		}
		axis := SplitAxis(L, newRemote, srcID, id)
		split := append([]string{"split-window", axis}, rendererSpawnArgs(cfg, id)...)
		if err := cfg.LocalTmux(append(split, "-t", last, cfg.RendererBin)...); err != nil {
			return fmt.Errorf("reconcile split-window: %w", err)
		}
		if err := refreshLocalPanes(cfg, w); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		added, ok := localPaneAt(w, len(w.localPanes)-1)
		if !ok {
			return fmt.Errorf("reconcile: split of %s produced no pane", w.localWin)
		}
		markRendererPane(cfg, added, id)
		srcID = id
	}

	if len(ops.Append) > 0 {
		// Before the hello wait below, not after: the splits above are always -h,
		// and the wait costs a renderer spawn, so leaving the geometry until the
		// caller's trailing pass puts the wrong shape on screen for the whole
		// handshake rather than for a frame (#408). The swaps below exchange
		// panes between cells without changing cell geometry, so this stays
		// correct and the caller's pass becomes idempotent.
		applyLayout(cfg, w, L)

		// Seeding is sequential over the single control stream, so every new
		// renderer must be connected first (mirrors setupWindow).
		added, err := waitHellos(len(ops.Append))
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
			if seedRenderer(rt, router, c, id, L.Panes[indexOf(newRemote, id)], cfg.graphicsFor(id)) {
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
		src, srcOK := localPaneAt(w, s[0])
		dst, dstOK := localPaneAt(w, s[1])
		if !srcOK || !dstOK {
			return fmt.Errorf("reconcile: swap %d<->%d out of range for %s", s[0], s[1], w.localWin)
		}
		if err := cfg.LocalTmux("swap-pane", "-d", "-s", src, "-t", dst); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reconcile swap-pane: %v\n", err)
		}
		w.localPanes[s[0]], w.localPanes[s[1]] = w.localPanes[s[1]], w.localPanes[s[0]]
	}
	return nil
}

// resetWindow rebuilds w from scratch, for the case where no current pane
// survives (planPaneOps.Reset). Killing every pane instead would make tmux
// destroy the mirror window and leave a registry entry pointing at nothing.
//
// The SHARED converger, like every other setupWindow caller: this reads
// cfg.LocalArea() independently of watchResize's own read and writes to the
// same stream, so a throwaway map lets the two disagree — a stale size written
// last while the shared record holds the new one, which watchResize then never
// re-sends. setupWindow's own cv.forget is what makes the reset re-cap.
func resetWindow(cfg Config, w *mirrorWindow, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, cv *converger, rt roundTrip) error {
	for _, id := range w.remotePanes {
		router.Unregister(id)
		if c := w.conns[id]; c != nil {
			c.Close()
			delete(w.conns, id)
		}
	}
	dropMirroredPanes(cfg, w)
	return setupWindow(cfg, send, router, waitHellos, cst, w, cv, rt)
}

// dropMirroredPanes kills every mirrored pane but the first, which resetWindow
// leaves for setupWindow to re-shape and respawn, and clears w's belief about
// what the window holds. Only the panes this daemon created: a float the window
// also holds is not its to reap.
func dropMirroredPanes(cfg Config, w *mirrorWindow) {
	for i := len(w.localPanes) - 1; i > 0; i-- {
		if err := cfg.LocalTmux("kill-pane", "-t", w.localPanes[i]); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: reset kill-pane: %v\n", err)
		}
	}
	w.remotePanes = nil
	w.localPanes = nil
	w.layout = ""
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
	local, ok := localPaneAt(w, i)
	if !ok {
		return
	}
	if err := cfg.LocalTmux("select-pane", "-t", local); err != nil {
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
