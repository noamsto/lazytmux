package main

import "testing"

func TestAgentPriority(t *testing.T) {
	cases := []struct {
		name string
		c    agentCounts
		want string
	}{
		{"error wins over everything", agentCounts{errorCnt: 1, waiting: 1, done: 1}, "error"},
		{"waiting beats denied/compacting/processing/done/idle",
			agentCounts{waiting: 1, denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "waiting"},
		{"denied beats compacting/processing/done/idle",
			agentCounts{denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "denied"},
		{"compacting beats processing/done/idle",
			agentCounts{compacting: 1, processing: 1, done: 1, idle: 1}, "compacting"},
		{"processing beats done/idle", agentCounts{processing: 1, done: 1, idle: 1}, "processing"},
		{"done beats idle", agentCounts{done: 1, idle: 1}, "done"},
		{"idle alone", agentCounts{idle: 1}, "idle"},
		{"all zero", agentCounts{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentPriority(c.c); got != c.want {
				t.Errorf("agentPriority(%+v) = %q, want %q", c.c, got, c.want)
			}
		})
	}
}

func TestAgentStateOrderMatchesPriority(t *testing.T) {
	// agentStateOrder is what groupWindowsByState (Task 2) walks to decide
	// header order; it must name every state agentPriority can return, in
	// the same order, or the two would silently diverge.
	if len(agentStateOrder) != 7 {
		t.Fatalf("agentStateOrder has %d entries, want 7 (error/waiting/denied/compacting/processing/done/idle)",
			len(agentStateOrder))
	}
	want := []string{"error", "waiting", "denied", "compacting", "processing", "done", "idle"}
	for i, s := range want {
		if agentStateOrder[i] != s {
			t.Errorf("agentStateOrder[%d] = %q, want %q", i, agentStateOrder[i], s)
		}
	}
}

// A remote window name reaches us through @window_bridge_name in the daemon's
// escaped form, because the status line collapses the doubling when it draws.
// The picker draws its own rows, so it has to undo the escape first.
func TestDecodeBridgeName(t *testing.T) {
	cases := map[string]string{
		"pr##367":               "pr#367",
		"a####b":                "a##b",
		"plain-name":            "plain-name",
		"":                      "",
		"[nix-amd-ai 󰪣 󰘭 ##46]": "[nix-amd-ai 󰪣 󰘭 #46]",
	}
	for in, want := range cases {
		if got := decodeBridgeName(in); got != want {
			t.Errorf("decodeBridgeName(%q) = %q, want %q", in, got, want)
		}
	}
}
