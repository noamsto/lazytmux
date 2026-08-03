package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsageCachesSkipsMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "claude.json"), []byte(`{"windows":[{"label":"5h","pct":42}],"monthly":null}`), 0o644)
	os.WriteFile(filepath.Join(dir, "codex.json"), []byte(`not json`), 0o644)

	caches := loadUsageCaches(dir)
	if len(caches) != 1 {
		t.Fatalf("caches = %v, want only claude", caches)
	}
	if caches["claude"].Windows[0].Pct != 42 {
		t.Fatalf("claude pct = %v, want 42", caches["claude"].Windows[0].Pct)
	}
}

func TestUsageSegmentDisabled(t *testing.T) {
	caches := map[string]usageCache{"claude": {Windows: []usageWindow{{Label: "5h", Pct: 42}}}}
	if got := usageSegment(args{usageMonthlyThreshold: 0}, caches); got != "" {
		t.Fatalf("threshold 0 = %q, want empty (feature off)", got)
	}
}

func TestUsageSegmentWindowsColorsAndOrder(t *testing.T) {
	a := args{
		usageMonthlyThreshold: 50,
		iconUsageClaude:       "CL", iconUsageCodex: "CX", iconUsageCursor: "CU",
		thmSubtext0: "#9a8", thmGreen: "#0f0", thmPeach: "#fa0", thmRed: "#f00",
	}
	caches := map[string]usageCache{
		// Codex first in the map literal on purpose: output order must follow
		// usageAgentOrder (claude, codex), not map iteration.
		"codex":  {Windows: []usageWindow{{Label: "5h", Pct: 85}, {Label: "7d", Pct: 100}}},
		"claude": {Windows: []usageWindow{{Label: "5h", Pct: 42}, {Label: "7d", Pct: 18}}, Monthly: &usageWindow{Label: "mo", Pct: 5}},
	}
	got := usageSegment(a, caches)
	want := "#[fg=#9a8]CL #[fg=#0f0]42%·5h #[fg=#0f0]18%·7d" +
		"  #[fg=#9a8]CX #[fg=#fa0]85%·5h #[fg=#f00]100%·7d  "
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestUsageSegmentMonthlyOnlyAboveThreshold(t *testing.T) {
	a := args{
		usageMonthlyThreshold: 50,
		iconUsageClaude:       "CL",
		thmSubtext0:           "#9a8", thmGreen: "#0f0", thmPeach: "#fa0", thmRed: "#f00",
	}
	caches := map[string]usageCache{
		"claude": {
			Windows: []usageWindow{{Label: "7d", Pct: 100}},
			Monthly: &usageWindow{Label: "mo", Pct: 51},
		},
	}
	got := usageSegment(a, caches)
	want := "#[fg=#9a8]CL #[fg=#f00]100%·7d #[fg=#0f0]51%·mo  "
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}

	caches["claude"] = usageCache{
		Windows: []usageWindow{{Label: "7d", Pct: 100}},
		Monthly: &usageWindow{Label: "mo", Pct: 49},
	}
	if got := usageSegment(a, caches); strings.Contains(got, "mo") {
		t.Fatalf("49%% monthly should stay hidden at threshold 50, got %q", got)
	}
}

func TestUsageSegmentEmptyAgentDropsOut(t *testing.T) {
	a := args{
		usageMonthlyThreshold: 50,
		iconUsageClaude:       "CL", iconUsageCursor: "CU",
		thmSubtext0: "#9a8", thmGreen: "#0f0",
	}
	// Cursor on an uncapped tier: provider writes windows:[] monthly:null.
	caches := map[string]usageCache{
		"claude": {Windows: []usageWindow{{Label: "5h", Pct: 10}}},
		"cursor": {Windows: []usageWindow{}, Monthly: nil},
	}
	got := usageSegment(a, caches)
	if strings.Contains(got, "CU") {
		t.Fatalf("empty cursor cache should not render, got %q", got)
	}
	if !strings.Contains(got, "CL") {
		t.Fatalf("claude block missing, got %q", got)
	}
}

func TestUsageSegmentAllEmpty(t *testing.T) {
	a := args{usageMonthlyThreshold: 50, thmSubtext0: "#9a8"}
	if got := usageSegment(a, map[string]usageCache{}); got != "" {
		t.Fatalf("no caches = %q, want empty", got)
	}
}
