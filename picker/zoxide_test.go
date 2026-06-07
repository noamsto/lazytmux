package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
