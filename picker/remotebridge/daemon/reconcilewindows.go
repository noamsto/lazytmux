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
