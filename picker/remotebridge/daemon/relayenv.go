package daemon

import "fmt"

// RelayEnvVar is the environment variable the daemon publishes the local
// terminal's relayable-graphics capability under. It is a binding cross-repo
// contract — noamsto/aeye reads it to decide whether a program running behind
// the bridge may hand the terminal a sixel image directly instead of going
// through the kitty/carousel path.
//
// Grammar: a comma-separated set of protocol tokens the *local* terminal can
// paint. Exactly one token is defined today, "sixel". Absent, empty, or an
// unrecognised token all mean no relayable capability; an unrecognised token
// is never permission to relay it.
const RelayEnvVar = "OG_RELAY_GRAPHICS"

// RelayEnvLegacyVar is the pre-rename spelling of RelayEnvVar, published
// beside it because the only reader in the world is the pinned `aeye` input,
// whose gallery.go still calls os.Getenv on this name. Nothing in this repo
// reads either variable, so dropping this one would break sixel relay over the
// bridge with no build or test failing.
//
// Removal condition: drop this write once an aeye release reads RelayEnvVar and
// flake.lock is bumped to it.
const RelayEnvLegacyVar = "LZTMUX_RELAY_GRAPHICS"

// RelayEnvCmd returns the control-mode commands that publish value (a
// graphics.Relay.String()) into the bridged remote session's environment
// table, where it is visible to, and inherited by, every pane the session
// gains from this point on.
//
// This rides a control-mode set-environment against the remote SESSION,
// deliberately not the ssh command's own env prefix (sshControlArgs): a
// variable on that prefix reaches a remote pane only because the remote's own
// tmux update-environment allowlist names it, so a newly introduced variable
// there is silently inert against any remote whose config predates it.
// set-environment against the session carries no such allowlist and is,
// verified, inherited by panes split afterwards.
//
// Both spellings are returned as one slice so callers hand them to a single
// variadic send: stampAll fails a whole batch as a unit, while two independent
// sends could leave the remote holding a fresh RelayEnvVar beside a stale
// RelayEnvLegacyVar — the one aeye actually reads.
func RelayEnvCmd(session, value string) []string {
	return []string{
		fmt.Sprintf("set-environment -t %s %s %s", tmuxQuote(session), RelayEnvVar, tmuxQuote(value)),
		fmt.Sprintf("set-environment -t %s %s %s", tmuxQuote(session), RelayEnvLegacyVar, tmuxQuote(value)),
	}
}

// RelayEnvUnsetCmd is RelayEnvCmd's teardown twin: it removes both variables
// from the remote session's environment table rather than leaving a stale
// value for whoever attaches to that session next.
func RelayEnvUnsetCmd(session string) []string {
	return []string{
		fmt.Sprintf("set-environment -u -t %s %s", tmuxQuote(session), RelayEnvVar),
		fmt.Sprintf("set-environment -u -t %s %s", tmuxQuote(session), RelayEnvLegacyVar),
	}
}
