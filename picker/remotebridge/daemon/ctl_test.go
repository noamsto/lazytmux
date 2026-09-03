package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			// Zoom is the remote's, not the local renderer pane's; the mirror picks
			// the flag up from the layout reconcile this schedules.
			name:   "zoom toggles on the remote pane",
			argv:   []string{wire.CtlProtocolVersion, "zoom", "%3"},
			want:   []string{"resize-pane -Z -t %3"},
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

// TestPingSubmitAcksWithNoLiveConnection: parseCtl returns a request with no
// commands for ping, so submit's loop never calls send and acks even against
// a send that always refuses — which is what connHolder.send does with an
// empty slot mid-outage. Without that, lztmux-remote-open's dedup would read a
// disconnected bridge as dead and stack a second daemon on the same socket.
func TestPingSubmitAcksWithNoLiveConnection(t *testing.T) {
	c := newCtlState()
	req, err := c.parseCtl([]string{wire.CtlProtocolVersion, "ping", "placeholder"}, "rem")
	if err != nil {
		t.Fatalf("parseCtl ping: %v", err)
	}
	if !c.submit(req, func(string) bool { return false }) {
		t.Error("submit reported ping unwritten with no live connection, want ack")
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

func TestToolVerbBuildsRemoteSplitInRemoteCwd(t *testing.T) {
	v, ok := verbs["tool"]
	if !ok {
		t.Fatal("no tool verb")
	}
	cmds, err := v.build("%5", "@2", "sess", []string{"prdash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("want one command, got %v", cmds)
	}
	script := toolResolveScript("prdash")
	if strings.Contains(script, "'") {
		t.Fatalf("resolve script must have zero single quotes: %q", script)
	}
	wantCmd := fmt.Sprintf("split-window -t %%5 -c '#{pane_current_path}' %s",
		tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
	if cmds[0] != wantCmd {
		t.Fatalf("command\n got %q\nwant %q", cmds[0], wantCmd)
	}
	// The cwd must stay a format for the remote to expand, and the tool must be
	// resolved off the remote PATH rather than a local store path.
	for _, want := range []string{"-c '#{pane_current_path}'", "show-environment -g PATH", "command -v prdash", "exec prdash"} {
		if !strings.Contains(cmds[0], want) {
			t.Fatalf("command %q missing %q", cmds[0], want)
		}
	}
	if strings.Contains(cmds[0], "/nix/store") {
		t.Fatalf("command %q must not carry a local store path", cmds[0])
	}
	if !v.moves || !v.layout {
		t.Fatal("the verb opens a split that takes focus: needs moves+layout")
	}
}

// A pane spawned through fish gets a PATH rebuilt from the login profile, so the
// script restores tmux's own before looking the tool up. The three shapes
// show-environment can answer with, run for real under /bin/sh against a stub.
func TestToolPathRestore(t *testing.T) {
	// The restore prefix, verbatim from the shipped script.
	prefix, _, found := strings.Cut(toolResolveScript("prdash"), "command -v")
	if !found {
		t.Fatal("toolResolveScript no longer has a command -v")
	}

	tests := []struct {
		name string
		stub string
		want string
	}{
		{"global PATH present", "echo PATH=/opt/a:/opt/b", "/opt/a:/opt/b:/login"},
		{"variable unset", "echo unknown variable: PATH >&2; exit 1", "/login"},
		{"empty value", "echo PATH=", "/login"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := filepath.Join(dir, "tmux")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\n"+tc.stub+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c", prefix+`printf %s "$PATH"`)
			cmd.Env = []string{"PATH=" + dir + ":/login"}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			// The stub dir is only there so `tmux` resolves, and the restore
			// prepends ahead of it; drop it wherever it landed to compare.
			if got := strings.Replace(string(out), dir+":", "", 1); got != tc.want {
				t.Errorf("PATH = %q, want %q", got, tc.want)
			}
		})
	}
}

// split-window does not format-expand its shell-command but run-shell does, so
// the restore trims with #* rather than #PATH= — under run-shell the latter's
// #P would expand to the pane index. Keep the body free of every sequence tmux
// would treat as a format, so it stays correct wherever it is used.
func TestToolResolveScriptSurvivesFormatExpansion(t *testing.T) {
	script := toolResolveScript("prdash")
	for _, bad := range []string{"#{", "#(", "#P", "#S", "#W", "#T", "#D", "#F", "#I", "#H"} {
		if strings.Contains(script, bad) {
			t.Errorf("script contains tmux format %q, which run-shell would expand: %q", bad, script)
		}
	}
}

func TestToolVerbRejectsUnlistedTool(t *testing.T) {
	v := verbs["tool"]
	for _, tool := range []string{"", "rm -rf /", "prdash; id", "PRDASH", "sh"} {
		if _, err := v.build("%5", "@2", "sess", []string{tool}); err == nil {
			t.Fatalf("tool %q was accepted", tool)
		}
	}
	for tool := range remoteTools {
		if _, err := v.build("%5", "@2", "sess", []string{tool}); err != nil {
			t.Fatalf("tool %q rejected: %v", tool, err)
		}
	}
}

func TestThemeVerbBuildsSilentRemoteApply(t *testing.T) {
	v, ok := verbs["theme"]
	if !ok {
		t.Fatal("no theme verb")
	}
	cmds, err := v.build("%5", "@2", "sess", []string{"light"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("want one command, got %v", cmds)
	}
	script := themeApplyScript("light")
	if strings.Contains(script, "'") {
		t.Fatalf("apply script must have zero single quotes: %q", script)
	}
	wantCmd := fmt.Sprintf("run-shell -b -t %%5 %s", tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
	if cmds[0] != wantCmd {
		t.Fatalf("command\n got %q\nwant %q", cmds[0], wantCmd)
	}
	// A remote without theme-toggle must stay silent: nothing may open a pane or
	// write where a mirror would repaint it.
	for _, ban := range []string{"split-window", "display-message", "echo"} {
		if strings.Contains(cmds[0], ban) {
			t.Fatalf("command %q must not contain %q", cmds[0], ban)
		}
	}
	if v.moves || v.layout || v.windows {
		t.Fatal("applying a theme changes no remote structure: no reconcile intent")
	}
}

func TestThemeVerbRejectsUnlistedTheme(t *testing.T) {
	v := verbs["theme"]
	for _, theme := range []string{"", "mocha", "dark; id", "Light"} {
		if _, err := v.build("%5", "@2", "sess", []string{theme}); err == nil {
			t.Fatalf("theme %q was accepted", theme)
		}
	}
	for theme := range remoteThemes {
		if _, err := v.build("%5", "@2", "sess", []string{theme}); err != nil {
			t.Fatalf("theme %q rejected: %v", theme, err)
		}
	}
}
