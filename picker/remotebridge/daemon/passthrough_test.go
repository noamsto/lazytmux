package daemon

import (
	"strings"
	"testing"
)

// `on` releases a passthrough only for a pane a client can see, and a mirrored
// session's only client is a control client, so the store dies before reaching
// %output (#529).
func TestPassthroughAllCmd(t *testing.T) {
	want := "set-option -w -t @3 allow-passthrough all"
	if got := PassthroughAllCmd("@3"); got != want {
		t.Errorf("PassthroughAllCmd = %q, want %q", got, want)
	}
	// Window-scoped, not `-p`: pane options inherit from the window's, so this
	// covers panes the remote splits after the command lands. A `-p` stamp
	// would silently miss exactly the carousel case that motivated this, since
	// that pane is created well after setup.
	if !strings.Contains(PassthroughAllCmd("@3"), " -w ") {
		t.Error("the opt-in must be window-scoped so later panes inherit it")
	}
	// `all`, not `on`: `on` is what the remote already inherits from tmux-og's
	// global, and is the setting that drops the store.
	if strings.HasSuffix(PassthroughAllCmd("@3"), " on") {
		t.Error("`on` is the broken setting, not the fix")
	}
}
