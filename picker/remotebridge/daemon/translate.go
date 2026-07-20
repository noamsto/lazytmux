package daemon

import "github.com/noamsto/lazytmux/picker/remotebridge/controlmode"

// translateWindowNotification maps a parsed remote window notification to a
// single local tmux argv, filtered to the bridged session's registry (B2): a
// window id outside the registry yields (nil, false). WindowAdd/WindowClose are
// orchestration (spawn pipeline / teardown), not a single argv, so they return
// (nil, false) here and are handled in Run's loop. WindowPaneChanged is a
// deliberate M2.2 no-op (focus routing is M2.3).
func translateWindowNotification(l controlmode.Line, reg *registry) ([]string, bool) {
	switch l.Kind {
	case controlmode.WindowRenamed:
		if len(l.Args) == 0 {
			return nil, false
		}
		w, ok := reg.byRemoteID(l.Args[0])
		if !ok {
			return nil, false
		}
		return []string{"rename-window", "-t", w.localWin, string(l.Data)}, true
	case controlmode.SessionWindowChanged:
		if len(l.Args) < 2 {
			return nil, false
		}
		w, ok := reg.byRemoteID(l.Args[1])
		if !ok {
			return nil, false
		}
		return []string{"select-window", "-t", w.localWin}, true
	default:
		return nil, false
	}
}
