// Package enrichstate is the shared PR-state precedence used by every Go
// renderer (statusline + enrichcard). It returns semantic roles, not output
// strings, because each renderer emits a different format (tmux #[fg=…] vs
// lipgloss). Keeping the precedence here prevents the recurring "fix the same
// rule in N renderers" regressions.
package enrichstate

type ColorRole int

const (
	ColorSuccess ColorRole = iota // green
	ColorPending                  // peach
	ColorFailure                  // red (failing check OR conflicting)
	ColorMerged                   // mauve
	ColorClosed                   // dim overlay (dead/superseded)
)

type GlyphRole int

const (
	GlyphSuccess GlyphRole = iota
	GlyphPending
	GlyphFailure
	GlyphConflict
	GlyphMerged
	GlyphClosed
)

// Classify maps a PR's state/check/mergeable to its color and glyph roles.
// merged/closed are terminal and win over check state; conflicting wins the
// glyph over a failing check; then failure → red, pending → peach, else green.
func Classify(state, check, mergeable string) (ColorRole, GlyphRole) {
	var color ColorRole
	switch {
	case state == "merged":
		color = ColorMerged
	case state == "closed":
		color = ColorClosed
	case check == "failure" || mergeable == "conflicting":
		color = ColorFailure
	case check == "pending":
		color = ColorPending
	default:
		color = ColorSuccess
	}

	var glyph GlyphRole
	switch {
	case state == "merged":
		glyph = GlyphMerged
	case state == "closed":
		glyph = GlyphClosed
	case mergeable == "conflicting":
		glyph = GlyphConflict
	case check == "failure":
		glyph = GlyphFailure
	case check == "pending":
		glyph = GlyphPending
	default:
		glyph = GlyphSuccess
	}
	return color, glyph
}
