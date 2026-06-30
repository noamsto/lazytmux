package main

import "testing"

func TestParseWindowOptions(t *testing.T) {
	out := `@issue_provider linear
@issue_id ENG-6794
@issue_title "Seamless ctrl+hjkl into the kitty carousel"
@pr_number 103
@pr_state open
@pr_check_state success
@pr_mergeable mergeable
@branch feat/103-kitty-nav
@git_root /home/noams/Data/git/noamsto/lazytmux
@window_claude_ago 4m
@unrelated_option ignored
`
	var w winState
	parseWindowOptions(out, &w)

	if w.issueProvider != "linear" {
		t.Errorf("issueProvider = %q, want linear", w.issueProvider)
	}
	if w.issueTitle != "Seamless ctrl+hjkl into the kitty carousel" {
		t.Errorf("issueTitle = %q (quotes not stripped?)", w.issueTitle)
	}
	if w.prNumber != "103" || w.prState != "open" || w.prCheck != "success" {
		t.Errorf("pr fields wrong: %+v", w)
	}
	if w.branch != "feat/103-kitty-nav" {
		t.Errorf("branch = %q", w.branch)
	}
	if w.claudeAgo != "4m" {
		t.Errorf("claudeAgo = %q", w.claudeAgo)
	}
}

func TestUnquote(t *testing.T) {
	if got := unquote(`"hi there"`); got != "hi there" {
		t.Errorf("unquote = %q", got)
	}
	if got := unquote("bare"); got != "bare" {
		t.Errorf("unquote bare = %q", got)
	}
}
