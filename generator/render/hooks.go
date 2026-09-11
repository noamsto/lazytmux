package render

import (
	"fmt"

	"github.com/noamsto/tmux-og/generator/paths"
)

// carouselHooks keeps the per-pane carousels in step with focus: stash the
// off-screen ones, restore the visible one. Indexed [60] so it coexists with
// the reflow (index 0) and splash ([50]) hooks on the same events. Empty when
// the toggle package is not wired in.
func carouselHooks(p *paths.Paths) string {
	if p.CarouselToggle == nil {
		return ""
	}
	cmd := *p.CarouselToggle + " --reconcile"
	return fmt.Sprintf(""+
		"set-hook -g client-session-changed[60] 'run-shell -b \"%[1]s\"'\n"+
		"set-hook -g session-window-changed[60] 'run-shell -b \"%[1]s\"'\n"+
		"set-hook -g client-attached[60]        'run-shell -b \"%[1]s\"'\n", cmd)
}

// persistBlock wires tmux-remux, or emits nothing when persist is off. The
// leading blank line is part of the value, not decoration; the template line it
// sits on carries the preceding one.
// #{q:version} is literal text — tmux expands it, nothing here does.
func persistBlock(p *paths.Paths) string {
	if p.PersistWireScript == nil {
		return ""
	}
	return "\n# === tmux-remux (Phase 2a, opt-in via programs.tmux-og.persist) ===\n" +
		fmt.Sprintf("run-shell \"%s #{q:version}\"\n", *p.PersistWireScript)
}
