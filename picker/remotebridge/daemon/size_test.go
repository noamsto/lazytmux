package daemon

import "testing"

func TestConvergeCmd(t *testing.T) {
	if got := ConvergeCmd(210, 52); got != "refresh-client -C 210x52" {
		t.Errorf("ConvergeCmd = %q, want %q", got, "refresh-client -C 210x52")
	}
}
