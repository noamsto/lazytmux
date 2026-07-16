package main

import (
	"os"
	"strings"
	"testing"
)

func TestBranchDisplay(t *testing.T) {
	if got := branchDisplay("feat/x", "/anything"); got != "feat/x" {
		t.Fatalf("got %q, want feat/x", got)
	}
}

func TestDirDisplay(t *testing.T) {
	if got := dirDisplay("/repo", "/repo"); got != "./" {
		t.Fatalf("at root = %q, want ./", got)
	}
	if got := dirDisplay("/repo/src/app", "/repo"); got != "./src/app" {
		t.Fatalf("subdir = %q, want ./src/app", got)
	}
}

func TestSessionSegmentBranchVariant(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x", panePath: "/repo",
		iconSession: "S", iconBranch: "B",
		thmRed: "#f00", thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd", claudeFg: "",
	}
	got := sessionSegment(a, false)
	want := "#[fg=#c6a] #[range=left]S work#[norange]  #[fg=#89b,bold]B feat/x"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestSessionSegmentIssueVariant(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x",
		issueID: "ENG-7", issueBranch: "feat/x", issueProvider: "linear", issueTitle: "Do it",
		iconSession: "S", iconLinear: "L", iconGitHub: "G",
		thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd", claudeFg: "",
	}
	got := sessionSegment(a, false)
	want := "#[fg=#c6a] #[range=left]S work#[norange]  #[fg=#89b,bold]L ENG-7 #[fg=#cdd,nobold]Do it"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestSessionSegmentCrewBadge(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x", panePath: "/repo",
		crewName: "coral", crewColor: "colour210",
		iconSession: "S", iconBranch: "B",
		thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd",
	}
	got := sessionSegment(a, false)
	want := "#[fg=#c6a] #[range=left]S work#[norange]  #[fg=colour210]coral  #[fg=#89b,bold]B feat/x"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestSessionSegmentCrewBadgeColorFallback(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x", panePath: "/repo",
		crewName: "coral", crewColor: "",
		iconSession: "S", iconBranch: "B",
		thmMauve: "#c6a", thmBlue: "#89b",
	}
	if got := sessionSegment(a, false); !strings.Contains(got, "#[fg=#c6a]coral  ") {
		t.Fatalf("empty crew-color should fall back to mauve, got %q", got)
	}
}

func TestSessionSegmentNoCrewBadge(t *testing.T) {
	a := args{
		session: "work", branch: "feat/x", panePath: "/repo",
		iconSession: "S", iconBranch: "B", thmMauve: "#c6a", thmBlue: "#89b",
	}
	if got := sessionSegment(a, false); strings.Contains(got, "coral") || strings.Count(got, "#[fg=") != 2 {
		t.Fatalf("untagged window should have no badge segment, got %q", got)
	}
}

func TestSessionSegmentPrefixColor(t *testing.T) {
	a := args{session: "s", iconSession: "S", thmRed: "#f00", thmMauve: "#c6a", branch: "m", iconBranch: "B", thmBlue: "#89b"}
	got := sessionSegment(a, true)
	if !strings.HasPrefix(got, "#[fg=#f00,bold] #[range=left]S s") {
		t.Fatalf("prefix variant = %q", got)
	}
}

func TestPRBadgeHidden(t *testing.T) {
	if got := prBadge(args{prNumber: "", branch: "x", prBranch: "x"}); got != "" {
		t.Fatalf("empty pr = %q, want empty", got)
	}
	if got := prBadge(args{prNumber: "none", branch: "x", prBranch: "x"}); got != "" {
		t.Fatalf("none pr = %q, want empty", got)
	}
	if got := prBadge(args{prNumber: "5", branch: "x", prBranch: "y"}); got != "" {
		t.Fatalf("branch mismatch = %q, want empty", got)
	}
}

func TestPRBadgeSuccess(t *testing.T) {
	a := args{
		prNumber: "42", branch: "x", prBranch: "x", prState: "open", prCheck: "success",
		thmGreen: "#0f0", iconSuccess: "OK", prTitle: "Title",
	}
	want := "#[fg=#0f0]OK #42 Title  "
	if got := prBadge(a); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestPRBadgeConflictWinsColorAndGlyph(t *testing.T) {
	a := args{
		prNumber: "9", branch: "x", prBranch: "x", prState: "open",
		prCheck: "success", prMergeable: "conflicting",
		thmRed: "#f00", iconConflict: "CF", prTitle: "T",
	}
	want := "#[fg=#f00]CF #9 T  "
	if got := prBadge(a); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestPRBadgeClosedWinsOverStaleCheck(t *testing.T) {
	a := args{
		prNumber: "9", branch: "x", prBranch: "x", prState: "closed",
		prCheck: "failure", prMergeable: "unknown",
		thmOverlay0: "#666", iconClosed: "XX", prTitle: "T",
	}
	want := "#[fg=#666]XX #9 T  "
	if got := prBadge(a); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestLastGoodRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readLastGood(dir, "work"); ok {
		t.Fatal("cold cache should miss")
	}
	line := "#[align=left]painted line"
	writeLastGood(dir, "work", line)
	got, ok := readLastGood(dir, "work")
	if !ok || got != line {
		t.Fatalf("round-trip = %q,%v, want %q,true", got, ok, line)
	}
}

func TestLastGoodSessionIsolated(t *testing.T) {
	dir := t.TempDir()
	writeLastGood(dir, "a/b", "line-ab")
	writeLastGood(dir, "c d", "line-cd")
	if got, _ := readLastGood(dir, "a/b"); got != "line-ab" {
		t.Fatalf("session with slash = %q", got)
	}
	if got, _ := readLastGood(dir, "c d"); got != "line-cd" {
		t.Fatalf("session with space = %q", got)
	}
}

func TestRenderLineFull(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/panes", 0o755)
	os.MkdirAll(dir+"/issues", 0o755)
	os.WriteFile(dir+"/panes/1", []byte("state=processing\ntimestamp=9000\nsession=work\n"), 0o644)
	now := int64(9000)

	a := args{
		session: "work", branch: "feat/x", panePath: "/repo", gitRoot: "/repo",
		iconSession: "S", iconBranch: "B", iconDir: "D",
		thmBg: "#000", thmMauve: "#c6a", thmBlue: "#89b", thmText: "#cdd",
		thmSubtext0: "#9a8", thmOverlay1: "#777", thmGreen: "#0f0",
		prNumber: "42", prBranch: "feat/x", prState: "open", prCheck: "success",
		iconSuccess: "OK", prTitle: "PR",
		paneIcon: "I", paneCmd: ".nvim-wrapped",
	}

	got := renderLine(a, dir, "dark", false, now)
	want := "#[align=left,bg=#000]" +
		"#[fg=#c6a] #[range=left]S work#[norange]  #[fg=#89b,bold]B feat/x" +
		"  #[fg=#9a8,nobold]D ./" +
		"  #[fg=#777]#[fg=#94e2d5]󰪞#[fg=default] " +
		" #[align=right]" +
		"#[fg=#0f0]OK #42 PR  " +
		"#[fg=#9a8]I nvim "
	if got != want {
		t.Fatalf("renderLine\n got %q\nwant %q", got, want)
	}
}
