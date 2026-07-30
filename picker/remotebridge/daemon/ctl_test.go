package daemon

import (
	"reflect"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// newCtlStateWith returns a state that already mirrors one window's panes, as
// the main loop would have recorded it.
func newCtlStateWith(win string, panes ...string) *ctlState {
	c := newCtlState()
	c.setWindowPanes(win, panes)
	return c
}

func TestParseCtlVerbTranslation(t *testing.T) {
	const sess = "my proj"
	tests := []struct {
		name    string
		argv    []string
		want    []string
		windows bool
		layout  string
	}{
		{
			name:   "split -h carries the pane cwd and targets the pane by id",
			argv:   []string{"1", "split-h", "%3"},
			want:   []string{"split-window -h -t %3 -c '#{pane_current_path}'"},
			layout: "@1",
		},
		{
			name:   "split -v",
			argv:   []string{"1", "split-v", "%3"},
			want:   []string{"split-window -v -t %3 -c '#{pane_current_path}'"},
			layout: "@1",
		},
		{
			// Killing a pane can empty its window, so it needs both reconciles.
			name:    "kill-pane wants both reconciles",
			argv:    []string{"1", "kill-pane", "%3"},
			want:    []string{"kill-pane -t %3"},
			windows: true,
			layout:  "@1",
		},
		{
			name:   "resize maps the direction and amount",
			argv:   []string{"1", "resize", "%3", "U", "5"},
			want:   []string{"resize-pane -t %3 -U 5"},
			layout: "@1",
		},
		{
			// No -d: the remote keeps the same pane active, which is what lets the
			// local reconcile's -d swap agree with it.
			name:   "swap sends no -d",
			argv:   []string{"1", "swap", "%3", "U"},
			want:   []string{"swap-pane -t %3 -U"},
			layout: "@1",
		},
		{
			// A session target with the index unspecified, quoted because the name
			// has a space — never a bare name in a target-window slot.
			name:    "new-window targets the session, quoted",
			argv:    []string{"1", "new-window", "%3"},
			want:    []string{"new-window -t 'my proj': -c '#{pane_current_path}'"},
			windows: true,
		},
		{
			name:    "kill-window resolves the pane's window",
			argv:    []string{"1", "kill-window", "%3"},
			want:    []string{"kill-window -t @1"},
			windows: true,
		},
		{
			name:    "rename quotes the new name",
			argv:    []string{"1", "rename", "%3", "my new name"},
			want:    []string{"rename-window -t @1 -- 'my new name'"},
			windows: true,
		},
		{
			// A name that would break the reflow delimiter or a command line is
			// sanitized before it reaches the remote.
			name:    "rename strips a pipe and control characters",
			argv:    []string{"1", "rename", "%3", "a|b\nc"},
			want:    []string{"rename-window -t @1 -- 'abc'"},
			windows: true,
		},
		{
			name:    "rename escapes an embedded quote",
			argv:    []string{"1", "rename", "%3", "it's"},
			want:    []string{`rename-window -t @1 -- 'it'\''s'`},
			windows: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCtlStateWith("@1", "%2", "%3")
			req, err := c.parseCtl(tc.argv, sess)
			if err != nil {
				t.Fatalf("parseCtl: %v", err)
			}
			if !reflect.DeepEqual(req.cmds, tc.want) {
				t.Errorf("cmds = %q, want %q", req.cmds, tc.want)
			}
			if req.wantWindows != tc.windows {
				t.Errorf("wantWindows = %v, want %v", req.wantWindows, tc.windows)
			}
			if req.wantLayout != tc.layout {
				t.Errorf("wantLayout = %q, want %q", req.wantLayout, tc.layout)
			}
		})
	}
}

func TestParseCtlRejects(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"unknown verb", []string{"1", "detach-client", "%3"}, "unknown verb"},
		{"unmirrored pane", []string{"1", "split-h", "%99"}, "not mirrored"},
		{"bad resize direction", []string{"1", "resize", "%3", "X", "5"}, "bad direction"},
		{"bad resize amount", []string{"1", "resize", "%3", "U", "abc"}, "bad amount"},
		{"resize amount out of range", []string{"1", "resize", "%3", "U", "0"}, "bad amount"},
		{"bad swap direction", []string{"1", "swap", "%3", "L"}, "bad direction"},
		{"wrong arity", []string{"1", "resize", "%3", "U"}, "wants 2 argument"},
		{"empty rename", []string{"1", "rename", "%3", "|||"}, "empty name"},
		{"truncated frame", []string{"1", "split-h"}, "at least version"},
		// A config reload can hand a new ctl to an old daemon; the mismatch must
		// be a message, not a silently-ignored gesture.
		{"version skew", []string{"99", "split-h", "%3"}, "reopen the bridge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCtlStateWith("@1", "%2", "%3")
			_, err := c.parseCtl(tc.argv, "rem")
			if err == nil {
				t.Fatalf("parseCtl(%q) succeeded, want error containing %q", tc.argv, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The daemon must build every remote command from the verb table, never forward
// text a caller supplied.
func TestParseCtlNeverForwardsRawCommandText(t *testing.T) {
	c := newCtlStateWith("@1", "%3")
	req, err := c.parseCtl([]string{"1", "rename", "%3", "x; kill-server"}, "rem")
	if err != nil {
		t.Fatalf("parseCtl: %v", err)
	}
	if len(req.cmds) != 1 || !strings.HasPrefix(req.cmds[0], "rename-window -t @1 -- ") {
		t.Fatalf("cmds = %q, want a single quoted rename-window", req.cmds)
	}
	if strings.Count(req.cmds[0], "\n") != 0 {
		t.Errorf("command must stay one line: %q", req.cmds[0])
	}
}

// submit must register the intent and send inside one critical section, so a
// drain that observes the sent command can never miss the intent.
func TestSubmitRegistersIntentBeforeSending(t *testing.T) {
	c := newCtlStateWith("@1", "%3")
	req, err := c.parseCtl([]string{"1", "split-h", "%3"}, "rem")
	if err != nil {
		t.Fatalf("parseCtl: %v", err)
	}

	sawIntent := false
	ok := c.submit(req, func(string) bool {
		// Read the field directly: takeIntents would deadlock on the held mutex,
		// which is itself the property under test.
		sawIntent = c.wantLayout["@1"]
		return true
	})
	if !ok {
		t.Fatal("submit reported not written")
	}
	if !sawIntent {
		t.Error("intent was not registered before the command was sent")
	}
}

// A request that loses the race with teardown must not be acked as accepted.
func TestSubmitReportsUnwritten(t *testing.T) {
	c := newCtlStateWith("@1", "%3")
	req, _ := c.parseCtl([]string{"1", "split-h", "%3"}, "rem")
	if c.submit(req, func(string) bool { return false }) {
		t.Error("submit reported written when send refused")
	}
}

func TestTakeIntentsCoalescesAndDrains(t *testing.T) {
	c := newCtlStateWith("@1", "%2", "%3")
	c.setWindowPanes("@2", []string{"%9"})
	for _, argv := range [][]string{
		{"1", "split-h", "%2"},
		{"1", "split-v", "%3"}, // same window: coalesces
		{"1", "split-h", "%9"}, // different window
		{"1", "new-window", "%2"},
	} {
		req, err := c.parseCtl(argv, "rem")
		if err != nil {
			t.Fatalf("parseCtl(%q): %v", argv, err)
		}
		c.submit(req, func(string) bool { return true })
	}

	windows, layouts := c.takeIntents()
	if !windows {
		t.Error("new-window should have registered the window reconcile")
	}
	if len(layouts) != 2 {
		t.Errorf("layouts = %v, want 2 distinct windows", layouts)
	}

	windows, layouts = c.takeIntents()
	if windows || len(layouts) != 0 {
		t.Errorf("second take should be empty, got %v %v", windows, layouts)
	}
}

// A closed window's focus state and pending intent must not outlive it.
func TestForgetWindowDropsState(t *testing.T) {
	c := newCtlStateWith("@1", "%3")
	req, _ := c.parseCtl([]string{"1", "split-h", "%3"}, "rem")
	c.submit(req, func(string) bool { return true })

	c.forgetWindow("@1")

	if _, layouts := c.takeIntents(); len(layouts) != 0 {
		t.Errorf("layouts = %v, want none after forgetWindow", layouts)
	}
	if _, err := c.parseCtl([]string{"1", "split-h", "%3"}, "rem"); err == nil {
		t.Error("a pane of a forgotten window must no longer resolve")
	}
}

func TestCtlArgvRoundTrip(t *testing.T) {
	tests := [][]string{
		{"1", "split-h", "%3"},
		{"1", "rename", "%3", "a name with spaces"},
		{"1", "rename", "%3", ""}, // an empty argument must survive as one field
		{"1", "rename", "%3", "quote's and |pipe|"},
	}
	for _, argv := range tests {
		if got := wire.DecodeArgv(wire.EncodeArgv(argv)); !reflect.DeepEqual(got, argv) {
			t.Errorf("round-trip %q -> %q", argv, got)
		}
	}
	if got := wire.DecodeArgv(nil); got != nil {
		t.Errorf("empty payload decoded to %q, want nil", got)
	}
}
