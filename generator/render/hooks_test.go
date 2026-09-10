package render

import (
	"testing"

	"github.com/noamsto/lazytmux/generator/config"
	"github.com/noamsto/lazytmux/generator/paths"
)

// I7's bytes, pinned literally. The reference and the generator are otherwise
// compared only to each other, so a transcription error shared by both would
// pass the extraction diff.
func TestPersistBlockBytes(t *testing.T) {
	wire := "/nix/store/aaaa-persist-wire-stub"
	want := "\n# === tmux-remux (Phase 2a, opt-in via programs.lazytmux.persist) ===\n" +
		"run-shell \"/nix/store/aaaa-persist-wire-stub #{q:version}\"\n"

	got := Build(&config.Config{}, &paths.Paths{PersistWireScript: &wire}).PersistBlock
	if got != want {
		t.Errorf("PersistBlock =\n%q\nwant\n%q", got, want)
	}

	if got := Build(&config.Config{}, &paths.Paths{}).PersistBlock; got != "" {
		t.Errorf("PersistBlock with persist off = %q, want %q", got, "")
	}
}

func TestCarouselHooksBytes(t *testing.T) {
	toggle := "/nix/store/bbbb-toggle/bin/tmux-claude-images"
	want := "set-hook -g client-session-changed[60] 'run-shell -b \"/nix/store/bbbb-toggle/bin/tmux-claude-images --reconcile\"'\n" +
		"set-hook -g session-window-changed[60] 'run-shell -b \"/nix/store/bbbb-toggle/bin/tmux-claude-images --reconcile\"'\n" +
		"set-hook -g client-attached[60]        'run-shell -b \"/nix/store/bbbb-toggle/bin/tmux-claude-images --reconcile\"'\n"

	got := Build(&config.Config{}, &paths.Paths{CarouselToggle: &toggle}).CarouselHooks
	if got != want {
		t.Errorf("CarouselHooks =\n%q\nwant\n%q", got, want)
	}

	if got := Build(&config.Config{}, &paths.Paths{}).CarouselHooks; got != "" {
		t.Errorf("CarouselHooks with no toggle = %q, want %q", got, "")
	}
}
