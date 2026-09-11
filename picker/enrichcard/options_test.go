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
@git_root /home/noams/Data/git/noamsto/tmux-og
@window_claude_ago 4m
@unrelated_option ignored
`
	var o winOpts
	parseWindowOptions(out, &o)
	w := o.local

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

func TestParseWindowOptionsBridge(t *testing.T) {
	out := `@bridge_win 1
@bridge_issue_provider github
@bridge_issue_id 42
@bridge_issue_title "Bridge issue title"
@bridge_issue_url https://github.com/o/r/issues/42
@bridge_pr_number 7
@bridge_pr_title "Bridge PR title"
@bridge_pr_state open
@bridge_pr_check_state success
@bridge_pr_url https://github.com/o/r/pull/7
@bridge_pr_mergeable mergeable
@bridge_pr_draft 1
@bridge_branch feat/remote-branch
@bridge_dir /home/remote/repo
`
	var o winOpts
	parseWindowOptions(out, &o)

	if !o.mirror {
		t.Fatalf("mirror = false, want true")
	}
	b := o.bridge
	if b.issueProvider != "github" || b.issueID != "42" || b.issueTitle != "Bridge issue title" || b.issueURL != "https://github.com/o/r/issues/42" {
		t.Errorf("bridge issue fields wrong: %+v", b)
	}
	if b.prNumber != "7" || b.prTitle != "Bridge PR title" || b.prState != "open" || b.prCheck != "success" || b.prURL != "https://github.com/o/r/pull/7" || b.prMergeable != "mergeable" || b.prDraft != "1" {
		t.Errorf("bridge pr fields wrong: %+v", b)
	}
	if b.branch != "feat/remote-branch" {
		t.Errorf("bridge branch = %q", b.branch)
	}
	if b.worktree != "/home/remote/repo" {
		t.Errorf("bridge dir did not land in worktree: %+v", b)
	}
	if b.gitRoot != "" {
		t.Errorf("bridge gitRoot = %q, want empty (dir is pre-resolved)", b.gitRoot)
	}
}

func TestUnquote(t *testing.T) {
	if got := unquote(`"hi there"`); got != "hi there" {
		t.Errorf("unquote = %q", got)
	}
	if got := unquote("bare"); got != "bare" {
		t.Errorf("unquote bare = %q", got)
	}
	// tmux renders an empty option value as '' and single-quotes some values;
	// both must strip so a cleared option parses as "" (see the empty-branch guard).
	if got := unquote("''"); got != "" {
		t.Errorf("unquote empty single-quote = %q, want empty", got)
	}
	if got := unquote("'main'"); got != "main" {
		t.Errorf("unquote single-quoted = %q, want main", got)
	}
}

func TestParseWindowOptionsEmptyClears(t *testing.T) {
	// A cleared branch comes back as `@branch ''`; it must parse to "" so the
	// card shows "no branch" and disables refresh, not a literal "''".
	var o winOpts
	parseWindowOptions("@branch ''\n@pr_number none\n", &o)
	if o.local.branch != "" {
		t.Errorf("branch = %q, want empty", o.local.branch)
	}
}
