package daemon

import (
	"fmt"
	"os"
	"os/exec"
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
			argv:   []string{wire.CtlProtocolVersion, "split-h", "%3"},
			want:   []string{"split-window -h -t %3 -c '#{pane_current_path}'"},
			layout: "@1",
		},
		{
			name:   "split -v",
			argv:   []string{wire.CtlProtocolVersion, "split-v", "%3"},
			want:   []string{"split-window -v -t %3 -c '#{pane_current_path}'"},
			layout: "@1",
		},
		{
			// Killing a pane can empty its window, so it needs both reconciles.
			name:    "kill-pane wants both reconciles",
			argv:    []string{wire.CtlProtocolVersion, "kill-pane", "%3"},
			want:    []string{"kill-pane -t %3"},
			windows: true,
			layout:  "@1",
		},
		{
			name:   "resize maps the direction and amount",
			argv:   []string{wire.CtlProtocolVersion, "resize", "%3", "U", "5"},
			want:   []string{"resize-pane -t %3 -U 5"},
			layout: "@1",
		},
		{
			// No -d: the remote keeps the same pane active, which is what lets the
			// local reconcile's -d swap agree with it.
			name:   "swap sends no -d",
			argv:   []string{wire.CtlProtocolVersion, "swap", "%3", "U"},
			want:   []string{"swap-pane -t %3 -U"},
			layout: "@1",
		},
		{
			// A session target with the index unspecified, quoted because the name
			// has a space — never a bare name in a target-window slot.
			name:    "new-window targets the session, quoted",
			argv:    []string{wire.CtlProtocolVersion, "new-window", "%3"},
			want:    []string{"new-window -t 'my proj': -c '#{pane_current_path}'"},
			windows: true,
		},
		{
			name:    "kill-window resolves the pane's window",
			argv:    []string{wire.CtlProtocolVersion, "kill-window", "%3"},
			want:    []string{"kill-window -t @1"},
			windows: true,
		},
		{
			name:    "rename quotes the new name",
			argv:    []string{wire.CtlProtocolVersion, "rename", "%3", "my new name"},
			want:    []string{"rename-window -t @1 -- 'my new name'"},
			windows: true,
		},
		{
			// A name that would break the reflow delimiter or a command line is
			// sanitized before it reaches the remote.
			name:    "rename strips a pipe and control characters",
			argv:    []string{wire.CtlProtocolVersion, "rename", "%3", "a|b\nc"},
			want:    []string{"rename-window -t @1 -- 'abc'"},
			windows: true,
		},
		{
			name:    "rename escapes an embedded quote",
			argv:    []string{wire.CtlProtocolVersion, "rename", "%3", "it's"},
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
		{"unknown verb", []string{wire.CtlProtocolVersion, "detach-client", "%3"}, "unknown verb"},
		{"unmirrored pane", []string{wire.CtlProtocolVersion, "split-h", "%99"}, "not mirrored"},
		{"bad resize direction", []string{wire.CtlProtocolVersion, "resize", "%3", "X", "5"}, "bad direction"},
		{"bad resize amount", []string{wire.CtlProtocolVersion, "resize", "%3", "U", "abc"}, "bad amount"},
		{"resize amount out of range", []string{wire.CtlProtocolVersion, "resize", "%3", "U", "0"}, "bad amount"},
		{"bad swap direction", []string{wire.CtlProtocolVersion, "swap", "%3", "L"}, "bad direction"},
		{"wrong arity", []string{wire.CtlProtocolVersion, "resize", "%3", "U"}, "wants 2 argument"},
		{"empty rename", []string{wire.CtlProtocolVersion, "rename", "%3", "|||"}, "empty name"},
		{"truncated frame", []string{wire.CtlProtocolVersion, "split-h"}, "at least version"},
		// A config reload can hand a new ctl to an old daemon; the mismatch must
		// be a message, not a silently-ignored gesture.
		{"version skew", []string{"1", "split-h", "%3"}, "reopen the bridge"},
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

func TestParseCtlPingProbesCompatibilityBeforePaneLookup(t *testing.T) {
	c := newCtlState()
	req, err := c.parseCtl([]string{wire.CtlProtocolVersion, "ping", "placeholder"}, "rem")
	if err != nil {
		t.Fatalf("parseCtl ping: %v", err)
	}
	if len(req.cmds) != 0 || req.wantWindows || req.wantLayout != "" || req.invalidate != "" {
		t.Errorf("ping request = %+v, want no side effects", req)
	}

	_, err = c.parseCtl([]string{"1", "ping", "placeholder"}, "rem")
	if err == nil || !strings.Contains(err.Error(), "reopen the bridge") {
		t.Fatalf("v1 daemon compatibility rejection = %v, want version mismatch", err)
	}
}

// The daemon must build every remote command from the verb table, never forward
// text a caller supplied.
func TestParseCtlNeverForwardsRawCommandText(t *testing.T) {
	c := newCtlStateWith("@1", "%3")
	req, err := c.parseCtl([]string{wire.CtlProtocolVersion, "rename", "%3", "x; kill-server"}, "rem")
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
	req, err := c.parseCtl([]string{wire.CtlProtocolVersion, "split-h", "%3"}, "rem")
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
	req, _ := c.parseCtl([]string{wire.CtlProtocolVersion, "split-h", "%3"}, "rem")
	if c.submit(req, func(string) bool { return false }) {
		t.Error("submit reported written when send refused")
	}
}

func TestTakeIntentsCoalescesAndDrains(t *testing.T) {
	c := newCtlStateWith("@1", "%2", "%3")
	c.setWindowPanes("@2", []string{"%9"})
	for _, argv := range [][]string{
		{wire.CtlProtocolVersion, "split-h", "%2"},
		{wire.CtlProtocolVersion, "split-v", "%3"}, // same window: coalesces
		{wire.CtlProtocolVersion, "split-h", "%9"}, // different window
		{wire.CtlProtocolVersion, "new-window", "%2"},
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
	req, _ := c.parseCtl([]string{wire.CtlProtocolVersion, "split-h", "%3"}, "rem")
	c.submit(req, func(string) bool { return true })

	c.forgetWindow("@1")

	if _, layouts := c.takeIntents(); len(layouts) != 0 {
		t.Errorf("layouts = %v, want none after forgetWindow", layouts)
	}
	if _, err := c.parseCtl([]string{wire.CtlProtocolVersion, "split-h", "%3"}, "rem"); err == nil {
		t.Error("a pane of a forgotten window must no longer resolve")
	}
}

func TestCtlArgvRoundTrip(t *testing.T) {
	tests := [][]string{
		{wire.CtlProtocolVersion, "split-h", "%3"},
		{wire.CtlProtocolVersion, "rename", "%3", "a name with spaces"},
		{wire.CtlProtocolVersion, "rename", "%3", ""}, // an empty argument must survive as one field
		{wire.CtlProtocolVersion, "rename", "%3", "quote's and |pipe|"},
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

func TestCarouselVerbBuildsRemoteToggle(t *testing.T) {
	v, ok := verbs["carousel"]
	if !ok {
		t.Fatal("no carousel verb")
	}
	cmds, err := v.build("%5", "@2", "sess", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("want one command, got %v", cmds)
	}
	script := carouselResolveScript("%5")
	if strings.Contains(script, "'") {
		t.Fatalf("resolve script must have zero single quotes: %q", script)
	}
	wantCmd := fmt.Sprintf("run-shell -b -t %%5 %s", tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
	if cmds[0] != wantCmd {
		t.Fatalf("command\n got %q\nwant %q", cmds[0], wantCmd)
	}
	for _, want := range []string{
		"show-options -pqv",
		"@claude_img_src",
		"TMUX_PANE=\"$src\"",
		"AEYE_BRIDGED=1",
		"tmux-claude-images",
		"command -v",
		"split-window",
	} {
		if !strings.Contains(cmds[0], want) {
			t.Fatalf("command %q missing %q", cmds[0], want)
		}
	}
	for _, ban := range []string{"display-message", "#{@claude_img_src}", "''|*"} {
		if strings.Contains(cmds[0], ban) {
			t.Fatalf("command %q must not contain %q", cmds[0], ban)
		}
	}
	if !v.moves || !v.layout {
		t.Fatal("the toggle opens a split that takes focus: needs moves+layout")
	}
}

func TestCarouselSrcValidation(t *testing.T) {
	const pane = "%1"
	// Mirror the case arms in carouselResolveScript (lookup stubbed via $src).
	validate := `case "$src" in %[0-9]*) case "${src#%}" in *[!0-9]*) src=` + pane + `;; esac;; *) src=` + pane + `;; esac; printf %s "$src"`
	tests := []struct {
		src  string
		want string
	}{
		{"%0", "%0"},
		{"", pane},
		{"%1", "%1"},
		{"junk", pane},
		{"%12", "%12"},
		{"%0x", pane},
	}
	for _, tc := range tests {
		cmd := exec.Command("/bin/sh", "-c", validate)
		cmd.Env = append(os.Environ(), "src="+tc.src)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("src=%q: %v", tc.src, err)
		}
		if got := string(out); got != tc.want {
			t.Errorf("src=%q: got %q, want %q", tc.src, got, tc.want)
		}
	}
}
