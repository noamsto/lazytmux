package manifest

import "testing"

func mkManifest(rules ...Rule) Manifest { return Manifest{ID: "t", Rules: rules} }

func TestMatchTitleRegionWins(t *testing.T) {
	m := mkManifest(
		Rule{State: "processing", Priority: 100, Region: "title", Contains: []string{"⠐"}},
		Rule{State: "idle", Priority: 10, Region: "whole", Contains: []string{"❯"}},
	)
	got, ok := Match(m, "❯ ", "⠐ doing things", false)
	if !ok || got != "processing" {
		t.Fatalf("Match = (%q,%v), want (processing,true)", got, ok)
	}
}

func TestMatchNotExcludes(t *testing.T) {
	m := mkManifest(Rule{
		State: "idle", Priority: 10, Region: "whole",
		Contains: []string{"❯"},
		Not:      []Predicate{{Contains: []string{"do you want to proceed?"}}},
	})
	if _, ok := Match(m, "❯ do you want to proceed?", "", false); ok {
		t.Fatal("Match should be excluded by not-predicate")
	}
}

func TestMatchAltScreenSuppressed(t *testing.T) {
	m := mkManifest(Rule{State: "processing", Priority: 100, Region: "whole", Contains: []string{"x"}})
	if _, ok := Match(m, "xxx", "", true); ok {
		t.Fatal("alt-screen should suppress all matches")
	}
}

func TestMatchSkipHoldsPrior(t *testing.T) {
	m := mkManifest(Rule{State: "skip", Priority: 100, Region: "whole", Contains: []string{"select model"}})
	got, ok := Match(m, "select model", "", false)
	if ok || got != "" {
		t.Fatalf("skip rule should yield (\"\",false), got (%q,%v)", got, ok)
	}
}

func TestMatchLastLines(t *testing.T) {
	m := mkManifest(Rule{State: "waiting", Priority: 100, Region: "last_lines:2", Contains: []string{"proceed?"}})
	screen := "line a\nline b\nline c\ndo you want to proceed?"
	if got, ok := Match(m, screen, "", false); !ok || got != "waiting" {
		t.Fatalf("last_lines match = (%q,%v)", got, ok)
	}
}

func TestMatchEmptyRuleDoesNotMatch(t *testing.T) {
	m := mkManifest(Rule{State: "idle", Priority: 100, Region: "whole"})
	if got, ok := Match(m, "anything at all", "", false); ok {
		t.Fatalf("empty rule should not match, got (%q,%v)", got, ok)
	}
}
