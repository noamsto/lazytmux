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
