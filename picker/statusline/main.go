package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/enrichstate"
)

// gitOutput runs git with a short timeout so a stalled repo (NFS, held
// index.lock) can't wedge the once-a-second statusline render. Returns the
// trimmed stdout and whether it succeeded.
func gitOutput(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// volatileFields lists the tmux tokens fetched in one display-message call, in
// output order. Keeping these OUT of the #() argv is what stops line 0 from
// blinking: tmux keys each #() job's output cache by the fully-expanded command
// string, so any changing arg (pane_current_command, client_prefix, @pr_*, …)
// mints a fresh job whose output starts empty — a blank frame that becomes
// visible once the machine is loaded enough that the recompute spans several
// redraws. A command string that stays constant across ticks lets tmux reuse
// the one job and keep the last line painted while this binary recomputes.
var volatileFields = []string{
	"#{client_prefix}",
	"#{@issue_id}", "#{@issue_branch}", "#{@issue_provider}", "#{@issue_title}",
	"#{@branch}", "#{pane_current_path}", "#{@git_root}",
	"#{@pr_number}", "#{@pr_branch}", "#{@pr_state}", "#{@pr_check_state}", "#{@pr_mergeable}", "#{@pr_title}",
	"#{@active_pane_icon}", "#{pane_current_command}", "#{@claude_session_fg}",
	"#{@crew_name}", "#{@crew_color}",
}

// fetchVolatile fills the volatile fields via a single display-message
// roundtrip to the session's active pane. It reports whether prefix is active
// and whether the fetch succeeded; a failed fetch leaves the fields empty (the
// caller re-paints the cached last-good line instead of that degraded frame).
// Fields are joined by US (0x1f), a byte no tmux value contains.
func (a *args) fetchVolatile() (prefixActive, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	format := strings.Join(volatileFields, "\x1f")
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", a.session, "-F", format).Output()
	if err != nil {
		return false, false
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\x1f")
	for len(f) < len(volatileFields) {
		f = append(f, "")
	}
	a.issueID, a.issueBranch, a.issueProvider, a.issueTitle = f[1], f[2], f[3], f[4]
	a.branch, a.panePath, a.gitRoot = f[5], f[6], f[7]
	a.prNumber, a.prBranch, a.prState, a.prCheck, a.prMergeable, a.prTitle = f[8], f[9], f[10], f[11], f[12], f[13]
	a.paneIcon, a.paneCmd, a.claudeFg = f[14], f[15], f[16]
	a.crewName, a.crewColor = f[17], f[18]
	return f[0] == "1", true
}

// statuslineCacheDir holds the per-session last-good rendered line so a failed
// fetchVolatile re-paints the previous frame rather than a degraded one.
const statuslineCacheDir = "/tmp/lazytmux-statusline"

// cacheFileName maps a session name to a filesystem-safe file name; distinct
// names stay distinct (any non-safe byte becomes its 2-hex escape).
func cacheFileName(session string) string {
	var b strings.Builder
	for i := 0; i < len(session); i++ {
		c := session[i]
		if c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, ".%02x", c)
	}
	return b.String()
}

func readLastGood(dir, session string) (string, bool) {
	out, err := os.ReadFile(filepath.Join(dir, cacheFileName(session)))
	if err != nil {
		return "", false
	}
	return string(out), true
}

func writeLastGood(dir, session, line string) {
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	path := filepath.Join(dir, cacheFileName(session))
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if os.WriteFile(tmp, []byte(line), 0o644) != nil {
		return
	}
	os.Rename(tmp, path)
}

type args struct {
	session                                                    string
	issueID, issueBranch, issueProvider, issueTitle            string
	branch, panePath, gitRoot                                  string
	prNumber, prBranch, prState, prCheck, prMergeable, prTitle string
	paneIcon, paneCmd, claudeFg                                string
	crewName, crewColor                                        string

	// theme palette (passed pre-expanded from tmux @thm_* options)
	thmBg, thmRed, thmMauve, thmBlue, thmText, thmSubtext0 string
	thmOverlay0, thmOverlay1, thmPeach, thmGreen           string

	// glyphs (tmux @icon_* options + Nix enrich icon set)
	iconSession, iconBranch, iconDir                                                        string
	iconLinear, iconGitHub, iconPending, iconSuccess, iconFailure, iconMerged, iconClosed, iconConflict string
}

// branchDisplay mirrors tmux-branch-display.sh: prefer the cached @branch,
// else `git -C <path> branch --show-current`.
func branchDisplay(branch, panePath string) string {
	if branch != "" {
		return branch
	}
	if panePath == "" {
		return ""
	}
	if s, ok := gitOutput(panePath, "branch", "--show-current"); ok {
		return s
	}
	return ""
}

// dirDisplay mirrors tmux-dir-display.sh: path relative to git root as ./sub,
// "./" at root, else ~-collapsed absolute path.
func dirDisplay(panePath, gitRoot string) string {
	if gitRoot == "" && panePath != "" {
		if s, ok := gitOutput(panePath, "rev-parse", "--show-toplevel"); ok {
			gitRoot = s
		}
	}
	if gitRoot != "" && strings.HasPrefix(panePath, gitRoot) {
		if panePath == gitRoot {
			return "./"
		}
		return "./" + strings.TrimPrefix(panePath, gitRoot+"/")
	}
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(panePath, home) {
		return "~" + strings.TrimPrefix(panePath, home)
	}
	return panePath
}

// sessionSegment renders the leading session + issue-or-branch chunk.
func sessionSegment(a args, prefixActive bool) string {
	var b strings.Builder
	switch {
	case prefixActive:
		b.WriteString("#[fg=" + a.thmRed + ",bold]")
	case a.claudeFg != "":
		b.WriteString("#[fg=" + a.claudeFg + "]")
	default:
		b.WriteString("#[fg=" + a.thmMauve + "]")
	}
	// range=left marks the session name as a click target; MouseDown1StatusLeft
	// opens the session picker.
	b.WriteString(" #[range=left]" + a.iconSession + " " + a.session + "#[norange]  ")

	// Agent-codename badge for the active window (fan-out harness stamp). Tinted
	// by its @crew_color when set; the issue/branch block below re-sets fg.
	if a.crewName != "" {
		fg := a.crewColor
		if fg == "" {
			fg = a.thmMauve
		}
		b.WriteString("#[fg=" + fg + "]" + a.crewName + "  ")
	}

	if a.issueID != "" && a.issueBranch == a.branch {
		glyph := a.iconGitHub
		if a.issueProvider == "linear" {
			glyph = a.iconLinear
		}
		b.WriteString("#[fg=" + a.thmBlue + ",bold]" + glyph + " " + a.issueID +
			" #[fg=" + a.thmText + ",nobold]" + a.issueTitle)
	} else {
		b.WriteString("#[fg=" + a.thmBlue + ",bold]" + a.iconBranch + " " +
			branchDisplay(a.branch, a.panePath))
	}
	return b.String()
}

// prBadge renders the right-side PR badge, matching the nested conditional in
// status-format[0]. Empty unless pr_number is a real number for this branch.
func prBadge(a args) string {
	if a.prNumber == "" || a.prNumber == "none" || a.prBranch != a.branch {
		return ""
	}

	cr, gr := enrichstate.Classify(a.prState, a.prCheck, a.prMergeable)

	color := map[enrichstate.ColorRole]string{
		enrichstate.ColorMerged:  a.thmMauve,
		enrichstate.ColorClosed:  a.thmOverlay0,
		enrichstate.ColorFailure: a.thmRed,
		enrichstate.ColorPending: a.thmPeach,
		enrichstate.ColorSuccess: a.thmGreen,
	}[cr]

	glyph := map[enrichstate.GlyphRole]string{
		enrichstate.GlyphMerged:   a.iconMerged,
		enrichstate.GlyphClosed:   a.iconClosed,
		enrichstate.GlyphConflict: a.iconConflict,
		enrichstate.GlyphFailure:  a.iconFailure,
		enrichstate.GlyphPending:  a.iconPending,
		enrichstate.GlyphSuccess:  a.iconSuccess,
	}[gr]

	return "#[fg=" + color + "]" + glyph + " #" + a.prNumber + " " + a.prTitle + "  "
}

var wrappedRe = regexp.MustCompile(`^\.(.*)-wrapped$`)

func paneCmdDisplay(cmd string) string {
	if m := wrappedRe.FindStringSubmatch(cmd); m != nil {
		return m[1]
	}
	return cmd
}

func renderLine(a args, claudeDir, theme string, prefixActive bool, now int64) string {
	var b strings.Builder
	b.WriteString("#[align=left,bg=" + a.thmBg + "]")
	b.WriteString(sessionSegment(a, prefixActive))
	b.WriteString("  #[fg=" + a.thmSubtext0 + ",nobold]" + a.iconDir + " " + dirDisplay(a.panePath, a.gitRoot))
	b.WriteString("  #[fg=" + a.thmOverlay1 + "]" + claudeSegment(claudeDir, a.session, theme, now))
	b.WriteString(" #[align=right]") // literal space mirrors `#(claude) #[align=right]` in the old format
	b.WriteString(prBadge(a))
	b.WriteString("#[fg=" + a.thmSubtext0 + "]" + a.paneIcon + " " + paneCmdDisplay(a.paneCmd) + " ")
	return b.String()
}

func main() {
	var a args
	// Only stable args here (see volatileFields). Session stays an arg — stable
	// per client, but needed to keep the #() distinct across clients on
	// different sessions.
	flag.StringVar(&a.session, "session", "", "")
	flag.StringVar(&a.thmBg, "thm-bg", "", "")
	flag.StringVar(&a.thmRed, "thm-red", "", "")
	flag.StringVar(&a.thmMauve, "thm-mauve", "", "")
	flag.StringVar(&a.thmBlue, "thm-blue", "", "")
	flag.StringVar(&a.thmText, "thm-text", "", "")
	flag.StringVar(&a.thmSubtext0, "thm-subtext0", "", "")
	flag.StringVar(&a.thmOverlay0, "thm-overlay0", "", "")
	flag.StringVar(&a.thmOverlay1, "thm-overlay1", "", "")
	flag.StringVar(&a.thmPeach, "thm-peach", "", "")
	flag.StringVar(&a.thmGreen, "thm-green", "", "")
	flag.StringVar(&a.iconSession, "icon-session", "", "")
	flag.StringVar(&a.iconBranch, "icon-branch", "", "")
	flag.StringVar(&a.iconDir, "icon-dir", "", "")
	flag.StringVar(&a.iconLinear, "icon-linear", "", "")
	flag.StringVar(&a.iconGitHub, "icon-github", "", "")
	flag.StringVar(&a.iconPending, "icon-pending", "", "")
	flag.StringVar(&a.iconSuccess, "icon-success", "", "")
	flag.StringVar(&a.iconFailure, "icon-failure", "", "")
	flag.StringVar(&a.iconMerged, "icon-merged", "", "")
	flag.StringVar(&a.iconClosed, "icon-closed", "", "")
	flag.StringVar(&a.iconConflict, "icon-conflict", "", "")
	flag.Parse()

	prefixActive, ok := a.fetchVolatile()

	claudeDir := os.Getenv("CLAUDE_STATUS_DIR")
	if claudeDir == "" {
		claudeDir = "/tmp/claude-status"
	}

	// On a failed fetch the volatile fields are empty; re-paint the cached
	// last-good line so a transient timeout (common under load) doesn't flash a
	// degraded frame. Cold start has no cache and falls through to render.
	if !ok {
		if line, hit := readLastGood(statuslineCacheDir, a.session); hit {
			os.Stdout.WriteString(line)
			return
		}
	}

	line := renderLine(a, claudeDir, detectTheme(), prefixActive, time.Now().Unix())
	if ok {
		writeLastGood(statuslineCacheDir, a.session, line)
	}
	os.Stdout.WriteString(line)
}
