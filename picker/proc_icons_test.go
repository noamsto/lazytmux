package main

import "testing"

func TestBuildProcIconsNormalizesWrapped(t *testing.T) {
	cases := []struct {
		name string
		proc string
		want string
	}{
		{"plain claude", "claude", "🧠"},
		{"nix-wrapped claude", ".claude-wrapped", "🧠"},
		{"unknown proc with empty fallback yields nothing", "some-unknown-proc", ""},
		{"leading dot without -wrapped suffix is left unchanged", ".foo", ""},
		{"trailing -wrapped without leading dot is left unchanged", "foo-wrapped", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := buildProcIcons([]string{tc.proc}, 1)
			want := ""
			if tc.want != "" {
				want = tc.want + " "
			}
			if got != want {
				t.Errorf("buildProcIcons([%q]) = %q, want %q", tc.proc, got, want)
			}
		})
	}
}
