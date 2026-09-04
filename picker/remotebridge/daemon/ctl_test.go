package daemon

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
		"new-pane",
		remoteFloatFull,
	} {
		if !strings.Contains(cmds[0], want) {
			t.Fatalf("command %q missing %q", cmds[0], want)
		}
	}
	for _, ban := range []string{"display-message", "#{@claude_img_src}", "''|*", "split-window", "@float_geom"} {
		if strings.Contains(cmds[0], ban) {
			t.Fatalf("command %q must not contain %q", cmds[0], ban)
		}
	}
	if !v.moves || !v.layout {
		t.Fatal("the toggle opens a float that takes focus: needs moves+layout")
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

// TestCarouselResolveScriptManifestCheck runs the ACTUAL "carousel" verb
// command through a real, private tmux server via run-shell — not a bare
// /bin/sh -c of carouselResolveScript's return value. That distinction is
// load-bearing: run-shell format-expands its whole argument before /bin/sh
// ever sees it, collapsing a run of literal '#' characters pairwise (`####`
// -> `##`, measured against the pinned tmux build), so a script executed
// directly never exercises that collapse and would silently miss a broken
// manifest-field extraction underneath it.
func TestCarouselResolveScriptManifestCheck(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	tests := []struct {
		name        string
		hasManifest bool
	}{
		{"empty manifest falls back to a visible split", false},
		{"present manifest launches the carousel, no split", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stubDir := filepath.Join(dir, "bin")
			if err := os.Mkdir(stubDir, 0o755); err != nil {
				t.Fatal(err)
			}
			launchLog := filepath.Join(dir, "launch.log")
			manifest := filepath.Join(dir, "manifest.jsonl")
			if tc.hasManifest {
				if err := os.WriteFile(manifest, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			writeStub(t, filepath.Join(stubDir, "tmux-claude-images"), `#!/bin/sh
if [ "$1" = --resolve ]; then
	printf 'tmux\tkey\t`+manifest+`\n'
	exit 0
fi
echo launched >>"`+launchLog+`"
`)

			tmux := startIsolatedTmux(t, "PATH="+stubDir+":"+os.Getenv("PATH"))

			paneOut, err := tmux("display-message", "-p", "-t", "w", "#{pane_id}").Output()
			if err != nil {
				t.Fatalf("display-message: %v", err)
			}
			pane := strings.TrimSpace(string(paneOut))

			cmds, err := verbs["carousel"].build(pane, "@0", "w", nil)
			if err != nil {
				t.Fatal(err)
			}
			conf := filepath.Join(dir, "cmd.conf")
			if err := os.WriteFile(conf, []byte(cmds[0]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// source-file drives the same command grammar (and the same
			// format-expansion pass) a control-mode client's command would.
			if out, err := tmux("source-file", conf).CombinedOutput(); err != nil {
				t.Fatalf("source-file: %v\n%s", err, out)
			}

			// run-shell -b is asynchronous; poll for its effect (either the
			// stub's launch marker, or a second, split-window-spawned pane).
			deadline := time.Now().Add(3 * time.Second)
			var launched bool
			var paneCount int
			for time.Now().Before(deadline) {
				if b, _ := os.ReadFile(launchLog); len(b) > 0 {
					launched = true
				}
				out, err := tmux("list-panes", "-t", "w").Output()
				if err == nil {
					paneCount = len(strings.Split(strings.TrimSpace(string(out)), "\n"))
				}
				if launched || paneCount > 1 {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}

			if tc.hasManifest {
				if !launched {
					t.Fatal("tmux-claude-images was never exec'd for a present manifest")
				}
				if paneCount > 1 {
					t.Fatalf("a fallback split was also opened (%d panes) for a present manifest", paneCount)
				}
				return
			}
			if launched {
				t.Fatal("tmux-claude-images was exec'd despite an empty manifest")
			}
			if paneCount <= 1 {
				t.Fatal("no fallback split appeared for an empty manifest")
			}
			capOut, err := tmux("capture-pane", "-p", "-t", "w.1").Output()
			if err != nil {
				t.Fatalf("capture-pane: %v", err)
			}
			if !strings.Contains(string(capOut), "no images yet for this pane") {
				t.Fatalf("fallback split content = %q, want the no-images-yet message", capOut)
			}
		})
	}
}

func writeStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writeStub %s: %v", path, err)
	}
}

// startIsolatedTmux starts a private tmux server whose unix socket path stays
// within macOS sun_path (104 bytes). Nix-build sandboxes give t.TempDir() a
// long prefix; a -S path derived from it plus the test name exceeds that
// limit, while -L with a fixed name under os.MkdirTemp("", "lz") does not.
func startIsolatedTmux(t *testing.T, extraEnv ...string) func(args ...string) *exec.Cmd {
	t.Helper()
	tmpdir, err := os.MkdirTemp("", "lz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpdir) })
	const socket = "s"
	env := append(os.Environ(), "TMUX_TMPDIR="+tmpdir)
	env = append(env, extraEnv...)
	start := exec.Command("tmux", "-L", socket, "-f", "/dev/null",
		"new-session", "-d", "-s", "w", "-x", "80", "-y", "24")
	start.Env = env
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		stop := exec.Command("tmux", "-L", socket, "kill-server")
		stop.Env = env
		stop.Run()
	})
	return func(args ...string) *exec.Cmd {
		cmd := exec.Command("tmux", append([]string{"-L", socket}, args...)...)
		cmd.Env = env
		return cmd
	}
}

func TestToolVerbBuildsRemoteFloatInRemoteCwd(t *testing.T) {
	v, ok := verbs["tool"]
	if !ok {
		t.Fatal("no tool verb")
	}
	// Exact shape per tool, matching config/tmux.conf.nix's floatShort/floatFull
	// binds byte for byte.
	tests := []struct {
		tool  string
		flags string
	}{
		{"prdash", remoteFloatShort},
		{"yazi", remoteFloatShort},
		{"lazygit", remoteFloatFull},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			cmds, err := v.build("%5", "@2", "sess", []string{tc.tool})
			if err != nil {
				t.Fatal(err)
			}
			if len(cmds) != 1 {
				t.Fatalf("want one command, got %v", cmds)
			}
			script := toolResolveScript(tc.tool)
			if strings.Contains(script, "'") {
				t.Fatalf("resolve script must have zero single quotes: %q", script)
			}
			wantCmd := fmt.Sprintf("new-pane -t %%5 -c '#{pane_current_path}' %s %s",
				tc.flags, tmuxQuote("exec /bin/sh -c "+tmuxQuote(script)))
			if cmds[0] != wantCmd {
				t.Fatalf("command\n got %q\nwant %q", cmds[0], wantCmd)
			}
			if strings.Contains(cmds[0], "@float_geom") {
				t.Fatalf("command %q must not stamp @float_geom: the remote's own tmux-float-refit would fight the mirror for authority over it", cmds[0])
			}
		})
	}
	// The cwd must stay a format for the remote to expand, and the tool must be
	// resolved off the remote PATH rather than a local store path.
	cmds, err := v.build("%5", "@2", "sess", []string{"prdash"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-c '#{pane_current_path}'", "show-environment -g PATH", "command -v prdash", "exec prdash"} {
		if !strings.Contains(cmds[0], want) {
			t.Fatalf("command %q missing %q", cmds[0], want)
		}
	}
	if strings.Contains(cmds[0], "/nix/store") {
		t.Fatalf("command %q must not carry a local store path", cmds[0])
	}
	if !v.moves || !v.layout {
		t.Fatal("the verb opens a float that takes focus: needs moves+layout")
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

// parseCtl refuses a pane it cannot map to a window, and the refusal reaches the
// user as a --display-error banner — so a gesture inside a mirrored float has to
// resolve from the moment the float exists. Two windows in the mapping: the
// reconcile's setWindowPanes clears every pane mapped to its own window before
// re-setting, and the float has to come back with them.
//
// Mid-pass as well as after: reconcileLayout's trailing re-read can send it round
// again, and the in-loop assertion is what keeps the float routable meanwhile.
// The probe rides the round-trip seam, which is the only place inside the loop a
// test can observe.
func TestCtlResolvesAMirroredFloatDuringAReconcile(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n"}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.localFloats["%9"] = "%l9"
	w.floatGeom["%9"] = float9
	w.layout = "stale"

	c := newCtlState()
	c.setWindowPanes("@1", w.allRemotePanes())

	base := setupWindowRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // readLayout
		"%begin 1 2 1", tiledFloatLayout + " %0 0", "%end 1 2 1", // trailing re-read: converged
	}, "\n") + "\n")
	reads := 0
	var midPass error
	rt := func(cmds ...string) replies {
		for _, cmd := range cmds {
			if !strings.Contains(cmd, "window_layout") {
				continue
			}
			reads++
			// The trailing re-read of the first pass: the in-loop
			// setWindowPanes has run, the post-loop one has not.
			if reads == 2 {
				_, midPass = c.parseCtl([]string{wire.CtlProtocolVersion, "zoom", "%9"}, "rem")
			}
		}
		return base(cmds...)
	}

	reconcileLayout(f.config(), w, func(string) {}, NewRouter(), noHellos, c, newConverger(), rt)

	if reads != 2 {
		t.Fatalf("%d layout reads, want 2 — the probe never ran mid-pass", reads)
	}
	if midPass != nil {
		t.Errorf("mid-pass zoom on the float: %v", midPass)
	}
	if _, err := c.parseCtl([]string{wire.CtlProtocolVersion, "zoom", "%9"}, "rem"); err != nil {
		t.Errorf("zoom on the float after the reconcile: %v", err)
	}
}

// A float the reconcile itself created has to be routable too: reconcileFloats
// runs after the pass loop, so only the trailing re-assert can know about it.
func TestCtlResolvesAFloatAddedByTheReconcile(t *testing.T) {
	f := &layoutTmux{windowLayout: localMatchingLayout, windowID: "@101\n", newPaneIDs: []string{"%l9\n"}}
	w := newRegistry().add("@1", "@101")
	w.remotePanes = []string{"%0", "%1"}
	w.localPanes = []string{"%l0", "%l1"}
	w.layout = "stale"

	c := newCtlState()
	c.setWindowPanes("@1", w.allRemotePanes())

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	go io.Copy(io.Discard, peer)

	rt := setupWindowRT(strings.Join([]string{
		"%begin 1 1 1", tiledFloatLayout + " %0 0", "%end 1 1 1", // readLayout: the float is already there
		"%begin 1 2 1", tiledFloatLayout + " %0 0", "%end 1 2 1", // trailing re-read: converged
		"%begin 1 3 1", "0 0 0 0", "%end 1 3 1", // the new float's seed: cursor
		"%begin 1 4 1", "FLOAT", "%end 1 4 1", // the new float's seed: capture
	}, "\n") + "\n")

	reconcileLayout(f.config(), w, func(string) {}, NewRouter(), hellos(map[string]net.Conn{"%9": conn}), c, newConverger(), rt)

	if w.localFloats["%9"] != "%l9" {
		t.Fatalf("localFloats = %v, want the reconcile to have mirrored %%9", w.localFloats)
	}
	if _, err := c.parseCtl([]string{wire.CtlProtocolVersion, "zoom", "%9"}, "rem"); err != nil {
		t.Errorf("zoom on the freshly mirrored float: %v", err)
	}
}
