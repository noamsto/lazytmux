package daemon

import "testing"

func TestRelayEnvCmd(t *testing.T) {
	want := "set-environment -t 'mysession' LZTMUX_RELAY_GRAPHICS 'sixel'"
	if got := RelayEnvCmd("mysession", "sixel"); got != want {
		t.Errorf("RelayEnvCmd = %q, want %q", got, want)
	}
}

// An empty value is the correct overwrite for "no relayable capability" under
// the grammar (RelayEnvVar's doc comment) — it must still produce a
// well-formed set-environment with an empty quoted argument, not a malformed
// line and not a skipped write.
func TestRelayEnvCmdEmptyValue(t *testing.T) {
	want := "set-environment -t 'mysession' LZTMUX_RELAY_GRAPHICS ''"
	if got := RelayEnvCmd("mysession", ""); got != want {
		t.Errorf("RelayEnvCmd with empty value = %q, want %q", got, want)
	}
}

func TestRelayEnvCmdQuotesSessionName(t *testing.T) {
	want := `set-environment -t 'my'\''session' LZTMUX_RELAY_GRAPHICS 'sixel'`
	if got := RelayEnvCmd("my'session", "sixel"); got != want {
		t.Errorf("RelayEnvCmd with quote in session name = %q, want %q", got, want)
	}
}

func TestRelayEnvUnsetCmd(t *testing.T) {
	want := "set-environment -u -t 'mysession' LZTMUX_RELAY_GRAPHICS"
	if got := RelayEnvUnsetCmd("mysession"); got != want {
		t.Errorf("RelayEnvUnsetCmd = %q, want %q", got, want)
	}
}
