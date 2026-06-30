package enrichstate

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name                    string
		state, check, mergeable string
		wantColor               ColorRole
		wantGlyph               GlyphRole
	}{
		{"merged wins over pending check", "merged", "pending", "", ColorMerged, GlyphMerged},
		{"closed is dim, distinct from merged", "closed", "success", "", ColorClosed, GlyphClosed},
		{"conflicting wins glyph over failure", "open", "failure", "conflicting", ColorFailure, GlyphConflict},
		{"failure check is red", "open", "failure", "", ColorFailure, GlyphFailure},
		{"pending check is peach", "open", "pending", "", ColorPending, GlyphPending},
		{"clean open PR is green success", "open", "success", "mergeable", ColorSuccess, GlyphSuccess},
		{"empty everything defaults to success", "", "", "", ColorSuccess, GlyphSuccess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotColor, gotGlyph := Classify(c.state, c.check, c.mergeable)
			if gotColor != c.wantColor || gotGlyph != c.wantGlyph {
				t.Errorf("Classify(%q,%q,%q) = (%v,%v), want (%v,%v)",
					c.state, c.check, c.mergeable, gotColor, gotGlyph, c.wantColor, c.wantGlyph)
			}
		})
	}
}
