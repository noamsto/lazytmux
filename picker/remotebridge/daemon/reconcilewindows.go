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
// It exists because a structural gesture performed locally cannot be driven by
// the remote's own notification: tmux emits a notification caused by a control
// client's own command inside that command's %begin/%end block, and the reply
// reader folds a block's body into the reply's Data — so our own %window-add /
// %window-close / %window-renamed never surfaces. Re-reading ground truth covers
// all three with one round-trip, and it self-heals a notification the reply
// reader dropped for any other reason.
//
// Round-trips are routing-aware: sibling windows are streaming throughout.
func reconcileWindows(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, cst *ctlState, reg *registry, cv *converger) {
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }

	send(fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowListFormat))
	lw, ok := reply(reader)
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
			if mirrorNewWindow(cfg, reader, send, router, connCh, cst, reg, cv, rw) {
				added = true
			}
			continue
		}
		// Cheap and idempotent: re-assert the name rather than tracking whether it
		// changed, since this is also the rename path.
		if name := sanitizeWindowName(rw.name); name != "" {
			cfg.LocalTmux("set-option", "-w", "-t", mw.localWin, "@window_bridge_name", name)
			cfg.LocalTmux("rename-window", "-t", mw.localWin, name)
		}
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

	cfg.reflow()
}

// mirrorNewWindow creates and wires the local mirror for one remote window,
// reporting whether it succeeded. Shared by reconcileWindows and addWindow.
func mirrorNewWindow(cfg Config, reader *controlmode.Reader, send func(string), router *Router, connCh chan helloConn, cst *ctlState, reg *registry, cv *converger, rw remoteWindow) bool {
	localWin := reg.allocLocalWin(cfg.LocalSess)
	if err := cfg.LocalTmux("new-window", "-d", "-t", localWin); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: mirror %s: new-window %s: %v\n", rw.id, localWin, err)
		return false
	}
	stampMirrorWindow(cfg, localWin, rw.name)
	mw := reg.add(rw.id, localWin)
	reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }
	if err := setupWindow(cfg, reader, send, router, connCh, cst, mw, cv, reply); err != nil {
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
