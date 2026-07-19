package daemon

import "fmt"

// ConvergeCmd returns the control-mode command that sets the single control
// client's size (and thus, under window-size=latest, the remote window) to WxH.
func ConvergeCmd(w, h int) string {
	return fmt.Sprintf("refresh-client -C %dx%d", w, h)
}
