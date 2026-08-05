package daemon

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

// roundTrip sends one control-mode command and returns that command's own reply
// block — always a closure over stream.stamp + readReplyRouting, so it neither
// drops another pane's live %output while it waits nor mistakes some other
// command's reply for its own. It is the only way a mirror path asks the remote
// a question; everything else is fire-and-forget.
type roundTrip = func(cmd string) (controlmode.Line, bool)

// PaneSeed issues display-message/capture-pane for paneID over an established
// control stream and returns the render.Seed bytes.
func PaneSeed(rt roundTrip, paneID string) ([]byte, error) {
	cx, cy, alt, appck := readCursor(rt, paneID)

	captured, isErr := readCapture(rt, paneID)
	// isErr, not len(captured)==0: a genuinely blank pane is a valid
	// successful capture with empty Data, so an emptiness check alone would
	// reject a legitimate blank seed. A pane that closed between list-panes
	// and this capture-pane instead gets a %error reply (non-empty body:
	// the error text) — isErr keys off Kind, not body length.
	if isErr {
		return nil, fmt.Errorf("capture-pane failed for %s", paneID)
	}
	captured = replaceLF(captured)
	return render.Seed(captured, cx, cy, alt, appck), nil
}

func replaceLF(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\n", "\r\n"))
}

// readCursor reads "cursor_x cursor_y alternate_on keypad_cursor_flag" for a
// pane.
func readCursor(rt roundTrip, paneID string) (cx, cy int, alt, appCursorKeys bool) {
	l, ok := rt(fmt.Sprintf("display-message -p -t %s -F '#{cursor_x} #{cursor_y} #{alternate_on} #{keypad_cursor_flag}'", paneID))
	if !ok || l.Kind == controlmode.Error {
		return 0, 0, false, false
	}
	fields := strings.Fields(string(l.Data))
	if len(fields) != 4 {
		return 0, 0, false, false
	}
	cx, _ = strconv.Atoi(fields[0])
	cy, _ = strconv.Atoi(fields[1])
	return cx, cy, fields[2] == "1", fields[3] == "1"
}

// readCapture captures a pane's screen and returns it (already newline-joined
// by the Reader) plus whether the reply was an error — either an %error block
// (e.g. the pane closed between list-panes and this capture-pane) or EOF before
// any reply arrived. isErr is the only signal PaneSeed uses to reject a seed: a
// successful reply with an empty body is a valid blank pane, not an error.
func readCapture(rt roundTrip, paneID string) (data []byte, isErr bool) {
	l, ok := rt(fmt.Sprintf("capture-pane -e -p -t %s", paneID))
	if !ok {
		return nil, true
	}
	return l.Data, l.Kind == controlmode.Error
}
