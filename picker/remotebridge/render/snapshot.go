package render

import (
	"bytes"
	"fmt"
)

func Seed(captured []byte, cursorX, cursorY int, altScreen, appCursorKeys bool) []byte {
	var b bytes.Buffer
	if altScreen {
		b.WriteString("\x1b[?1049h")
	}
	if appCursorKeys {
		b.WriteString("\x1b[?1h")
	}
	b.WriteString("\x1b[2J\x1b[H") // clear + home
	b.Write(captured)
	fmt.Fprintf(&b, "\x1b[%d;%dH", cursorY+1, cursorX+1)
	return b.Bytes()
}
