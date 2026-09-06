package daemon

import "fmt"

// PassthroughAllCmd returns the control-mode command that opts one remote
// window's panes into unrestricted DCS passthrough.
//
// This is the source-side twin of markRendererPane's local stamp (#464): that
// one keeps a store from being dropped on its way to the terminal, this one
// keeps it from being dropped before it ever reaches the control stream.
//
// Every remote window inherits lazytmux's global `allow-passthrough on`, and
// `on` releases a passthrough sequence only for a pane a client can see. The
// bridge holds one *control* client on the mirrored session, which satisfies no
// such test, so a kitty image store written by a program on the remote never
// enters %output at all — and tmux stores nothing and never retransmits, so it
// is gone. The unicode placeholders naming that image are ordinary grid text
// and cross regardless, which is why the failure looks like a pane painting
// full chrome around an empty picture rather than like a pane that is broken
// (#529).
//
// `-w`, not `-p`: allow-passthrough is a pane option, and pane options inherit
// from the window's, so one command covers every pane of the window including
// ones split after it lands (verified). A per-pane stamp would have to be
// re-issued for each pane the remote gains.
//
// Not reverted on teardown, as with AggressiveResizeOffCmd: the window keeps
// the wider setting for the rest of its life. The reach stays confined to the
// user's own panes on their own host, unlike the local mirror pane the #464
// comment argues about, which replays another host's bytes verbatim.
func PassthroughAllCmd(remoteID string) string {
	return fmt.Sprintf("set-option -w -t %s allow-passthrough all", remoteID)
}
