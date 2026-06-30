package main

import (
	"flag"
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	var c cfg
	flag.StringVar(&c.target, "target", "", "session_id:window_id to inspect")
	flag.StringVar(&c.prEnrichBin, "pr-enrich-bin", "tmux-pr-enrich", "path to the PR poller binary")
	flag.StringVar(&c.fg, "thm-fg", "", "")
	flag.StringVar(&c.mauve, "thm-mauve", "", "")
	flag.StringVar(&c.red, "thm-red", "", "")
	flag.StringVar(&c.green, "thm-green", "", "")
	flag.StringVar(&c.peach, "thm-peach", "", "")
	flag.StringVar(&c.blue, "thm-blue", "", "")
	flag.StringVar(&c.overlay0, "thm-overlay0", "", "")
	flag.StringVar(&c.subtext0, "thm-subtext0", "", "")
	flag.StringVar(&c.icLinear, "icon-linear", "", "")
	flag.StringVar(&c.icGitHub, "icon-github", "", "")
	flag.StringVar(&c.icPending, "icon-pending", "", "")
	flag.StringVar(&c.icSuccess, "icon-success", "", "")
	flag.StringVar(&c.icFailure, "icon-failure", "", "")
	flag.StringVar(&c.icMerged, "icon-merged", "", "")
	flag.StringVar(&c.icClosed, "icon-closed", "", "")
	flag.StringVar(&c.icConflict, "icon-conflict", "", "")
	flag.Parse()

	w := readWindowState(c.target)
	dir := w.worktree
	if dir == "" {
		dir = w.gitRoot
	}
	m := model{cfg: c, win: w, baseBranch: detectBaseBranch(dir)}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		os.Exit(1)
	}
}
