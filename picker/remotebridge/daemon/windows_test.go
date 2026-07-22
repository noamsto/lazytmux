package daemon

import "testing"

func TestRegistryMonotonicIndices(t *testing.T) {
	r := newRegistry(1)
	if got := r.allocLocalWin("h-s"); got != "h-s:1" {
		t.Fatalf("first alloc = %q, want h-s:1", got)
	}
	if got := r.allocLocalWin("h-s"); got != "h-s:2" {
		t.Fatalf("second alloc = %q, want h-s:2", got)
	}
	// A close must not free an index for reuse: the next alloc still advances.
	r.add("@1", "h-s:1")
	r.remove("@1")
	if got := r.allocLocalWin("h-s"); got != "h-s:3" {
		t.Fatalf("post-remove alloc = %q, want h-s:3 (no reuse)", got)
	}
}

func TestRegistryLookup(t *testing.T) {
	r := newRegistry(1)
	w := r.add("@5", "h-s:1")
	w.remotePanes = []string{"%3", "%4"}
	if got, ok := r.byRemoteID("@5"); !ok || got.localWin != "h-s:1" {
		t.Fatalf("byRemoteID(@5) = %+v %v", got, ok)
	}
	if _, ok := r.byRemoteID("@99"); ok {
		t.Fatal("byRemoteID(@99) should be false")
	}
	if r.empty() {
		t.Fatal("registry with one window must not be empty")
	}
	if _, ok := r.remove("@5"); !ok || !r.empty() {
		t.Fatal("remove(@5) then empty() should be true")
	}
}

func TestParseWindowList(t *testing.T) {
	// index and id are distinct namespaces: window at index 3 has id @5.
	// A name may contain spaces; a `|` is preserved here (sanitized at write time).
	got := parseWindowList("1 @1 shell\n2 @2 my window\n3 @5 a|b\n4 @7\n")
	want := []remoteWindow{
		{"1", "@1", "shell"},
		{"2", "@2", "my window"},
		{"3", "@5", "a|b"},
		{"4", "@7", ""}, // no name field -> empty
	}
	if len(got) != len(want) {
		t.Fatalf("parseWindowList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseWindowList[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(parseWindowList("  \n\n")) != 0 {
		t.Fatal("blank body must yield no windows")
	}
}

// TestInitialWindowSelectsByIndexNotID pins the blocking fix: --window carries a
// window INDEX, not a window id. A remote session where index 2 has id @7 must
// select the local window mirroring @7 — never "@2" (which here is a different
// window at index 1).
func TestInitialWindowSelectsByIndexNotID(t *testing.T) {
	wins := []remoteWindow{{"1", "@2", ""}, {"2", "@7", ""}} // index 1 -> @2, index 2 -> @7
	reg := newRegistry(1)
	reg.add("@2", "h-s:1")
	reg.add("@7", "h-s:2")
	if got, ok := localWinForRemoteIndex(wins, reg, "2"); !ok || got != "h-s:2" {
		t.Fatalf("--window 2 selected %q ok=%v, want h-s:2 (mirror of @7, not @2)", got, ok)
	}
	if got, ok := localWinForRemoteIndex(wins, reg, "1"); !ok || got != "h-s:1" {
		t.Fatalf("--window 1 selected %q ok=%v, want h-s:1", got, ok)
	}
	if _, ok := localWinForRemoteIndex(wins, reg, "9"); ok {
		t.Fatal("out-of-range index must not select")
	}
}

func TestSanitizeWindowName(t *testing.T) {
	cases := map[string]string{
		"shell":      "shell",
		"my window":  "my window", // spaces preserved
		"a|b":        "ab",        // FMT delimiter stripped
		"a\nb\r":     "ab",        // newlines stripped
		"tab\tend":   "tabend",    // control char stripped
	}
	for in, want := range cases {
		if got := sanitizeWindowName(in); got != want {
			t.Errorf("sanitizeWindowName(%q) = %q, want %q", in, got, want)
		}
	}
}
