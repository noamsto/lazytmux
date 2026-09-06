package daemon

import (
	"fmt"
	"os"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// reconcileWindows re-reads the bridged session's whole window set and makes the
// mirror match: mirror any remote window that has appeared, tear down any that
// has gone, and re-assert the remote name on the ones that stayed.
//
// A local structural gesture does get its remote notification (tmux emits it
// inside the causing command's %begin/%end block, which the reader surfaces),
// but that notification arrives with the mirror mid-gesture. Re-reading ground
// truth covers add, close and rename with one round-trip, and self-heals a
// notification lost for any other reason.
func reconcileWindows(cfg Config, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, reg *registry, cv *converger, rt roundTrip) {
	// Every return path reflows: the round-trip below can bail, and Run's
	// startup batch has no other forced reflow — skipping it leaves those
	// windows on the label the after-new-window hook raced in (#196).
	defer cfg.reflow()

	lw, ok := one(rt, fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowListFormat))
	if !ok || lw.Kind == controlmode.Error {
		fmt.Fprintf(os.Stderr, "daemon: reconcile-windows: list-windows failed\n")
		return
	}
	remoteWins := parseWindowList(string(lw.Data))
	if len(remoteWins) == 0 {
		// An empty reply is more likely a lost round-trip than a session with no
		// windows (the remote emits %exit for that), and acting on it would kill
		// every mirror window. Leave the mirror alone.
		return
	}

	live := make(map[string]bool, len(remoteWins))
	added := false
	activeRemote := ""
	for _, rw := range remoteWins {
		live[rw.id] = true
		if rw.active {
			activeRemote = rw.id
		}
		mw, known := reg.byRemoteID(rw.id)
		if !known {
			if mirrorNewWindow(cfg, send, router, waitHellos, cst, reg, cv, rt, rw) {
				added = true
			}
			continue
		}
		// Cheap and idempotent: re-assert the name rather than tracking whether it
		// changed, since this is also the rename path.
		applyMirrorName(cfg, mw.localWin, rw.name)
	}

	for _, remoteID := range reg.remoteIDs() {
		if !live[remoteID] {
			closeWindow(cfg, router, cst, reg, cv, remoteID)
		}
	}

	// Follow the remote's selection only when a window appeared. A local
	// `prefix c` makes the new remote window active, and without this the human
	// would stay on the old window while the remote moved; gating on "added"
	// keeps it from fighting ordinary local window navigation.
	if added && activeRemote != "" {
		if mw, ok := reg.byRemoteID(activeRemote); ok {
			cfg.LocalTmux("select-window", "-t", mw.localWin)
		}
	}
}

// mirrorNewWindow creates and wires the local mirror for one remote window,
// reporting whether it succeeded. Shared by reconcileWindows and addWindow.
func mirrorNewWindow(cfg Config, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, reg *registry, cv *converger, rt roundTrip, rw remoteWindow) bool {
	localWin, err := createMirrorWindow(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: mirror %s: %v\n", rw.id, err)
		return false
	}
	stampMirrorWindow(cfg, localWin, rw.name)
	mw := reg.add(rw.id, localWin)
	if err := setupWindow(cfg, send, router, waitHellos, cst, mw, cv, rt); err != nil {
		// Drop the half-created entry + local window so a later retry for this id
		// is not blocked by the already-registered guard.
		fmt.Fprintf(os.Stderr, "daemon: mirror %s: %v\n", rw.id, err)
		reg.remove(rw.id)
		cv.forget(rw.id)
		cst.forgetWindow(rw.id)
		cfg.LocalTmux("kill-window", "-t", localWin)
		return false
	}
	return true
}

// healLostWindows retires every mirror whose local window has gone, rebuilding
// it from the remote's own window list.
//
// The reattach sweep asks the same question, but once: localWindowGone answers
// on positive evidence alone, so a read it cannot make leaves the dead entry in
// place, and no other pass re-reads the local window set. Riding the coarse
// tick bounds a missed retire at one interval rather than the session (#514).
func healLostWindows(cfg Config, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, reg *registry, cv *converger, rt roundTrip) {
	// By id, re-read each time: retireMirror reconciles the whole registry, so
	// an entry taken before it ran may no longer be the one for that window.
	for _, remoteID := range reg.remoteIDs() {
		mw, ok := reg.byRemoteID(remoteID)
		if !ok {
			continue
		}
		if localWindowGone(cfg, mw.localWin) {
			retireMirror(cfg, send, router, waitHellos, cst, reg, cv, rt, remoteID)
		}
	}
}

// retryFailedShapes re-drives reconcile for any mirror whose last select-layout
// failed, once the float that blocked it has gone.
//
// applyLayout drops its OWN floats and retries within the pass, so a failure
// that outlives one is a float the user opened over the mirror (prefix + b/k/I,
// and ^o's remote picker) — not ours to reap, and tmux has no float-tolerant
// select-layout to work around it (tmux/tmux#5577: even its own window_layout
// does not parse back in). The reshape genuinely has to wait.
//
// What must not wait is the recovery. reconcileLayout runs on a %layout-change,
// a coalesced batch of them, or a reattach — all remote events. Closing a local
// float is none of those, so the mirror kept the stale shape until the remote
// happened to move that window again, which on an idle one can be a long time.
// applyLayout already leaves w.layout stale on failure precisely so a later pass
// retries; this is the pass.
//
// Gated on the local float check rather than retried blind: the retry costs a
// readLayout round-trip, and while the float is still open it can only fail
// again. That check is a local fork, and only for a window actually in the
// failed state — normally none are, so the steady-state cost is zero.
func retryFailedShapes(cfg Config, send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, reg *registry, cv *converger, rt roundTrip) {
	// By id, re-read each time: retireMirror reconciles the whole registry, so
	// an entry taken before it ran may no longer be the one for that window.
	for _, remoteID := range reg.remoteIDs() {
		mw, ok := reg.byRemoteID(remoteID)
		if !ok || mw.shapeFailedFor == "" {
			continue
		}
		if localWindowHasFloat(cfg, mw.localWin) {
			continue
		}
		if reconcileLayout(cfg, mw, send, router, waitHellos, cst, cv, rt) {
			retireMirror(cfg, send, router, waitHellos, cst, reg, cv, rt, remoteID)
		}
	}
}

// localWindowHasFloat reports whether the local window still holds any floating
// pane. A failed read answers true: that keeps a window whose state we cannot
// establish out of the retry, which is the same thing the tick would do next
// pass anyway, rather than spending a round-trip on a guess.
func localWindowHasFloat(cfg Config, localWin string) bool {
	out, err := cfg.LocalTmuxOut("list-panes", "-t", localWin, "-F", localPaneListFormat)
	if err != nil {
		return true
	}
	_, floats := parseLocalPaneList(out)
	return len(floats) > 0
}
