package manifest

import "testing"

func TestLoadParsesAndSortsRules(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	m, ok := ForCommand(ms, "codex")
	if !ok {
		t.Fatal("ForCommand(codex) not found")
	}
	if len(m.Rules) == 0 || m.Rules[0].State != "processing" {
		t.Fatalf("unexpected rules: %+v", m.Rules)
	}
}

func TestForCommandUnknown(t *testing.T) {
	ms, _ := Load()
	if _, ok := ForCommand(ms, "fish"); ok {
		t.Fatal("ForCommand(fish) should be false")
	}
}

func TestForCommandNormalizesNixWrapped(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	cases := []struct {
		name   string
		cmd    string
		wantID string
		wantOK bool
	}{
		{"wrapped claude matches", ".claude-wrapped", "claude", true},
		{"wrapped unknown agent matches nothing", ".nvim-wrapped", "", false},
		{"bare claude still matches", "claude", "claude", true},
		{"bare codex still matches", "codex", "codex", true},
		{"bare cursor-agent still matches", "cursor-agent", "cursor", true},
		{"bare unknown still matches nothing", "nvim", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := ForCommand(ms, tc.cmd)
			if ok != tc.wantOK {
				t.Fatalf("ForCommand(%q) ok = %v, want %v", tc.cmd, ok, tc.wantOK)
			}
			if ok && m.ID != tc.wantID {
				t.Fatalf("ForCommand(%q) id = %q, want %q", tc.cmd, m.ID, tc.wantID)
			}
		})
	}
}
