package daemon

import (
	"reflect"
	"strings"
	"testing"
)

// Both spellings, in one slice, so a caller cannot send the new name without
// the legacy one aeye still reads (D7). The assertion is on the whole slice
// rather than on a membership test: a constructor returning only one command
// must fail here.
func TestRelayEnvCmd(t *testing.T) {
	want := []string{
		"set-environment -t 'mysession' OG_RELAY_GRAPHICS 'sixel'",
		"set-environment -t 'mysession' LZTMUX_RELAY_GRAPHICS 'sixel'",
	}
	if got := RelayEnvCmd("mysession", "sixel"); !reflect.DeepEqual(got, want) {
		t.Errorf("RelayEnvCmd = %q, want %q", got, want)
	}
}

// An empty value is the correct overwrite for "no relayable capability" under
// the grammar (RelayEnvVar's doc comment) — it must still produce a
// well-formed set-environment with an empty quoted argument, not a malformed
// line and not a skipped write. Both spellings carry the same value, since
// both read the one graphics.RelaySource cell.
func TestRelayEnvCmdEmptyValue(t *testing.T) {
	want := []string{
		"set-environment -t 'mysession' OG_RELAY_GRAPHICS ''",
		"set-environment -t 'mysession' LZTMUX_RELAY_GRAPHICS ''",
	}
	if got := RelayEnvCmd("mysession", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("RelayEnvCmd with empty value = %q, want %q", got, want)
	}
}

func TestRelayEnvCmdQuotesSessionName(t *testing.T) {
	want := []string{
		`set-environment -t 'my'\''session' OG_RELAY_GRAPHICS 'sixel'`,
		`set-environment -t 'my'\''session' LZTMUX_RELAY_GRAPHICS 'sixel'`,
	}
	if got := RelayEnvCmd("my'session", "sixel"); !reflect.DeepEqual(got, want) {
		t.Errorf("RelayEnvCmd with quote in session name = %q, want %q", got, want)
	}
}

func TestRelayEnvUnsetCmd(t *testing.T) {
	want := []string{
		"set-environment -u -t 'mysession' OG_RELAY_GRAPHICS",
		"set-environment -u -t 'mysession' LZTMUX_RELAY_GRAPHICS",
	}
	if got := RelayEnvUnsetCmd("mysession"); !reflect.DeepEqual(got, want) {
		t.Errorf("RelayEnvUnsetCmd = %q, want %q", got, want)
	}
}

// Teardown must unset as many variables as publish sets, or a rename that adds
// a spelling to one constructor and not the other leaves a stale value behind.
func TestRelayEnvConstructorsStayPaired(t *testing.T) {
	set := RelayEnvCmd("s", "sixel")
	unset := RelayEnvUnsetCmd("s")
	if len(set) != 2 || len(unset) != 2 {
		t.Fatalf("RelayEnvCmd=%d commands, RelayEnvUnsetCmd=%d — want 2 each", len(set), len(unset))
	}
	for i, v := range []string{RelayEnvVar, RelayEnvLegacyVar} {
		if !strings.Contains(set[i], v) || !strings.Contains(unset[i], v) {
			t.Errorf("command pair %d does not name %s: %q / %q", i, v, set[i], unset[i])
		}
	}
}
