package main

import "testing"

func TestFormatIssueIDs(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		max  int
		want string
	}{
		{"empty", nil, 2, ""},
		{"under cap", []string{"ENG-1"}, 2, "ENG-1"},
		{"at cap", []string{"ENG-1", "GH-2"}, 2, "ENG-1 GH-2"},
		{"over cap", []string{"ENG-1", "GH-2", "ENG-3", "ENG-4"}, 2, "ENG-1 GH-2 +2"},
	}
	for _, c := range cases {
		if got := formatIssueIDs(c.ids, c.max); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAddIssuesDedupes(t *testing.T) {
	cc := &claudeCounts{}
	addIssues(cc, []string{"ENG-1", "GH-2"})
	addIssues(cc, []string{"GH-2", "ENG-3"})
	want := []string{"ENG-1", "GH-2", "ENG-3"}
	if len(cc.issues) != len(want) {
		t.Fatalf("got %v, want %v", cc.issues, want)
	}
	for i := range want {
		if cc.issues[i] != want[i] {
			t.Fatalf("got %v, want %v", cc.issues, want)
		}
	}
}

func TestAppendIssueIDsEmptyPassthrough(t *testing.T) {
	icons, dw := appendIssueIDs("X ", 2, nil, "\033[2m", "\033[0m")
	if icons != "X " || dw != 2 {
		t.Errorf("got (%q, %d), want (%q, %d)", icons, dw, "X ", 2)
	}
}
