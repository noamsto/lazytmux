package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseExcludePatterns(t *testing.T) {
	got := parseExcludePatterns("  */.ssh , /tmp/* ,, ")
	want := []string{"*/.ssh", "/tmp/*"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pattern %d: got %q want %q", i, got[i], want[i])
		}
	}
	if parseExcludePatterns("") != nil {
		t.Errorf("empty string should yield nil")
	}
}

func TestIsExcluded(t *testing.T) {
	pats := []string{".ssh", "/tmp/*", "/home/noams/Downloads"}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/noams/.ssh", true},                 // basename
		{"/tmp/zr-rank-test", true},                // glob child
		{"/home/noams/Downloads", true},            // exact dir
		{"/home/noams/Downloads/teamviewer", true}, // subtree
		{"/home/noams/Data/git/lazytmux", false},
		{"/home/noams/.config", false},
	}
	for _, c := range cases {
		if got := isExcluded(c.path, pats); got != c.want {
			t.Errorf("isExcluded(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if isExcluded("/anything", nil) {
		t.Errorf("nil patterns should exclude nothing")
	}
}

func TestSessionNameFromPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/n/Data/git/lazytmux", "lazytmux"},
		{"/home/n/proj/foo.bar", "foo_bar"},    // tmux forbids '.' in names
		{"/home/n/proj/a:b", "a_b"},            // tmux forbids ':' in names
		{"/home/n/proj/trailing/", "trailing"}, // trailing slash trimmed
		{"/", ""},                              // root has no usable basename
	}
	for _, c := range cases {
		if got := sessionNameFromPath(c.in); got != c.want {
			t.Errorf("sessionNameFromPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePathResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := normalizePath(link); got != normalizePath(real) {
		t.Errorf("normalizePath(%q) = %q, want %q", link, got, normalizePath(real))
	}
	// Nonexistent path: falls back to Clean, no error
	if got := normalizePath("/no/such/dir/../dir2/"); got != "/no/such/dir2" {
		t.Errorf("normalizePath nonexistent = %q, want /no/such/dir2", got)
	}
}

func TestZoxideSuggestions(t *testing.T) {
	paths := []string{
		"/home/n/git/covered",  // dropped: session path match
		"/home/n/git/lazytmux", // dropped: derived name collides with session "lazytmux"
		"/home/n/git/alpha",
		"/home/n/work/alpha", // dropped: name collides with earlier suggestion "alpha"
		"/home/n/git/beta",
		"/home/n/git/gamma",
	}
	sessionPaths := map[string]bool{"/home/n/git/covered": true}
	sessionNames := map[string]bool{"lazytmux": true}

	got := zoxideSuggestions(paths, sessionPaths, sessionNames, 15)
	want := []suggestion{
		{path: "/home/n/git/alpha", name: "alpha"},
		{path: "/home/n/git/beta", name: "beta"},
		{path: "/home/n/git/gamma", name: "gamma"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestZoxideSuggestionsTopN(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		p := filepath.Join(dir, n)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, normalizePath(p))
	}
	// nil maps are safe to read in Go — also anchors that contract
	got := zoxideSuggestions(paths, nil, nil, 3)
	if len(got) != 3 {
		t.Fatalf("limit not applied: got %d, want 3", len(got))
	}
	// Rank order preserved
	if got[0].name != "a" || got[2].name != "c" {
		t.Errorf("rank order broken: %v", got)
	}
}

func TestSessionFilterMapsSkipsScratch(t *testing.T) {
	dir := t.TempDir()
	sessions := []sessionData{
		{name: "real", path: dir},
		{name: "scratch-hidden", path: dir},
	}
	paths, names := sessionFilterMaps(sessions)
	if !paths[normalizePath(dir)] || !names["real"] {
		t.Errorf("real session not recorded: paths=%v names=%v", paths, names)
	}
	if names["scratch-hidden"] {
		t.Errorf("scratch session leaked into name filter: %v", names)
	}
	// A dir hosting only a scratch session must still be suggested.
	paths, names = sessionFilterMaps(sessions[1:])
	got := zoxideSuggestions([]string{normalizePath(dir)}, paths, names, 15)
	if len(got) != 1 {
		t.Errorf("scratch-only dir suppressed from suggestions: %v", got)
	}
}

func TestCollapseWorktree(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/n/git/lazytmux/.worktrees/feat-x", "/home/n/git/lazytmux"},
		{"/home/n/git/lazytmux/.worktrees/feat-x/sub/dir", "/home/n/git/lazytmux"},
		{"/home/n/git/lazytmux", "/home/n/git/lazytmux"},
		{"/home/n/notes/.worktrees-backup/x", "/home/n/notes/.worktrees-backup/x"},
		{"/.worktrees/x", ""}, // degenerate root; empty path is dropped by os.Stat("") in collectZoxide
	}
	for _, c := range cases {
		if got := collapseWorktree(c.in); got != c.want {
			t.Errorf("collapseWorktree(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCollapseWorktreeNonNested(t *testing.T) {
	// A linked worktree placed OUTSIDE <repo>/.worktrees/ (e.g. a central or
	// sibling layout). Its ".git" is a file "gitdir: <main>/.git/worktrees/<n>",
	// so collapse must recover <main> even with no "/.worktrees/" in the path.
	main := t.TempDir()
	wt := t.TempDir()
	gitfile := "gitdir: " + main + "/.git/worktrees/feat-x\n"
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := collapseWorktree(wt); got != main {
		t.Errorf("collapseWorktree(%q) = %q, want %q (main repo root)", wt, got, main)
	}

	// A subdir of that worktree (e.g. a monorepo's apps/mobile) collapses too,
	// via the walk-up to the worktree's .git file. This is the case that would
	// regress under the sibling layout without the walk-up.
	sub := filepath.Join(wt, "apps", "mobile")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := collapseWorktree(sub); got != main {
		t.Errorf("collapseWorktree(%q) = %q, want %q (main repo root)", sub, got, main)
	}

	// A plain directory (no .git anywhere above) is returned unchanged.
	plain := t.TempDir()
	if got := collapseWorktree(plain); got != plain {
		t.Errorf("collapseWorktree(%q) = %q, want unchanged", plain, got)
	}

	// A normal repo (.git is a directory) and its subdirs are NOT collapsed —
	// only worktrees fold into their root; main-checkout dirs stay as-is.
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := collapseWorktree(repo); got != repo {
		t.Errorf("collapseWorktree(%q) = %q, want unchanged", repo, got)
	}
	repoSub := filepath.Join(repo, "src")
	if err := os.Mkdir(repoSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := collapseWorktree(repoSub); got != repoSub {
		t.Errorf("collapseWorktree(%q) = %q, want unchanged (main-repo subdir)", repoSub, got)
	}
}

func TestCollapseThenSuggest(t *testing.T) {
	// Mirror collectZoxide: collapse every path, then suggest.
	raw := []string{
		"/home/n/git/delta/.worktrees/feat-a", // -> /home/n/git/delta
		"/home/n/git/delta/.worktrees/feat-b", // -> same root, deduped away
		"/home/n/git/epsilon/.worktrees/wip",  // -> /home/n/git/epsilon, suppressed (live session)
	}
	var paths []string
	for _, p := range raw {
		paths = append(paths, collapseWorktree(p))
	}
	sessionPaths := map[string]bool{"/home/n/git/epsilon": true}

	// nil sessionNames is a safe read in Go; this case has no name collisions to suppress
	got := zoxideSuggestions(paths, sessionPaths, nil, 15)
	want := []suggestion{{path: "/home/n/git/delta", name: "delta"}}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestZoxideSuggestionsSymlinkDedupe(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// collectZoxide normalizes both sides; mirror that contract here.
	sessionPaths := map[string]bool{normalizePath(real): true}
	got := zoxideSuggestions([]string{normalizePath(link)}, sessionPaths, nil, 15)
	if len(got) != 0 {
		t.Errorf("symlinked dir not deduped against session at target: %v", got)
	}
}
