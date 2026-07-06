package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
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

type args struct {
	session, prefix                                            string
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
	b.WriteString(" " + a.iconSession + " " + a.session + "  ")

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
	flag.StringVar(&a.session, "session", "", "")
	prefix := flag.String("prefix", "", "client_prefix flag")
	flag.StringVar(&a.issueID, "issue-id", "", "")
	flag.StringVar(&a.issueBranch, "issue-branch", "", "")
	flag.StringVar(&a.issueProvider, "issue-provider", "", "")
	flag.StringVar(&a.issueTitle, "issue-title", "", "")
	flag.StringVar(&a.branch, "branch", "", "")
	flag.StringVar(&a.panePath, "path", "", "")
	flag.StringVar(&a.gitRoot, "git-root", "", "")
	flag.StringVar(&a.prNumber, "pr-number", "", "")
	flag.StringVar(&a.prBranch, "pr-branch", "", "")
	flag.StringVar(&a.prState, "pr-state", "", "")
	flag.StringVar(&a.prCheck, "pr-check", "", "")
	flag.StringVar(&a.prMergeable, "pr-mergeable", "", "")
	flag.StringVar(&a.prTitle, "pr-title", "", "")
	flag.StringVar(&a.paneIcon, "pane-icon", "", "")
	flag.StringVar(&a.paneCmd, "pane-cmd", "", "")
	flag.StringVar(&a.claudeFg, "claude-fg", "", "")
	flag.StringVar(&a.crewName, "crew-name", "", "")
	flag.StringVar(&a.crewColor, "crew-color", "", "")
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

	claudeDir := os.Getenv("CLAUDE_STATUS_DIR")
	if claudeDir == "" {
		claudeDir = "/tmp/claude-status"
	}
	os.Stdout.WriteString(renderLine(a, claudeDir, detectTheme(), *prefix != "" && *prefix != "0", time.Now().Unix()))
}
