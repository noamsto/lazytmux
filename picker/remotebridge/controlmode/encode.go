package controlmode

import "fmt"

func SendKeysArgs(pane string, b []byte, maxHexPerCmd int) [][]string {
	if len(b) == 0 {
		return nil
	}
	if maxHexPerCmd < 1 {
		maxHexPerCmd = 1
	}
	var cmds [][]string
	for i := 0; i < len(b); i += maxHexPerCmd {
		end := i + maxHexPerCmd
		if end > len(b) {
			end = len(b)
		}
		cmd := []string{"send-keys", "-H", "-t", pane}
		for _, by := range b[i:end] {
			cmd = append(cmd, fmt.Sprintf("%02x", by))
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}
