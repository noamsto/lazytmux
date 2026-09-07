package graphics

import "strings"

// Relay is the local terminal's relayable-graphics capability: the one value
// computed once and read by both the local drop policy (R1, Proxy.Filter) and
// the value the daemon publishes to the remote (R5) — so the two can never
// disagree about what may be relayed (R6). The zero value means no relayable
// capability.
type Relay struct {
	sixel bool
	raw   string // the raw client_termfeatures this was derived from (R1 logging)
}

// RelayFromTermFeatures derives a Relay from tmux's raw, comma-separated
// client_termfeatures (e.g. "bpaste,focus,RGB,sixel,title"). It is the ONLY
// constructor from external input, so nothing wider than tmux's own render
// condition can be injected at the boundary (R6).
//
// Sixel is set iff a whole "sixel" token is present — a token that merely
// contains "sixel" as a substring ("nosixel", "sixelfoo") must not match, so
// this splits on commas and compares whole, trimmed tokens rather than
// searching the raw string.
func RelayFromTermFeatures(feats string) Relay {
	r := Relay{raw: feats}
	for _, tok := range strings.Split(feats, ",") {
		if strings.TrimSpace(tok) == "sixel" {
			r.sixel = true
			break
		}
	}
	return r
}

// String renders the cross-repo grammar the daemon publishes to the remote
// session (R5): "sixel" when set, "" when not. What the daemon publishes is
// exactly this string of exactly the value that gates Sixel() below — that
// sentence is R6.
func (r Relay) String() string {
	if r.sixel {
		return "sixel"
	}
	return ""
}

// Sixel reports whether a complete bare sixel may be relayed to the local
// terminal. The predicate Proxy.Filter gates on.
func (r Relay) Sixel() bool { return r.sixel }

// DefaultRasterHold is the byte budget for holding a bare partial sixel meant
// for relay (R4), and the default for the daemon's -gfx-relay-max-bytes flag.
//
// 16 MiB: the largest chafa sixel measured is 7.64 MB (240x60 cells), so this
// is a little over 2x the worst case actually observed, and payload size
// scales with cell count, so a larger terminal needs the headroom.
//
// Deliberately independent of wire.maxFrameSize, which is also 16 MiB:
// wire.WriteStream splits an oversized payload across frames, so a held
// payload never has to fit in one. The two constants sharing a value is a
// coincidence, not a constraint.
const DefaultRasterHold = 16 << 20
