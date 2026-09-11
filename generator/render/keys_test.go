package render

import (
	"strings"
	"testing"

	"github.com/noamsto/tmux-og/generator/config"
	"github.com/noamsto/tmux-og/generator/paths"
)

func keysPaths() *paths.Paths {
	return &paths.Paths{
		Scripts: map[string]string{
			"tmux-pr-enrich":   "/store/tmux-pr-enrich",
			"tmux-issue-stamp": "/store/tmux-issue-stamp",
		},
		Bin: map[string]string{
			"og-remote-bridge-ctl": "/store/ctl",
			"tmux-enrich-card":     "/store/card",
		},
	}
}

// The escaping is scoped to the inner new-pane command; the if-shell wrapper's
// own quotes stay bare, and a quote inside the suffix must come out escaped.
func TestFloatNewPaneGuardEscapesInnerCommandOnly(t *testing.T) {
	f := mkFloat("90%", "90%", "5%", "5%")
	got := floatNewPaneGuard(f, "", `"echo hi" \; set -p @pane_label hi`)
	want := `if-shell "tmux list-commands new-pane | grep -q -- -A" ` +
		`"new-pane -x 90% -y 90% -X 5% -Y 5% -B heavy -A \"echo hi\" \; set -p @pane_label hi \; ` +
		`set -p @float_geom '90% 90% 5% 5%' \; set -p remain-on-exit off" ` +
		`"new-pane -x 90% -y 90% -X 5% -Y 5% -B heavy \"echo hi\" \; set -p @pane_label hi \; ` +
		`set -p @float_geom '90% 90% 5% 5%' \; set -p remain-on-exit off"`
	if got != want {
		t.Fatalf("guard =\n%q\nwant\n%q", got, want)
	}
}

// An absent optional tool must leave the template line's newline alone, so the
// output keeps exactly one empty line where the bind would have been.
func TestOptionalBindsCarryNoTrailingNewline(t *testing.T) {
	p := keysPaths()
	if got := carouselBind(p); got != "" {
		t.Fatalf("carouselBind with no toggle = %q, want empty", got)
	}
	if got := prdashBind(p); got != "" {
		t.Fatalf("prdashBind with no prdash = %q, want empty", got)
	}
	toggle, prdash := "/store/tmux-claude-images", "/store/prdash"
	p.CarouselToggle, p.Prdash = &toggle, &prdash
	for name, got := range map[string]string{
		"carouselBind": carouselBind(p),
		"prdashBind":   prdashBind(p),
	} {
		if got == "" || strings.HasSuffix(got, "\n") {
			t.Fatalf("%s = %q, want one line with no trailing newline", name, got)
		}
	}
}

func TestEnrichCardBindUsesRawIcons(t *testing.T) {
	cfg := &config.Config{Enrich: config.Enrich{Icons: map[string]string{"linear": "L#"}}}
	got := enrichCardBind(cfg, keysPaths())
	if !strings.Contains(got, `--icon-linear 'L#' --icon-github '`+enrichIconDefaults["github"]+`'`) {
		t.Fatalf("override or default icon missing: %q", got)
	}
	if strings.Contains(got, "L##") {
		t.Fatalf("card must carry the raw dialect: %q", got)
	}
}
