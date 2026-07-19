package daemon

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

// PaneSeed issues display-message/capture-pane for paneID over an established
// control stream and returns the render.Seed bytes. send writes a command
// line to the control connection; reader is the shared controlmode.Reader.
// Reply blocks are consumed in issue order (one reply per command).
func PaneSeed(reader *controlmode.Reader, send func(string), paneID string) ([]byte, error) {
	send(fmt.Sprintf("display-message -p -t %s -F '#{cursor_x} #{cursor_y} #{alternate_on} #{keypad_cursor_flag}'", paneID))
	cx, cy, alt, appck := readCursor(reader)

	send(fmt.Sprintf("capture-pane -e -p -t %s", paneID))
	captured, isErr := readCapture(reader)
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

// readReply returns the next command-reply block (Kind End or Error),
// skipping %output and async notifications (%session-changed, %layout-change,
// …). ok is false at EOF.
func readReply(reader *controlmode.Reader) (controlmode.Line, bool) {
	for {
		l, ok := reader.Next()
		if !ok {
			return controlmode.Line{}, false
		}
		if l.Kind == controlmode.End || l.Kind == controlmode.Error {
			return l, true
		}
	}
}

// readCursor reads the display-message reply and parses
// "cursor_x cursor_y alternate_on keypad_cursor_flag".
func readCursor(reader *controlmode.Reader) (cx, cy int, alt, appCursorKeys bool) {
	l, ok := readReply(reader)
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

// readCapture reads the capture-pane reply and returns its body (pane
// content, already newline-joined by the Reader) plus whether the reply was
// an error — either an %error block (e.g. the pane closed between
// list-panes and this capture-pane) or EOF before any reply arrived. isErr
// is the only signal PaneSeed uses to reject a seed: a successful reply with
// an empty body is a valid blank pane, not an error.
func readCapture(reader *controlmode.Reader) (data []byte, isErr bool) {
	l, ok := readReply(reader)
	if !ok {
		return nil, true
	}
	return l.Data, l.Kind == controlmode.Error
}
