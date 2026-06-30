package main

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func testCfg() cfg {
	return cfg{
		target: "$0:@0", prEnrichBin: "/bin/true",
		bg: "#1e1e2e", fg: "#cdd6f4", mauve: "#cba6f7", red: "#f38ba8",
		green: "#a6e3a1", peach: "#fab387", blue: "#89b4fa",
		overlay0: "#6c7086", subtext0: "#a6adc8", lavender: "#b4befe",
		icLinear: "L", icGitHub: "G", icPending: "P", icSuccess: "S",
		icFailure: "F", icMerged: "M", icClosed: "C", icConflict: "X",
	}
}

func render(m model) string { return stripANSI(m.card()) }

func TestCardFullIssueAndPR(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, win: winState{
		issueProvider: "linear", issueID: "ENG-6794", issueTitle: "Carousel nav",
		prNumber: "103", prState: "open", prCheck: "success", prMergeable: "mergeable",
		prTitle: "kitty nav", branch: "feat/103-kitty-nav",
	}}
	out := render(m)
	for _, want := range []string{"ENG-6794", "Carousel nav", "#103", "kitty nav", "[r] refresh", "[q] close"} {
		if !strings.Contains(out, want) {
			t.Errorf("card missing %q\n%s", want, out)
		}
	}
}

func TestCardNoIssueNoPR(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, win: winState{branch: "main"}}
	out := render(m)
	if !strings.Contains(out, "no issue") || !strings.Contains(out, "no PR") {
		t.Errorf("expected no-issue/no-PR fallbacks\n%s", out)
	}
}

func TestCardMergedGlyph(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, win: winState{
		prNumber: "103", prState: "merged", prCheck: "pending", branch: "b"}}
	out := render(m)
	if !strings.Contains(out, "M #103") { // merged glyph wins over pending check
		t.Errorf("expected merged glyph 'M #103'\n%s", out)
	}
}

func TestCardEmptyBranchDisablesRefresh(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, win: winState{prNumber: "103", prState: "open"}}
	out := render(m)
	if !strings.Contains(out, "no branch") || strings.Contains(out, "[r] refresh") {
		t.Errorf("empty branch should disable refresh\n%s", out)
	}
}

func TestCardRefreshingSpinner(t *testing.T) {
	m := model{cfg: testCfg(), width: 60, height: 18, refreshing: true, win: winState{
		prNumber: "103", prState: "open", prCheck: "success", branch: "b"}}
	if !strings.Contains(render(m), "refreshing") {
		t.Errorf("expected refreshing spinner")
	}
}
