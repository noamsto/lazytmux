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
	captured := readCapture(reader)
	// len, not == nil: a pane that closed between list-panes and this
	// capture-pane gets an %error reply, whose Data is empty-but-non-nil —
	// == nil alone would miss it and seed the renderer with a bogus blank
	// screen (cross-task delta: Task 8 owns this race).
	if len(captured) == 0 {
		return nil, fmt.Errorf("capture-pane returned no data for %s", paneID)
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
// content, already newline-joined by the Reader).
func readCapture(reader *controlmode.Reader) []byte {
	l, _ := readReply(reader)
	return l.Data
}
