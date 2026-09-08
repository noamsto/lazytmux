package daemon

import (
	"strings"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// layoutFlagAlphabet is window_printable_flags' output alphabet
// (window.c:1288-1313, pinned tree): activity, bell, silence, current, last,
// marked, modal, zoomed. Confirmed against the pinned tmux source, not copied
// from the spec.
const layoutFlagAlphabet = "#!~*-MOZ"

// layoutNotice is what a %layout-change line carries that readLayout would
// otherwise fetch: the unzoomed layout string and the zoom flag.
type layoutNotice struct {
	layout string
	zoomed bool
}

// parseLayoutNotice validates a %layout-change line's fields positionally
// rather than by count alone, since a shifted line (the trailing flags field
// pushed into a layout slot, or vice versa) can carry the right number of
// fields with the wrong shape. Anything that doesn't fit — including a shape
// this tmux has never been observed to emit — returns ok=false so the caller
// falls back to a read: a future flag character costs one round-trip, never a
// wrong answer.
func parseLayoutNotice(l controlmode.Line) (layoutNotice, bool) {
	switch len(l.Args) {
	case 3:
		if !layoutShaped(l.Args[1]) || !layoutShaped(l.Args[2]) {
			return layoutNotice{}, false
		}
		return layoutNotice{layout: l.Args[1]}, true
	case 4:
		if !layoutShaped(l.Args[1]) || !layoutShaped(l.Args[2]) || !flagsShaped(l.Args[3]) {
			return layoutNotice{}, false
		}
		return layoutNotice{layout: l.Args[1], zoomed: strings.ContainsRune(l.Args[3], 'Z')}, true
	default:
		return layoutNotice{}, false
	}
}

// layoutShaped reports whether s could be a %layout-change layout field: a
// four-digit lowercase hex checksum, a comma, then at least one more byte.
// It doesn't call controlmode.ParseLayout — the caller does that once, on the
// field this picks out, and treats a parse error the same as ok=false here.
func layoutShaped(s string) bool {
	if len(s) < 6 || s[4] != ',' {
		return false
	}
	for i := 0; i < 4; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// flagsShaped reports whether s is entirely drawn from layoutFlagAlphabet.
// Empty is rejected: window_printable_flags with nothing to report yields
// an empty field, which tmux drops rather than emit, so a genuinely empty
// 4th field never occurs.
func flagsShaped(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(layoutFlagAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}
